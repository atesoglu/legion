package broker

import (
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
)

// Default returns the configured manifest set for every agent Legion runs
// today. It is a starting configuration, not a claim about correct least
// privilege -- the same posture SentinelPolicy's default weights take -- and
// it is reviewed the way any other Go source change is, since there is no
// registration API for real-time agents (ADR-005 leaves that as future
// work; investigation agents get closer to one via agent_definitions, but
// even that has no RegisterAgent RPC yet).
//
// workload_id equals agent_id here, a Phase 1 stand-in for the
// platform-asserted identity mTLS would otherwise provide (ADR-011 is
// Phase 4) -- the same stand-in gateway/internal/auth's shared keys are for
// the same reason.
func Default() []*agentv1.AgentManifest {
	return []*agentv1.AgentManifest{
		{
			Identity:      identity("velocity"),
			SignalEnabled: true,
			Grants: []*agentv1.CapabilityGrant{
				{Capability: agentv1.Capability_CAPABILITY_GET_VELOCITY_FEATURES,
					Constraint: &agentv1.CapabilityConstraint{
						AllowedFeatureNames: []string{"transactions", "amount", "failed_attempts"},
					}},
				{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION},
			},
		},
		{
			Identity:      identity("device"),
			SignalEnabled: true,
			Grants: []*agentv1.CapabilityGrant{
				{Capability: agentv1.Capability_CAPABILITY_GET_DEVICE_FEATURES,
					Constraint: &agentv1.CapabilityConstraint{
						AllowedFeatureNames: []string{"device_age", "accounts_per_device"},
					}},
				{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION},
			},
		},
		{
			Identity:      identity("geo"),
			SignalEnabled: true,
			Grants: []*agentv1.CapabilityGrant{
				{Capability: agentv1.Capability_CAPABILITY_GET_GEO_FEATURES,
					Constraint: &agentv1.CapabilityConstraint{
						AllowedFeatureNames: []string{"unique_locations"},
					}},
				{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION},
			},
		},
		{
			// The behavioural agent does not exist yet (Phase 3); this
			// manifest is ready for it, per capability-model.md section 4's
			// "narrowest useful set" -- no velocity/device/geo capability at
			// all, only what it is given directly.
			Identity:      identity("behavioral"),
			SignalEnabled: true,
			Grants: []*agentv1.CapabilityGrant{
				{Capability: agentv1.Capability_CAPABILITY_GET_TRANSACTION},
				{Capability: agentv1.Capability_CAPABILITY_GET_ACCOUNT_FEATURES,
					Constraint: &agentv1.CapabilityConstraint{
						AllowedFeatureNames: []string{"transactions"},
						MaxInvocations:      5,
					}},
				{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION},
			},
		},
		{
			// Mirrors investigation/controller's seeded agent_definitions
			// row for the same agent_id -- a duplication kept manually in
			// sync for now (documented gap; see docs/capability-model.md).
			Identity: identity("device_investigation_agent"),
			ToolGrants: []*agentv1.ToolGrant{
				{ToolName: "lookup_device_history"},
			},
		},
		{
			Identity: identity("velocity_investigation_agent"),
			ToolGrants: []*agentv1.ToolGrant{
				{ToolName: "lookup_transaction_velocity"},
			},
		},
	}
}

func identity(agentID string) *agentv1.AgentIdentity {
	return &agentv1.AgentIdentity{AgentId: agentID, WorkloadId: agentID}
}
