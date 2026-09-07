// Command orchestrator coordinates one risk evaluation.
//
// The orchestrator decides what work happens, in what order, with what share
// of the remaining deadline, and what to do when a dependency does not answer.
// It fans out to agents, applies circuit breakers, assembles the agent
// evaluations, and hands them to the Rust sentinel.
//
// It deliberately does not decide. It never maps a score to ALLOW, REVIEW or
// DECLINE; that authority belongs to the sentinel (ADR-004).
//
// It implements the same DecisionService contract as the gateway, because the
// gateway is a policy-enforcing front for exactly this operation. Keeping one
// contract avoids a second, silently diverging internal shape.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/redis/go-redis/v9"

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/evaluation"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/features"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/registry"
	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
)

const (
	defaultListenAddress   = ":9200"
	defaultAgentEndpoints  = "velocity=127.0.0.1:9300,device=127.0.0.1:9400,geo=127.0.0.1:9500"
	defaultSentinelAddress = "127.0.0.1:9600"
	defaultFeatureStore    = "127.0.0.1:6379"

	// How long startup waits for dependencies to become reachable before
	// serving anyway.
	warmupTimeout = 5 * time.Second
)

type server struct {
	gatewayv1.UnimplementedDecisionServiceServer

	coordinator *evaluation.Coordinator
}

func (s *server) EvaluateTransaction(
	ctx context.Context,
	request *gatewayv1.EvaluateTransactionRequest,
) (*gatewayv1.EvaluateTransactionResponse, error) {
	subject := request.GetTransaction()
	if subject == nil {
		return nil, status.Error(codes.InvalidArgument, "transaction is required")
	}
	if subject.GetId().GetValue() == "" {
		return nil, status.Error(codes.InvalidArgument, "transaction.id is required")
	}

	decisionID, err := newDecisionID()
	if err != nil {
		return nil, status.Error(codes.Internal, "could not assign a decision identifier")
	}

	outcome, err := s.coordinator.Evaluate(ctx, decisionID, subject, request.GetOptions().GetPolicyId())
	if err != nil {
		return nil, err
	}

	return &gatewayv1.EvaluateTransactionResponse{
		DecisionId: decisionID,
		Outcome:    outcome,
	}, nil
}

type agentClient struct{ client agentv1.AgentServiceClient }

func (a agentClient) Evaluate(
	ctx context.Context,
	in *agentv1.EvaluateRequest,
) (*agentv1.EvaluateResponse, error) {
	return a.client.Evaluate(ctx, in)
}

type sentinelClient struct {
	client dataplanev1.SentinelServiceClient
}

func (s sentinelClient) Decide(
	ctx context.Context,
	in *dataplanev1.DecideRequest,
) (*dataplanev1.DecideResponse, error) {
	return s.client.Decide(ctx, in)
}

func main() {
	cfg, err := config.LoadService("orchestrator", defaultListenAddress)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	log := runtime.NewLogger(cfg)

	agents, err := registry.Parse(envOr("LEGION_AGENT_ENDPOINTS", defaultAgentEndpoints))
	if err != nil {
		log.Error("agent registry is not usable", "error", err)
		os.Exit(1)
	}

	clients, conns, err := dialAgents(agents)
	if err != nil {
		log.Error("agent endpoint is not dialable", "error", err)
		os.Exit(1)
	}

	sentinelConn, err := dial(envOr("LEGION_SENTINEL_ENDPOINT", defaultSentinelAddress))
	if err != nil {
		log.Error("sentinel endpoint is not dialable", "error", err)
		os.Exit(1)
	}
	conns = append(conns, sentinelConn)
	defer closeAll(conns, log)

	// Zone 2 holds the only feature-store credential; no agent has a
	// connection to it (ADR-005). A store that is down degrades an evaluation
	// rather than failing it, so this does not block startup.
	redisClient := redis.NewClient(&redis.Options{
		Addr: envOr("LEGION_FEATURE_STORE", defaultFeatureStore),
	})
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Warn("feature store close failed", "error", err)
		}
	}()

	coordinator, err := evaluation.New(evaluation.Options{
		Registry:        agents,
		Clients:         clients,
		Sentinel:        sentinelClient{client: dataplanev1.NewSentinelServiceClient(sentinelConn)},
		Store:           features.NewRedisStore(redisClient),
		Plan:            budget.Default(),
		RequestDeadline: cfg.RequestDeadline,
		Breaker:         breaker.Default(),
	})
	if err != nil {
		log.Error("orchestration cannot be configured", "error", err)
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Error("listen failed", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	gatewayv1.RegisterDecisionServiceServer(grpcServer, &server{coordinator: coordinator})

	warm(conns, log)
	log.Info("agent registry loaded", "agents", agents.IDs())
	err = runtime.Serve(cfg, log,
		func() error { return grpcServer.Serve(listener) },
		func(context.Context) error { grpcServer.GracefulStop(); return nil },
	)
	if err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

// warm establishes dependency connections before the first request needs them.
//
// gRPC dials lazily, so without this the first evaluation pays for a TCP and
// HTTP/2 handshake out of a stage budget measured in single-digit milliseconds,
// and times out. Startup does not block on a dependency that is down: the
// breakers and the failure model handle that.
func warm(conns []*grpc.ClientConn, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
	defer cancel()

	for _, conn := range conns {
		conn.Connect()
	}
	for _, conn := range conns {
		for conn.GetState() != connectivity.Ready {
			if !conn.WaitForStateChange(ctx, conn.GetState()) {
				log.Warn("dependency not ready at startup",
					"target", conn.Target(), "state", conn.GetState().String())
				break
			}
		}
	}
}

// dialAgents opens one connection per registered agent. Connections are lazy,
// so an agent that is down does not prevent startup; its breaker handles it.
func dialAgents(agents *registry.Registry) (map[string]evaluation.AgentClient, []*grpc.ClientConn, error) {
	clients := make(map[string]evaluation.AgentClient)
	conns := make([]*grpc.ClientConn, 0, len(agents.Agents()))

	for _, agent := range agents.Agents() {
		conn, err := dial(agent.Endpoint)
		if err != nil {
			return nil, conns, err
		}
		conns = append(conns, conn)
		clients[agent.ID] = agentClient{client: agentv1.NewAgentServiceClient(conn)}
	}
	return clients, conns, nil
}

// Transport security is Phase 4 work: mTLS with workload identity (ADR-011).
func dial(endpoint string) (*grpc.ClientConn, error) {
	return grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func closeAll(conns []*grpc.ClientConn, log *slog.Logger) {
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			log.Warn("connection close failed", "error", err)
		}
	}
}

// The decision identifier is assigned here, not taken from the caller: it is
// the correlation key for lineage, and a caller-supplied value could collide
// with another caller's or be reused across attempts.
func newDecisionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
