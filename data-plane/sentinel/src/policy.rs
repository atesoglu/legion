//! The policy the sentinel applies, and the agent weights it applies it with.
//!
//! Everything here is configuration. Phase 1 carries a documented default in
//! code; versioned policy artefacts under `policy/` replace it later without
//! changing the shape of this type.

use std::collections::BTreeMap;

use legion_common::{Confidence, Decision, Policy, Score, Thresholds, Weight};

/// What the policy says about one agent.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct AgentPolicy {
    /// The agent's configured weight in basis points.
    pub weight: Weight,

    /// Whether this agent's signal may be consumed at all.
    ///
    /// Mirrors `AgentManifest.signal_enabled`. The manifest held by the
    /// capability runtime becomes authoritative in Phase 3; until then this is
    /// where an agent is deployed and observed before it is allowed to matter.
    pub enabled: bool,
}

/// A complete, identified policy.
///
/// The agent set is a map rather than a list of code paths: adding an agent is
/// a configuration change, never a change to the sentinel (ADR-014).
#[derive(Debug, Clone)]
pub struct SentinelPolicy {
    /// Stable policy identifier, recorded on every decision.
    pub policy_id: String,

    /// Version of the policy artefact, recorded on every decision.
    pub version: String,

    /// Thresholds, confidence floor, minimum included weight and fallback.
    pub decision: Policy,

    /// Configured agents, keyed by `agent_id`.
    pub agents: BTreeMap<String, AgentPolicy>,
}

impl SentinelPolicy {
    /// Looks up an agent, returning `None` when it is not configured.
    #[must_use]
    pub fn agent(&self, agent_id: &str) -> Option<AgentPolicy> {
        self.agents.get(agent_id).copied()
    }
}

impl Default for SentinelPolicy {
    /// The illustrative policy from `docs/decision-model.md`.
    ///
    /// These values are a starting configuration, not a claim about correct
    /// fraud weighting or thresholds. They are tuned against the evaluation
    /// framework in Phase 5, and every published change ships with a measured
    /// precision/recall comparison.
    fn default() -> Self {
        let agents = [
            ("velocity", 3_000_u16),
            ("device", 2_500),
            ("geo", 1_500),
            ("behavioral", 3_000),
        ]
        .into_iter()
        .map(|(agent_id, basis_points)| {
            (
                agent_id.to_owned(),
                AgentPolicy {
                    weight: Weight::new(basis_points).unwrap_or(Weight::ZERO),
                    enabled: true,
                },
            )
        })
        .collect();

        Self {
            policy_id: "payments-default".to_owned(),
            version: "0.1.0".to_owned(),
            decision: Policy {
                thresholds: Thresholds::new(Score::saturating_new(40), Score::saturating_new(70))
                    .unwrap_or(DEGENERATE_THRESHOLDS),
                minimum_confidence: Confidence::saturating_new(20),
                // Half the configured weight must be present to decide normally.
                minimum_included_weight: Weight::new(5_000).unwrap_or(Weight::FULL),
                // Insufficient evidence must never auto-allow or auto-decline.
                fallback: Decision::Review,
            },
            agents,
        }
    }
}

/// Unreachable in practice; exists so the default policy needs no panic.
const DEGENERATE_THRESHOLDS: Thresholds = match Thresholds::new(Score::MIN, Score::MAX) {
    Ok(thresholds) => thresholds,
    Err(_) => unreachable!(),
};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_default_policy_weights_sum_to_the_whole_budget() {
        let policy = SentinelPolicy::default();
        let total: u32 = policy
            .agents
            .values()
            .map(|agent| u32::from(agent.weight.get()))
            .sum();
        assert_eq!(total, Weight::SCALE);
    }

    #[test]
    fn the_behavioural_agent_never_outweighs_the_largest_deterministic_one() {
        let policy = SentinelPolicy::default();
        let behavioral = policy.agent("behavioral").map(|a| a.weight.get());
        let largest_deterministic = ["velocity", "device", "geo"]
            .into_iter()
            .filter_map(|id| policy.agent(id))
            .map(|agent| agent.weight.get())
            .max();

        assert_eq!(behavioral, largest_deterministic);
    }

    #[test]
    fn insufficient_evidence_does_not_auto_allow() {
        assert_eq!(
            SentinelPolicy::default().decision.fallback,
            Decision::Review
        );
    }
}
