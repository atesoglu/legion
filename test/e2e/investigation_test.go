//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// startPipelineWithInvestigation is startPipeline plus the investigation
// plane (ADR-017): a real PostgreSQL store (shared with lineage, per ADR-017
// section 1.4 -- "may share a Postgres instance initially"), a separate
// in-process Redis for the task queue, and the controller and worker
// processes wired to both.
func startPipelineWithInvestigation(t *testing.T, history []storedFeature) (*harness, string) {
	t.Helper()

	h := newHarness(t)
	dsn := h.startLineageStore()

	store := h.startFeatureStore(history)
	dedupStore := h.startDedupStore()
	investigationQueue := h.startInvestigationQueue()

	sentinel := h.startRust("legion-sentinel", "sentinel")
	velocity := h.startRust("legion-velocity", "velocity")
	device := h.startRust("legion-device", "device")
	geo := h.startRust("legion-geo", "geo")

	orchestrator := h.startGo("control-plane/orchestrator", "orchestrator", []string{
		"LEGION_AGENT_ENDPOINTS=velocity=" + velocity + ",device=" + device + ",geo=" + geo,
		"LEGION_SENTINEL_ENDPOINT=" + sentinel,
		"LEGION_FEATURE_STORE=" + store,
		"LEGION_LINEAGE_STORE=" + dsn,
		"LEGION_INVESTIGATION_QUEUE=" + investigationQueue,
	})

	h.startGo("gateway", "gateway", []string{
		"LEGION_ORCHESTRATOR_ENDPOINT=" + orchestrator,
		"LEGION_API_KEYS=e2e-caller=" + apiKey,
		"LEGION_IDEMPOTENCY_STORE=" + dedupStore,
	})

	h.startInvestigationService("investigation/controller", "investigation-controller", []string{
		"LEGION_INVESTIGATION_STORE=" + dsn,
		"LEGION_INVESTIGATION_QUEUE=" + investigationQueue,
	})
	h.startInvestigationService("investigation/worker", "investigation-worker", []string{
		"LEGION_INVESTIGATION_STORE=" + dsn,
		"LEGION_INVESTIGATION_QUEUE=" + investigationQueue,
	})

	h.waitReady()
	return h, dsn
}

// caseStatus polls for the status of the case opened for decisionID. Case
// creation and investigation completion both happen off the orchestrator's
// critical path (ADR-017), through two queue hops and a worker's mocked tool
// call, so this polls rather than assuming the row exists or is complete the
// instant the caller's response returns.
func caseStatus(t *testing.T, dsn, decisionID string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the investigation store: %v", err)
	}
	defer pool.Close()

	deadline := time.Now().Add(20 * time.Second)
	var last string
	for {
		var status string
		err := pool.QueryRow(ctx,
			`SELECT status FROM cases WHERE decision_id = $1`, decisionID).Scan(&status)
		if err == nil {
			last = status
			if status == "COMPLETED" {
				return status
			}
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func findingCount(t *testing.T, dsn, decisionID string) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the investigation store: %v", err)
	}
	defer pool.Close()

	var count int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_findings f
		JOIN tasks t ON t.task_id = f.task_id
		JOIN investigations i ON i.investigation_id = t.investigation_id
		JOIN cases c ON c.case_id = i.case_id
		WHERE c.decision_id = $1`, decisionID).Scan(&count)
	if err != nil {
		t.Fatalf("counting findings: %v", err)
	}
	return count
}

// TestAReviewDecisionProducesACompletedInvestigation is the critical
// acceptance test investigation-model.md section 9 describes, scoped to the
// one seeded agent this first slice ships: a transaction with both a high
// device-risk signal and a card-testing velocity pattern reaches REVIEW,
// which the orchestrator's lineage writer turns into a CaseTrigger, which
// the controller turns into a case, an investigation and a task, which the
// worker runs (with a mocked tool/agent call, since neither the capability
// runtime nor Phase 3 inference exist yet) through to a completed
// investigation with a recorded finding.
func TestAReviewDecisionProducesACompletedInvestigation(t *testing.T) {
	h, dsn := startPipelineWithInvestigation(t, cardTestingHistory())

	response := decide(t, h, compromisedDevice())
	if response.GetOutcome().GetDecision() != riskv1.Decision_DECISION_REVIEW {
		t.Fatalf("decision = %v, want REVIEW (this test's fixture must reach REVIEW for the rest of it to mean anything)",
			response.GetOutcome().GetDecision())
	}

	status := caseStatus(t, dsn, response.GetDecisionId())
	if status != "COMPLETED" {
		t.Fatalf("case status = %q, want COMPLETED", status)
	}

	if count := findingCount(t, dsn, response.GetDecisionId()); count == 0 {
		t.Fatal("the investigation completed with no findings recorded")
	}
}

// TestAnAllowDecisionOpensNoCase proves the trigger is conditional on REVIEW:
// an unremarkable transaction must never cost Zone 6 a case.
func TestAnAllowDecisionOpensNoCase(t *testing.T) {
	h, dsn := startPipelineWithInvestigation(t, settledHistory())

	response := decide(t, h, transaction())
	if response.GetOutcome().GetDecision() != riskv1.Decision_DECISION_ALLOW {
		t.Fatalf("decision = %v, want ALLOW", response.GetOutcome().GetDecision())
	}

	// Nothing should ever appear for this decision; a short, fixed wait
	// (rather than the completion-polling helper above, which is built to
	// wait for a case that is expected to exist) is appropriate precisely
	// because the assertion here is an absence.
	time.Sleep(2 * time.Second)
	if exists := caseExists(t, dsn, response.GetDecisionId()); exists {
		t.Fatal("a case was opened for an ALLOW decision")
	}
}

// caseExists reports whether any case row exists for decisionID, with no
// retry: used only to assert an absence, where waiting would just make a
// failing test slower without making a passing one more trustworthy.
func caseExists(t *testing.T, dsn, decisionID string) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the investigation store: %v", err)
	}
	defer pool.Close()

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM cases WHERE decision_id = $1`, decisionID).Scan(&status)
	return err == nil
}
