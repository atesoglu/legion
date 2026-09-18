//go:build e2e

// Package e2e exercises Legion across process and language boundaries.
//
// Every other test in the repository runs inside one package: the aggregation
// tests call a function, the fan-out tests use fake clients. Those prove the
// pieces. Nothing proves that a transaction survives the whole chain — Go
// dialling Rust, protobuf on the wire, four services agreeing on the same
// contract — and an interface mismatch is invisible until something does.
//
// These tests therefore start the real binaries and speak gRPC to them. They
// skip, loudly, when those binaries have not been built.
package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// readinessTimeout bounds how long a service may take to accept connections.
// Generous on purpose: a slow CI machine must not produce a flaky failure.
const readinessTimeout = 30 * time.Second

// harness owns the processes started for one test.
type harness struct {
	t    *testing.T
	root string

	// Endpoints of the started services, keyed by service name.
	endpoints map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{
		t:         t,
		root:      repoRoot(t),
		endpoints: make(map[string]string),
	}
}

// repoRoot walks up from the test's own directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root")
		}
		dir = parent
	}
}

func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// startRust launches a data plane binary from the cargo build directory.
func (h *harness) startRust(crate, service string) string {
	h.t.Helper()

	binary := filepath.Join(h.root, "target", "debug", executable(crate))
	if _, err := os.Stat(binary); err != nil {
		h.t.Skipf("%s is not built; run `cargo build --workspace` first (looked for %s)", crate, binary)
	}

	address := fmt.Sprintf("127.0.0.1:%d", freePort(h.t))
	h.spawn(service, binary, nil, []string{"LEGION_LISTEN_ADDRESS=" + address})
	h.endpoints[service] = address
	return address
}

// startGo launches a Go service. It is built rather than run through `go run`
// so that killing the process actually stops the server rather than the
// wrapper that spawned it.
func (h *harness) startGo(pkg, service string, env []string) string {
	h.t.Helper()

	binary := h.buildGo(pkg, service)
	address := fmt.Sprintf("127.0.0.1:%d", freePort(h.t))
	h.spawn(service, binary, nil, append(env, "LEGION_LISTEN_ADDRESS="+address))
	h.endpoints[service] = address
	return address
}

// Compilation is shared across every test in the package: three pipeline tests
// rebuilding the same binary dominated the suite's runtime.
var (
	buildOnce sync.Once
	builtDir  string
	buildErr  error
)

func (h *harness) buildGo(pkg, service string) string {
	h.t.Helper()

	buildOnce.Do(func() {
		builtDir, buildErr = os.MkdirTemp("", "legion-e2e")
	})
	if buildErr != nil {
		h.t.Fatalf("creating a build directory: %v", buildErr)
	}

	binary := filepath.Join(builtDir, executable(service))
	if _, err := os.Stat(binary); err == nil {
		return binary
	}

	build := exec.Command("go", "build", "-o", binary, "./"+pkg)
	build.Dir = h.root
	if output, err := build.CombinedOutput(); err != nil {
		h.t.Fatalf("building %s: %v\n%s", pkg, err, output)
	}
	return binary
}

func (h *harness) spawn(service, binary string, args, env []string) {
	h.t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), env...)

	// Service logs are captured and only surfaced on failure, so a passing run
	// stays readable and a failing one has everything needed to diagnose it.
	var output logBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		h.t.Fatalf("starting %s: %v", service, err)
	}

	h.t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if h.t.Failed() {
			h.t.Logf("--- %s output ---\n%s", service, output.String())
		}
	})
}

