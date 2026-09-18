// Command investigation-worker is Zone 6's generic worker (ADR-017).
//
// It is generic on purpose: it does not know which logical agent it will run
// until it loads a task. It claims a task, loads the referenced agent
// definition, executes the agent's tools, persists evidence and findings,
// and marks the task complete. This decouples worker count from agent count
// (investigation-model.md section 6) — the property that makes "many
// logical agents, few processes" true.
//
// The tool/model call is mocked: neither the capability runtime (ADR-005)
// nor the shared inference runtime (Phase 3) exist yet. This worker proves
// the task lifecycle and the evidence/finding data model end to end, exactly
// as investigation-model.md's "initially with a mocked inference call" says
// to build it before the shared model exists.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	"github.com/atesoglu/legion/investigation/internal/queue"
	"github.com/atesoglu/legion/investigation/internal/store"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
)

const (
	defaultListenAddress             = ":9800"
	defaultInvestigationDSN          = "postgres://legion:legion@127.0.0.1:5432/legion?sslmode=disable"
	defaultInvestigationRedis        = "127.0.0.1:6381"
	defaultCapabilityRuntimeEndpoint = "127.0.0.1:9900"

	taskStream  = "investigation.tasks"
	workerGroup = "workers"
	readCount   = 10
	readBlock   = 2 * time.Second

	// leaseWindow is how long a claimed-but-unacknowledged message may sit
	// before another consumer may reclaim it (ADR-017 section 3's
	// lease-based recovery): long enough that a healthy in-flight task is
	// never reclaimed out from under it, short enough that a crashed
	// worker's task is not stuck for long.
	leaseWindow  = 30 * time.Second
	reclaimCount = 10
)

func main() {
	cfg, err := config.LoadService("investigation-worker", defaultListenAddress)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	log := runtime.NewLogger(cfg)

	st, err := store.Open(envOr("LEGION_INVESTIGATION_STORE", defaultInvestigationDSN))
	if err != nil {
		log.Error("investigation store is not usable", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.ApplyMigrations(); err != nil {
		log.Warn("investigation schema migration failed; operations will fail until it succeeds", "error", err)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: envOr("LEGION_INVESTIGATION_QUEUE", defaultInvestigationRedis),
	})
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Warn("investigation queue close failed", "error", err)
		}
	}()

	tasks := queue.New(redisClient, taskStream, workerGroup)
	if err := tasks.EnsureGroup(context.Background()); err != nil {
		log.Warn("task consumer group could not be ensured at startup; will retry on each read", "error", err)
	}

	// A down capability runtime must not open the door: CheckToolCapability
	// failing closed (see checkTool below) is what makes it safe to dial
	// without blocking startup, the same posture every other dependency in
	// this worker takes.
	capabilityConn, err := grpc.NewClient(
		envOr("LEGION_CAPABILITY_RUNTIME", defaultCapabilityRuntimeEndpoint),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Error("capability runtime endpoint is not dialable", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := capabilityConn.Close(); err != nil {
			log.Warn("capability runtime connection close failed", "error", err)
		}
	}()
	capabilityClient := agentv1.NewCapabilityRuntimeServiceClient(capabilityConn)

	consumer := consumerName()
	stop := make(chan struct{})

	start := func() error {
		runLoop(stop, consumer, tasks, st, capabilityClient, log)
		return nil
	}
	stopFn := func(context.Context) error {
		close(stop)
		return nil
	}

	if err := runtime.Serve(cfg, log, start, stopFn); err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

func consumerName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}

// taskQueue and taskStore are the worker's view of its two dependencies,
// narrow enough to fake in tests that exercise delivery semantics without a
// live Redis or Postgres. *queue.Stream and *store.Store satisfy them.
type taskQueue interface {
	Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]queue.Message, error)
	Reclaim(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]queue.Message, error)
	Ack(ctx context.Context, id string) error
}

