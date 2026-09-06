//! Turning rule outcomes into one bounded signal.
//!
//! Two decisions are made here, and both are configuration awaiting Phase 5
//! measurement rather than claims about correct fraud scoring:
//!
//! **Score is the maximum of the rules that fired**, not their sum. Summing
//! lets several weak observations manufacture a strong one, and makes scores
//! incomparable between agents with different numbers of rules.
//!
//! **Confidence is the share of the rule set that could actually be
//! evaluated.** It measures coverage, not certainty. An agent that could run
//! none of its rules has no opinion, and says so with a failure rather than
//! with a zero.

use std::time::Duration;

use legion_common::Score;
use legion_proto::legion::agent::v1::{EvaluateResponse, evaluate_response};
use legion_proto::legion::common::v1::{Failure, FailureKind, Version};
use legion_proto::legion::risk::v1::{
    EvidenceItem, FeatureValue, ReasonCode, RiskSignal, feature_value,
};

/// One rule firing, with the evidence that made it fire.
#[derive(Debug, Clone, PartialEq)]
pub struct Finding {
    /// How much risk this rule attributes.
    pub score: Score,
    /// The machine-readable reason.
    pub reason: ReasonCode,
    /// The values the rule actually compared, so the finding can be argued with.
    pub evidence: Vec<EvidenceItem>,
}

impl Finding {
    /// Creates a finding with no evidence attached.
    #[must_use]
    pub const fn new(score: Score, reason: ReasonCode) -> Self {
        Self {
            score,
            reason,
            evidence: Vec::new(),
        }
    }

    /// Attaches an observed count and the threshold it was compared against.
    #[must_use]
    pub fn counted(mut self, key: &str, observed: i64, threshold: i64) -> Self {
        self.evidence.push(EvidenceItem {
            key: key.to_owned(),
            value: Some(count(observed)),
            threshold: Some(count(threshold)),
        });
        self
    }

    /// Attaches an observed flag with no threshold.
    #[must_use]
    pub fn flagged(mut self, key: &str, observed: bool) -> Self {
        self.evidence.push(EvidenceItem {
            key: key.to_owned(),
            value: Some(FeatureValue {
                kind: Some(feature_value::Kind::Flag(observed)),
            }),
            threshold: None,
        });
        self
    }

    /// Attaches an observed category with no threshold.
    #[must_use]
    pub fn categorised(mut self, key: &str, observed: &str) -> Self {
        self.evidence.push(EvidenceItem {
            key: key.to_owned(),
            value: Some(FeatureValue {
                kind: Some(feature_value::Kind::Category(observed.to_owned())),
            }),
            threshold: None,
        });
        self
    }
}

fn count(value: i64) -> FeatureValue {
    FeatureValue {
        kind: Some(feature_value::Kind::Count(value)),
    }
}

/// What happened when one rule was applied.
#[derive(Debug, Clone, PartialEq)]
pub enum RuleOutcome {
    /// The rule ran and observed nothing of concern.
    Clear,
    /// The rule ran and fired.
    Fired(Box<Finding>),
    /// The rule could not run because an input was unknown.
    Unevaluable,
}

impl RuleOutcome {
    /// Convenience for `Fired(Box::new(finding))`.
    #[must_use]
    pub fn fired(finding: Finding) -> Self {
        Self::Fired(Box::new(finding))
    }
}

/// Accumulates rule outcomes into a single signal.
#[derive(Debug, Default)]
pub struct Assessment {
    findings: Vec<Finding>,
    rules: u32,
    evaluated: u32,
    stale: bool,
}

impl Assessment {
    /// Creates an empty assessment.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Records one rule outcome.
    pub fn rule(&mut self, outcome: RuleOutcome) -> &mut Self {
        self.rules = self.rules.saturating_add(1);
        match outcome {
            RuleOutcome::Clear => self.evaluated = self.evaluated.saturating_add(1),
            RuleOutcome::Fired(finding) => {
                self.evaluated = self.evaluated.saturating_add(1);
                self.findings.push(*finding);
            }
            RuleOutcome::Unevaluable => {}
        }
        self
    }

    /// Notes that at least one input was served beyond its staleness budget.
    pub fn mark_stale(&mut self) -> &mut Self {
        self.stale = true;
        self
    }

    /// Renders the accumulated outcomes as an agent response.
    ///
    /// Returns a failure rather than a zero-scored signal when no rule could be
    /// evaluated: an agent with no opinion must not be mistaken for an agent
    /// reporting no risk.
    #[must_use]
    pub fn into_response(
        self,
        agent_id: &str,
        version: &str,
        elapsed: Duration,
    ) -> EvaluateResponse {
        if self.evaluated == 0 {
            return EvaluateResponse {
                result: Some(evaluate_response::Result::Failure(Failure {
                    kind: FailureKind::DependencyUnavailable as i32,
                    component: agent_id.to_owned(),
                    message: "no rule could be evaluated from the supplied features".to_owned(),
                    elapsed: Some(duration(elapsed)),
                    retryable: true,
                })),
            };
        }

        let confidence = self.confidence();
        let degraded = self.stale || self.evaluated < self.rules;

        let mut findings = self.findings;
        findings.sort_by(|left, right| {
            right
                .score
                .cmp(&left.score)
                .then((left.reason as i32).cmp(&(right.reason as i32)))
        });

        let score = findings
            .iter()
            .map(|finding| finding.score)
            .max()
            .unwrap_or(Score::MIN);

        let reason_codes = findings
            .iter()
            .map(|finding| finding.reason as i32)
            .collect();
        let evidence = findings
            .into_iter()
            .flat_map(|finding| finding.evidence)
            .collect();

        EvaluateResponse {
            result: Some(evaluate_response::Result::Signal(RiskSignal {
                agent_id: agent_id.to_owned(),
                agent_version: Some(Version {
                    name: agent_id.to_owned(),
                    version: version.to_owned(),
                    digest: String::new(),
                }),
                score: u32::from(score.get()),
                confidence,
                reason_codes,
                evidence,
                compute_time: Some(duration(elapsed)),
                degraded,
            })),
        }
    }

