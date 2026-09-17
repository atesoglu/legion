// Command investigation-controller is Zone 6's case-creation and
// agent-activation loop (ADR-017).
//
// It consumes CaseTrigger messages the orchestrator's lineage writer
// publishes on REVIEW, creates a case and its first investigation, decides
// which agents to activate (rule-based, ADR-017 section 1), and creates
// tasks for the generic worker pool to run. It never decides anything: it
// has no type it can populate with ALLOW/REVIEW/DECLINE, and its output is a
// recommendation, never a reversal of the sentinel's decision (ADR-004).
//
// This is entirely asynchronous and off the 80ms decision path (ADR-009);
// nothing here is ever consulted by the gateway or the orchestrator.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/runtime"
	"github.com/atesoglu/legion/investigation/internal/queue"
	"github.com/atesoglu/legion/investigation/internal/store"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

const (
	defaultListenAddress      = ":9700"
	defaultInvestigationDSN   = "postgres://legion:legion@127.0.0.1:5432/legion?sslmode=disable"
	defaultInvestigationRedis = "127.0.0.1:6381"

	triggerStream   = "investigation.triggers"
	taskStream      = "investigation.tasks"
	controllerGroup = "controller"

	consumerName = "controller-1"
	readCount    = 10
	readBlock    = 2 * time.Second

	// deviceRiskThreshold and velocityRiskThreshold are the two activation
	// rules this cut implements (ADR-017 section 45.1 also gives a
	// relationship rule as an example; no relationship investigation agent is
	// seeded, so no such rule exists yet). One rule per seeded agent, see
	// seedAgents below.
	deviceRiskThreshold   = 50
	velocityRiskThreshold = 50

	deviceInvestigationAgent   = "device_investigation_agent"
	velocityInvestigationAgent = "velocity_investigation_agent"

	defaultMaxAttempts = 3
)

