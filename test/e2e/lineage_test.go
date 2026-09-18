//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// startPipelineWithLineage is startPipeline plus a real PostgreSQL lineage
// store, wired to the orchestrator exactly as it is in a deployment.
func startPipelineWithLineage(t *testing.T, history []storedFeature) (*harness, string) {
	t.Helper()

	h := newHarness(t)
	dsn := h.startLineageStore()

	// startPipeline would build a second harness; the lineage store must be
	// part of the same one so its container is cleaned up alongside the rest.
	store := h.startFeatureStore(history)
	dedupStore := h.startDedupStore()
	sentinel := h.startRust("legion-sentinel", "sentinel")
	velocity := h.startRust("legion-velocity", "velocity")
	device := h.startRust("legion-device", "device")
	geo := h.startRust("legion-geo", "geo")

	orchestrator := h.startGo("control-plane/orchestrator", "orchestrator", []string{
		"LEGION_AGENT_ENDPOINTS=velocity=" + velocity + ",device=" + device + ",geo=" + geo,
		"LEGION_SENTINEL_ENDPOINT=" + sentinel,
		"LEGION_FEATURE_STORE=" + store,
		"LEGION_LINEAGE_STORE=" + dsn,
	})

	h.startGo("gateway", "gateway", []string{
		"LEGION_ORCHESTRATOR_ENDPOINT=" + orchestrator,
		"LEGION_API_KEYS=e2e-caller=" + apiKey,
		"LEGION_IDEMPOTENCY_STORE=" + dedupStore,
	})

	h.waitReady()
	return h, dsn
}

// decideWithLineage is decide, but asking for the lineage inline.
func decideWithLineage(t *testing.T, h *harness, subject *riskv1.Transaction) *gatewayv1.EvaluateTransactionResponse {
	t.Helper()

	ctx, cancel := authenticated(t)
	defer cancel()

	response, err := gatewayClient(t, h).EvaluateTransaction(ctx, &gatewayv1.EvaluateTransactionRequest{
		Transaction: subject,
		Options:     &gatewayv1.EvaluationOptions{IncludeLineage: true},
	})
	if err != nil {
		t.Fatalf("EvaluateTransaction: %v", err)
	}
	return response
}

