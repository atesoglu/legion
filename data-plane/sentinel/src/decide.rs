//! Translation between the contract and the decision model.
//!
//! The arithmetic lives in `legion_common` and knows nothing about protobuf.
//! This module is the boundary: it validates what arrived, hands pure values to
//! the aggregator, and renders the result as a `DecisionOutcome`.

use std::collections::{BTreeMap, BTreeSet};

use legion_common::{
    AgentInput, Aggregation, Confidence, Decision as CoreDecision, Degradation, ExclusionReason,
    FeatureState, Score, Signal, aggregate,
};
use legion_proto::legion::common::v1::{FailureKind, Version};
use legion_proto::legion::dataplane::v1::{DecideRequest, DecideResponse, decide_response};
use legion_proto::legion::risk::v1::{
    AgentEvaluation, Decision as ProtoDecision, DecisionOutcome, DegradationState, PolicyRef,
    PolicyThresholds, ReasonCode, SignalContribution, agent_evaluation,
};

use crate::policy::SentinelPolicy;

/// Version of the sentinel that produced an outcome.
fn sentinel_version() -> Version {
    Version {
        name: "sentinel".to_owned(),
        version: env!("CARGO_PKG_VERSION").to_owned(),
        digest: String::new(),
    }
}

/// Produces a decision for one evaluation.
///
/// Total: every request yields a response. The sentinel performs no I/O, so
/// there is no dependency that can fail and no reason to return an error
/// instead of a decision.
#[must_use]
pub fn decide(request: &DecideRequest, policy: &SentinelPolicy) -> DecideResponse {
    let (inputs, exclusion_codes) = build_inputs(request, policy);

    let features = if request.feature_store_degraded {
        FeatureState::Degraded
    } else {
        FeatureState::Fresh
    };

    let aggregation = aggregate(&inputs, &policy.decision, features);
    let outcome = render(&aggregation, request, policy, &exclusion_codes);

    DecideResponse {
        result: Some(decide_response::Result::Outcome(outcome)),
        sentinel_version: Some(sentinel_version()),
    }
}

/// Builds one input per agent, over the union of the configured agents and the
/// agents the orchestrator actually reported.
///
/// A configured agent with no evaluation is a missing agent, not an absent one:
/// it is excluded and recorded, so an outage cannot quietly look like a policy
/// with fewer agents in it.
fn build_inputs(
    request: &DecideRequest,
    policy: &SentinelPolicy,
) -> (Vec<AgentInput>, BTreeMap<String, ReasonCode>) {
    let reported: BTreeMap<&str, &AgentEvaluation> = request
        .evaluations
        .iter()
        .map(|evaluation| (evaluation.agent_id.as_str(), evaluation))
        .collect();

    let agent_ids: BTreeSet<&str> = policy
        .agents
        .keys()
        .map(String::as_str)
        .chain(reported.keys().copied())
        .collect();

    let mut inputs = Vec::with_capacity(agent_ids.len());
    let mut codes = BTreeMap::new();

    for agent_id in agent_ids {
        let configured = policy.agent(agent_id);
        let evaluation = reported.get(agent_id).copied();

        let (result, code) = match (configured, evaluation) {
            (None, _) => (
                Err(ExclusionReason::AgentNotConfigured),
                Some(ReasonCode::AgentNotConfigured),
            ),
            (Some(_), None) => (
                Err(ExclusionReason::AgentFailed),
                Some(ReasonCode::AgentUnavailable),
            ),
            (Some(_), Some(evaluation)) => interpret(evaluation),
        };

        if let Some(code) = code {
            codes.insert(agent_id.to_owned(), code);
        }

        inputs.push(AgentInput {
            agent_id: agent_id.to_owned(),
            weight: configured.map_or(legion_common::Weight::ZERO, |agent| agent.weight),
            // An agent absent from the policy has no manifest to disable it, so
            // it is not "disabled" — its exclusion is carried by the result.
            enabled: configured.is_none_or(|agent| agent.enabled),
            result,
        });
    }

    (inputs, codes)
}

