package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/investigation/internal/queue"
	"github.com/atesoglu/legion/investigation/internal/store"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeCapabilityClient struct {
	verdict agentv1.CapabilityVerdict
	detail  string
	err     error
}

func (f *fakeCapabilityClient) CheckCapability(
	context.Context, *agentv1.CheckCapabilityRequest, ...grpc.CallOption,
) (*agentv1.CheckCapabilityResponse, error) {
	return nil, errors.New("not used by these tests")
}

func (f *fakeCapabilityClient) CheckToolCapability(
	_ context.Context, _ *agentv1.CheckToolCapabilityRequest, _ ...grpc.CallOption,
) (*agentv1.CheckToolCapabilityResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &agentv1.CheckToolCapabilityResponse{Verdict: f.verdict, Detail: f.detail}, nil
}

func TestCheckToolAllowsAGrantedTool(t *testing.T) {
	client := &fakeCapabilityClient{verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_device_history", testLogger())
	if err != nil {
		t.Fatalf("checkTool: %v", err)
	}
}

func TestCheckToolDeniesOnAnExplicitDenial(t *testing.T) {
	client := &fakeCapabilityClient{
		verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED,
		detail:  "tool not granted",
	}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_transaction_velocity", testLogger())
	if !errors.Is(err, errCapabilityDenied) {
		t.Fatalf("checkTool error = %v, want errCapabilityDenied", err)
	}
}

func TestCheckToolFailsClosedWhenTheRuntimeIsUnreachable(t *testing.T) {
	client := &fakeCapabilityClient{err: errors.New("connection refused")}

	err := checkTool(context.Background(), client, "device_investigation_agent", "task-1", "lookup_device_history", testLogger())
	if !errors.Is(err, errCapabilityDenied) {
		t.Fatalf("checkTool error = %v, want errCapabilityDenied (a broker that cannot answer must not be mistaken for one that said yes)", err)
	}
}

// fakeStore records what a delivery did to persistence, and can be told to
// fail any single call, which is how these tests stand in for an outage the
// e2e suite cannot manufacture (a Postgres that dies mid-task).
type fakeStore struct {
	task  *store.Task
	agent *store.AgentDefinition

	getTaskErr       error
	claimErr         error
	agentErr         error
	completeTaskErr  error
	failErr          error
	investigationErr error

	investigationCompleted bool

	claims               int
	completions          int
	failures             []bool
	findings             int
	investigationChecks  int
	auditEvents          int
	deniedToolExecutions int
}

func (f *fakeStore) GetTask(context.Context, string) (*store.Task, error) {
	if f.getTaskErr != nil {
		return nil, f.getTaskErr
	}
	return f.task, nil
}

func (f *fakeStore) ClaimTask(context.Context, string) (uint32, error) {
	if f.claimErr != nil {
		return 0, f.claimErr
	}
	f.claims++
	return 1, nil
}

func (f *fakeStore) CompleteTask(context.Context, string) error {
	if f.completeTaskErr != nil {
		return f.completeTaskErr
	}
	f.completions++
	return nil
}

func (f *fakeStore) FailTask(_ context.Context, _ string, deadLetter bool) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.failures = append(f.failures, deadLetter)
	return nil
}

func (f *fakeStore) GetEnabledAgent(context.Context, string) (*store.AgentDefinition, error) {
	if f.agentErr != nil {
		return nil, f.agentErr
	}
	return f.agent, nil
}

func (f *fakeStore) CompleteInvestigationIfDone(context.Context, string) (bool, string, error) {
	if f.investigationErr != nil {
		return false, "", f.investigationErr
	}
	f.investigationChecks++
	return f.investigationCompleted, "case-1", nil
}

func (f *fakeStore) InsertAuditEvent(context.Context, string, string, map[string]any) error {
	f.auditEvents++
	return nil
}

func (f *fakeStore) InsertToolExecution(
	_ context.Context, _, _ string, _, _ map[string]any, status string, _ time.Duration,
) (string, error) {
	if status == "DENIED" {
		f.deniedToolExecutions++
	}
	return "tool-execution-1", nil
}

func (f *fakeStore) InsertEvidence(context.Context, string, string, map[string]any) (string, error) {
	return "evidence-1", nil
}

