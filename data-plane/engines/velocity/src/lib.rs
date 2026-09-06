//! Velocity signal computation.
//!
//! # Responsibility
//!
//! Detects rate-of-activity patterns from windowed features: transaction
//! frequency, amount accumulation, failed-attempt bursts, card-testing shapes
//! and low-and-slow accumulation. Emits a bounded score with the reason codes
//! and evidence that produced it.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and does not see any
//! subject other than the one in its request.
//!
//! # Status
//!
//! Thresholds are configured defaults awaiting Phase 5 measurement. They are
//! not a claim about correct fraud detection.

use std::time::Duration;

use legion_common::Score;
use legion_engine::{Assessment, Features, Finding, Reading, RuleOutcome, window};
use legion_proto::legion::agent::v1::{EvaluateRequest, EvaluateResponse};
use legion_proto::legion::risk::v1::ReasonCode;

/// The agent identifier, matching `agent_id` in lineage and the workload name.
pub const AGENT_ID: &str = "velocity";

const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Thresholds governing the velocity rules.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Thresholds {
    /// Transactions in five minutes that constitute a spike.
    pub transactions_5m: i64,
    /// Transactions in one hour that constitute a burst.
    pub transactions_1h: i64,
    /// Summed minor units in five minutes that constitute an amount spike.
    pub amount_5m: i64,
    /// Failed authorisations in ten minutes that constitute a burst.
    pub failed_attempts_10m: i64,
    /// Transactions in five minutes required before card testing is considered.
    pub card_testing_transactions: i64,
    /// Mean minor units at or below which activity looks like card testing.
    pub card_testing_mean_amount: i64,
    /// Transactions in twenty-four hours that look like accumulation.
    pub low_and_slow_daily: i64,
    /// Transactions in one hour at or below which accumulation looks patient.
    pub low_and_slow_hourly: i64,
}

impl Default for Thresholds {
    fn default() -> Self {
        Self {
            transactions_5m: 5,
            transactions_1h: 15,
            amount_5m: 100_000,
            failed_attempts_10m: 3,
            card_testing_transactions: 4,
            card_testing_mean_amount: 500,
            low_and_slow_daily: 40,
            low_and_slow_hourly: 3,
        }
    }
}

/// Evaluates the velocity rules against one request.
#[must_use]
pub fn evaluate(
    request: &EvaluateRequest,
    thresholds: &Thresholds,
    elapsed: Duration,
) -> EvaluateResponse {
    let features = Features::new(request.features.as_ref());
    let mut assessment = Assessment::new();

    let transactions_5m = features.count("transactions", window::M5).absent_as(0);
    let transactions_1h = features.count("transactions", window::H1).absent_as(0);
    let transactions_24h = features.count("transactions", window::H24).absent_as(0);
    let amount_5m = features.count("amount", window::M5).absent_as(0);
    let failed_10m = features.count("failed_attempts", window::M10).absent_as(0);

    for reading in [
        transactions_5m,
        transactions_1h,
        transactions_24h,
        amount_5m,
        failed_10m,
    ] {
        if reading.is_stale() {
            assessment.mark_stale();
        }
    }

    assessment
        .rule(over(
            transactions_5m,
            thresholds.transactions_5m,
            "transactions_5m",
            70,
            ReasonCode::VelocitySpike,
        ))
        .rule(over(
            transactions_1h,
            thresholds.transactions_1h,
            "transactions_1h",
            55,
            ReasonCode::AccountActivityBurst,
        ))
        .rule(over(
            amount_5m,
            thresholds.amount_5m,
            "amount_5m",
            65,
            ReasonCode::AmountVelocitySpike,
        ))
        .rule(over(
            failed_10m,
            thresholds.failed_attempts_10m,
            "failed_attempts_10m",
            75,
            ReasonCode::FailedAttemptVelocity,
        ))
        .rule(card_testing(transactions_5m, amount_5m, thresholds))
        .rule(low_and_slow(transactions_24h, transactions_1h, thresholds));

    assessment.into_response(AGENT_ID, VERSION, elapsed)
}

/// Fires when an observed count reaches its threshold.
fn over(
    reading: Reading<i64>,
    threshold: i64,
    key: &str,
    score: u8,
    reason: ReasonCode,
) -> RuleOutcome {
    let Some(observed) = reading.value() else {
        return RuleOutcome::Unevaluable;
    };
    if observed >= threshold {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(score), reason).counted(key, observed, threshold),
        )
    } else {
        RuleOutcome::Clear
    }
}