func main() {
	cfg, err := config.LoadService("investigation-controller", defaultListenAddress)
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
		// Not fatal: like every other Legion store, a database that is not
		// yet reachable must not block startup. Every operation below will
		// fail and log until it can be applied.
		log.Warn("investigation schema migration failed; operations will fail until it succeeds", "error", err)
	}
	seedAgents(context.Background(), st, log)

	redisClient := redis.NewClient(&redis.Options{
		Addr: envOr("LEGION_INVESTIGATION_QUEUE", defaultInvestigationRedis),
	})
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Warn("investigation queue close failed", "error", err)
		}
	}()

	triggers := queue.New(redisClient, triggerStream, controllerGroup)
	tasks := queue.New(redisClient, taskStream, "")
	if err := triggers.EnsureGroup(context.Background()); err != nil {
		log.Warn("trigger consumer group could not be ensured at startup; will retry on each read", "error", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	start := func() error {
		runLoop(stop, triggers, tasks, st, log)
		close(done)
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

// seedAgents registers the investigation agents this cut ships.
//
// ADR-017 section 2 describes a RegisterAgent API; it is not built yet, and
// nothing else needs it, so this stands in for it. See
// docs/investigation-model.md and the commit that introduced this package
// for that gap.
//
// Each agent's Configuration carries its mocked tool result and finding
// text (investigation/worker reads it generically) so that no worker code
// branches on agent_id to decide what to say for which agent (ADR-014).
func seedAgents(ctx context.Context, st *store.Store, log *slog.Logger) {
	agents := []store.AgentDefinition{
		{
			AgentID:      deviceInvestigationAgent,
			Version:      "v1",
			Name:         "Device investigation agent",
			Description:  "Investigates device-risk-driven REVIEW cases.",
			SystemPrompt: "not used yet: this agent's inference call is mocked (Phase 3 has no shared inference runtime yet)",
			AllowedTools: []string{"lookup_device_history"},
			Configuration: map[string]any{
				"result_key":   "device_history",
				"result_value": "no prior fraud reports on file",
				"observation":  "Device history shows no prior fraud reports.",
				"hypothesis":   "The elevated device-risk signal is not corroborated by device history.",
				"confidence":   40,
			},
			Enabled: true,
		},
		{
			AgentID:      velocityInvestigationAgent,
			Version:      "v1",
			Name:         "Velocity investigation agent",
			Description:  "Investigates velocity-risk-driven REVIEW cases.",
			SystemPrompt: "not used yet: this agent's inference call is mocked (Phase 3 has no shared inference runtime yet)",
			AllowedTools: []string{"lookup_transaction_velocity"},
			Configuration: map[string]any{
				"result_key":   "velocity_history",
				"result_value": "burst of authorisations in the last 5 minutes matches a card-testing pattern",
				"observation":  "Recent transaction velocity shows a burst of small authorisations.",
				"hypothesis":   "The velocity signal is consistent with card testing rather than a false positive.",
				"confidence":   65,
			},
			Enabled: true,
		},
	}

	for _, agent := range agents {
		if err := st.SeedAgent(ctx, agent); err != nil {
			log.Warn("seeding an investigation agent failed", "agent_id", agent.AgentID, "error", err)
		}
	}
}

// runLoop consumes triggers until stop is closed.
func runLoop(stop <-chan struct{}, triggers, tasks *queue.Stream, st *store.Store, log *slog.Logger) {
	ctx := context.Background()
	for {
		select {
		case <-stop:
			return
		default:
		}

		messages, err := triggers.Read(ctx, consumerName, readCount, readBlock)
		if err != nil {
			log.Warn("trigger read failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		for _, message := range messages {
			handleTrigger(ctx, st, tasks, message.Payload, log)
			if err := triggers.Ack(ctx, message.ID); err != nil {
				log.Warn("trigger ack failed", "id", message.ID, "error", err)
			}
		}
	}
}

// handleTrigger creates a case and its investigation for one CaseTrigger, or
// silently confirms an already-created one: the queue's at-least-once
// delivery (ADR-017 section 3) means the same trigger may be handled more
// than once, and it must never open two cases for one transaction.
func handleTrigger(ctx context.Context, st *store.Store, tasks *queue.Stream, payload []byte, log *slog.Logger) {
	trigger := &investigationv1.CaseTrigger{}
	if err := proto.Unmarshal(payload, trigger); err != nil {
		log.Warn("case trigger is corrupt; dropping", "error", err)
		return
	}

	caseID, created, err := st.CreateCaseIfAbsent(ctx, store.NewCase{
		DecisionID:     trigger.GetDecisionId(),
		TransactionID:  trigger.GetTransactionId().GetValue(),
		IdempotencyKey: trigger.GetIdempotencyKey(),
	})
	if err != nil {
		log.Warn("case creation failed", "decision_id", trigger.GetDecisionId(), "error", err)
		return
	}
	if !created {
		log.Info("duplicate case trigger acknowledged without a new case", "case_id", caseID, "decision_id", trigger.GetDecisionId())
		return
	}

	activated := activateAgents(trigger.GetOutcome())
	investigationID, err := st.CreateInvestigation(ctx, caseID, activated)
	if err != nil {
		log.Warn("investigation creation failed", "case_id", caseID, "error", err)
		return
	}

	auditDetail := map[string]any{"decision_id": trigger.GetDecisionId()}
	if err := st.InsertAuditEvent(ctx, caseID, "CASE_CREATED", auditDetail); err != nil {
		log.Warn("audit event insert failed", "case_id", caseID, "error", err)
	}
	if err := st.InsertAuditEvent(ctx, caseID, "INVESTIGATION_STARTED",
		map[string]any{"investigation_id": investigationID, "activated_agents": activated}); err != nil {
		log.Warn("audit event insert failed", "case_id", caseID, "error", err)
	}

	for _, agentID := range activated {
		createTask(ctx, st, tasks, caseID, investigationID, agentID, log)
	}
}

// activateAgents is the rule-based selection ADR-017 section 45.1
// describes. One rule per seeded agent: device and velocity contributions
// above their thresholds each activate their own investigation agent; a
// relationship rule arrives with the agent it would activate.
func activateAgents(outcome *riskv1.DecisionOutcome) []string {
	var activated []string
	for _, contribution := range outcome.GetContributions() {
		if !contribution.GetIncluded() {
			continue
		}
		switch {
		case contribution.GetAgentId() == "device" && contribution.GetScore() > deviceRiskThreshold:
			activated = append(activated, deviceInvestigationAgent)
		case contribution.GetAgentId() == "velocity" && contribution.GetScore() > velocityRiskThreshold:
			activated = append(activated, velocityInvestigationAgent)
		}
	}
	return activated
}

func createTask(
	ctx context.Context, st *store.Store, tasks *queue.Stream, caseID, investigationID, agentID string, log *slog.Logger,
) {
	taskID, err := st.CreateTask(ctx, store.NewTask{
		InvestigationID: investigationID,
		AgentID:         agentID,
		MaxAttempts:     defaultMaxAttempts,
	})
	if err != nil {
		log.Warn("task creation failed", "investigation_id", investigationID, "agent_id", agentID, "error", err)
		return
	}
	if err := st.InsertAuditEvent(ctx, caseID, "TASK_CREATED", map[string]any{"task_id": taskID, "agent_id": agentID}); err != nil {
		log.Warn("audit event insert failed", "case_id", caseID, "error", err)
	}

	body, err := proto.Marshal(&investigationv1.Task{
		TaskId:          taskID,
		InvestigationId: investigationID,
		AgentId:         agentID,
	})
	if err != nil {
		log.Warn("task envelope could not be serialised", "task_id", taskID, "error", err)
		return
	}
	if err := tasks.Publish(ctx, body); err != nil {
		log.Warn("task publish failed", "task_id", taskID, "error", err)
	}
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
