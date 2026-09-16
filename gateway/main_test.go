package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/atesoglu/legion/gateway/internal/auth"
	"github.com/atesoglu/legion/gateway/internal/dedup"
	"github.com/atesoglu/legion/gateway/internal/ratelimit"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

const testAPIKey = "gateway-test-caller-key-long-enough-to-pass"

// fakeOrchestrator stands in for the orchestrator, counting how many times it
// was actually invoked so tests can assert a deduplicated resubmission never
// reaches it.
type fakeOrchestrator struct {
	calls atomic.Int32
	err   error
}

func (f *fakeOrchestrator) EvaluateTransaction(
	_ context.Context, request *gatewayv1.EvaluateTransactionRequest, _ ...grpc.CallOption,
) (*gatewayv1.EvaluateTransactionResponse, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &gatewayv1.EvaluateTransactionResponse{
		DecisionId: "decision-" + request.GetTransaction().GetIdempotencyKey(),
	}, nil
}

func newTestServer(t *testing.T, orchestrator gatewayv1.DecisionServiceClient) *server {
	t.Helper()

	authenticator, err := auth.New(map[string]string{"caller": testAPIKey})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return &server{
		authenticator: authenticator,
		limiter:       ratelimit.New(ratelimit.Default()),
		dedup:         dedup.New(client),
		orchestrator:  orchestrator,
		maxDeadline:   time.Second,
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func authenticatedContext() context.Context {
	md := metadata.Pairs("authorization", "Bearer "+testAPIKey)
	return metadata.NewIncomingContext(context.Background(), md)
}

func testTransaction(key string) *riskv1.Transaction {
	return &riskv1.Transaction{
		Id:         &commonv1.PseudonymousId{Value: "1e2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff001", KeyVersion: "v1", Domain: commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_TRANSACTION},
		OccurredAt: timestamppb.New(time.Now()),
		Type:       riskv1.TransactionType_TRANSACTION_TYPE_PURCHASE,
		Channel:    riskv1.Channel_CHANNEL_ECOMMERCE,
		Amount:     &commonv1.Money{CurrencyCode: "EUR", MinorUnits: 4_250},
		Account: &riskv1.Account{
			Id: &commonv1.PseudonymousId{Value: "1e2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff001", KeyVersion: "v1", Domain: commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_ACCOUNT},
		},
		IdempotencyKey: key,
	}
}

func TestAResubmissionWithTheSameKeyIsNotReEvaluated(t *testing.T) {
	orchestrator := &fakeOrchestrator{}
	s := newTestServer(t, orchestrator)
	ctx := authenticatedContext()

	request := &gatewayv1.EvaluateTransactionRequest{Transaction: testTransaction("key-1")}

	first, err := s.EvaluateTransaction(ctx, request)
	if err != nil {
		t.Fatalf("first EvaluateTransaction: %v", err)
	}

	second, err := s.EvaluateTransaction(ctx, request)
	if err != nil {
		t.Fatalf("second EvaluateTransaction: %v", err)
	}

	if second.GetDecisionId() != first.GetDecisionId() {
		t.Fatalf("resubmission decision id = %q, want %q (the original)", second.GetDecisionId(), first.GetDecisionId())
	}
	if got := orchestrator.calls.Load(); got != 1 {
		t.Fatalf("orchestrator was called %d times, want 1", got)
	}
}

func TestAReusedKeyWithADifferentBodyIsRejected(t *testing.T) {
	orchestrator := &fakeOrchestrator{}
	s := newTestServer(t, orchestrator)
	ctx := authenticatedContext()

	first := &gatewayv1.EvaluateTransactionRequest{Transaction: testTransaction("key-1")}
	if _, err := s.EvaluateTransaction(ctx, first); err != nil {
		t.Fatalf("first EvaluateTransaction: %v", err)
	}

	second := &gatewayv1.EvaluateTransactionRequest{Transaction: testTransaction("key-1")}
	second.Transaction.Amount.MinorUnits = 999_999

	_, err := s.EvaluateTransaction(ctx, second)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("reused key with a different body: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestAFailedEvaluationReleasesTheClaimForRetry(t *testing.T) {
	orchestrator := &fakeOrchestrator{err: status.Error(codes.Unavailable, "boom")}
	s := newTestServer(t, orchestrator)
	ctx := authenticatedContext()

	request := &gatewayv1.EvaluateTransactionRequest{Transaction: testTransaction("key-1")}

	if _, err := s.EvaluateTransaction(ctx, request); err == nil {
		t.Fatal("a failing orchestrator call was reported as a success")
	}

	orchestrator.err = nil
	response, err := s.EvaluateTransaction(ctx, request)
	if err != nil {
		t.Fatalf("retry after a failed evaluation: %v", err)
	}
	if response.GetDecisionId() == "" {
		t.Fatal("retry after a failed evaluation did not run a fresh evaluation")
	}
	if got := orchestrator.calls.Load(); got != 2 {
		t.Fatalf("orchestrator was called %d times, want 2 (the failed attempt and the retry)", got)
	}
}