/// Interprets one reported evaluation.
///
/// An out-of-range score is a contract violation by the producer. It is not
/// clamped: a signal that cannot be trusted to stay inside `[0, 100]` cannot be
/// trusted to have meant anything, so it is excluded like any other failure.
fn interpret(
    evaluation: &AgentEvaluation,
) -> (Result<Signal, ExclusionReason>, Option<ReasonCode>) {
    match &evaluation.result {
        Some(agent_evaluation::Result::Signal(signal)) => {
            let score = u8::try_from(signal.score).ok().and_then(Score::new);
            let confidence = u8::try_from(signal.confidence)
                .ok()
                .and_then(Confidence::new);

            match (score, confidence) {
                (Some(score), Some(confidence)) => (
                    Ok(Signal {
                        score,
                        confidence,
                        degraded: signal.degraded,
                    }),
                    None,
                ),
                _ => (
                    Err(ExclusionReason::AgentFailed),
                    Some(ReasonCode::ModelOutputInvalid),
                ),
            }
        }
        Some(agent_evaluation::Result::Failure(failure)) => (
            Err(ExclusionReason::AgentFailed),
            Some(reason_for_failure(failure.kind)),
        ),
        None => (
            Err(ExclusionReason::AgentFailed),
            Some(ReasonCode::AgentUnavailable),
        ),
    }
}

fn reason_for_failure(kind: i32) -> ReasonCode {
    match FailureKind::try_from(kind) {
        Ok(FailureKind::DeadlineExceeded) => ReasonCode::AgentTimeout,
        Ok(FailureKind::CapabilityDenied) => ReasonCode::CapabilityDenied,
        Ok(FailureKind::InvalidResponse) => ReasonCode::ModelOutputInvalid,
        _ => ReasonCode::AgentUnavailable,
    }
}

fn render(
    aggregation: &Aggregation,
    request: &DecideRequest,
    policy: &SentinelPolicy,
    exclusion_codes: &BTreeMap<String, ReasonCode>,
) -> DecisionOutcome {
    let contributions: Vec<SignalContribution> = aggregation
        .contributions
        .iter()
        .map(|contribution| SignalContribution {
            agent_id: contribution.agent_id.clone(),
            score: u32::from(contribution.score.get()),
            weight_basis_points: u32::from(contribution.weight_basis_points),
            weighted_contribution: u32::from(contribution.weighted_contribution),
            included: contribution.included,
            exclusion_reason: contribution.exclusion_reason.map_or(0, |reason| {
                exclusion_code(&contribution.agent_id, reason, exclusion_codes) as i32
            }),
        })
        .collect();

    DecisionOutcome {
        decision: proto_decision(aggregation.decision) as i32,
        aggregate_score: u32::from(aggregation.aggregate.get()),
        reason_codes: reason_codes(aggregation, request, &contributions, exclusion_codes),
        contributions,
        degradation: proto_degradation(aggregation.degradation) as i32,
        policy: Some(PolicyRef {
            policy_id: policy.policy_id.clone(),
            version: Some(Version {
                name: policy.policy_id.clone(),
                version: policy.version.clone(),
                digest: String::new(),
            }),
            fallback: aggregation.fallback_applied,
        }),
        thresholds: Some(PolicyThresholds {
            review_at: u32::from(policy.decision.thresholds.review_at().get()),
            decline_at: u32::from(policy.decision.thresholds.decline_at().get()),
        }),
    }
}

/// A failure's specific cause is recorded where one was reported; otherwise the
/// exclusion category itself is the reason.
fn exclusion_code(
    agent_id: &str,
    reason: ExclusionReason,
    reported: &BTreeMap<String, ReasonCode>,
) -> ReasonCode {
    match reason {
        ExclusionReason::AgentFailed => reported
            .get(agent_id)
            .copied()
            .unwrap_or(ReasonCode::AgentUnavailable),
        ExclusionReason::SignalDisabled => ReasonCode::SignalDisabled,
        ExclusionReason::ConfidenceBelowMinimum => ReasonCode::ConfidenceBelowMinimum,
        ExclusionReason::AgentNotConfigured => ReasonCode::AgentNotConfigured,
    }
}

