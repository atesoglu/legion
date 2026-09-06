//! Geographic signal computation.
//!
//! # Responsibility
//!
//! Detects location anomalies from the coarse position carried on the subject
//! and windowed location features: jurisdiction risk, mismatch against the
//! account's country of record, and location churn.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and does not resolve
//! addresses. It receives reduced-precision coordinates by design.
//!
//! # Status
//!
//! Thresholds and the high-risk jurisdiction list are configured defaults
//! awaiting Phase 5 measurement. A jurisdiction list is a policy artefact with
//! real consequences for real people; the placeholder here is empty rather than
//! guessed.
//!
//! `REASON_CODE_IMPOSSIBLE_TRAVEL` is defined in the contract but not emitted:
//! it needs the previous location and its timestamp, and no feature in the
//! current catalogue carries them. The rule arrives with the feature.

use std::collections::BTreeSet;
use std::time::Duration;

use legion_common::Score;
use legion_engine::{Assessment, Features, Finding, Reading, RuleOutcome, window};
use legion_proto::legion::agent::v1::{EvaluateRequest, EvaluateResponse};
use legion_proto::legion::risk::v1::{Location, ReasonCode, Transaction};

/// The agent identifier, matching `agent_id` in lineage and the workload name.
pub const AGENT_ID: &str = "geo";

const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Thresholds and lists governing the geographic rules.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Thresholds {
    /// Distinct coarse locations in 24h that constitute unusual movement.
    pub unique_locations_24h: i64,
    /// Accuracy radius in metres beyond which a position is too coarse for
    /// geographic rules to mean anything.
    pub insufficient_precision_metres: u32,
    /// ISO 3166-1 alpha-2 codes treated as elevated risk.
    pub high_risk_countries: BTreeSet<String>,
}

impl Default for Thresholds {
    fn default() -> Self {
        Self {
            unique_locations_24h: 4,
            insufficient_precision_metres: 500_000,
            // Deliberately empty: a jurisdiction list is a policy decision with
            // consequences for real people, not a default to be invented here.
            high_risk_countries: BTreeSet::new(),
        }
    }
}

/// Evaluates the geographic rules against one request.
#[must_use]
pub fn evaluate(
    request: &EvaluateRequest,
    thresholds: &Thresholds,
    elapsed: Duration,
) -> EvaluateResponse {
    let features = Features::new(request.features.as_ref());
    let mut assessment = Assessment::new();

    let unique_locations = features.count("unique_locations", window::H24).absent_as(0);
    if unique_locations.is_stale() {
        assessment.mark_stale();
    }

    let subject = request.subject.as_ref();
    let location = subject.and_then(|subject| subject.location.as_ref());

    assessment
        .rule(precision(location, thresholds))
        .rule(high_risk_jurisdiction(location, thresholds))
        .rule(country_mismatch(subject, location))
        .rule(unusual_location(unique_locations, thresholds));

    assessment.into_response(AGENT_ID, VERSION, elapsed)
}

/// A position too coarse to reason about. Reported as a low score because it is
/// a caveat on the other rules rather than evidence of fraud.
fn precision(location: Option<&Location>, thresholds: &Thresholds) -> RuleOutcome {
    let Some(location) = location else {
        return RuleOutcome::Unevaluable;
    };
    if location.accuracy_radius_metres > thresholds.insufficient_precision_metres {
        RuleOutcome::fired(
            Finding::new(
                Score::saturating_new(20),
                ReasonCode::LocationPrecisionInsufficient,
            )
            .counted(
                "accuracy_radius_metres",
                i64::from(location.accuracy_radius_metres),
                i64::from(thresholds.insufficient_precision_metres),
            ),
        )
    } else {
        RuleOutcome::Clear
    }
}

fn high_risk_jurisdiction(location: Option<&Location>, thresholds: &Thresholds) -> RuleOutcome {
    let Some(country) = location.map(|location| location.country_code.as_str()) else {
        return RuleOutcome::Unevaluable;
    };
    if country.is_empty() {
        return RuleOutcome::Unevaluable;
    }

    if thresholds.high_risk_countries.contains(country) {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(70), ReasonCode::HighRiskJurisdiction)
                .categorised("location_country_code", country),
        )
    } else {
        RuleOutcome::Clear
    }
}

/// The transaction is happening outside the account's country of record.
fn country_mismatch(subject: Option<&Transaction>, location: Option<&Location>) -> RuleOutcome {
    let home = subject
        .and_then(|subject| subject.account.as_ref())
        .map(|account| account.home_country_code.as_str())
        .unwrap_or_default();
    let here = location
        .map(|location| location.country_code.as_str())
        .unwrap_or_default();

    if home.is_empty() || here.is_empty() {
        return RuleOutcome::Unevaluable;
    }

    if home == here {
        RuleOutcome::Clear
    } else {
        RuleOutcome::fired(
            Finding::new(
                Score::saturating_new(50),
                ReasonCode::LocationAccountMismatch,
            )
            .categorised("home_country_code", home)
            .categorised("location_country_code", here),
        )
    }
}

