//! Device signal computation.
//!
//! # Responsibility
//!
//! Detects device novelty, churn, sharing across accounts and reported
//! integrity compromise, from windowed features and the device attributes
//! carried on the subject.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and does not see any
//! subject other than the one in its request. It never receives a raw device
//! fingerprint: the identifier it sees is already pseudonymised.
//!
//! # Status
//!
//! Thresholds are configured defaults awaiting Phase 5 measurement.
//!
//! `REASON_CODE_DEVICE_ACCOUNT_MISMATCH` is defined in the contract but not
//! emitted here: no feature in the current catalogue distinguishes "this device
//! is new *to this account*" from "this device is new". The rule arrives with
//! the feature, not before it.

use std::time::Duration;

use legion_common::Score;
use legion_engine::{Assessment, Features, Finding, Reading, RuleOutcome, window};
use legion_proto::legion::agent::v1::{EvaluateRequest, EvaluateResponse};
use legion_proto::legion::risk::v1::ReasonCode;

/// The agent identifier, matching `agent_id` in lineage and the workload name.
pub const AGENT_ID: &str = "device";

const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Thresholds governing the device rules.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Thresholds {
    /// Device age in seconds below which a device counts as new.
    pub new_device_age_seconds: i64,
    /// Distinct accounts on one device in 24h that constitute sharing.
    pub accounts_per_device_24h: i64,
    /// Distinct devices for one account in 24h that constitute churn.
    pub unique_devices_24h: i64,
}

impl Default for Thresholds {
    fn default() -> Self {
        Self {
            new_device_age_seconds: 86_400,
            accounts_per_device_24h: 4,
            unique_devices_24h: 4,
        }
    }
}

/// Evaluates the device rules against one request.
#[must_use]
pub fn evaluate(
    request: &EvaluateRequest,
    thresholds: &Thresholds,
    elapsed: Duration,
) -> EvaluateResponse {
    let features = Features::new(request.features.as_ref());
    let mut assessment = Assessment::new();

    let device_age = features.count("device_age", window::INSTANT);
    let accounts_per_device = features
        .count("accounts_per_device", window::H24)
        .absent_as(0);
    let unique_devices = features.count("unique_devices", window::H24).absent_as(0);

    for reading in [device_age, accounts_per_device, unique_devices] {
        if reading.is_stale() {
            assessment.mark_stale();
        }
    }

    assessment
        .rule(integrity(request))
        .rule(new_device(device_age, thresholds))
        .rule(shared_across_accounts(accounts_per_device, thresholds))
        .rule(churn(unique_devices, thresholds));

    assessment.into_response(AGENT_ID, VERSION, elapsed)
}

/// The caller's own device intelligence reported tampering. This is evidence
/// supplied with the subject, so it is always evaluable.
fn integrity(request: &EvaluateRequest) -> RuleOutcome {
    let compromised = request
        .subject
        .as_ref()
        .and_then(|subject| subject.device.as_ref())
        .is_some_and(|device| device.integrity_compromised);

    if compromised {
        RuleOutcome::fired(
            Finding::new(
                Score::saturating_new(90),
                ReasonCode::DeviceIntegrityCompromised,
            )
            .flagged("device_integrity_compromised", true),
        )
    } else {
        RuleOutcome::Clear
    }
}

/// A device first seen recently. An absent age means the device has no history
/// at all, which is the strongest form of novelty rather than an unknown.
fn new_device(age: Reading<i64>, thresholds: &Thresholds) -> RuleOutcome {
    let observed = match age {
        Reading::Absent => 0,
        other => match other.value() {
            Some(value) => value,
            None => return RuleOutcome::Unevaluable,
        },
    };

    if observed < thresholds.new_device_age_seconds {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(45), ReasonCode::NewDevice).counted(
                "device_age_seconds",
                observed,
                thresholds.new_device_age_seconds,
            ),
        )
    } else {
        RuleOutcome::Clear
    }
}

fn shared_across_accounts(accounts: Reading<i64>, thresholds: &Thresholds) -> RuleOutcome {
    let Some(observed) = accounts.value() else {
        return RuleOutcome::Unevaluable;
    };
    if observed >= thresholds.accounts_per_device_24h {
        RuleOutcome::fired(
            Finding::new(
                Score::saturating_new(70),
                ReasonCode::DeviceSharedAcrossAccounts,
            )
            .counted(
                "accounts_per_device_24h",
                observed,
                thresholds.accounts_per_device_24h,
            ),
        )
    } else {
        RuleOutcome::Clear
    }
}

