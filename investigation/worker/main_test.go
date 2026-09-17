package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeCapabilityClient struct {
	verdict agentv1.CapabilityVerdict
	detail  string
	err     error
}

func (f *fakeCapabilityClient) CheckCapability(
	context.Context, *agentv1.CheckCapabilityRequest, ...grpc.CallOption,
) (*agentv1.CheckCapabilityResponse, error) {
	return nil, errors.New("not used by these tests")
}

func (f *fakeCapabilityClient) CheckToolCapability(
	_ context.Context, _ *agentv1.CheckToolCapabilityRequest, _ ...grpc.CallOption,
) (*agentv1.CheckToolCapabilityResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &agentv1.CheckToolCapabilityResponse{Verdict: f.verdict, Detail: f.detail}, nil
}

func TestCheckToolAllowsAGrantedTool(t *testing.T) {
	client := &fakeCapabilityClient{verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_device_history", testLogger())
	if err != nil {
		t.Fatalf("checkTool: %v", err)
	}
}

func TestCheckToolDeniesOnAnExplicitDenial(t *testing.T) {
	client := &fakeCapabilityClient{
		verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED,
		detail:  "tool not granted",
	}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_transaction_velocity", testLogger())
	if !errors.Is(err, errCapabilityDenied) {
		t.Fatalf("checkTool error = %v, want errCapabilityDenied", err)
	}
}

func TestCheckToolFailsClosedWhenTheRuntimeIsUnreachable(t *testing.T) {
	client := &fakeCapabilityClient{err: errors.New("connection refused")}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_device_history", testLogger())
	if !errors.Is(err, errCapabilityDenied) {
		t.Fatalf("checkTool error = %v, want errCapabilityDenied (a broker that cannot answer must not be mistaken for one that said yes)", err)
	}
}
