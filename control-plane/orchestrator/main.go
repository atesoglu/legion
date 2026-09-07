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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/evaluation"
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

	coordinator, err := evaluation.New(evaluation.Options{
		Registry:        agents,
		Clients:         clients,
		Sentinel:        sentinelClient{client: dataplanev1.NewSentinelServiceClient(sentinelConn)},
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
