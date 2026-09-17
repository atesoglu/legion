// Command capability is Zone 2's capability broker (ADR-005).
//
// Agents receive no infrastructure credentials. Every action an agent may
// take -- reading a feature family, submitting a signal, calling an
// investigation tool -- is an explicit capability, checked here, denied by
// default. See docs/capability-model.md.
//
// This is a new component in the control plane; it concentrates trust
// rather than adding it, since the control plane already holds the feature
// store credential (ADR-005's own trade-off).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/atesoglu/legion/control-plane/capability/internal/broker"
	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
)

const (
	defaultListenAddress = ":9900"

	sweepInterval = 5 * time.Minute
	budgetTTL     = time.Hour
)

type server struct {
	agentv1.UnimplementedCapabilityRuntimeServiceServer
	broker *broker.Broker
}

func (s *server) CheckCapability(
	_ context.Context, req *agentv1.CheckCapabilityRequest,
) (*agentv1.CheckCapabilityResponse, error) {
	verdict, detail := s.broker.CheckCapability(
		req.GetIdentity(), req.GetEvaluationId(), req.GetCapability(),
		req.GetRequestedFeatureNames(), req.GetRequestedWindow())
	return &agentv1.CheckCapabilityResponse{Verdict: verdict, Detail: detail}, nil
}

func (s *server) CheckToolCapability(
	_ context.Context, req *agentv1.CheckToolCapabilityRequest,
) (*agentv1.CheckToolCapabilityResponse, error) {
	verdict, detail := s.broker.CheckToolCapability(req.GetIdentity(), req.GetScopeId(), req.GetToolName())
	return &agentv1.CheckToolCapabilityResponse{Verdict: verdict, Detail: detail}, nil
}

// slogAudit logs every capability invocation, allowed or denied.
//
// Not persisted anywhere durable: Phase 4 owns real observability and audit
// storage everywhere else in the platform, and the capability runtime is no
// exception. CapabilityAudit carries no feature values or subject data, so
// logging it is safe under the same rule internal/platform/runtime states
// for every other service.
type slogAudit struct{ log *slog.Logger }

func (a slogAudit) Audit(entry *agentv1.CapabilityAudit) {
	a.log.Info("capability audit",
		"evaluation_id", entry.GetEvaluationId(),
		"workload_id", entry.GetIdentity().GetWorkloadId(),
		"agent_id", entry.GetIdentity().GetAgentId(),
		"capability", entry.GetCapability().String(),
		"verdict", entry.GetVerdict().String(),
		"detail", entry.GetDetail(),
	)
}

func main() {
	cfg, err := config.LoadService("capability", defaultListenAddress)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	log := runtime.NewLogger(cfg)

	b, err := broker.New(broker.Default(), slogAudit{log: log})
	if err != nil {
		log.Error("capability manifest is not usable", "error", err)
		os.Exit(1)
	}

	stopSweeping := sweep(b)
	defer stopSweeping()

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Error("listen failed", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	agentv1.RegisterCapabilityRuntimeServiceServer(grpcServer, &server{broker: b})

	err = runtime.Serve(cfg, log,
		func() error { return grpcServer.Serve(listener) },
		func(context.Context) error { grpcServer.GracefulStop(); return nil },
	)
	if err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

// sweep discards budget counters for (workload, scope) pairs that have gone
// quiet, mirroring gateway/internal/ratelimit's sweep for the same reason:
// the map cannot be allowed to grow with every distinct pair ever seen.
func sweep(b *broker.Broker) func() {
	ticker := time.NewTicker(sweepInterval)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				b.Forget(budgetTTL)
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	return func() { close(done) }
}
