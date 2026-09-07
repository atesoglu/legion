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
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/atesoglu/legion/gateway/internal/auth"
	"github.com/atesoglu/legion/gateway/internal/deadline"
	"github.com/atesoglu/legion/gateway/internal/ratelimit"
	"github.com/atesoglu/legion/gateway/internal/validation"
	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
)

const (
	defaultListenAddress       = ":9100"
	defaultOrchestratorAddress = "127.0.0.1:9200"

	warmupTimeout = 5 * time.Second
	callerIdleTTL = time.Hour
	sweepInterval = 5 * time.Minute
)

type server struct {
	gatewayv1.UnimplementedDecisionServiceServer

	authenticator *auth.Authenticator
	limiter       *ratelimit.Limiter
	orchestrator  gatewayv1.DecisionServiceClient
	maxDeadline   time.Duration
	log           *slog.Logger
}

// EvaluateTransaction applies the edge controls in the order that spends the
// least on a request that will be refused: identify the caller, check its rate,
// then validate the payload.
func (s *server) EvaluateTransaction(
	ctx context.Context,
	request *gatewayv1.EvaluateTransactionRequest,
) (*gatewayv1.EvaluateTransactionResponse, error) {
	caller, err := s.authenticator.Authenticate(ctx)
	if err != nil {
		return nil, err
	}

	if !s.limiter.Allow(caller.ID) {
		return nil, status.Error(codes.ResourceExhausted, "caller rate limit exceeded")
	}

	if err := validation.Request(request, time.Now()); err != nil {
		return nil, err
	}

	// The gateway is the only origin of the request deadline. A caller may ask
	// for less; it can never ask for more.
	forwardCtx, cancel := deadline.Establish(
		ctx, request.GetOptions().GetDeadline().AsDuration(), s.maxDeadline)
	defer cancel()

	response, err := s.orchestrator.EvaluateTransaction(forwardCtx, request)
	if err != nil {
		return nil, s.translate(caller, err)
	}
	return response, nil
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

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Error("listen failed", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	gatewayv1.RegisterDecisionServiceServer(grpcServer, &server{
		authenticator: authenticator,
		limiter:       limiter,
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