type taskStore interface {
	GetTask(ctx context.Context, taskID string) (*store.Task, error)
	ClaimTask(ctx context.Context, taskID string) (uint32, error)
	CompleteTask(ctx context.Context, taskID string) error
	FailTask(ctx context.Context, taskID string, deadLetter bool) error
	GetEnabledAgent(ctx context.Context, agentID string) (*store.AgentDefinition, error)
	CompleteInvestigationIfDone(ctx context.Context, investigationID string) (bool, string, error)
	InsertAuditEvent(ctx context.Context, caseID, eventType string, detail map[string]any) error
	InsertToolExecution(
		ctx context.Context, taskID, toolName string, arguments, result map[string]any,
		status string, duration time.Duration,
	) (string, error)
	InsertEvidence(ctx context.Context, investigationID, source string, content map[string]any) (string, error)
	InsertFinding(
		ctx context.Context, taskID, agentID string, evidenceIDs []string,
		observation, hypothesis string, confidence uint32,
	) (string, error)
}

// disposition is what one delivery concluded about its message, and so
// whether the message may be acknowledged.
type disposition int

const (
	// settled: this delivery reached a conclusion that is now durable in
	// Postgres (or never could be). Acknowledging drops it for good.
	settled disposition = iota

	// retry: the work did not conclude and nothing durable records that,
	// so the message must stay in the group's pending list until the lease
	// expires and Reclaim hands it back (ADR-017 §3's at-least-once
	// contract). Acknowledging here would silently drop the task.
	retry
)

