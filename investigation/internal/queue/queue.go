// Package queue wraps Redis Streams as the investigation plane's task
// delivery mechanism (ADR-017 §3): at-least-once delivery via consumer
// groups, on a Redis instance separate from the feature store and the
// gateway's idempotency store, on the same isolation reasoning as both.
//
// Redis Streams is delivery only. Postgres (investigation/internal/store)
// is the source of truth for task state; nothing here is ever queried for
// state, only read, claimed and acknowledged.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/atesoglu/legion/internal/platform/observability"
)

// payloadField is the single field a message carries: an opaque,
// caller-marshalled proto payload. Streams here are transport, not schema.
const payloadField = "payload"

// Stream is one named Redis Stream consumed by one consumer group.
type Stream struct {
	client redis.Cmdable
	name   string
	group  string
}

// New wraps an established client for one stream/group pair.
func New(client redis.Cmdable, name, group string) *Stream {
	return &Stream{client: client, name: name, group: group}
}

// EnsureGroup creates the stream and consumer group if they do not already
// exist. It is idempotent: a group that already exists is not an error.
func (s *Stream) EnsureGroup(ctx context.Context) error {
	err := s.client.XGroupCreateMkStream(ctx, s.name, s.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("queue: create group %q on %q: %w", s.group, s.name, err)
	}
	return nil
}

// Publish appends payload to the stream, alongside the calling trace's
// propagation fields (ADR-020: trace context propagates across the Redis
// Streams queue hops), so a consumer can continue the same trace.
func (s *Stream) Publish(ctx context.Context, payload []byte) error {
	values := map[string]any{payloadField: string(payload)}
	for key, value := range observability.InjectFields(ctx) {
		values[key] = value
	}
	err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.name,
		Values: values,
	}).Err()
	if err != nil {
		return fmt.Errorf("queue: publish to %q: %w", s.name, err)
	}
	return nil
}

// Message is one claimed stream entry.
type Message struct {
	ID      string
	Payload []byte

	// Fields carries every field on the entry other than the payload -- in
	// practice the trace-propagation fields Publish added, if any. Pass to
	// observability.ExtractContext to continue the producer's trace.
	Fields map[string]string
}

// Read claims up to count new messages for consumer, blocking up to block
// for at least one to arrive. It returns (nil, nil) on a read timeout, which
// is the normal, expected outcome of an idle queue, not a failure.
func (s *Stream) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Message, error) {
	result, err := s.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    s.group,
		Consumer: consumer,
		Streams:  []string{s.name, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: read %q: %w", s.name, err)
	}
	return messagesFrom(result), nil
}

// Reclaim takes ownership of up to count messages that have been pending
// (claimed but never acknowledged) for at least minIdle, the recovery path
// for a worker that claimed a task and then crashed before finishing it
// (ADR-017 §3's lease-based recovery).
func (s *Stream) Reclaim(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]Message, error) {
	messages, _, err := s.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   s.name,
		Group:    s.group,
		MinIdle:  minIdle,
		Start:    "0-0",
		Consumer: consumer,
		Count:    count,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("queue: reclaim on %q: %w", s.name, err)
	}
	return messagesFromSlice(messages), nil
}

// Ack acknowledges a message, removing it from the group's pending list.
func (s *Stream) Ack(ctx context.Context, id string) error {
	if err := s.client.XAck(ctx, s.name, s.group, id).Err(); err != nil {
		return fmt.Errorf("queue: ack %q on %q: %w", id, s.name, err)
	}
	return nil
}

func messagesFrom(streams []redis.XStream) []Message {
	var out []Message
	for _, stream := range streams {
		out = append(out, messagesFromSlice(stream.Messages)...)
	}
	return out
}

func messagesFromSlice(raw []redis.XMessage) []Message {
	out := make([]Message, 0, len(raw))
	for _, m := range raw {
		value, ok := m.Values[payloadField]
		if !ok {
			continue
		}
		str, ok := value.(string)
		if !ok {
			continue
		}
		message := Message{ID: m.ID, Payload: []byte(str)}
		for key, raw := range m.Values {
			if key == payloadField {
				continue
			}
			if s, ok := raw.(string); ok {
				if message.Fields == nil {
					message.Fields = make(map[string]string, len(m.Values)-1)
				}
				message.Fields[key] = s
			}
		}
		out = append(out, message)
	}
	return out
}