/// Many small authorisations in a short window: someone is testing whether
/// stolen credentials still work, and the amounts are kept small on purpose.
fn card_testing(
    transactions: Reading<i64>,
    amount: Reading<i64>,
    thresholds: &Thresholds,
) -> RuleOutcome {
    let (Some(count), Some(total)) = (transactions.value(), amount.value()) else {
        return RuleOutcome::Unevaluable;
    };
    if count < thresholds.card_testing_transactions || count <= 0 {
        return RuleOutcome::Clear;
    }

    let Some(mean) = total.checked_div(count) else {
        return RuleOutcome::Unevaluable;
    };
    if mean <= thresholds.card_testing_mean_amount {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(85), ReasonCode::CardTestingPattern)
                .counted(
                    "transactions_5m",
                    count,
                    thresholds.card_testing_transactions,
                )
                .counted("mean_amount_5m", mean, thresholds.card_testing_mean_amount),
        )
    } else {
        RuleOutcome::Clear
    }
}

/// Sustained daily volume without any hourly burst: accumulation deliberately
/// paced to stay under every short-window rule above.
fn low_and_slow(daily: Reading<i64>, hourly: Reading<i64>, thresholds: &Thresholds) -> RuleOutcome {
    let (Some(daily), Some(hourly)) = (daily.value(), hourly.value()) else {
        return RuleOutcome::Unevaluable;
    };
    if daily >= thresholds.low_and_slow_daily && hourly <= thresholds.low_and_slow_hourly {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(45), ReasonCode::LowAndSlowPattern)
                .counted("transactions_24h", daily, thresholds.low_and_slow_daily)
                .counted("transactions_1h", hourly, thresholds.low_and_slow_hourly),
        )
    } else {
        RuleOutcome::Clear
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use legion_proto::legion::agent::v1::evaluate_response;
    use legion_proto::legion::risk::v1::{
        Feature, FeatureFreshness, FeatureSet, FeatureValue, FeatureWindow, RiskSignal,
        feature_value,
    };

    fn counted(name: &str, w: FeatureWindow, value: i64, freshness: FeatureFreshness) -> Feature {
        Feature {
            name: name.to_owned(),
            window: w as i32,
            value: Some(FeatureValue {
                kind: Some(feature_value::Kind::Count(value)),
            }),
            freshness: freshness as i32,
            ..Feature::default()
        }
    }

    fn fresh(name: &str, w: FeatureWindow, value: i64) -> Feature {
        counted(name, w, value, FeatureFreshness::Fresh)
    }

    fn request(features: Vec<Feature>) -> EvaluateRequest {
        EvaluateRequest {
            evaluation_id: "evaluation-1".to_owned(),
            features: Some(FeatureSet {
                features,
                ..FeatureSet::default()
            }),
            ..EvaluateRequest::default()
        }
    }

    fn quiet() -> Vec<Feature> {
        vec![
            fresh("transactions", window::M5, 1),
            fresh("transactions", window::H1, 2),
            fresh("transactions", window::H24, 5),
            fresh("amount", window::M5, 2_000),
            fresh("failed_attempts", window::M10, 0),
        ]
    }

    fn signal(response: &EvaluateResponse) -> Option<&RiskSignal> {
        match &response.result {
            Some(evaluate_response::Result::Signal(signal)) => Some(signal),
            _ => None,
        }
    }

    fn evaluate_default(features: Vec<Feature>) -> EvaluateResponse {
        evaluate(&request(features), &Thresholds::default(), Duration::ZERO)
    }

    #[test]
    fn ordinary_activity_scores_zero_at_full_confidence() {
        let response = evaluate_default(quiet());
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(0));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
        assert_eq!(signal.map(|s| s.degraded), Some(false));
        assert_eq!(signal.map(|s| s.reason_codes.is_empty()), Some(true));
    }

    #[test]
    fn a_burst_of_transactions_fires_a_velocity_spike() {
        let features = vec![
            fresh("transactions", window::M5, 9),
            fresh("transactions", window::H1, 9),
            fresh("transactions", window::H24, 9),
            fresh("amount", window::M5, 90_000),
            fresh("failed_attempts", window::M10, 0),
        ];

        let response = evaluate_default(features);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(70));
        assert_eq!(
            signal.map(|s| s.reason_codes.contains(&(ReasonCode::VelocitySpike as i32))),
            Some(true)
        );
    }

    #[test]
    fn many_small_authorisations_look_like_card_testing() {
        let features = vec![
            fresh("transactions", window::M5, 6),
            fresh("transactions", window::H1, 6),
            fresh("transactions", window::H24, 6),
            fresh("amount", window::M5, 1_200), // mean 200
            fresh("failed_attempts", window::M10, 0),
        ];

        let response = evaluate_default(features);
        let signal = signal(&response);

        // Card testing outranks the plain spike that also fired.
        assert_eq!(signal.map(|s| s.score), Some(85));
        assert_eq!(
            signal.and_then(|s| s.reason_codes.first().copied()),
            Some(ReasonCode::CardTestingPattern as i32)
        );
    }

    #[test]
    fn large_authorisations_at_the_same_rate_are_not_card_testing() {
        let features = vec![
            fresh("transactions", window::M5, 6),
            fresh("transactions", window::H1, 6),
            fresh("transactions", window::H24, 6),
            fresh("amount", window::M5, 600_000), // mean 100_000
            fresh("failed_attempts", window::M10, 0),
        ];

        let response = evaluate_default(features);
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(!codes.contains(&(ReasonCode::CardTestingPattern as i32)));
        assert!(codes.contains(&(ReasonCode::AmountVelocitySpike as i32)));
    }

    #[test]
    fn patient_accumulation_is_caught_despite_no_short_window_burst() {
        let features = vec![
            fresh("transactions", window::M5, 1),
            fresh("transactions", window::H1, 2),
            fresh("transactions", window::H24, 60),
            fresh("amount", window::M5, 5_000),
            fresh("failed_attempts", window::M10, 0),
        ];

        let response = evaluate_default(features);
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(codes.contains(&(ReasonCode::LowAndSlowPattern as i32)));
    }

    #[test]
    fn an_unavailable_feature_lowers_confidence_without_silencing_the_rest() {
        let features = vec![
            fresh("transactions", window::M5, 9),
            fresh("transactions", window::H1, 9),
            fresh("transactions", window::H24, 9),
            fresh("amount", window::M5, 90_000),
            // failed_attempts absent from the set entirely: unknown, not zero.
        ];

        let response = evaluate_default(features);
        let signal = signal(&response);

        // Five of six rules ran; the spike still fires.
        assert_eq!(signal.map(|s| s.confidence), Some(83));
        assert_eq!(signal.map(|s| s.degraded), Some(true));
        assert_eq!(signal.map(|s| s.score), Some(70));
    }

    #[test]
    fn no_features_at_all_produces_a_failure_not_a_clean_bill_of_health() {
        let response = evaluate(
            &EvaluateRequest::default(),
            &Thresholds::default(),
            Duration::ZERO,
        );
        assert!(signal(&response).is_none());
    }

    #[test]
    fn absent_history_is_read_as_zero_rather_than_unknown() {
        let features = vec![
            counted("transactions", window::M5, 0, FeatureFreshness::Absent),
            counted("transactions", window::H1, 0, FeatureFreshness::Absent),
            counted("transactions", window::H24, 0, FeatureFreshness::Absent),
            counted("amount", window::M5, 0, FeatureFreshness::Absent),
            counted("failed_attempts", window::M10, 0, FeatureFreshness::Absent),
        ];

        let response = evaluate_default(features);
        let signal = signal(&response);

        // A brand-new account is assessable, and unremarkable.
        assert_eq!(signal.map(|s| s.score), Some(0));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
    }

    #[test]
    fn stale_features_still_score_but_mark_the_signal_degraded() {
        let features = vec![
            counted("transactions", window::M5, 9, FeatureFreshness::Stale),
            fresh("transactions", window::H1, 9),
            fresh("transactions", window::H24, 9),
            fresh("amount", window::M5, 90_000),
            fresh("failed_attempts", window::M10, 0),
        ];

        let response = evaluate_default(features);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(70));
        assert_eq!(signal.map(|s| s.degraded), Some(true));
    }
}
