package casetrigger

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPublishAppendsToTheTriggerStream(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	publisher := New(client, testLogger())
	t.Cleanup(publisher.Close)

	trigger := &investigationv1.CaseTrigger{
		DecisionId:     "d-1",
		TransactionId:  &commonv1.PseudonymousId{Value: "txn-1"},
		Outcome:        &riskv1.DecisionOutcome{Decision: riskv1.Decision_DECISION_REVIEW},
		IdempotencyKey: "key-1",
	}
	publisher.Publish(context.Background(), trigger)

	deadline := time.Now().Add(time.Second)
	for {
		entries, err := client.XRange(context.Background(), streamName, "-", "+").Result()
		if err != nil {
			t.Fatalf("XRange: %v", err)
		}
		if len(entries) == 1 {
			raw, ok := entries[0].Values["payload"].(string)
			if !ok {
				t.Fatal("published entry has no payload field")
			}
			got := &investigationv1.CaseTrigger{}
			if err := proto.Unmarshal([]byte(raw), got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.GetDecisionId() != "d-1" || got.GetIdempotencyKey() != "key-1" {
				t.Fatalf("published trigger = %+v, want decision_id=d-1 idempotency_key=key-1", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no entry appeared on %q within the deadline", streamName)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