fn churn(devices: Reading<i64>, thresholds: &Thresholds) -> RuleOutcome {
    let Some(observed) = devices.value() else {
        return RuleOutcome::Unevaluable;
    };
    if observed >= thresholds.unique_devices_24h {
        RuleOutcome::fired(
            Finding::new(Score::saturating_new(60), ReasonCode::DeviceChurn).counted(
                "unique_devices_24h",
                observed,
                thresholds.unique_devices_24h,
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
        Device, Feature, FeatureFreshness, FeatureSet, FeatureValue, FeatureWindow, RiskSignal,
        Transaction, feature_value,
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

    fn request(features: Vec<Feature>, compromised: bool) -> EvaluateRequest {
        EvaluateRequest {
            evaluation_id: "evaluation-1".to_owned(),
            subject: Some(Transaction {
                device: Some(Device {
                    integrity_compromised: compromised,
                    ..Device::default()
                }),
                ..Transaction::default()
            }),
            features: Some(FeatureSet {
                features,
                ..FeatureSet::default()
            }),
            ..EvaluateRequest::default()
        }
    }

    fn established() -> Vec<Feature> {
        vec![
            fresh("device_age", window::INSTANT, 5_000_000),
            fresh("accounts_per_device", window::H24, 1),
            fresh("unique_devices", window::H24, 1),
        ]
    }

    fn signal(response: &EvaluateResponse) -> Option<&RiskSignal> {
        match &response.result {
            Some(evaluate_response::Result::Signal(signal)) => Some(signal),
            _ => None,
        }
    }

    fn evaluate_default(features: Vec<Feature>, compromised: bool) -> EvaluateResponse {
        evaluate(
            &request(features, compromised),
            &Thresholds::default(),
            Duration::ZERO,
        )
    }

    #[test]
    fn an_established_device_scores_zero() {
        let response = evaluate_default(established(), false);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(0));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
    }

    #[test]
    fn reported_tampering_is_the_strongest_device_signal() {
        let response = evaluate_default(established(), true);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(90));
        assert_eq!(
            signal.and_then(|s| s.reason_codes.first().copied()),
            Some(ReasonCode::DeviceIntegrityCompromised as i32)
        );
    }

    #[test]
    fn a_device_with_no_history_is_new_rather_than_unknown() {
        let features = vec![
            counted("device_age", window::INSTANT, 0, FeatureFreshness::Absent),
            fresh("accounts_per_device", window::H24, 1),
            fresh("unique_devices", window::H24, 1),
        ];

        let response = evaluate_default(features, false);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(45));
        assert_eq!(signal.map(|s| s.confidence), Some(100));
    }

    #[test]
    fn a_device_serving_many_accounts_fires_sharing() {
        let features = vec![
            fresh("device_age", window::INSTANT, 5_000_000),
            fresh("accounts_per_device", window::H24, 6),
            fresh("unique_devices", window::H24, 1),
        ];

        let response = evaluate_default(features, false);
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(codes.contains(&(ReasonCode::DeviceSharedAcrossAccounts as i32)));
    }

    #[test]
    fn an_account_cycling_devices_fires_churn() {
        let features = vec![
            fresh("device_age", window::INSTANT, 5_000_000),
            fresh("accounts_per_device", window::H24, 1),
            fresh("unique_devices", window::H24, 7),
        ];

        let response = evaluate_default(features, false);
        let codes = signal(&response)
            .map(|s| s.reason_codes.clone())
            .unwrap_or_default();

        assert!(codes.contains(&(ReasonCode::DeviceChurn as i32)));
    }

    #[test]
    fn tampering_is_still_reported_when_every_feature_is_unavailable() {
        // The integrity rule reads the subject, so the agent always has an
        // opinion even during a total feature-store outage.
        let response = evaluate_default(vec![], true);
        let signal = signal(&response);

        assert_eq!(signal.map(|s| s.score), Some(90));
        assert_eq!(signal.map(|s| s.confidence), Some(25));
        assert_eq!(signal.map(|s| s.degraded), Some(true));
    }

    #[test]
    fn a_missing_device_on_the_subject_is_not_treated_as_tampering() {
        let response = evaluate(
            &EvaluateRequest {
                features: Some(FeatureSet {
                    features: established(),
                    ..FeatureSet::default()
                }),
                ..EvaluateRequest::default()
            },
            &Thresholds::default(),
            Duration::ZERO,
        );

        assert_eq!(signal(&response).map(|s| s.score), Some(0));
    }
}
