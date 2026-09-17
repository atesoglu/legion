package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func stream(t *testing.T, name, group string) *Stream {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	s := New(client, name, group)
	if err := s.EnsureGroup(context.Background()); err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	return s
}

func TestAPublishedMessageIsReadByTheGroup(t *testing.T) {
	s := stream(t, "investigation.tasks", "workers")
	ctx := context.Background()

	if err := s.Publish(ctx, []byte("task-1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	messages, err := s.Read(ctx, "worker-a", 10, time.Second)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(messages) != 1 || string(messages[0].Payload) != "task-1" {
		t.Fatalf("messages = %+v, want one message with payload task-1", messages)
	}
}

func TestAnEmptyStreamReadTimesOutWithoutError(t *testing.T) {
	s := stream(t, "investigation.tasks", "workers")

	messages, err := s.Read(context.Background(), "worker-a", 10, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %+v, want none", messages)
	}
}

func TestAnAcknowledgedMessageIsNotReclaimed(t *testing.T) {
	s := stream(t, "investigation.tasks", "workers")
	ctx := context.Background()

	if err := s.Publish(ctx, []byte("task-1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	messages, err := s.Read(ctx, "worker-a", 10, time.Second)
	if err != nil || len(messages) != 1 {
		t.Fatalf("Read: messages=%+v err=%v", messages, err)
	}
	if err := s.Ack(ctx, messages[0].ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	reclaimed, err := s.Reclaim(ctx, "worker-b", 0, 10)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("reclaimed = %+v, want none (the message was acked)", reclaimed)
	}
}

func TestAnUnacknowledgedMessageIsReclaimedByAnotherConsumer(t *testing.T) {
	s := stream(t, "investigation.tasks", "workers")
	ctx := context.Background()

	if err := s.Publish(ctx, []byte("task-1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, err := s.Read(ctx, "worker-a", 10, time.Second); err != nil {
		t.Fatalf("Read (worker-a, never acked): %v", err)
	}

	// worker-a crashed without acking; worker-b takes over.
	reclaimed, err := s.Reclaim(ctx, "worker-b", 0, 10)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if len(reclaimed) != 1 || string(reclaimed[0].Payload) != "task-1" {
		t.Fatalf("reclaimed = %+v, want one message with payload task-1", reclaimed)
	}
}
