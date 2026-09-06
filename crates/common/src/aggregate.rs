//! Weighted aggregation of agent signals into one score.
//!
//! Three properties make this reproducible, and all three are tested:
//!
//! 1. **Renormalisation.** An excluded signal's weight is redistributed across
//!    the signals that remain. A missing agent must never look like safety.
//! 2. **Rounding.** Division rounds half-to-even, once, at the final step.
//! 3. **Ordering.** Contributions are summed in agent-identifier order,
//!    independent of the order responses arrived in.
//!
//! See `docs/decision-model.md` §3.

use crate::policy::{Decision, Policy};
use crate::score::{Confidence, Score, Weight};

/// Whether the features backing this evaluation were fresh.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FeatureState {
    /// All features were retrieved and within their freshness window.
    Fresh,
    /// Retrieval was degraded, so the evaluation is at best `Partial`.
    Degraded,
}

/// Why a signal did not contribute to the aggregate.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ExclusionReason {
    /// The agent returned a failure, timed out, or could not be reached.
    AgentFailed,
    /// The agent's manifest does not permit its signal to be consumed.
    SignalDisabled,
    /// The signal's confidence was below the policy minimum.
    ConfidenceBelowMinimum,
    /// The agent has no configured weight in this policy.
    AgentNotConfigured,
}

/// One agent's answer.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Signal {
    /// The agent's normalised risk contribution.
    pub score: Score,
    /// The agent's self-reported confidence.
    pub confidence: Confidence,
    /// Set when the agent produced a signal despite missing or stale inputs.
    pub degraded: bool,
}

/// One agent's answer together with the configuration that governs it.
#[derive(Debug, Clone)]
pub struct AgentInput {
    /// Stable agent identifier. Also the summation order.
    pub agent_id: String,
    /// The agent's configured weight.
    pub weight: Weight,
    /// `AgentManifest.signal_enabled`: whether this signal may be consumed.
    pub enabled: bool,
    /// The signal, or the reason there is not one.
    pub result: Result<Signal, ExclusionReason>,
}

/// How one signal moved the aggregate, or why it did not.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Contribution {
    /// Stable agent identifier.
    pub agent_id: String,
    /// The agent's score, or [`Score::MIN`] when excluded.
    pub score: Score,
    /// Renormalised weight in basis points. Included weights sum to exactly
    /// 10000; excluded signals carry zero.
    pub weight_basis_points: u16,
    /// `score * weight_basis_points / 10000`, rounded half-to-even.
    pub weighted_contribution: u16,
    /// Whether this signal entered the aggregate.
    pub included: bool,
    /// Populated only when `included` is false.
    pub exclusion_reason: Option<ExclusionReason>,
}

/// How complete the evaluation was.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Degradation {
    /// Every configured agent contributed and features were fresh.
    None,
    /// At least one signal was missing or degraded; policy permitted deciding.
    Partial,
    /// Too little evidence for the normal policy; the fallback decided.
    Fallback,
}

/// The complete result of aggregating one evaluation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Aggregation {
    /// The decision, from thresholds or from the fallback policy.
    pub decision: Decision,
    /// The weighted aggregate. Zero when nothing was included.
    pub aggregate: Score,
    /// Every signal, included or not, in agent-identifier order.
    pub contributions: Vec<Contribution>,
    /// How complete the evaluation was.
    pub degradation: Degradation,
    /// Whether the fallback policy produced the decision.
    pub fallback_applied: bool,
}