func (f *fakeStore) InsertFinding(
	context.Context, string, string, []string, string, string, uint32,
) (string, error) {
	f.findings++
	return "finding-1", nil
}

func runnableTask() *store.Task {
	return &store.Task{
		TaskID:          "task-1",
		InvestigationID: "investigation-1",
		AgentID:         "device_investigation_agent",
		Status:          store.TaskPending,
		Attempt:         0,
		MaxAttempts:     3,
	}
}

func seededAgent() *store.AgentDefinition {
	return &store.AgentDefinition{
		AgentID:      "device_investigation_agent",
		AllowedTools: []string{"lookup_device_history"},
		Enabled:      true,
	}
}

func allowingCapability() *fakeCapabilityClient {
	return &fakeCapabilityClient{verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED}
}

func taskPayload(t *testing.T, taskID string) []byte {
	t.Helper()
	payload, err := proto.Marshal(&investigationv1.Task{TaskId: taskID})
	if err != nil {
		t.Fatalf("marshal task envelope: %v", err)
	}
	return payload
}

func handle(st taskStore, capability agentv1.CapabilityRuntimeServiceClient, payload []byte) disposition {
	return handleTask(context.Background(), st, capability, payload, testLogger())
}

func TestACorruptEnvelopeIsSettledRatherThanRedelivered(t *testing.T) {
	st := &fakeStore{}

	// Wire type 7 does not exist, so this can never become a valid Task.
	if got := handle(st, allowingCapability(), []byte{0xff, 0xff, 0xff}); got != settled {
		t.Fatalf("disposition = %v, want settled (redelivery cannot fix an unparseable payload)", got)
	}
}

func TestAnUnknownTaskIsSettled(t *testing.T) {
	st := &fakeStore{task: nil}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != settled {
		t.Fatalf("disposition = %v, want settled", got)
	}
}

func TestATaskLookupOutageIsNotAcknowledged(t *testing.T) {
	st := &fakeStore{getTaskErr: errors.New("connection refused")}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != retry {
		t.Fatalf("disposition = %v, want retry (a task the worker never read must not be dropped)", got)
	}
}

func TestAClaimOutageIsNotAcknowledged(t *testing.T) {
	st := &fakeStore{task: runnableTask(), agent: seededAgent(), claimErr: errors.New("connection refused")}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != retry {
		t.Fatalf("disposition = %v, want retry", got)
	}
	if st.findings != 0 {
		t.Fatalf("findings = %d, want 0: no agent may run for an unclaimed task", st.findings)
	}
}

func TestACompletionOutageIsNotAcknowledged(t *testing.T) {
	st := &fakeStore{
		task:            runnableTask(),
		agent:           seededAgent(),
		completeTaskErr: errors.New("connection refused"),
	}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != retry {
		t.Fatalf("disposition = %v, want retry (a task stuck RUNNING must be redelivered, not dropped)", got)
	}
}

func TestAnInvestigationCheckOutageIsNotAcknowledged(t *testing.T) {
	st := &fakeStore{
		task:             runnableTask(),
		agent:            seededAgent(),
		investigationErr: errors.New("connection refused"),
	}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != retry {
		t.Fatalf("disposition = %v, want retry", got)
	}
}

func TestAFailureThatCannotBeRecordedIsNotAcknowledged(t *testing.T) {
	st := &fakeStore{task: runnableTask(), agent: nil, failErr: errors.New("connection refused")}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != retry {
		t.Fatalf("disposition = %v, want retry (nothing durable records the failure)", got)
	}
}

func TestASuccessfulTaskIsAcknowledgedAndClosesItsInvestigation(t *testing.T) {
	st := &fakeStore{task: runnableTask(), agent: seededAgent(), investigationCompleted: true}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != settled {
		t.Fatalf("disposition = %v, want settled", got)
	}
	if st.claims != 1 || st.completions != 1 || st.findings != 1 {
		t.Fatalf("claims=%d completions=%d findings=%d, want 1/1/1", st.claims, st.completions, st.findings)
	}
	if st.auditEvents != 1 {
		t.Fatalf("auditEvents = %d, want 1", st.auditEvents)
	}
}

