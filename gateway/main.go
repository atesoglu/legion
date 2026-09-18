// Command gateway is Legion's edge service.
//
// The gateway owns everything that must happen before a request is allowed to
// consume platform resources: transport termination, authentication, request
// validation, rate limiting, and establishing the request deadline that every
// downstream stage inherits.
//
// It owns no risk logic. It does not compute features, does not talk to
// agents, and cannot produce a decision. It forwards to the orchestrator and
// returns what comes back.
//
// Transport security is Phase 4 work: mTLS with workload identity replaces the
// shared-key authentication used here (ADR-011).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/atesoglu/legion/gateway/internal/auth"
	"github.com/atesoglu/legion/gateway/internal/deadline"
	"github.com/atesoglu/legion/gateway/internal/dedup"
	"github.com/atesoglu/legion/gateway/internal/ratelimit"
	"github.com/atesoglu/legion/gateway/internal/validation"
	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/observability"
	"github.com/atesoglu/legion/internal/platform/runtime"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
)

const (
	defaultListenAddress       = ":9100"
	defaultOrchestratorAddress = "127.0.0.1:9200"

	// defaultDedupStore deliberately differs from the feature store's default
	// port: ADR-019/ADR-017 require a separate Redis instance, never a shared
	// one, so an outage or compromise of one cannot touch the other.
	defaultDedupStore = "127.0.0.1:6380"

	warmupTimeout = 5 * time.Second
	callerIdleTTL = time.Hour
	sweepInterval = 5 * time.Minute
)

type server struct {
	gatewayv1.UnimplementedDecisionServiceServer

	authenticator *auth.Authenticator
	limiter       *ratelimit.Limiter
	dedup         *dedup.Store
	orchestrator  gatewayv1.DecisionServiceClient
	maxDeadline   time.Duration
	log           *slog.Logger
}

// decisionLatency is the number ADR-009's target is stated for: end to end,
// measured at the gateway, as the caller experiences it. Everything the
// orchestrator records is a component of this one.
//
// It is recorded for refusals too, attributed by outcome, because a fast
// rejection and a fast decision are not the same thing and averaging them
// together flatters the latter.
var decisionLatency = observability.NewLatency(
	"legion.decision.duration",
	"End-to-end decision latency measured at the gateway, by outcome.",
)

// EvaluateTransaction applies the edge controls in the order that spends the
// least on a request that will be refused: identify the caller, check its rate,
// then validate the payload.
func (s *server) EvaluateTransaction(
	ctx context.Context,
	request *gatewayv1.EvaluateTransactionRequest,
) (response *gatewayv1.EvaluateTransactionResponse, err error) {
	started := time.Now()
	outcome := "error"
	defer func() {
		decisionLatency.Record(ctx, time.Since(started), observability.Outcome(outcome))
	}()

	caller, err := s.authenticator.Authenticate(ctx)
	if err != nil {
		outcome = "unauthenticated"
		return nil, err
	}

	if !s.limiter.Allow(caller.ID) {
		outcome = "rate_limited"
		return nil, status.Error(codes.ResourceExhausted, "caller rate limit exceeded")
	}

	if err := validation.Request(request, time.Now()); err != nil {
		outcome = "invalid"
		return nil, err
	}

	transaction := request.GetTransaction()
	idempotencyKey := transaction.GetIdempotencyKey()

	cached, found, err := s.dedup.Claim(ctx, caller.ID, idempotencyKey, transaction)
	if err != nil {
		outcome = "dedup_refused"
		return nil, s.translateDedup(caller, err)
	}
	if found {
		outcome = "replayed"
		return cached, nil
	}

	// The gateway is the only origin of the request deadline. A caller may ask
	// for less; it can never ask for more.
	forwardCtx, cancel := deadline.Establish(
		ctx, request.GetOptions().GetDeadline().AsDuration(), s.maxDeadline)
	defer cancel()

	response, err = s.orchestrator.EvaluateTransaction(forwardCtx, request)
	if err != nil {
		outcome = "failed"
		// The claim must not outlive an evaluation that never happened, or a
		// legitimate retry would wait out the full retention window for nothing.
		if releaseErr := s.dedup.Release(context.WithoutCancel(ctx), caller.ID, idempotencyKey); releaseErr != nil {
			s.log.Warn("dedup release failed", "caller", caller.ID, "error", releaseErr.Error())
		}
		return nil, s.translate(caller, err)
	}
	outcome = response.GetOutcome().GetDecision().String()

	if storeErr := s.dedup.Store(context.WithoutCancel(ctx), caller.ID, idempotencyKey, transaction, response); storeErr != nil {
		s.log.Warn("dedup store failed", "caller", caller.ID, "error", storeErr.Error())
	}
	return response, nil
}

