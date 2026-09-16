package dedup

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

func store(t *testing.T) *Store {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client)
}

func transaction(amountMinorUnits int64) *riskv1.Transaction {
	return &riskv1.Transaction{
		Id:             &commonv1.PseudonymousId{Value: "txn-1"},
		IdempotencyKey: "key-1",
		Amount:         &commonv1.Money{CurrencyCode: "EUR", MinorUnits: amountMinorUnits},
	}
}

func TestAFreshClaimRunsAnEvaluation(t *testing.T) {
	s := store(t)
	ctx := context.Background()

	_, found, err := s.Claim(ctx, "caller-1", "key-1", transaction(100))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if found {
		t.Fatal("a first claim must not report a cached response")
	}
}

func TestAResubmissionWithTheSameBodyReturnsTheStoredResponse(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	subject := transaction(100)

	if _, found, err := s.Claim(ctx, "caller-1", "key-1", subject); err != nil || found {
		t.Fatalf("Claim: found=%v err=%v", found, err)
	}

	original := &gatewayv1.EvaluateTransactionResponse{DecisionId: "d-1"}
	if err := s.Store(ctx, "caller-1", "key-1", subject, original); err != nil {
		t.Fatalf("Store: %v", err)
	}

	cached, found, err := s.Claim(ctx, "caller-1", "key-1", subject)
	if err != nil {
		t.Fatalf("Claim (resubmission): %v", err)
	}
	if !found {
		t.Fatal("a resubmission with the same body must return the cached response")
	}
	if cached.GetDecisionId() != "d-1" {
		t.Fatalf("cached response decision id = %q, want d-1", cached.GetDecisionId())
	}
}

func TestAResubmissionWithADifferentBodyConflicts(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	subject := transaction(100)

	if _, found, err := s.Claim(ctx, "caller-1", "key-1", subject); err != nil || found {
		t.Fatalf("Claim: found=%v err=%v", found, err)
	}
	if err := s.Store(ctx, "caller-1", "key-1", subject, &gatewayv1.EvaluateTransactionResponse{DecisionId: "d-1"}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	different := transaction(999)
	if _, _, err := s.Claim(ctx, "caller-1", "key-1", different); err != ErrConflict {
		t.Fatalf("Claim (different body) error = %v, want ErrConflict", err)
	}
}

func TestADifferentCallerWithTheSameKeyDoesNotCollide(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	subject := transaction(100)

	if _, found, err := s.Claim(ctx, "caller-1", "key-1", subject); err != nil || found {
		t.Fatalf("Claim: found=%v err=%v", found, err)
	}
	if _, found, err := s.Claim(ctx, "caller-2", "key-1", subject); err != nil || found {
		t.Fatalf("Claim (different caller): found=%v err=%v", found, err)
	}
}

func TestAnInFlightClaimReportsInFlightRatherThanConflict(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	subject := transaction(100)

	if _, found, err := s.Claim(ctx, "caller-1", "key-1", subject); err != nil || found {
		t.Fatalf("Claim: found=%v err=%v", found, err)
	}

	if _, _, err := s.Claim(ctx, "caller-1", "key-1", subject); err != ErrInFlight {
		t.Fatalf("Claim (in-flight) error = %v, want ErrInFlight", err)
	}
}

func TestReleaseAllowsAFreshClaimAfterAFailedEvaluation(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	subject := transaction(100)

	if _, found, err := s.Claim(ctx, "caller-1", "key-1", subject); err != nil || found {
		t.Fatalf("Claim: found=%v err=%v", found, err)
	}
	if err := s.Release(ctx, "caller-1", "key-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	_, found, err := s.Claim(ctx, "caller-1", "key-1", subject)
	if err != nil {
		t.Fatalf("Claim (after release): %v", err)
	}
	if found {
		t.Fatal("a claim after release must run a fresh evaluation, not report a cached response")
	}
}
