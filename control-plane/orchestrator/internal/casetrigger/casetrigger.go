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

	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
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
	Publish(trigger *investigationv1.CaseTrigger)
	Close()
}

// RedisPublisher is the Publisher used in every deployment.
type RedisPublisher struct {
	client redis.Cmdable
	queue  chan *investigationv1.CaseTrigger
	done   chan struct{}
	log    *slog.Logger
}

// New starts the background publisher. It does not verify connectivity: a
// down investigation queue must not block orchestrator startup.
func New(client redis.Cmdable, log *slog.Logger) *RedisPublisher {
	p := &RedisPublisher{
		client: client,
		queue:  make(chan *investigationv1.CaseTrigger, queueDepth),
		done:   make(chan struct{}),
		log:    log,
	}
	go p.run()
	return p
}

func (p *RedisPublisher) run() {
	for trigger := range p.queue {
		p.publish(trigger)
	}
	close(p.done)
}

func (p *RedisPublisher) publish(trigger *investigationv1.CaseTrigger) {
	payload, err := proto.Marshal(trigger)
	if err != nil {
		p.log.Warn("case trigger could not be serialised", "decision_id", trigger.GetDecisionId(), "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	err = p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamName,
		Values: map[string]any{"payload": string(payload)},
	}).Err()
	if err != nil {
		p.log.Warn("case trigger publish failed", "decision_id", trigger.GetDecisionId(), "error", err)
	}
}

// Publish enqueues trigger. See Publisher.
func (p *RedisPublisher) Publish(trigger *investigationv1.CaseTrigger) {
	select {
	case p.queue <- trigger:
	default:
		p.log.Warn("case trigger queue full; dropping trigger", "decision_id", trigger.GetDecisionId())
	}
}

// Close drains the queue and stops the publisher.
func (p *RedisPublisher) Close() {
	close(p.queue)
	<-p.done
}