// translateDedup maps a dedup failure to the status a caller should act on.
// A store outage fails closed: silently skipping deduplication once Zone 6
// depends on the same key (ADR-017) would risk a duplicate case, which is a
// worse failure than an evaluation the caller must retry.
func (s *server) translateDedup(caller auth.Caller, err error) error {
	switch {
	case errors.Is(err, dedup.ErrConflict):
		return status.Error(codes.InvalidArgument, "idempotency_key already used with a different transaction")
	case errors.Is(err, dedup.ErrInFlight):
		return status.Error(codes.Aborted, "duplicate request is already being evaluated, retry")
	default:
		s.log.Warn("idempotency check failed", "caller", caller.ID, "error", err.Error())
		return status.Error(codes.Unavailable, "idempotency check unavailable")
	}
}

// translate keeps internal failure detail inside the trust boundary. Zone 0
// learns that the platform could not decide, not which component failed.
func (s *server) translate(caller auth.Caller, err error) error {
	code := status.Code(err)
	s.log.Warn("evaluation failed",
		"caller", caller.ID, "code", code.String(), "error", err.Error())

	switch code {
	case codes.DeadlineExceeded:
		// Nothing returns late: a caller that has stopped waiting must not
		// receive a decision it could still act upon.
		return status.Error(codes.DeadlineExceeded, "no decision within the request deadline")
	case codes.InvalidArgument:
		return status.Error(codes.InvalidArgument, "request rejected")
	default:
		return status.Error(codes.Unavailable, "no decision could be produced")
	}
}

func main() {
	cfg, err := config.LoadService("gateway", defaultListenAddress)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	log := runtime.NewLogger(cfg)

	authenticator, err := auth.Parse(os.Getenv("LEGION_API_KEYS"))
	if err != nil {
		// An edge service that authenticates nobody is open, not degraded.
		log.Error("caller credentials are not usable", "error", err)
		os.Exit(1)
	}

	conn, err := grpc.NewClient(
		envOr("LEGION_ORCHESTRATOR_ENDPOINT", defaultOrchestratorAddress),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Error("orchestrator endpoint is not dialable", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Warn("connection close failed", "error", err)
		}
	}()

	limiter := ratelimit.New(ratelimit.Default())

	// A separate Redis instance from the feature/lineage stores (ADR-019,
	// ADR-017's isolation reasoning applied here too). Down at startup is not
	// fatal: the first request to need it fails closed with Unavailable
	// instead of the whole process refusing to start.
	dedupClient := redis.NewClient(&redis.Options{
		Addr: envOr("LEGION_IDEMPOTENCY_STORE", defaultDedupStore),
	})
	defer func() {
		if err := dedupClient.Close(); err != nil {
			log.Warn("idempotency store close failed", "error", err)
		}
	}()

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Error("listen failed", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	gatewayv1.RegisterDecisionServiceServer(grpcServer, &server{
		authenticator: authenticator,
		limiter:       limiter,
		dedup:         dedup.New(dedupClient),
		orchestrator:  gatewayv1.NewDecisionServiceClient(conn),
		maxDeadline:   cfg.RequestDeadline,
		log:           log,
	})

	warm(conn, log)
	stopSweeping := sweep(limiter)
	defer stopSweeping()

	err = runtime.Serve(cfg, log,
		func() error { return grpcServer.Serve(listener) },
		func(context.Context) error { grpcServer.GracefulStop(); return nil },
	)
	if err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

// warm establishes the orchestrator connection before the first request needs
// it. gRPC dials lazily, and a handshake does not fit inside an 80 ms budget.
func warm(conn *grpc.ClientConn, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
	defer cancel()

	conn.Connect()
	for conn.GetState() != connectivity.Ready {
		if !conn.WaitForStateChange(ctx, conn.GetState()) {
			log.Warn("orchestrator not ready at startup",
				"target", conn.Target(), "state", conn.GetState().String())
			return
		}
	}
}

// sweep discards rate-limit state for callers that have gone quiet, so the
// table cannot grow without bound.
func sweep(limiter *ratelimit.Limiter) func() {
	ticker := time.NewTicker(sweepInterval)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				limiter.Forget(callerIdleTTL)
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	return func() { close(done) }
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