// fetchStoredLineage reads back the row the orchestrator's background writer
// persisted. Persistence happens off the hot path, so this polls briefly
// rather than assuming the row exists the instant the response returns.
func fetchStoredLineage(t *testing.T, dsn, decisionID string) *riskv1.DecisionLineage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the lineage store: %v", err)
	}
	defer pool.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var payload []byte
		err := pool.QueryRow(ctx,
			`SELECT raw_lineage FROM decisions WHERE id = $1`, decisionID).Scan(&payload)
		if err == nil {
			var entry riskv1.DecisionLineage
			if err := proto.Unmarshal(payload, &entry); err != nil {
				t.Fatalf("stored lineage did not unmarshal: %v", err)
			}
			return &entry
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasEvaluationFor(entry *riskv1.DecisionLineage, agentID string) bool {
	for _, evaluation := range entry.GetEvaluations() {
		if evaluation.GetAgentId() == agentID {
			return true
		}
	}
	return false
}

// TestLineageIsPersistedAndReproducesTheDecision proves the orchestrator
// writes DecisionLineage to PostgreSQL and that the stored record carries the
// same decision the caller actually received — the property replay (ADR-013)
// and evaluation (ADR-012) both depend on, even though neither is built yet.
func TestLineageIsPersistedAndReproducesTheDecision(t *testing.T) {
	h, dsn := startPipelineWithLineage(t, settledHistory())
	response := decideWithLineage(t, h, transaction())

	inline := response.GetLineage()
	if inline == nil {
		t.Fatal("include_lineage was set but no lineage was returned inline")
	}
	if inline.GetDecisionId() != response.GetDecisionId() {
		t.Errorf("inline lineage decision_id = %q, want %q", inline.GetDecisionId(), response.GetDecisionId())
	}

	stored := fetchStoredLineage(t, dsn, response.GetDecisionId())
	if stored == nil {
		t.Fatalf("no lineage row was written for decision %q", response.GetDecisionId())
	}

	if stored.GetOutcome().GetDecision() != response.GetOutcome().GetDecision() {
		t.Errorf("stored decision = %v, want %v (the decision the caller received)",
			stored.GetOutcome().GetDecision(), response.GetOutcome().GetDecision())
	}
	if stored.GetOutcome().GetAggregateScore() != response.GetOutcome().GetAggregateScore() {
		t.Errorf("stored aggregate score = %d, want %d",
			stored.GetOutcome().GetAggregateScore(), response.GetOutcome().GetAggregateScore())
	}

	for _, agent := range []string{"velocity", "device", "geo"} {
		if !hasEvaluationFor(stored, agent) {
			t.Errorf("stored lineage is missing an evaluation for %q", agent)
		}
	}
	if stored.GetVersions().GetFeatureCatalogue() == nil {
		t.Error("stored lineage carries no feature catalogue version")
	}
	if stored.GetVersions().GetPolicy() == nil {
		t.Error("stored lineage carries no policy version")
	}
	if len(stored.GetSpans()) == 0 {
		t.Error("stored lineage carries no execution spans")
	}
}

// TestLineageIsWrittenEvenWhenNotRequestedInline proves persistence does not
// depend on the caller asking for the inline copy: lineage is for replay and
// analysis, which read it from the store, not from a hot-path response.
func TestLineageIsWrittenEvenWhenNotRequestedInline(t *testing.T) {
	h, dsn := startPipelineWithLineage(t, settledHistory())
	response := decide(t, h, transaction())

	if response.GetLineage() != nil {
		t.Fatal("lineage was returned inline despite include_lineage not being set")
	}
	if fetchStoredLineage(t, dsn, response.GetDecisionId()) == nil {
		t.Fatalf("no lineage row was written for decision %q", response.GetDecisionId())
	}
}

// TestStoredDurationsResolveBelowAMillisecond guards the defect migration
// 0002 exists for. The projection stored integer milliseconds, so every stage
// of an 80 ms budget — all of which run in hundreds of microseconds — rounded
// to 0 or 1, and no percentile was computable from the queryable half of
// lineage at all (docs/deadline-model.md section 8).
//
// Budgets are the discriminator rather than elapsed times: they are
// configured constants, so the assertion is deterministic. Under the old
// schema they were 3, 8 and 12; expressed in nanoseconds the smallest is
// 3,000,000.
func TestStoredDurationsResolveBelowAMillisecond(t *testing.T) {
	h, dsn := startPipelineWithLineage(t, settledHistory())
	response := decide(t, h, transaction())

	if fetchStoredLineage(t, dsn, response.GetDecisionId()) == nil {
		t.Fatalf("no lineage row was written for decision %q", response.GetDecisionId())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the lineage store: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx,
		`SELECT stage, elapsed_ns, budget_ns FROM execution_spans WHERE decision_id = $1`,
		response.GetDecisionId())
	if err != nil {
		t.Fatalf("querying execution_spans: %v", err)
	}
	defer rows.Close()

	spans := 0
	for rows.Next() {
		var (
			stage               string
			elapsedNs, budgetNs int64
		)
		if err := rows.Scan(&stage, &elapsedNs, &budgetNs); err != nil {
			t.Fatalf("scanning execution_spans: %v", err)
		}
		spans++
		if budgetNs < int64(time.Millisecond) {
			t.Errorf("stage %q recorded budget_ns = %d, which is a millisecond value in a nanosecond column: "+
				"durations are being stored at a resolution coarser than the stages they measure", stage, budgetNs)
		}
		if elapsedNs < 0 {
			t.Errorf("stage %q recorded a negative elapsed_ns = %d", stage, elapsedNs)
		}
	}
	if spans == 0 {
		t.Fatal("no execution spans were persisted for the decision")
	}

	var deadlineNs int64
	if err := pool.QueryRow(ctx,
		`SELECT deadline_ns FROM decisions WHERE id = $1`, response.GetDecisionId()).Scan(&deadlineNs); err != nil {
		t.Fatalf("querying decisions.deadline_ns: %v", err)
	}
	if deadlineNs < int64(time.Millisecond) {
		t.Errorf("deadline_ns = %d, which is a millisecond value in a nanosecond column", deadlineNs)
	}
}