/// Aggregates agent signals into a decision under `policy`.
///
/// Total: every input combination yields an [`Aggregation`]. An evaluation with
/// no usable signals produces the policy's fallback decision rather than an
/// error, because a risk decision path that can fail to produce an answer has
/// simply moved the failure to its caller.
#[must_use]
pub fn aggregate(signals: &[AgentInput], policy: &Policy, features: FeatureState) -> Aggregation {
    let mut ordered: Vec<&AgentInput> = signals.iter().collect();
    ordered.sort_by(|left, right| left.agent_id.cmp(&right.agent_id));

    let assessed: Vec<(&AgentInput, Option<ExclusionReason>)> = ordered
        .iter()
        .map(|input| (*input, exclusion_for(input, policy)))
        .collect();

    let total_configured: u64 = assessed
        .iter()
        .map(|(input, _)| u64::from(input.weight.get()))
        .sum();

    let included_weight: u64 = assessed
        .iter()
        .filter(|(_, excluded)| excluded.is_none())
        .map(|(input, _)| u64::from(input.weight.get()))
        .sum();

    let fallback_applied = below_minimum_included_weight(included_weight, total_configured, policy);

    let aggregate = weighted_average(&assessed, included_weight);
    let renormalised = renormalise(&assessed, included_weight);

    let contributions = assessed
        .iter()
        .zip(renormalised)
        .map(|((input, excluded), weight_basis_points)| Contribution {
            agent_id: input.agent_id.clone(),
            score: input.result.as_ref().map_or(Score::MIN, |s| s.score),
            weight_basis_points,
            weighted_contribution: weighted_contribution(input, *excluded, weight_basis_points),
            included: excluded.is_none(),
            exclusion_reason: *excluded,
        })
        .collect();

    let any_excluded = assessed.iter().any(|(_, excluded)| excluded.is_some());
    let any_degraded = assessed
        .iter()
        .any(|(input, _)| matches!(&input.result, Ok(signal) if signal.degraded));

    let degradation = if fallback_applied {
        Degradation::Fallback
    } else if any_excluded || any_degraded || features == FeatureState::Degraded {
        Degradation::Partial
    } else {
        Degradation::None
    };

    let decision = if fallback_applied {
        policy.fallback
    } else {
        policy.thresholds.classify(aggregate)
    };

    Aggregation {
        decision,
        aggregate,
        contributions,
        degradation,
        fallback_applied,
    }
}

/// A disabled agent is excluded on that ground alone: its manifest says the
/// signal may not be consumed, so whether it answered is not a decision input.
fn exclusion_for(input: &AgentInput, policy: &Policy) -> Option<ExclusionReason> {
    if !input.enabled {
        return Some(ExclusionReason::SignalDisabled);
    }
    match &input.result {
        Err(reason) => Some(*reason),
        Ok(signal) if signal.confidence < policy.minimum_confidence => {
            Some(ExclusionReason::ConfidenceBelowMinimum)
        }
        Ok(_) => None,
    }
}

fn below_minimum_included_weight(included: u64, configured: u64, policy: &Policy) -> bool {
    if included == 0 {
        return true;
    }
    let scale = u64::from(Weight::SCALE);
    let Some(share) = included
        .checked_mul(scale)
        .and_then(|scaled| scaled.checked_div(configured))
    else {
        return true;
    };
    share < u64::from(policy.minimum_included_weight.get())
}

/// The single rounding step: `Σ(score × weight) / Σ(weight)` over included
/// signals, using configured rather than renormalised weights so that only one
/// rounding occurs.
fn weighted_average(assessed: &[(&AgentInput, Option<ExclusionReason>)], included: u64) -> Score {
    let numerator: u64 = assessed
        .iter()
        .filter_map(|(input, excluded)| match (excluded, &input.result) {
            (None, Ok(signal)) => {
                Some(u64::from(signal.score.get()).saturating_mul(u64::from(input.weight.get())))
            }
            _ => None,
        })
        .sum();

    div_round_half_even(numerator, included)
        .and_then(|value| u8::try_from(value).ok())
        .map_or(Score::MIN, Score::saturating_new)
}

/// Redistributes the included weights so they sum to exactly 10000.
///
/// Uses largest-remainder apportionment, with ties broken by position — which,
/// because the inputs are already sorted, means by agent identifier.
fn renormalise(assessed: &[(&AgentInput, Option<ExclusionReason>)], included: u64) -> Vec<u16> {
    let mut shares = vec![0_u16; assessed.len()];
    if included == 0 {
        return shares;
    }

    let scale = u64::from(Weight::SCALE);
    let mut remainders: Vec<(u64, usize)> = Vec::new();
    let mut allocated: u64 = 0;

    for (index, (input, excluded)) in assessed.iter().enumerate() {
        if excluded.is_some() {
            continue;
        }
        let scaled = u64::from(input.weight.get()).saturating_mul(scale);
        let floor = scaled.checked_div(included).unwrap_or(0);
        let remainder = scaled.checked_rem(included).unwrap_or(0);

        if let Some(slot) = shares.get_mut(index) {
            *slot = u16::try_from(floor).unwrap_or(u16::MAX);
        }
        allocated = allocated.saturating_add(floor);
        remainders.push((remainder, index));
    }

    // Largest remainder first; equal remainders keep agent order.
    remainders.sort_by(|left, right| right.0.cmp(&left.0).then(left.1.cmp(&right.1)));

    let mut leftover = scale.saturating_sub(allocated);
    for (_, index) in remainders {
        if leftover == 0 {
            break;
        }
        if let Some(slot) = shares.get_mut(index) {
            *slot = slot.saturating_add(1);
        }
        leftover = leftover.saturating_sub(1);
    }

    shares
}