/// Risk reasons first, ordered by how much each signal moved the score, then
/// the platform reasons describing how the evaluation itself went.
fn reason_codes(
    aggregation: &Aggregation,
    request: &DecideRequest,
    contributions: &[SignalContribution],
    exclusion_codes: &BTreeMap<String, ReasonCode>,
) -> Vec<i32> {
    let mut included: Vec<&SignalContribution> =
        contributions.iter().filter(|c| c.included).collect();
    included.sort_by(|left, right| {
        right
            .weighted_contribution
            .cmp(&left.weighted_contribution)
            .then(left.agent_id.cmp(&right.agent_id))
    });

    let signals: BTreeMap<&str, &legion_proto::legion::risk::v1::RiskSignal> = request
        .evaluations
        .iter()
        .filter_map(|evaluation| match &evaluation.result {
            Some(agent_evaluation::Result::Signal(signal)) => {
                Some((evaluation.agent_id.as_str(), signal))
            }
            _ => None,
        })
        .collect();

    let mut codes: Vec<i32> = Vec::new();
    let mut seen: BTreeSet<i32> = BTreeSet::new();

    for contribution in included {
        if let Some(signal) = signals.get(contribution.agent_id.as_str()) {
            for code in &signal.reason_codes {
                if *code != ReasonCode::Unspecified as i32 && seen.insert(*code) {
                    codes.push(*code);
                }
            }
        }
    }

    for contribution in contributions.iter().filter(|c| !c.included) {
        let _ = exclusion_codes;
        if contribution.exclusion_reason != ReasonCode::Unspecified as i32
            && seen.insert(contribution.exclusion_reason)
        {
            codes.push(contribution.exclusion_reason);
        }
    }

    if request.feature_store_degraded {
        let code = ReasonCode::FeatureStoreDegraded as i32;
        if seen.insert(code) {
            codes.push(code);
        }
    }

    if aggregation.fallback_applied {
        let code = ReasonCode::FallbackPolicyUsed as i32;
        if seen.insert(code) {
            codes.push(code);
        }
    }

    codes
}

const fn proto_decision(decision: CoreDecision) -> ProtoDecision {
    match decision {
        CoreDecision::Allow => ProtoDecision::Allow,
        CoreDecision::Review => ProtoDecision::Review,
        CoreDecision::Decline => ProtoDecision::Decline,
    }
}

