// Package dedup deduplicates transaction resubmissions at the gateway edge.
//
// ADR-019: deduplication is decided at the one edge component every request
// passes through, before an orchestrator or sentinel is ever consumed. The
// key is (caller, idempotency_key); a resubmission of the same key with the
// same transaction body returns the response already produced for it, and a
// resubmission with a different body is rejected outright.
package dedup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/internal/platform/observability"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// claims counts what each claim turned out to be. threat-model T-13's
// detection column asks for "duplicate-subject rate; decision reuse
// attempts", and neither was instrumented (ADR-020).
//
// caller_id is not an attribute: it is caller-controlled and therefore
// unbounded, which is the label rule's other half.
var claims = observability.NewCounter(
	"legion.gateway.dedup.claims",
	"Idempotency claims by what they turned out to be: new, replayed, conflict or in_flight.",
)

// Retention is how long a resubmission is recognised as a duplicate rather
// than a new submission (ADR-019), matching the gateway's own limit on how
// old a transaction may be (validation.MaxAge).
const Retention = 24 * time.Hour

// placeholder marks a key claimed by an evaluation that has not finished yet.
// It is shorter than a sha256 sum, so it can never be mistaken for a
// completed record.
var placeholder = []byte("claimed")

// ErrConflict is returned when an idempotency key was reused with a
// materially different transaction body.
var ErrConflict = errors.New("dedup: idempotency_key reused with a different transaction")

// ErrInFlight is returned when a concurrent evaluation for the same key has
// been claimed but has not finished yet; the caller should retry.
var ErrInFlight = errors.New("dedup: duplicate request is already being evaluated")

// Store deduplicates by (caller, idempotency key) in Redis.
//
// This is a separate Redis instance from the feature store (ADR-017's
// isolation reasoning applies here too): a compromised or overloaded feature
// path must not be able to see or evict dedup records, and vice versa.
type Store struct {
	client redis.Cmdable
}

// New wraps an established client.
func New(client redis.Cmdable) *Store {
	return &Store{client: client}
}

// Claim reserves (caller, key) for a new evaluation.
//
// It returns (nil, false, nil) when the caller must run a fresh evaluation
// and is responsible for calling Store or Release afterwards. It returns the
// previously stored response when the same request already completed.
func (s *Store) Claim(
	ctx context.Context, caller, key string, transaction *riskv1.Transaction,
) (*gatewayv1.EvaluateTransactionResponse, bool, error) {
	fp, err := fingerprint(transaction)
	if err != nil {
		return nil, false, err
	}

	redisKey := recordKey(caller, key)
	claimed, err := s.client.SetNX(ctx, redisKey, placeholder, Retention).Result()
	if err != nil {
		return nil, false, fmt.Errorf("dedup: claim failed: %w", err)
	}
	if claimed {
		claims.Inc(ctx, observability.Outcome("new"))
		return nil, false, nil
	}

	raw, err := s.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		return nil, false, fmt.Errorf("dedup: read failed: %w", err)
	}
	if len(raw) < sha256.Size {
		claims.Inc(ctx, observability.Outcome("in_flight"))
		return nil, false, ErrInFlight
	}
	if !bytes.Equal(raw[:sha256.Size], fp) {
		claims.Inc(ctx, observability.Outcome("conflict"))
		return nil, false, ErrConflict
	}

	response := &gatewayv1.EvaluateTransactionResponse{}
	if err := proto.Unmarshal(raw[sha256.Size:], response); err != nil {
		return nil, false, fmt.Errorf("dedup: stored response is corrupt: %w", err)
	}
	claims.Inc(ctx, observability.Outcome("replayed"))
	return response, true, nil
}

// Store records a completed response so a later resubmission of the same key
// returns it instead of being evaluated again.
func (s *Store) Store(
	ctx context.Context, caller, key string,
	transaction *riskv1.Transaction, response *gatewayv1.EvaluateTransactionResponse,
) error {
	fp, err := fingerprint(transaction)
	if err != nil {
		return err
	}
	body, err := proto.Marshal(response)
	if err != nil {
		return fmt.Errorf("dedup: response is not serialisable: %w", err)
	}

	record := make([]byte, 0, len(fp)+len(body))
	record = append(record, fp...)
	record = append(record, body...)
	if err := s.client.Set(ctx, recordKey(caller, key), record, Retention).Err(); err != nil {
		return fmt.Errorf("dedup: store failed: %w", err)
	}
	return nil
}

// Release clears a claim whose evaluation failed, so a later resubmission is
// not stuck waiting on an evaluation that will never complete.
func (s *Store) Release(ctx context.Context, caller, key string) error {
	if err := s.client.Del(ctx, recordKey(caller, key)).Err(); err != nil {
		return fmt.Errorf("dedup: release failed: %w", err)
	}
	return nil
}

// fingerprint is a deterministic digest of the transaction body, used to
// distinguish a legitimate retry from a reused key on a different body.
func fingerprint(transaction *riskv1.Transaction) ([]byte, error) {
	canonical, err := proto.MarshalOptions{Deterministic: true}.Marshal(transaction)
	if err != nil {
		return nil, fmt.Errorf("dedup: transaction is not serialisable: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}

func recordKey(caller, key string) string {
	return "idem:" + caller + ":" + key
}