// runLoop consumes new tasks, and reclaims tasks abandoned by a crashed
// worker, until stop is closed.
func runLoop(
	stop <-chan struct{}, consumer string, tasks taskQueue, st taskStore,
	capability agentv1.CapabilityRuntimeServiceClient, log *slog.Logger,
) {
	ctx := context.Background()
	for {
		select {
		case <-stop:
			return
		default:
		}

		messages, err := tasks.Read(ctx, consumer, readCount, readBlock)
		if err != nil {
			log.Warn("task read failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		if len(messages) == 0 {
			reclaimed, err := tasks.Reclaim(ctx, consumer, leaseWindow, reclaimCount)
			if err != nil {
				log.Warn("task reclaim failed", "error", err)
			}
			messages = reclaimed
		}

		for _, message := range messages {
			if handleTask(ctx, st, capability, message.Payload, log) == retry {
				log.Warn("task left unacknowledged for redelivery", "message_id", message.ID)
				continue
			}
			if err := tasks.Ack(ctx, message.ID); err != nil {
				log.Warn("task ack failed", "id", message.ID, "error", err)
			}
		}
	}
}

// handleTask runs one task to completion, or fails it, and reports whether
// the message may be acknowledged. It is safe to call more than once for the
// same task_id (at-least-once delivery, ADR-017 section 3): a task already in
// a terminal state runs no agent a second time.
func handleTask(
	ctx context.Context, st taskStore, capability agentv1.CapabilityRuntimeServiceClient, payload []byte, log *slog.Logger,
) disposition {
	envelope := &investigationv1.Task{}
	if err := proto.Unmarshal(payload, envelope); err != nil {
		// Redelivery cannot turn an unparseable payload into a parseable one.
		log.Warn("task envelope is corrupt; dropping", "error", err)
		return settled
	}

	task, err := st.GetTask(ctx, envelope.GetTaskId())
	if err != nil {
		log.Warn("task lookup failed", "task_id", envelope.GetTaskId(), "error", err)
		return retry
	}
	if task == nil {
		log.Warn("task envelope refers to no known task", "task_id", envelope.GetTaskId())
		return settled
	}
	if store.IsTerminal(task.Status) {
		// The agent already ran under a previous delivery, but that delivery
		// may have died before closing the investigation out, so the
		// completion check is still owed.
		return finishInvestigation(ctx, st, task, log)
	}
	if task.Attempt >= task.MaxAttempts {
		return failTask(ctx, st, task, true, log)
	}

	if _, err := st.ClaimTask(ctx, task.TaskID); err != nil {
		log.Warn("task claim failed", "task_id", task.TaskID, "error", err)
		return retry
	}

	agent, err := st.GetEnabledAgent(ctx, task.AgentID)
	if err != nil {
		log.Warn("agent lookup failed", "task_id", task.TaskID, "agent_id", task.AgentID, "error", err)
		return failTask(ctx, st, task, task.Attempt+1 >= task.MaxAttempts, log)
	}
	if agent == nil {
		log.Warn("task references an agent that is not registered or not enabled",
			"task_id", task.TaskID, "agent_id", task.AgentID)
		return failTask(ctx, st, task, true, log)
	}

	if err := runAgent(ctx, st, capability, task, agent, log); err != nil {
		log.Warn("agent execution failed", "task_id", task.TaskID, "agent_id", task.AgentID, "error", err)
		// A capability denial is a policy decision, not a transient fault:
		// retrying would ask the same broker the same question and get the
		// same answer, so it goes straight to DEAD_LETTER rather than
		// waiting out max_attempts.
		return failTask(ctx, st, task, errors.Is(err, errCapabilityDenied) || task.Attempt+1 >= task.MaxAttempts, log)
	}

	if err := st.CompleteTask(ctx, task.TaskID); err != nil {
		// The agent's work is persisted but the task is still RUNNING.
		// Acknowledging now would strand it there forever, so redeliver and
		// accept that the mocked agent may run twice — at-least-once is the
		// contract, and task.max_attempts bounds it.
		log.Warn("task completion failed", "task_id", task.TaskID, "error", err)
		return retry
	}

	return finishInvestigation(ctx, st, task, log)
}

// finishInvestigation closes out the investigation and case if this task was
// the last one outstanding. It runs after a task reaches ANY terminal state,
// failures included: an investigation whose final task failed is just as
// finished as one whose final task succeeded.
func finishInvestigation(ctx context.Context, st taskStore, task *store.Task, log *slog.Logger) disposition {
	completed, caseID, err := st.CompleteInvestigationIfDone(ctx, task.InvestigationID)
	if err != nil {
		log.Warn("investigation completion check failed", "investigation_id", task.InvestigationID, "error", err)
		return retry
	}
	if completed {
		// Log-only: the investigation has already transitioned, so a
		// redelivery would report completed == false and never retry this
		// write. A lost audit event is preferable to replaying the task.
		if err := st.InsertAuditEvent(ctx, caseID, "INVESTIGATION_COMPLETED",
			map[string]any{"investigation_id": task.InvestigationID}); err != nil {
			log.Warn("audit event insert failed", "case_id", caseID, "error", err)
		}
	}
	return settled
}

func failTask(ctx context.Context, st taskStore, task *store.Task, deadLetter bool, log *slog.Logger) disposition {
	if err := st.FailTask(ctx, task.TaskID, deadLetter); err != nil {
		// Nothing durable records the failure, so the task would be left
		// mid-flight if this message were acknowledged.
		log.Warn("marking task failed did not succeed", "task_id", task.TaskID, "error", err)
		return retry
	}
	return finishInvestigation(ctx, st, task, log)
}

// errCapabilityDenied marks a runAgent failure caused by the capability
// runtime, distinct from an ordinary tool/store error, so handleTask can
// route it straight to DEAD_LETTER instead of the normal retry path.
var errCapabilityDenied = errors.New("investigation-worker: capability denied")

// runAgent checks the agent's capability to call its one tool (ADR-005),
// then produces the mocked tool call and finding every investigation agent
// currently produces. The finding is deterministic and reads its canned
// output from agent.Configuration rather than branching on agent.AgentID
// (ADR-014: no file may branch on an agent id) -- there is no shared
// inference runtime (Phase 3) to call yet, only a real capability check in
// front of a fake tool.
func runAgent(
	ctx context.Context, st taskStore, capability agentv1.CapabilityRuntimeServiceClient,
	task *store.Task, agent *store.AgentDefinition, log *slog.Logger,
) error {
	started := time.Now()
	toolName := "lookup_history"
	if len(agent.AllowedTools) > 0 {
		toolName = agent.AllowedTools[0]
	}

	if err := checkTool(ctx, capability, agent.AgentID, task.TaskID, toolName, log); err != nil {
		if _, insertErr := st.InsertToolExecution(ctx, task.TaskID, toolName,
			map[string]any{"agent_id": agent.AgentID}, nil, "DENIED", time.Since(started)); insertErr != nil {
			log.Warn("recording a denied tool execution failed", "task_id", task.TaskID, "error", insertErr)
		}
		return err
	}

	resultKey := stringConfig(agent, "result_key", "history")
	resultValue := stringConfig(agent, "result_value", "no history available")
	observation := stringConfig(agent, "observation", "No observation was configured for this agent.")
	hypothesis := stringConfig(agent, "hypothesis", "No hypothesis was configured for this agent.")
	confidence := uint32(intConfig(agent, "confidence", 40))

	arguments := map[string]any{"agent_id": agent.AgentID}
	result := map[string]any{resultKey: resultValue}
	if _, err := st.InsertToolExecution(ctx, task.TaskID, toolName, arguments, result, "SUCCEEDED", time.Since(started)); err != nil {
		return err
	}

	evidenceID, err := st.InsertEvidence(ctx, task.InvestigationID, toolName, result)
	if err != nil {
		return err
	}

	_, err = st.InsertFinding(ctx, task.TaskID, agent.AgentID, []string{evidenceID}, observation, hypothesis, confidence)
	if err != nil {
		return err
	}

	log.Info("investigation task completed", "task_id", task.TaskID, "agent_id", agent.AgentID)
	return nil
}

// checkTool enforces ADR-005 before a tool runs. A denial from the broker,
// and an unreachable broker, are both treated as denied: a capability
// runtime that cannot answer must not be mistaken for one that said yes.
func checkTool(
	ctx context.Context, capability agentv1.CapabilityRuntimeServiceClient, agentID, taskID, toolName string, log *slog.Logger,
) error {
	response, err := capability.CheckToolCapability(ctx, &agentv1.CheckToolCapabilityRequest{
		Identity: &agentv1.AgentIdentity{AgentId: agentID, WorkloadId: agentID},
		ScopeId:  taskID,
		ToolName: toolName,
	})
	if err != nil {
		log.Warn("capability runtime unavailable; denying rather than assuming allowed",
			"agent_id", agentID, "task_id", taskID, "tool", toolName, "error", err)
		return fmt.Errorf("%w: capability runtime unavailable: %v", errCapabilityDenied, err)
	}
	if response.GetVerdict() != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		log.Warn("tool call denied by the capability runtime",
			"agent_id", agentID, "task_id", taskID, "tool", toolName,
			"verdict", response.GetVerdict().String(), "detail", response.GetDetail())
		return fmt.Errorf("%w: %s: %s", errCapabilityDenied, response.GetVerdict().String(), response.GetDetail())
	}
	return nil
}

// stringConfig and intConfig read agent.Configuration generically. A missing
// or mistyped key falls back rather than failing the task: a misconfigured
// mock is a worse test signal than a missing one, never a reason for an
// investigation to error.
func stringConfig(agent *store.AgentDefinition, key, fallback string) string {
	if value, ok := agent.Configuration[key].(string); ok {
		return value
	}
	return fallback
}

func intConfig(agent *store.AgentDefinition, key string, fallback int) int {
	if value, ok := agent.Configuration[key].(float64); ok {
		return int(value)
	}
	return fallback
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