fn weighted_contribution(
    input: &AgentInput,
    excluded: Option<ExclusionReason>,
    weight_basis_points: u16,
) -> u16 {
    if excluded.is_some() {
        return 0;
    }
    let Ok(signal) = &input.result else {
        return 0;
    };
    let numerator = u64::from(signal.score.get()).saturating_mul(u64::from(weight_basis_points));
    div_round_half_even(numerator, u64::from(Weight::SCALE))
        .and_then(|value| u16::try_from(value).ok())
        .unwrap_or(0)
}

/// Integer division rounding halves toward the even quotient.
///
/// Half-to-even rather than half-up because half-up biases every tie in the
/// same direction, and across millions of decisions that bias is systematic
/// rather than noise.
fn div_round_half_even(numerator: u64, denominator: u64) -> Option<u64> {
    let quotient = numerator.checked_div(denominator)?;
    let remainder = numerator.checked_rem(denominator)?;
    let doubled = remainder.checked_mul(2)?;

    let exactly_half = doubled == denominator;
    let round_up = doubled > denominator || (exactly_half && quotient & 1 == 1);

    if round_up {
        quotient.checked_add(1)
    } else {
        Some(quotient)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::policy::Thresholds;

    fn policy() -> Policy {
        Policy {
            thresholds: match Thresholds::new(Score::saturating_new(40), Score::saturating_new(70))
            {
                Ok(thresholds) => thresholds,
                Err(_) => unreachable!(),
            },
            minimum_confidence: Confidence::saturating_new(20),
            minimum_included_weight: match Weight::new(5_000) {
                Some(weight) => weight,
                None => Weight::FULL,
            },
            fallback: Decision::Review,
        }
    }

    fn weight(basis_points: u16) -> Weight {
        Weight::new(basis_points).unwrap_or(Weight::ZERO)
    }

    fn answered(agent_id: &str, basis_points: u16, score: u8) -> AgentInput {
        AgentInput {
            agent_id: agent_id.to_owned(),
            weight: weight(basis_points),
            enabled: true,
            result: Ok(Signal {
                score: Score::saturating_new(score),
                confidence: Confidence::MAX,
                degraded: false,
            }),
        }
    }

    fn failed(agent_id: &str, basis_points: u16) -> AgentInput {
        AgentInput {
            agent_id: agent_id.to_owned(),
            weight: weight(basis_points),
            enabled: true,
            result: Err(ExclusionReason::AgentFailed),
        }
    }

    #[test]
    fn half_to_even_rounds_ties_toward_the_even_quotient() {
        // 5/2 = 2.5 -> 2 (even), 7/2 = 3.5 -> 4 (even).
        assert_eq!(div_round_half_even(5, 2), Some(2));
        assert_eq!(div_round_half_even(7, 2), Some(4));
        // Non-ties are unaffected.
        assert_eq!(div_round_half_even(4, 3), Some(1));
        assert_eq!(div_round_half_even(5, 3), Some(2));
    }

    #[test]
    fn division_by_zero_yields_none_rather_than_panicking() {
        assert_eq!(div_round_half_even(10, 0), None);
    }

    #[test]
    fn aggregate_is_the_weighted_average_of_included_signals() {
        let signals = [
            answered("device", 2_500, 40),
            answered("velocity", 3_000, 80),
        ];
        // (80*3000 + 40*2500) / 5500 = 340000/5500 = 61.81... -> 62
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);
        assert_eq!(result.aggregate, Score::saturating_new(62));
        assert_eq!(result.decision, Decision::Review);
    }

    #[test]
    fn an_excluded_signal_renormalises_rather_than_lowering_the_score() {
        let present = [answered("velocity", 3_000, 80)];
        let with_failure = [answered("velocity", 3_000, 80), failed("geo", 1_500)];

        let alone = aggregate(&present, &policy(), FeatureState::Fresh);
        let degraded = aggregate(&with_failure, &policy(), FeatureState::Fresh);

        // A dead agent must not make an 80 look like a 53.
        assert_eq!(alone.aggregate, Score::saturating_new(80));
        assert_eq!(degraded.aggregate, Score::saturating_new(80));
        assert_eq!(degraded.degradation, Degradation::Partial);
    }

    #[test]
    fn included_weights_sum_to_exactly_ten_thousand() {
        // Three equal weights cannot divide 10000 evenly; largest remainder
        // must still close the gap exactly.
        let signals = [
            answered("device", 1_000, 10),
            answered("geo", 1_000, 20),
            answered("velocity", 1_000, 30),
        ];
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);

        let total: u32 = result
            .contributions
            .iter()
            .map(|c| u32::from(c.weight_basis_points))
            .sum();
        assert_eq!(total, Weight::SCALE);
    }

    #[test]
    fn contribution_order_is_independent_of_arrival_order() {
        let forward = [
            answered("device", 2_500, 40),
            answered("geo", 1_500, 10),
            answered("velocity", 3_000, 80),
        ];
        let reversed = [
            answered("velocity", 3_000, 80),
            answered("geo", 1_500, 10),
            answered("device", 2_500, 40),
        ];

        let first = aggregate(&forward, &policy(), FeatureState::Fresh);
        let second = aggregate(&reversed, &policy(), FeatureState::Fresh);

        assert_eq!(first, second);
        let ids: Vec<&str> = first
            .contributions
            .iter()
            .map(|c| c.agent_id.as_str())
            .collect();
        assert_eq!(ids, ["device", "geo", "velocity"]);
    }

    #[test]
    fn a_zero_weight_agent_is_included_but_moves_nothing() {
        let without = [answered("velocity", 3_000, 80)];
        let with_observer = [
            answered("velocity", 3_000, 80),
            answered("amount", 0, 100), // registered, observed, not yet trusted
        ];

        let baseline = aggregate(&without, &policy(), FeatureState::Fresh);
        let observed = aggregate(&with_observer, &policy(), FeatureState::Fresh);

        assert_eq!(baseline.aggregate, observed.aggregate);
        let observer = observed
            .contributions
            .iter()
            .find(|c| c.agent_id == "amount");
        assert_eq!(observer.map(|c| c.included), Some(true));
        assert_eq!(observer.map(|c| c.weighted_contribution), Some(0));
    }

    #[test]
    fn too_little_evidence_falls_back_instead_of_deciding() {
        // 1500 of 4500 configured basis points is 3333bp, below the 5000 floor.
        let signals = [answered("geo", 1_500, 5), failed("velocity", 3_000)];
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);

        assert!(result.fallback_applied);
        assert_eq!(result.degradation, Degradation::Fallback);
        assert_eq!(result.decision, Decision::Review);
    }

    #[test]
    fn low_confidence_excludes_rather_than_down_weights() {
        let signals = [
            AgentInput {
                agent_id: "velocity".to_owned(),
                weight: weight(3_000),
                enabled: true,
                result: Ok(Signal {
                    score: Score::saturating_new(90),
                    confidence: Confidence::saturating_new(10),
                    degraded: false,
                }),
            },
            answered("device", 2_500, 10),
        ];
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);

        let velocity = result
            .contributions
            .iter()
            .find(|c| c.agent_id == "velocity");
        assert_eq!(
            velocity.and_then(|c| c.exclusion_reason),
            Some(ExclusionReason::ConfidenceBelowMinimum)
        );
        assert_eq!(result.aggregate, Score::saturating_new(10));
    }

    #[test]
    fn a_disabled_agent_is_excluded_on_that_ground_alone() {
        let signals = [
            AgentInput {
                agent_id: "behavioral".to_owned(),
                weight: weight(3_000),
                enabled: false,
                result: Err(ExclusionReason::AgentFailed),
            },
            answered("velocity", 3_000, 80),
        ];
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);

        let behavioral = result
            .contributions
            .iter()
            .find(|c| c.agent_id == "behavioral");
        assert_eq!(
            behavioral.and_then(|c| c.exclusion_reason),
            Some(ExclusionReason::SignalDisabled)
        );
    }

    #[test]
    fn degraded_features_downgrade_a_clean_evaluation_to_partial() {
        let signals = [answered("velocity", 10_000, 10)];

        let fresh = aggregate(&signals, &policy(), FeatureState::Fresh);
        let stale = aggregate(&signals, &policy(), FeatureState::Degraded);

        assert_eq!(fresh.degradation, Degradation::None);
        assert_eq!(stale.degradation, Degradation::Partial);
        assert_eq!(fresh.aggregate, stale.aggregate);
    }

    #[test]
    fn no_usable_signals_falls_back_without_panicking() {
        let signals = [failed("velocity", 3_000), failed("geo", 1_500)];
        let result = aggregate(&signals, &policy(), FeatureState::Fresh);

        assert!(result.fallback_applied);
        assert_eq!(result.aggregate, Score::MIN);
        assert_eq!(result.decision, Decision::Review);
        assert_eq!(result.contributions.len(), 2);
    }

    #[test]
    fn an_empty_evaluation_falls_back() {
        let result = aggregate(&[], &policy(), FeatureState::Fresh);

        assert!(result.fallback_applied);
        assert_eq!(result.decision, Decision::Review);
        assert!(result.contributions.is_empty());
    }
}