    /// Coverage of the rule set, floored so that partial coverage is never
    /// overstated.
    fn confidence(&self) -> u32 {
        if self.rules == 0 {
            return 0;
        }
        self.evaluated
            .saturating_mul(100)
            .checked_div(self.rules)
            .unwrap_or(0)
    }
}

fn duration(elapsed: Duration) -> prost_types::Duration {
    prost_types::Duration {
        seconds: i64::try_from(elapsed.as_secs()).unwrap_or(i64::MAX),
        nanos: i32::try_from(elapsed.subsec_nanos()).unwrap_or(0),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn score(value: u8) -> Score {
        Score::saturating_new(value)
    }

    fn signal(response: &EvaluateResponse) -> Option<&RiskSignal> {
        match &response.result {
            Some(evaluate_response::Result::Signal(signal)) => Some(signal),
            _ => None,
        }
    }

    #[test]
    fn score_is_the_strongest_rule_not_the_sum() {
        let mut assessment = Assessment::new();
        assessment
            .rule(RuleOutcome::fired(Finding::new(
                score(40),
                ReasonCode::VelocitySpike,
            )))
            .rule(RuleOutcome::fired(Finding::new(
                score(55),
                ReasonCode::AccountActivityBurst,
            )));

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        assert_eq!(signal(&response).map(|s| s.score), Some(55));
    }

    #[test]
    fn confidence_reports_how_much_of_the_rule_set_ran() {
        let mut assessment = Assessment::new();
        assessment
            .rule(RuleOutcome::Clear)
            .rule(RuleOutcome::Clear)
            .rule(RuleOutcome::Unevaluable)
            .rule(RuleOutcome::Unevaluable);

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        let signal = signal(&response);
        assert_eq!(signal.map(|s| s.confidence), Some(50));
        assert_eq!(signal.map(|s| s.degraded), Some(true));
    }

    #[test]
    fn an_agent_with_no_opinion_fails_rather_than_scoring_zero() {
        let mut assessment = Assessment::new();
        assessment
            .rule(RuleOutcome::Unevaluable)
            .rule(RuleOutcome::Unevaluable);

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        assert!(signal(&response).is_none());
        match response.result {
            Some(evaluate_response::Result::Failure(failure)) => {
                assert_eq!(failure.kind, FailureKind::DependencyUnavailable as i32);
            }
            _ => unreachable!("expected a failure"),
        }
    }

    #[test]
    fn a_clean_evaluation_is_fully_confident_and_scores_zero() {
        let mut assessment = Assessment::new();
        assessment.rule(RuleOutcome::Clear).rule(RuleOutcome::Clear);

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        let signal = signal(&response);
        assert_eq!(signal.map(|s| s.score), Some(0));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
        assert_eq!(signal.map(|s| s.degraded), Some(false));
    }

    #[test]
    fn stale_input_degrades_a_signal_that_still_scores() {
        let mut assessment = Assessment::new();
        assessment.rule(RuleOutcome::Clear).mark_stale();

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        assert_eq!(signal(&response).map(|s| s.degraded), Some(true));
        assert_eq!(signal(&response).map(|s| s.confidence), Some(100));
    }

    #[test]
    fn reason_codes_lead_with_the_strongest_finding() {
        let mut assessment = Assessment::new();
        assessment
            .rule(RuleOutcome::fired(Finding::new(
                score(45),
                ReasonCode::LowAndSlowPattern,
            )))
            .rule(RuleOutcome::fired(Finding::new(
                score(85),
                ReasonCode::CardTestingPattern,
            )));

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        assert_eq!(
            signal(&response).map(|s| s.reason_codes.clone()),
            Some(vec![
                ReasonCode::CardTestingPattern as i32,
                ReasonCode::LowAndSlowPattern as i32
            ])
        );
    }

    #[test]
    fn evidence_records_what_was_compared() {
        let mut assessment = Assessment::new();
        assessment.rule(RuleOutcome::fired(
            Finding::new(score(70), ReasonCode::VelocitySpike).counted("transactions_5m", 9, 5),
        ));

        let response = assessment.into_response("velocity", "0.1.0", Duration::ZERO);
        let evidence = signal(&response)
            .map(|s| s.evidence.clone())
            .unwrap_or_default();
        assert_eq!(evidence.len(), 1);
        assert_eq!(
            evidence.first().map(|item| item.key.clone()),
            Some("transactions_5m".to_owned())
        );
    }
}