const fn proto_degradation(degradation: Degradation) -> DegradationState {
    match degradation {
        Degradation::None => DegradationState::None,
        Degradation::Partial => DegradationState::Partial,
        Degradation::Fallback => DegradationState::Fallback,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use legion_proto::legion::common::v1::Failure;
    use legion_proto::legion::risk::v1::RiskSignal;

    fn signal(agent_id: &str, score: u32, codes: &[ReasonCode]) -> AgentEvaluation {
        AgentEvaluation {
            agent_id: agent_id.to_owned(),
            result: Some(agent_evaluation::Result::Signal(RiskSignal {
                agent_id: agent_id.to_owned(),
                score,
                confidence: 100,
                reason_codes: codes.iter().map(|code| *code as i32).collect(),
                ..RiskSignal::default()
            })),
            observed_latency: None,
        }
    }

    fn failure(agent_id: &str, kind: FailureKind) -> AgentEvaluation {
        AgentEvaluation {
            agent_id: agent_id.to_owned(),
            result: Some(agent_evaluation::Result::Failure(Failure {
                kind: kind as i32,
                component: agent_id.to_owned(),
                ..Failure::default()
            })),
            observed_latency: None,
        }
    }

    fn request(evaluations: Vec<AgentEvaluation>) -> DecideRequest {
        DecideRequest {
            evaluation_id: "evaluation-1".to_owned(),
            evaluations,
            ..DecideRequest::default()
        }
    }

    fn outcome(response: &DecideResponse) -> &DecisionOutcome {
        match &response.result {
            Some(decide_response::Result::Outcome(outcome)) => outcome,
            _ => unreachable!("the sentinel always produces an outcome"),
        }
    }

    #[test]
    fn a_complete_evaluation_decides_without_degradation() {
        let evaluations = vec![
            signal("velocity", 10, &[]),
            signal("device", 10, &[]),
            signal("geo", 10, &[]),
            signal("behavioral", 10, &[]),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert_eq!(outcome.decision, ProtoDecision::Allow as i32);
        assert_eq!(outcome.aggregate_score, 10);
        assert_eq!(outcome.degradation, DegradationState::None as i32);
        assert_eq!(outcome.contributions.len(), 4);
    }

    #[test]
    fn a_configured_agent_that_did_not_answer_is_recorded_as_unavailable() {
        let evaluations = vec![
            signal("velocity", 80, &[]),
            signal("device", 80, &[]),
            signal("geo", 80, &[]),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        let behavioral = outcome
            .contributions
            .iter()
            .find(|c| c.agent_id == "behavioral");
        assert_eq!(behavioral.map(|c| c.included), Some(false));
        assert_eq!(
            behavioral.map(|c| c.exclusion_reason),
            Some(ReasonCode::AgentUnavailable as i32)
        );
        // The remaining agents renormalise to 80 rather than dropping to 56.
        assert_eq!(outcome.aggregate_score, 80);
        assert_eq!(outcome.degradation, DegradationState::Partial as i32);
    }

    #[test]
    fn a_timeout_is_distinguishable_from_an_outage() {
        let evaluations = vec![
            signal("velocity", 10, &[]),
            signal("device", 10, &[]),
            signal("geo", 10, &[]),
            failure("behavioral", FailureKind::DeadlineExceeded),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert!(
            outcome
                .reason_codes
                .contains(&(ReasonCode::AgentTimeout as i32))
        );
    }

    #[test]
    fn an_unknown_agent_cannot_influence_the_decision() {
        let mut evaluations = vec![
            signal("velocity", 10, &[]),
            signal("device", 10, &[]),
            signal("geo", 10, &[]),
            signal("behavioral", 10, &[]),
        ];
        evaluations.push(signal("rogue", 100, &[]));

        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        let rogue = outcome.contributions.iter().find(|c| c.agent_id == "rogue");
        assert_eq!(rogue.map(|c| c.included), Some(false));
        assert_eq!(
            rogue.map(|c| c.exclusion_reason),
            Some(ReasonCode::AgentNotConfigured as i32)
        );
        assert_eq!(outcome.aggregate_score, 10);
    }

    #[test]
    fn an_out_of_range_score_is_excluded_rather_than_clamped() {
        let evaluations = vec![
            signal("velocity", 4_000, &[]),
            signal("device", 10, &[]),
            signal("geo", 10, &[]),
            signal("behavioral", 10, &[]),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        let velocity = outcome
            .contributions
            .iter()
            .find(|c| c.agent_id == "velocity");
        assert_eq!(velocity.map(|c| c.included), Some(false));
        assert_eq!(outcome.aggregate_score, 10);
    }

    #[test]
    fn losing_most_of_the_weight_falls_back_to_review() {
        let evaluations = vec![
            failure("velocity", FailureKind::DependencyUnavailable),
            failure("device", FailureKind::DependencyUnavailable),
            failure("behavioral", FailureKind::DependencyUnavailable),
            signal("geo", 5, &[]),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert_eq!(outcome.decision, ProtoDecision::Review as i32);
        assert_eq!(outcome.degradation, DegradationState::Fallback as i32);
        assert!(
            outcome
                .reason_codes
                .contains(&(ReasonCode::FallbackPolicyUsed as i32))
        );
        assert_eq!(outcome.policy.as_ref().map(|p| p.fallback), Some(true));
    }

    #[test]
    fn risk_reasons_are_ordered_by_how_much_each_signal_moved_the_score() {
        let evaluations = vec![
            signal("velocity", 90, &[ReasonCode::VelocitySpike]),
            signal("device", 10, &[ReasonCode::NewDevice]),
            signal("geo", 10, &[ReasonCode::UnusualLocation]),
            signal("behavioral", 10, &[ReasonCode::BehavioralAnomaly]),
        ];
        let response = decide(&request(evaluations), &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert_eq!(
            outcome.reason_codes.first(),
            Some(&(ReasonCode::VelocitySpike as i32))
        );
    }

    #[test]
    fn a_degraded_feature_store_is_visible_on_the_decision() {
        let evaluations = vec![
            signal("velocity", 10, &[]),
            signal("device", 10, &[]),
            signal("geo", 10, &[]),
            signal("behavioral", 10, &[]),
        ];
        let mut degraded = request(evaluations);
        degraded.feature_store_degraded = true;

        let response = decide(&degraded, &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert_eq!(outcome.degradation, DegradationState::Partial as i32);
        assert!(
            outcome
                .reason_codes
                .contains(&(ReasonCode::FeatureStoreDegraded as i32))
        );
    }

    #[test]
    fn the_thresholds_in_force_are_recorded_on_the_outcome() {
        let response = decide(&request(vec![]), &SentinelPolicy::default());
        let outcome = outcome(&response);

        assert_eq!(
            outcome
                .thresholds
                .as_ref()
                .map(|t| (t.review_at, t.decline_at)),
            Some((40, 70))
        );
        assert_eq!(
            response.sentinel_version.map(|v| v.name),
            Some("sentinel".to_owned())
        );
    }
}
