// Package store persists Zone 6's state in PostgreSQL (ADR-017 §1.4).
//
// This schema is separate from decision_lineage/ADR-018's reproducibility
// tables: a case evolves after the decision that created it, and lineage must
// never be mutated once written. Postgres is deliberately the source of truth
// for every status transition here; the task queue (investigation/internal/
// queue) is delivery only, never authoritative (ADR-017 §3).
package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/atesoglu/legion/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsTable is private to the investigation schema (ADR-017): lineage
// (ADR-018) and investigation may share a Postgres instance, and each
// schema's migration history must not collide with the other's.
const migrationsTable = "schema_migrations_investigation"

// Task, investigation and case statuses. Plain strings rather than the
// proto enum's String() form (which carries a "TASK_STATUS_" prefix): these
// are a Postgres-only concern, not a wire format.
const (
	CaseOpen          = "OPEN"
	CaseInvestigating = "INVESTIGATING"
	CaseCompleted     = "COMPLETED"

	InvestigationRunning   = "RUNNING"
	InvestigationCompleted = "COMPLETED"

	TaskPending    = "PENDING"
	TaskRunning    = "RUNNING"
	TaskRetrying   = "RETRYING"
	TaskCompleted  = "COMPLETED"
	TaskFailed     = "FAILED"
	TaskDeadLetter = "DEAD_LETTER"
)

// terminal task states: once in one of these, a task never transitions again.
//
// RETRYING is deliberately absent. It is the state of a task whose attempt
// failed for a reason another attempt might survive; FAILED is reserved for
// a failure no retry can change, and DEAD_LETTER for one that ran out of
// attempts trying.
var terminalTaskStates = map[string]bool{
	TaskCompleted:  true,
	TaskFailed:     true,
	TaskDeadLetter: true,
}

// writeTimeout bounds one statement, so an unreachable database degrades a
// consumer's loop rather than leaking a goroutine on it forever.
const writeTimeout = 5 * time.Second

// Store is the Postgres-backed persistence for Zone 6.
type Store struct {
	pool *pgxpool.Pool
	dsn  string
}

// Open connects without verifying reachability: like every other Legion
// store, a database that is down must not block startup.
func Open(dsn string) (*Store, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool, dsn: dsn}, nil
}

// ApplyMigrations applies every pending migration (ADR-018 retires the
// informal idempotent-DDL approach this used to be; investigation adopts
// the same real migration tool lineage does). Call it once at startup; a
// failure here means every operation below will fail until it succeeds.
func (s *Store) ApplyMigrations() error {
	return migrate.Run(migrationsFS, s.dsn, migrationsTable)
}

// Close releases the pool.
func (s *Store) Close() {
	s.pool.Close()
}