fn unusual_location(locations: Reading<i64>, thresholds: &Thresholds) -> RuleOutcome {
    let Some(observed) = locations.value() else {
        return RuleOutcome::Unevaluable;
    };
    if observed >= thresholds.unique_locations_24h {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(55), ReasonCode::UnusualLocation).counted(
                "unique_locations_24h",
                observed,
                thresholds.unique_locations_24h,
            ),
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
        Account, Feature, FeatureFreshness, FeatureSet, FeatureValue, FeatureWindow, RiskSignal,
        feature_value,
    };

    fn fresh(name: &str, w: FeatureWindow, value: i64) -> Feature {
        Feature {
            name: name.to_owned(),
            window: w as i32,
            value: Some(FeatureValue {
                kind: Some(feature_value::Kind::Count(value)),
            }),
            freshness: FeatureFreshness::Fresh as i32,
            ..Feature::default()
        }
    }

    fn request(home: &str, here: &str, radius: u32, locations: i64) -> EvaluateRequest {
        EvaluateRequest {
            evaluation_id: "evaluation-1".to_owned(),
            subject: Some(Transaction {
                account: Some(Account {
                    home_country_code: home.to_owned(),
                    ..Account::default()
                }),
                location: Some(Location {
                    country_code: here.to_owned(),
                    accuracy_radius_metres: radius,
                    ..Location::default()
                }),
                ..Transaction::default()
            }),
            features: Some(FeatureSet {
                features: vec![fresh("unique_locations", window::H24, locations)],
                ..FeatureSet::default()
            }),
            ..EvaluateRequest::default()
        }
    }

    fn signal(response: &EvaluateResponse) -> Option<&RiskSignal> {
        match &response.result {
            Some(evaluate_response::Result::Signal(signal)) => Some(signal),
            _ => None,
        }
    }

    fn evaluate_default(request: &EvaluateRequest) -> EvaluateResponse {
        evaluate(request, &Thresholds::default(), Duration::ZERO)
    }

    #[test]
    fn a_domestic_transaction_at_home_scores_zero() {
        let response = evaluate_default(&request("DE", "DE", 5_000, 1));
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(0));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
    }

    #[test]
    fn a_transaction_outside_the_country_of_record_fires_a_mismatch() {
        let response = evaluate_default(&request("DE", "BR", 5_000, 1));
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(50));
        assert_eq!(
            signal.and_then(|s| s.reason_codes.first().copied()),
            Some(ReasonCode::LocationAccountMismatch as i32)
        );
    }

    #[test]
    fn movement_across_many_places_in_a_day_is_unusual() {
        let response = evaluate_default(&request("DE", "DE", 5_000, 6));
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(codes.contains(&(ReasonCode::UnusualLocation as i32)));
    }

    #[test]
    fn a_country_only_position_is_reported_as_imprecise() {
        let response = evaluate_default(&request("DE", "DE", 900_000, 1));
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(20));
        assert_eq!(
            signal.and_then(|s| s.reason_codes.first().copied()),
            Some(ReasonCode::LocationPrecisionInsufficient as i32)
        );
    }

    #[test]
    fn the_default_jurisdiction_list_accuses_nobody() {
        let response = evaluate_default(&request("BR", "BR", 5_000, 1));
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(!codes.contains(&(ReasonCode::HighRiskJurisdiction as i32)));
    }

    #[test]
    fn a_configured_jurisdiction_fires_when_listed() {
        let thresholds = Thresholds {
            high_risk_countries: ["XX".to_owned()].into_iter().collect(),
            ..Thresholds::default()
        };
        let response = evaluate(&request("DE", "XX", 5_000, 1), &thresholds, Duration::ZERO);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(70));
    }

    #[test]
    fn a_subject_without_a_location_yields_no_opinion() {
        let response = evaluate(
            &EvaluateRequest::default(),
            &Thresholds::default(),
            Duration::ZERO,
        );
        assert!(signal(&response).is_none());
    }

    #[test]
    fn an_unknown_home_country_does_not_manufacture_a_mismatch() {
        let response = evaluate_default(&request("", "BR", 5_000, 1));
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(0));
        // Three of four rules ran: the mismatch rule could not be evaluated.
        assert_eq!(signal.map(|s| s.confidence), Some(75));
        assert_eq!(signal.map(|s| s.degraded), Some(true));
    }
}
