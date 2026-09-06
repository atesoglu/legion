// Command orchestrator coordinates one risk evaluation.
//
// The orchestrator decides what work happens, in what order, with what share
// of the remaining deadline, and what to do when a dependency does not answer.
// It fetches features, fans out to agents, applies circuit breakers, assembles
// the agent evaluations, and hands them to the Rust sentinel.
//
// It deliberately does not decide. It never maps a score to ALLOW, REVIEW or
// DECLINE; that authority belongs to the sentinel (ADR-004).
//
// It implements the same DecisionService contract as the gateway, because the
// gateway is a policy-enforcing front for exactly this operation. Keeping one
// contract avoids a second, silently diverging internal shape.
//
// Phase 0 scaffolding: the service starts, serves the registered contract as
// Unimplemented, and shuts down cleanly. Behaviour arrives in Phase 1.
package main

import (
	"context"
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
)

const defaultListenAddress = ":9200"

type server struct {
	gatewayv1.UnimplementedDecisionServiceServer
}

func main() {
	cfg, err := config.LoadService("orchestrator", defaultListenAddress)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	log := runtime.NewLogger(cfg)

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Error("listen failed", "error", err)
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	gatewayv1.RegisterDecisionServiceServer(grpcServer, &server{})

	err = runtime.Serve(cfg, log,
		func() error { return grpcServer.Serve(listener) },
		func(context.Context) error { grpcServer.GracefulStop(); return nil },
	)
	if err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}
