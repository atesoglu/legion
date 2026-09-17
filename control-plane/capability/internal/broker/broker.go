// Package broker is the capability runtime's enforcement pipeline
// (ADR-005, docs/capability-model.md section 5): identity, grant, scope,
// constraint, budget, in that order, before any operation runs. Every check
// is audited, allowed or denied.
//
// This package has no gRPC in it: it is pure enough to unit-test the whole
// pipeline without a network, the same reason evaluation.Coordinator keeps
// transport out of its own package.
package broker

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// AuditLogger records one capability invocation, allowed or denied. The
// runtime does not persist audit records anywhere durable yet -- Phase 4
// owns real observability everywhere else in the platform, and this is no
// exception; see the note in docs/capability-model.md section 6.
type AuditLogger interface {
	Audit(entry *agentv1.CapabilityAudit)
}

// Broker enforces the pipeline against a fixed, reviewable manifest set.
//
// The manifest set is Go configuration (manifest.go's Default), not an
// env-var or database-driven registration flow yet -- reviewable because a
// pull request is review, the same posture the sentinel's default policy
// takes for its weights and thresholds.
type Broker struct {
	manifests map[string]*agentv1.AgentManifest // keyed by workload_id
	audit     AuditLogger

	mu      sync.Mutex
	budgets map[string]uint32
	seen    map[string]time.Time
	now     func() time.Time
}

// New builds a Broker from a manifest set. A workload_id that appears more
// than once is a configuration error: two manifests for the same identity
// is not "deny by default", it is "which one wins", and that ambiguity must
// be caught before serving, the same posture registry.New takes for a
// duplicate agent identifier.
func New(manifests []*agentv1.AgentManifest, audit AuditLogger) (*Broker, error) {
	byWorkload := make(map[string]*agentv1.AgentManifest, len(manifests))
	for _, manifest := range manifests {
		workloadID := manifest.GetIdentity().GetWorkloadId()
		if strings.TrimSpace(workloadID) == "" {
			return nil, fmt.Errorf("broker: a manifest has no workload_id")
		}
		if _, duplicate := byWorkload[workloadID]; duplicate {
			return nil, fmt.Errorf("broker: workload_id %q is manifested twice", workloadID)
		}
		byWorkload[workloadID] = manifest
	}

	return &Broker{
		manifests: byWorkload,
		audit:     audit,
		budgets:   make(map[string]uint32),
		seen:      make(map[string]time.Time),
		now:       time.Now,
	}, nil
}

// CheckCapability enforces the pipeline for a fixed-enum capability request
// from a real-time agent.
func (b *Broker) CheckCapability(
	identity *agentv1.AgentIdentity,
	evaluationID string,
	capability agentv1.Capability,
	requestedFeatureNames []string,
	requestedWindow riskv1.FeatureWindow,
) (agentv1.CapabilityVerdict, string) {
	started := b.now()

	verdict, detail := func() (agentv1.CapabilityVerdict, string) {
		manifest, ok := b.manifests[identity.GetWorkloadId()]
		if !ok {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED, "unknown workload identity"
		}

		grant := findGrant(manifest, capability)
		if grant == nil {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED, "capability not granted"
		}

		if strings.TrimSpace(evaluationID) == "" {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_SCOPE, "no evaluation_id supplied"
		}

		if verdict, detail := checkConstraint(grant.GetConstraint(), requestedFeatureNames, requestedWindow); verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
			return verdict, detail
		}

		key := budgetKey(identity.GetWorkloadId(), evaluationID, capability.String())
		if !b.spend(key, grant.GetConstraint().GetMaxInvocations()) {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_BUDGET, "invocation budget exhausted for this evaluation"
		}

		return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED, ""
	}()

	b.auditRecord(evaluationID, identity, capability, verdict, detail, b.now().Sub(started))
	return verdict, detail
}

