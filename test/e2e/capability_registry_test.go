//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
)

// The investigation plane's agent registry has two halves, and they are
// deliberately not one source of truth:
//
//   - `agent_definitions.allowed_tools` (Zone 6, Postgres) is a DECLARATION:
//     which tools an agent intends to call. Zone 6 owns it, and the worker
//     holds write credentials for that database, so a compromised worker
//     could rewrite it.
//   - `broker.Default()` (Zone 2, reviewed Go source) is the GRANT: which
//     tools the agent is permitted to call. Nothing in Zone 6 can change it.
//
// Collapsing the two would let the thing being constrained define its own
// constraint, which is what T-14 exists to prevent. What must hold instead
// is that the declaration never exceeds the grant. Otherwise the first time
// an agent reaches for a tool it declared but was never granted, the
// capability runtime denies it and the task fails for a reason that looks
// like an outage rather than a configuration mistake.
//
// This is the only place both halves exist at once. Go's internal/ rule
// (ADR-015) stops Zone 2 and Zone 6 importing each other, and the
// controller's seed list lives in a `package main`, so the check cannot be
// a unit test in either component.

// startAgentRegistry starts only what it takes to compare the two halves:
// the controller, which seeds `agent_definitions` at startup, and the
// capability runtime, which serves the grants. No decision path is involved,
// so none of it is started.
func startAgentRegistry(t *testing.T) (*harness, string, string) {
	t.Helper()

	h := newHarness(t)
	dsn := h.startLineageStore()
	investigationQueue := h.startInvestigationQueue()

	capability := h.startGo("control-plane/capability", "capability", nil)
	h.startInvestigationService("investigation/controller", "investigation-controller", []string{
		"LEGION_INVESTIGATION_STORE=" + dsn,
		"LEGION_INVESTIGATION_QUEUE=" + investigationQueue,
	})
	h.waitReady()

	return h, dsn, capability
}

// declaredTools polls until the controller has seeded `agent_definitions`.
// The controller has no listener, so waitReady cannot cover it; the seeded
// rows are the only observable proof it started.
func declaredTools(t *testing.T, dsn string) map[string][]string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to the investigation store: %v", err)
	}
	defer pool.Close()

	deadline := time.Now().Add(readinessTimeout)
	for {
		declared, err := readDeclaredTools(ctx, pool)
		if err == nil && len(declared) > 0 {
			return declared
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent_definitions was never seeded: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func readDeclaredTools(ctx context.Context, pool *pgxpool.Pool) (map[string][]string, error) {
	rows, err := pool.Query(ctx, `SELECT agent_id, allowed_tools FROM agent_definitions WHERE enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	declared := make(map[string][]string)
	for rows.Next() {
		var (
			agentID string
			raw     []byte
		)
		if err := rows.Scan(&agentID, &raw); err != nil {
			return nil, err
		}
		var tools []string
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, fmt.Errorf("agent %q allowed_tools: %w", agentID, err)
		}
		declared[agentID] = tools
	}
	return declared, rows.Err()
}

// capabilityVerdict asks the running capability runtime the same question
// investigation/worker asks it, with the same caller-asserted identity.
func capabilityVerdict(
	t *testing.T, client agentv1.CapabilityRuntimeServiceClient, agentID, tool string,
) (agentv1.CapabilityVerdict, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A distinct scope per check: the broker's budget is keyed by
	// (workload, scope, tool), so a shared scope would eventually produce
	// DENIED_BUDGET and be read as a missing grant.
	response, err := client.CheckToolCapability(ctx, &agentv1.CheckToolCapabilityRequest{
		Identity: &agentv1.AgentIdentity{AgentId: agentID, WorkloadId: agentID},
		ScopeId:  fmt.Sprintf("registry-check-%s-%s", agentID, tool),
		ToolName: tool,
	})
	if err != nil {
		t.Fatalf("CheckToolCapability(agent=%q, tool=%q): %v", agentID, tool, err)
	}
	return response.GetVerdict(), response.GetDetail()
}

// TestTheCapabilityRuntimeGrantsWhatTheAgentRegistryDeclares is the drift
// detector for the two halves described above. It is the reason adding a
// tool to a seeded agent without also granting it in broker.Default() fails
// in CI rather than at runtime, months later, as a dead-lettered task.
func TestTheCapabilityRuntimeGrantsWhatTheAgentRegistryDeclares(t *testing.T) {
	h, dsn, capability := startAgentRegistry(t)

	declared := declaredTools(t, dsn)
	client := agentv1.NewCapabilityRuntimeServiceClient(h.dial(capability))

	t.Run("every declared tool is granted", func(t *testing.T) {
		for agentID, tools := range declared {
			if len(tools) == 0 {
				t.Errorf("agent %q declares no tools at all; a registered agent that cannot call anything is a seeding mistake", agentID)
			}
			for _, tool := range tools {
				verdict, detail := capabilityVerdict(t, client, agentID, tool)
				if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
					t.Errorf("agent %q declares tool %q in agent_definitions, but the capability runtime answers %s (%s): "+
						"the seeded agent definitions and broker.Default() have drifted apart",
						agentID, tool, verdict, detail)
				}
			}
		}
	})

	t.Run("no agent is granted another agent's tool", func(t *testing.T) {
		for agentID := range declared {
			for otherAgent, tools := range declared {
				if otherAgent == agentID {
					continue
				}
				for _, tool := range tools {
					if slices.Contains(declared[agentID], tool) {
						// Two agents may legitimately share a tool; only a
						// tool this agent never declared proves over-granting.
						continue
					}
					verdict, _ := capabilityVerdict(t, client, agentID, tool)
					if verdict == agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
						t.Errorf("agent %q was granted %q, which only %q declares: "+
							"broker.Default() grants wider than the registry declares (T-14)",
							agentID, tool, otherAgent)
					}
				}
			}
		}
	})
}