// waitReady blocks until every started service accepts connections.
func (h *harness) waitReady() {
	h.t.Helper()

	deadline := time.Now().Add(readinessTimeout)
	for service, address := range h.endpoints {
		for {
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				_ = conn.Close()
				break
			}
			if time.Now().After(deadline) {
				h.t.Fatalf("%s at %s never accepted connections", service, address)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

// startFeatureStore runs an in-process Redis on a real port, so the
// orchestrator talks to it exactly as it would to a deployed one.
func (h *harness) startFeatureStore(history []storedFeature) string {
	h.t.Helper()

	server := miniredis.RunT(h.t)
	computedAt := time.Now()
	for _, feature := range history {
		server.Set(feature.key(), feature.encoded(computedAt))
	}
	h.endpoints["feature-store"] = server.Addr()
	return server.Addr()
}

// startDedupStore runs a second, independent in-process Redis for the
// gateway's idempotency dedup store (ADR-019). It is a separate instance from
// the feature store on purpose, the same isolation ADR-017 argues for the
// task queue: nothing shares state between them.
func (h *harness) startDedupStore() string {
	h.t.Helper()

	server := miniredis.RunT(h.t)
	h.endpoints["dedup-store"] = server.Addr()
	return server.Addr()
}

// startInvestigationQueue runs a third, independent in-process Redis for the
// investigation plane's task queue (ADR-017 section 3) — separate again from
// both the feature store and the gateway's dedup store, for the same
// isolation reasoning.
func (h *harness) startInvestigationQueue() string {
	h.t.Helper()

	server := miniredis.RunT(h.t)
	h.endpoints["investigation-queue"] = server.Addr()
	return server.Addr()
}

// startInvestigationService builds and starts an investigation-plane binary
// that has no gRPC listener of its own (the controller and worker are pure
// queue consumers). Unlike startGo, it is not registered in h.endpoints:
// there is no port for waitReady to dial, and readiness is instead proven by
// polling Postgres for the state the service is expected to produce.
func (h *harness) startInvestigationService(pkg, service string, env []string) {
	h.t.Helper()
	binary := h.buildGo(pkg, service)
	h.spawn(service, binary, nil, env)
}

// One PostgreSQL container is shared by the whole package, and each test that
// needs a database gets one of its own on it. Six tests starting six
// containers dominated the suite's runtime -- the same reason buildOnce above
// exists, applied to the other expensive thing.
//
// Isolation is preserved because a database, not a schema, is handed out: each
// test's services run their migrations against an empty one, exactly as they
// did when each test had a container to itself.
var (
	postgresOnce      sync.Once
	postgresContainer string
	postgresPort      int
	postgresErr       error
	postgresDatabases atomic.Int64
)

// TestMain removes the shared container once the last test has finished.
//
// It deliberately does not start it. A container that cannot start must skip
// the tests that need it rather than fail the package, and only a *testing.T
// can skip; tests needing no database must not be skipped for the absence of
// one.
func TestMain(m *testing.M) {
	code := m.Run()
	if postgresContainer != "" {
		_ = exec.Command("docker", "rm", "-f", postgresContainer).Run()
	}
	os.Exit(code)
}

func postgresDSN(port int, database string) string {
	return fmt.Sprintf("postgres://postgres:legion@127.0.0.1:%d/%s?sslmode=disable", port, database)
}

// postgres returns a DSN for a database of this test's own, created on the
// package's shared container. Creating a database costs milliseconds; starting
// a container costs tens of seconds.
//
// It skips loudly, in the same spirit as a missing Rust binary, when Docker is
// not available or the image cannot be obtained. There is no pure-Go in-process
// stand-in for PostgreSQL, which is why the suite reaches for Docker here and
// nowhere else.
func (h *harness) postgres() string {
	h.t.Helper()

	postgresOnce.Do(startSharedPostgres)
	if postgresErr != nil {
		h.t.Skipf("no PostgreSQL is available: %v", postgresErr)
	}

	name := fmt.Sprintf("legion_e2e_%d", postgresDatabases.Add(1))

	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, postgresDSN(postgresPort, "legion"))
	if err != nil {
		h.t.Fatalf("connecting to the shared PostgreSQL: %v", err)
	}
	defer pool.Close()

	// The name is generated, never caller-supplied, so it cannot be an
	// injection vector -- CREATE DATABASE takes no parameters anyway.
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		h.t.Fatalf("creating database %s: %v", name, err)
	}
	return postgresDSN(postgresPort, name)
}

func startSharedPostgres() {
	if _, err := exec.LookPath("docker"); err != nil {
		postgresErr = fmt.Errorf("docker is not on PATH: %w", err)
		return
	}

	port, err := reservePort()
	if err != nil {
		postgresErr = err
		return
	}

	name := fmt.Sprintf("legion-e2e-postgres-%d", port)
	run := exec.Command("docker", "run", "-d", "--rm",
		"--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"-e", "POSTGRES_PASSWORD=legion",
		"-e", "POSTGRES_DB=legion",
		"postgres:16-alpine")
	if output, err := run.CombinedOutput(); err != nil {
		postgresErr = fmt.Errorf("could not start a PostgreSQL container: %v\n%s", err, output)
		return
	}
	postgresContainer = name
	postgresPort = port

	if err := awaitPostgres(postgresDSN(port, "legion")); err != nil {
		postgresErr = err
	}
}

// awaitPostgres blocks until dsn accepts a real connection. The container port
// opens well before the server inside it accepts authenticated connections, so
// a bare TCP dial (as waitReady does for the Go/Rust binaries) is not enough.
func awaitPostgres(dsn string) error {
	deadline := time.Now().Add(readinessTimeout)
	for {
		pingCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		pool, err := pgxpool.New(pingCtx, dsn)
		if err == nil {
			err = pool.Ping(pingCtx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("PostgreSQL at %s never became ready: %w", dsn, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func reservePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserving a port: %w", err)
	}
	defer func() { _ = listener.Close() }()

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("listener did not yield a TCP address")
	}
	return address.Port, nil
}

// dial opens a client connection to an already-started service.
func (h *harness) dial(address string) *grpc.ClientConn {
	h.t.Helper()

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		h.t.Fatalf("dialling %s: %v", address, err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// callContext bounds a single request well above the 80ms production deadline,
// because these tests assert on behaviour rather than on latency.
func callContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func freePort(t *testing.T) int {
	t.Helper()

	port, err := reservePort()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return port
}
