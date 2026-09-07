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
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener did not yield a TCP address")
	}
	return address.Port
}