func TestAFailedTaskStillClosesItsInvestigation(t *testing.T) {
	// An agent that is not registered fails its task permanently. The
	// investigation it belonged to is just as finished as a successful one,
	// so the completion check must still run or the case hangs forever.
	st := &fakeStore{task: runnableTask(), agent: nil, investigationCompleted: true}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != settled {
		t.Fatalf("disposition = %v, want settled", got)
	}
	if len(st.failures) != 1 || !st.failures[0] {
		t.Fatalf("failures = %v, want one dead-lettered task", st.failures)
	}
	if st.investigationChecks != 1 {
		t.Fatalf("investigationChecks = %d, want 1", st.investigationChecks)
	}
}

func TestARedeliveredTerminalTaskRerunsOnlyTheCompletionCheck(t *testing.T) {
	task := runnableTask()
	task.Status = store.TaskCompleted
	st := &fakeStore{task: task, agent: seededAgent()}

	if got := handle(st, allowingCapability(), taskPayload(t, "task-1")); got != settled {
		t.Fatalf("disposition = %v, want settled", got)
	}
	if st.claims != 0 || st.findings != 0 {
		t.Fatalf("claims=%d findings=%d, want 0/0: a terminal task must not run its agent again", st.claims, st.findings)
	}
	if st.investigationChecks != 1 {
		t.Fatalf("investigationChecks = %d, want 1: the previous delivery may have died before closing out", st.investigationChecks)
	}
}

func TestADeniedToolDeadLettersWithoutRetrying(t *testing.T) {
	st := &fakeStore{task: runnableTask(), agent: seededAgent()}
	denied := &fakeCapabilityClient{
		verdict: agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED,
		detail:  "tool not granted",
	}

	if got := handle(st, denied, taskPayload(t, "task-1")); got != settled {
		t.Fatalf("disposition = %v, want settled (a policy denial is not a transient fault)", got)
	}
	if len(st.failures) != 1 || !st.failures[0] {
		t.Fatalf("failures = %v, want one dead-lettered task", st.failures)
	}
	if st.deniedToolExecutions != 1 {
		t.Fatalf("deniedToolExecutions = %d, want 1: a denial must still be auditable", st.deniedToolExecutions)
	}
}

// fakeQueue delivers a fixed set of batches and then stops the loop, so
// runLoop's acknowledgement decision can be observed without a live Redis.
type fakeQueue struct {
	batches [][]queue.Message
	read    int
	acked   []string
	stop    chan struct{}
	drained bool
}

func (f *fakeQueue) Read(context.Context, string, int64, time.Duration) ([]queue.Message, error) {
	if f.read < len(f.batches) {
		batch := f.batches[f.read]
		f.read++
		return batch, nil
	}
	if !f.drained {
		f.drained = true
		close(f.stop)
	}
	return nil, nil
}

func (f *fakeQueue) Reclaim(context.Context, string, time.Duration, int64) ([]queue.Message, error) {
	return nil, nil
}

func (f *fakeQueue) Ack(_ context.Context, id string) error {
	f.acked = append(f.acked, id)
	return nil
}

func drainLoop(t *testing.T, st taskStore, payload []byte) *fakeQueue {
	t.Helper()
	q := &fakeQueue{
		batches: [][]queue.Message{{{ID: "1-0", Payload: payload}}},
		stop:    make(chan struct{}),
	}
	runLoop(q.stop, "test-consumer", q, st, allowingCapability(), testLogger())
	return q
}

func TestRunLoopLeavesAnUnfinishedTaskUnacknowledged(t *testing.T) {
	st := &fakeStore{getTaskErr: errors.New("connection refused")}

	q := drainLoop(t, st, taskPayload(t, "task-1"))

	if len(q.acked) != 0 {
		t.Fatalf("acked = %v, want none: acknowledging here drops the task instead of redelivering it", q.acked)
	}
}

func TestRunLoopAcknowledgesAFinishedTask(t *testing.T) {
	st := &fakeStore{task: runnableTask(), agent: seededAgent()}

	q := drainLoop(t, st, taskPayload(t, "task-1"))

	if len(q.acked) != 1 || q.acked[0] != "1-0" {
		t.Fatalf("acked = %v, want [1-0]", q.acked)
	}
}