// newID mirrors the orchestrator's own newDecisionID: a caller-opaque
// identifier is assigned here, not derived from anything a caller supplied.
func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("store: could not generate an identifier: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func marshalJSON(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

// AgentDefinition is one row of agent_definitions (ADR-017 §2).
type AgentDefinition struct {
	AgentID      string
	Version      string
	Name         string
	Description  string
	SystemPrompt string
	AllowedTools []string

	// Configuration is free-form, agent-specific data (ADR-017's
	// `configuration` jsonb column). Today it carries the mocked worker's
	// canned tool result and finding, so that no worker code branches on
	// agent_id (ADR-014) to decide what to say for which agent.
	Configuration map[string]any
	Enabled       bool
}

// SeedAgent registers an agent definition if it does not already exist.
//
// This stands in for ADR-017's RegisterAgent API, which is not built yet
// (no caller needs it beyond bootstrapping the first agent) — see
// docs/investigation-model.md and the commit that introduced this package
// for that gap.
func (s *Store) SeedAgent(ctx context.Context, def AgentDefinition) error {
	tools, err := marshalJSON(def.AllowedTools)
	if err != nil {
		return fmt.Errorf("store: allowed_tools: %w", err)
	}
	configuration, err := marshalJSON(def.Configuration)
	if err != nil {
		return fmt.Errorf("store: configuration: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_definitions (agent_id, version, name, description, system_prompt, allowed_tools, configuration, enabled)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8)
		ON CONFLICT (agent_id, version) DO NOTHING`,
		def.AgentID, def.Version, def.Name, def.Description, def.SystemPrompt, string(tools), string(configuration), def.Enabled)
	if err != nil {
		return fmt.Errorf("store: seed agent %q: %w", def.AgentID, err)
	}
	return nil
}

// GetEnabledAgent returns the most recently created enabled definition for
// agent_id, or (nil, nil) if none is enabled.
func (s *Store) GetEnabledAgent(ctx context.Context, agentID string) (*AgentDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	row := s.pool.QueryRow(ctx, `
		SELECT agent_id, version, name, description, system_prompt, allowed_tools, configuration
		FROM agent_definitions
		WHERE agent_id = $1 AND enabled
		ORDER BY created_at DESC
		LIMIT 1`, agentID)

	var (
		def           AgentDefinition
		tools         []byte
		configuration []byte
	)
	if err := row.Scan(&def.AgentID, &def.Version, &def.Name, &def.Description, &def.SystemPrompt, &tools, &configuration); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get agent %q: %w", agentID, err)
	}
	def.Enabled = true
	if err := json.Unmarshal(tools, &def.AllowedTools); err != nil {
		return nil, fmt.Errorf("store: agent %q allowed_tools is corrupt: %w", agentID, err)
	}
	if err := json.Unmarshal(configuration, &def.Configuration); err != nil {
		return nil, fmt.Errorf("store: agent %q configuration is corrupt: %w", agentID, err)
	}
	return &def, nil
}

// NewCase describes a case not yet created.
type NewCase struct {
	DecisionID     string
	TransactionID  string
	IdempotencyKey string
}

// CreateCaseIfAbsent creates a case, or returns the existing one.
//
// It is idempotent on idempotency_key alone (see CaseTrigger's proto
// comment for why that is narrower than the gateway's (caller, key) scope,
// and the known gap that follows from it): the queue's at-least-once
// delivery (ADR-017 §3) must never be allowed to open two cases for one
// transaction.
func (s *Store) CreateCaseIfAbsent(ctx context.Context, nc NewCase) (caseID string, created bool, err error) {
	caseID, err = newID()
	if err != nil {
		return "", false, err
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	var returnedID string
	row := s.pool.QueryRow(ctx, `
		INSERT INTO cases (case_id, decision_id, transaction_id, idempotency_key, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING case_id`,
		caseID, nc.DecisionID, nc.TransactionID, nc.IdempotencyKey, CaseOpen)

	switch err := row.Scan(&returnedID); err {
	case nil:
		return returnedID, true, nil
	case pgx.ErrNoRows:
		// Already exists: this is a resubmission of the same trigger, not a
		// new case. Look up the one that owns this key.
		existing, lookupErr := s.pool.Query(ctx, `SELECT case_id FROM cases WHERE idempotency_key = $1`, nc.IdempotencyKey)
		if lookupErr != nil {
			return "", false, fmt.Errorf("store: look up existing case: %w", lookupErr)
		}
		defer existing.Close()
		if !existing.Next() {
			return "", false, fmt.Errorf("store: case for idempotency_key %q vanished after conflict", nc.IdempotencyKey)
		}
		if scanErr := existing.Scan(&returnedID); scanErr != nil {
			return "", false, fmt.Errorf("store: scan existing case: %w", scanErr)
		}
		return returnedID, false, nil
	default:
		return "", false, fmt.Errorf("store: create case: %w", err)
	}
}

// CreateInvestigation starts the one investigation a freshly created case
// gets (ADR-017 §1: a case may have more than one over time, but this
// package does not yet implement re-opening one).
func (s *Store) CreateInvestigation(ctx context.Context, caseID string, activatedAgentIDs []string) (string, error) {
	investigationID, err := newID()
	if err != nil {
		return "", err
	}
	activated, err := marshalJSON(activatedAgentIDs)
	if err != nil {
		return "", fmt.Errorf("store: activated_agent_ids: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO investigations (investigation_id, case_id, status, activated_agent_ids)
		VALUES ($1, $2, $3, $4::jsonb)`,
		investigationID, caseID, InvestigationRunning, string(activated))
	if err != nil {
		return "", fmt.Errorf("store: create investigation: %w", err)
	}

	_, err = s.pool.Exec(ctx, `UPDATE cases SET status = $1, updated_at = now() WHERE case_id = $2`,
		CaseInvestigating, caseID)
	if err != nil {
		return "", fmt.Errorf("store: mark case investigating: %w", err)
	}
	return investigationID, nil
}

// NewTask describes a task not yet created.
type NewTask struct {
	InvestigationID string
	AgentID         string
	MaxAttempts     uint32
}

// CreateTask inserts one PENDING task for one activated agent.
func (s *Store) CreateTask(ctx context.Context, nt NewTask) (string, error) {
	taskID, err := newID()
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tasks (task_id, investigation_id, agent_id, status, max_attempts)
		VALUES ($1, $2, $3, $4, $5)`,
		taskID, nt.InvestigationID, nt.AgentID, TaskPending, nt.MaxAttempts)
	if err != nil {
		return "", fmt.Errorf("store: create task: %w", err)
	}
	return taskID, nil
}

// Task is one row of tasks, as seen by the worker.
type Task struct {
	TaskID          string
	InvestigationID string
	AgentID         string
	Status          string
	Attempt         uint32
	MaxAttempts     uint32
}

// GetTask reads one task by ID.
func (s *Store) GetTask(ctx context.Context, taskID string) (*Task, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	row := s.pool.QueryRow(ctx, `
		SELECT task_id, investigation_id, agent_id, status, attempt, max_attempts
		FROM tasks WHERE task_id = $1`, taskID)

	var t Task
	if err := row.Scan(&t.TaskID, &t.InvestigationID, &t.AgentID, &t.Status, &t.Attempt, &t.MaxAttempts); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get task %q: %w", taskID, err)
	}
	return &t, nil
}

// IsTerminal reports whether a task status never transitions again.
func IsTerminal(status string) bool {
	return terminalTaskStates[status]
}

// ClaimTask transitions a task to RUNNING and increments its attempt count,
// returning the new attempt number. The worker must call this before doing
// any work, so a crash between claiming and finishing is visible in Postgres
// rather than silently lost (ADR-017 §3).
func (s *Store) ClaimTask(ctx context.Context, taskID string) (uint32, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	row := s.pool.QueryRow(ctx, `
		UPDATE tasks SET status = $1, attempt = attempt + 1, updated_at = now()
		WHERE task_id = $2
		RETURNING attempt`,
		TaskRunning, taskID)

	var attempt uint32
	if err := row.Scan(&attempt); err != nil {
		return 0, fmt.Errorf("store: claim task %q: %w", taskID, err)
	}
	return attempt, nil
}

// CompleteTask marks a task COMPLETED.
func (s *Store) CompleteTask(ctx context.Context, taskID string) error {
	return s.setTaskStatus(ctx, taskID, TaskCompleted)
}

// FailTask records why a task stopped: TaskRetrying when another attempt is
// owed, TaskFailed when no retry can change the outcome, TaskDeadLetter when
// the attempts are spent.
func (s *Store) FailTask(ctx context.Context, taskID, status string) error {
	return s.setTaskStatus(ctx, taskID, status)
}

func (s *Store) setTaskStatus(ctx context.Context, taskID, status string) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE tasks SET status = $1, updated_at = now() WHERE task_id = $2`, status, taskID)
	if err != nil {
		return fmt.Errorf("store: set task %q status %q: %w", taskID, status, err)
	}
	return nil
}

// OpenTaskCount returns how many tasks for an investigation have not yet
// reached a terminal state, so a worker can tell whether it just finished
// the last one. A RETRYING task is open: its investigation is not finished
// until the retry resolves one way or the other.
func (s *Store) OpenTaskCount(ctx context.Context, investigationID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	row := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM tasks
		WHERE investigation_id = $1 AND status NOT IN ($2, $3, $4)`,
		investigationID, TaskCompleted, TaskFailed, TaskDeadLetter)

	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count open tasks for %q: %w", investigationID, err)
	}
	return count, nil
}

// CompleteInvestigationIfDone marks an investigation and its case COMPLETED
// once every task belonging to it has reached a terminal state. It is safe
// to call after every task completion; it is a no-op (completed == false)
// until the last one finishes.
//
// completed reports the TRANSITION, not the state: a second call for an
// investigation already marked COMPLETED returns false. The worker keys its
// INVESTIGATION_COMPLETED audit event off that, so a redelivered task
// (at-least-once, ADR-017 §3) cannot append a duplicate audit record.
func (s *Store) CompleteInvestigationIfDone(ctx context.Context, investigationID string) (completed bool, caseID string, err error) {
	openTasks, err := s.OpenTaskCount(ctx, investigationID)
	if err != nil {
		return false, "", err
	}
	if openTasks > 0 {
		return false, "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	row := s.pool.QueryRow(ctx, `SELECT case_id FROM investigations WHERE investigation_id = $1`, investigationID)
	if err := row.Scan(&caseID); err != nil {
		return false, "", fmt.Errorf("store: look up case for investigation %q: %w", investigationID, err)
	}

	// The case is updated first so that a failure between the two writes
	// leaves the investigation un-transitioned, and therefore retryable.
	_, err = s.pool.Exec(ctx, `UPDATE cases SET status = $1, updated_at = now() WHERE case_id = $2`,
		CaseCompleted, caseID)
	if err != nil {
		return false, "", fmt.Errorf("store: complete case %q: %w", caseID, err)
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE investigations SET status = $1, completed_at = now()
		WHERE investigation_id = $2 AND status <> $1`,
		InvestigationCompleted, investigationID)
	if err != nil {
		return false, "", fmt.Errorf("store: complete investigation %q: %w", investigationID, err)
	}
	return tag.RowsAffected() > 0, caseID, nil
}

// InsertToolExecution records one tool call a worker made on an agent's
// behalf, independent of what the agent concluded from it.
func (s *Store) InsertToolExecution(
	ctx context.Context, taskID, toolName string, arguments, result map[string]any, status string, duration time.Duration,
) (string, error) {
	toolExecutionID, err := newID()
	if err != nil {
		return "", err
	}
	args, err := marshalJSON(arguments)
	if err != nil {
		return "", fmt.Errorf("store: tool execution arguments: %w", err)
	}
	res, err := marshalJSON(result)
	if err != nil {
		return "", fmt.Errorf("store: tool execution result: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tool_executions (tool_execution_id, task_id, tool_name, arguments, result, status, duration_ms)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7)`,
		toolExecutionID, taskID, toolName, string(args), string(res), status, duration.Milliseconds())
	if err != nil {
		return "", fmt.Errorf("store: insert tool execution: %w", err)
	}
	return toolExecutionID, nil
}

// InsertEvidence records one fact gathered during investigation, independent
// of any agent's interpretation of it.
func (s *Store) InsertEvidence(ctx context.Context, investigationID, source string, content map[string]any) (string, error) {
	evidenceID, err := newID()
	if err != nil {
		return "", err
	}
	body, err := marshalJSON(content)
	if err != nil {
		return "", fmt.Errorf("store: evidence content: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO evidence (evidence_id, investigation_id, source, content)
		VALUES ($1, $2, $3, $4::jsonb)`,
		evidenceID, investigationID, source, string(body))
	if err != nil {
		return "", fmt.Errorf("store: insert evidence: %w", err)
	}
	return evidenceID, nil
}

// InsertFinding records one agent's observation, hypothesis and confidence,
// referencing the evidence it is based on.
func (s *Store) InsertFinding(
	ctx context.Context, taskID, agentID string, evidenceIDs []string, observation, hypothesis string, confidence uint32,
) (string, error) {
	findingID, err := newID()
	if err != nil {
		return "", err
	}
	ids, err := marshalJSON(evidenceIDs)
	if err != nil {
		return "", fmt.Errorf("store: evidence_ids: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_findings (finding_id, task_id, agent_id, evidence_ids, observation, hypothesis, confidence)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)`,
		findingID, taskID, agentID, string(ids), observation, hypothesis, confidence)
	if err != nil {
		return "", fmt.Errorf("store: insert finding: %w", err)
	}
	return findingID, nil
}

// InsertAuditEvent appends one record to the investigation's audit trail.
func (s *Store) InsertAuditEvent(ctx context.Context, caseID, eventType string, detail map[string]any) error {
	eventID, err := newID()
	if err != nil {
		return err
	}
	body, err := marshalJSON(detail)
	if err != nil {
		return fmt.Errorf("store: audit detail: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO investigation_audit_events (event_id, case_id, event_type, detail)
		VALUES ($1, $2, $3, $4::jsonb)`,
		eventID, caseID, eventType, string(body))
	if err != nil {
		return fmt.Errorf("store: insert audit event %q: %w", eventType, err)
	}
	return nil
}
