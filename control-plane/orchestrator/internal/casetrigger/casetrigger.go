// Package casetrigger publishes a CaseTrigger to the investigation plane's
// queue whenever a decision is REVIEW (ADR-017 section 1.3), reusing the
// exact off-critical-path posture internal/lineage established: Publish
// enqueues and returns immediately, a background goroutine owns the actual
// I/O, and a queue that cannot keep up drops entries rather than slowing a
// decision that has already been returned to the caller.
//
// This package does not import investigation/internal/queue: that path is
// private to the investigation zone (ADR-015), and Go enforces that at
// compile time. A single XADD does not justify a shared library either way.
package casetrigger

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/internal/platform/observability"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
)

// publications counts what became of every trigger. A dropped trigger means a
// REVIEW decision that opened no case, which is invisible from the decision
// path by design -- the caller already has its answer -- and was therefore
// invisible everywhere until this existed.
var publications = observability.NewCounter(
	"legion.casetrigger.publications",
	"Case triggers by what became of them: published, dropped before the queue, or failed to publish.",
)

// streamName is the trigger stream the investigation controller consumes
// (ADR-017 section 1.3). Distinct from investigation.tasks, which carries
// controller-to-worker task assignments, never orchestrator-originated
// triggers.
const streamName = "investigation.triggers"

// queueDepth mirrors internal/lineage's bound: a burst of REVIEW decisions
// must not be allowed to grow this queue without limit.
const queueDepth = 256

const writeTimeout = 5 * time.Second

// Publisher enqueues a CaseTrigger for background publication.
type Publisher interface {
	Publish(ctx context.Context, trigger *investigationv1.CaseTrigger)
	Close()
}

// pending is a trigger together with the trace-propagation fields captured at
// enqueue time (ADR-020: trace context propagates across the Redis Streams
// queue hops). They must be captured synchronously, in the caller's own
// context, because by the time the background publisher runs, the request
// that produced them has already returned to its own caller and its context
// may be cancelled.
type pending struct {
	trigger     *investigationv1.CaseTrigger
	traceFields map[string]string
}

// RedisPublisher is the Publisher used in every deployment.
type RedisPublisher struct {
	client redis.Cmdable
	queue  chan pending
	done   chan struct{}
	log    *slog.Logger
}

// New starts the background publisher. It does not verify connectivity: a
// down investigation queue must not block orchestrator startup.
func New(client redis.Cmdable, log *slog.Logger) *RedisPublisher {
	p := &RedisPublisher{
		client: client,
		queue:  make(chan pending, queueDepth),
		done:   make(chan struct{}),
		log:    log,
	}
	go p.run()
	return p
}

func (p *RedisPublisher) run() {
	for item := range p.queue {
		p.publish(item)
	}
	close(p.done)
}

func (p *RedisPublisher) publish(item pending) {
	trigger := item.trigger
	payload, err := proto.Marshal(trigger)
	if err != nil {
		p.log.Warn("case trigger could not be serialised", "decision_id", trigger.GetDecisionId(), "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	values := map[string]any{"payload": string(payload)}
	for key, value := range item.traceFields {
		values[key] = value
	}

	err = p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: values,
	}).Err()
	if err != nil {
		p.log.Warn("case trigger publish failed", "decision_id", trigger.GetDecisionId(), "error", err)
		publications.Inc(ctx, observability.Outcome("failed"))
		return
	}
	publications.Inc(ctx, observability.Outcome("published"))
}

// Publish enqueues trigger. See Publisher.
//
// ctx's trace context is captured now, not read later: the background
// publisher runs after the request that produced trigger has already
// returned, by which point ctx may be cancelled.
func (p *RedisPublisher) Publish(ctx context.Context, trigger *investigationv1.CaseTrigger) {
	item := pending{trigger: trigger, traceFields: observability.InjectFields(ctx)}
	select {
	case p.queue <- item:
	default:
		p.log.Warn("case trigger queue full; dropping trigger", "decision_id", trigger.GetDecisionId())
		publications.Inc(context.Background(), observability.Outcome("dropped"))
	}
}

// Close drains the queue and stops the publisher.
func (p *RedisPublisher) Close() {
	close(p.queue)
	<-p.done
}
