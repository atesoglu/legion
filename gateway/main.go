// Command gateway is Legion's edge service.
//
// The gateway owns everything that must happen before a request is allowed to
// consume platform resources: transport termination, authentication,
// authorisation, request validation, rate limiting, and establishing the
// request deadline that every downstream stage inherits.
//
// It owns no risk logic. It does not compute features, does not talk to
// agents, and cannot produce a decision.
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

const defaultListenAddress = ":9100"

type server struct {
	gatewayv1.UnimplementedDecisionServiceServer
}

func main() {
	cfg, err := config.LoadService("gateway", defaultListenAddress)
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