// CheckToolCapability is CheckCapability's analogue for an investigation
// agent's tool call (ADR-017): the grantable verb is a tool name, checked
// against the agent's ToolGrant entries rather than a fixed Capability enum
// member. There is no per-tool feature/window constraint to check -- a tool
// grant either names this tool or it does not.
func (b *Broker) CheckToolCapability(identity *agentv1.AgentIdentity, scopeID, toolName string) (agentv1.CapabilityVerdict, string) {
	started := b.now()

	verdict, detail := func() (agentv1.CapabilityVerdict, string) {
		manifest, ok := b.manifests[identity.GetWorkloadId()]
		if !ok {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED, "unknown workload identity"
		}

		grant := findToolGrant(manifest, toolName)
		if grant == nil {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED, "tool not granted"
		}

		if strings.TrimSpace(scopeID) == "" {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_SCOPE, "no scope_id supplied"
		}

		key := budgetKey(identity.GetWorkloadId(), scopeID, toolName)
		if !b.spend(key, grant.GetMaxInvocations()) {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_BUDGET, "invocation budget exhausted for this task"
		}

		return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED, ""
	}()

	b.auditRecord(scopeID, identity, agentv1.Capability_CAPABILITY_UNSPECIFIED, verdict, toolDetail(toolName, detail), b.now().Sub(started))
	return verdict, detail
}

// Forget drops budget counters idle for longer than ttl, the same posture
// gateway/internal/ratelimit takes for its per-caller buckets: without it
// the map grows with every distinct (workload, scope) pair ever seen.
func (b *Broker) Forget(ttl time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := b.now().Add(-ttl)
	for key, lastSeen := range b.seen {
		if lastSeen.Before(cutoff) {
			delete(b.seen, key)
			delete(b.budgets, key)
		}
	}
}

// defaultBudget applies when a grant's max_invocations is zero, which
// capability-model.md section 4 defines as "the runtime default, which is
// never unlimited".
const defaultBudget = 100

// spend increments the counter for key and reports whether it is still
// within budget. limit of zero takes defaultBudget, never "unlimited".
func (b *Broker) spend(key string, limit uint32) bool {
	if limit == 0 {
		limit = defaultBudget
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.budgets[key]++
	b.seen[key] = b.now()
	return b.budgets[key] <= limit
}

func (b *Broker) auditRecord(
	scopeID string, identity *agentv1.AgentIdentity, capability agentv1.Capability,
	verdict agentv1.CapabilityVerdict, detail string, elapsed time.Duration,
) {
	if b.audit == nil {
		return
	}
	b.audit.Audit(&agentv1.CapabilityAudit{
		EvaluationId: scopeID,
		Identity:     identity,
		Capability:   capability,
		Verdict:      verdict,
		OccurredAt:   timestamppb.New(b.now()),
		Elapsed:      durationpb.New(elapsed),
		Detail:       detail,
	})
}

func findGrant(manifest *agentv1.AgentManifest, capability agentv1.Capability) *agentv1.CapabilityGrant {
	for _, grant := range manifest.GetGrants() {
		if grant.GetCapability() == capability {
			return grant
		}
	}
	return nil
}

func findToolGrant(manifest *agentv1.AgentManifest, toolName string) *agentv1.ToolGrant {
	for _, grant := range manifest.GetToolGrants() {
		if grant.GetToolName() == toolName {
			return grant
		}
	}
	return nil
}

func checkConstraint(
	constraint *agentv1.CapabilityConstraint, requestedFeatureNames []string, requestedWindow riskv1.FeatureWindow,
) (agentv1.CapabilityVerdict, string) {
	if constraint == nil {
		return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED, ""
	}

	if allowed := constraint.GetAllowedFeatureNames(); len(allowed) > 0 {
		for _, name := range requestedFeatureNames {
			if !contains(allowed, name) {
				return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_CONSTRAINT, "feature name not in the allow-list: " + name
			}
		}
	}

	if allowed := constraint.GetAllowedWindows(); len(allowed) > 0 && requestedWindow != riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED {
		found := false
		for _, window := range allowed {
			if window == requestedWindow {
				found = true
				break
			}
		}
		if !found {
			return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_CONSTRAINT, "window not in the allow-list"
		}
	}

	return agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED, ""
}

func contains(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}

func budgetKey(workloadID, scopeID, verb string) string {
	return workloadID + "|" + scopeID + "|" + verb
}

func toolDetail(toolName, detail string) string {
	if detail == "" {
		return "tool " + toolName
	}
	return "tool " + toolName + ": " + detail
}
