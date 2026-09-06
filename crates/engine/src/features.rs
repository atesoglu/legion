//! Reading supplied features without erasing what is unknown.
//!
//! Protobuf's defaults make an absent number and a zero indistinguishable. That
//! distinction is the whole game here: a velocity rule that treats a missing
//! count as zero stops firing during a feature-store outage, which is precisely
//! when it is most needed. Every read therefore returns a [`Reading`], and a
//! rule must say explicitly what it does with each case.

use legion_proto::legion::risk::v1::{
    Feature, FeatureFreshness, FeatureSet, FeatureWindow, feature_value,
};

/// One feature read, carrying how much the value can be trusted.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Reading<T> {
    /// Read from the primary store within its staleness budget.
    Fresh(T),
    /// Usable, but older than the staleness budget.
    Stale(T),
    /// The store answered and there genuinely is no value, such as a
    /// first-ever transaction. A rule may legitimately read this as zero.
    Absent,
    /// The store could not answer. The value is unknown, not empty, and a rule
    /// that needs it cannot be evaluated.
    Unavailable,
}

impl<T: Copy> Reading<T> {
    /// The value, if one was actually observed.
    #[must_use]
    pub const fn value(self) -> Option<T> {
        match self {
            Self::Fresh(value) | Self::Stale(value) => Some(value),
            Self::Absent | Self::Unavailable => None,
        }
    }

    /// Substitutes `zero` for [`Reading::Absent`], leaving every other case
    /// untouched.
    ///
    /// Use this where "the store says there is nothing" genuinely means zero —
    /// no devices seen, no prior transactions. Never use it for
    /// [`Reading::Unavailable`], which this deliberately does not touch.
    #[must_use]
    pub const fn absent_as(self, zero: T) -> Self {
        match self {
            Self::Absent => Self::Fresh(zero),
            other => other,
        }
    }

    /// Whether this reading was served beyond its staleness budget.
    #[must_use]
    pub const fn is_stale(self) -> bool {
        matches!(self, Self::Stale(_))
    }
}

/// The features supplied for one evaluation.
///
/// A feature the orchestrator did not supply reads as [`Reading::Unavailable`]:
/// not being asked for and not being answerable are indistinguishable from
/// here, and both mean the value is unknown.
#[derive(Debug, Clone, Copy)]
pub struct Features<'a> {
    set: Option<&'a FeatureSet>,
}

impl<'a> Features<'a> {
    /// Wraps the supplied feature set.
    #[must_use]
    pub const fn new(set: Option<&'a FeatureSet>) -> Self {
        Self { set }
    }

    /// Whether the orchestrator reported the feature set as degraded.
    #[must_use]
    pub fn degraded(&self) -> bool {
        self.set.is_some_and(|set| set.degraded)
    }

    /// Reads a discrete count.
    #[must_use]
    pub fn count(&self, name: &str, window: FeatureWindow) -> Reading<i64> {
        self.read(name, window, |value| match value {
            feature_value::Kind::Count(count) => Some(*count),
            _ => None,
        })
    }

    /// Reads a continuous quantity.
    #[must_use]
    pub fn number(&self, name: &str, window: FeatureWindow) -> Reading<f64> {
        self.read(name, window, |value| match value {
            feature_value::Kind::Number(number) => Some(*number),
            _ => None,
        })
    }

    /// Reads a boolean.
    #[must_use]
    pub fn flag(&self, name: &str, window: FeatureWindow) -> Reading<bool> {
        self.read(name, window, |value| match value {
            feature_value::Kind::Flag(flag) => Some(*flag),
            _ => None,
        })
    }

    fn read<T: Copy>(
        &self,
        name: &str,
        window: FeatureWindow,
        extract: impl Fn(&feature_value::Kind) -> Option<T>,
    ) -> Reading<T> {
        let Some(feature) = self.find(name, window) else {
            return Reading::Unavailable;
        };

        match FeatureFreshness::try_from(feature.freshness) {
            Ok(FeatureFreshness::Absent) => return Reading::Absent,
            Ok(FeatureFreshness::Fresh | FeatureFreshness::Stale) => {}
            // UNSPECIFIED is a producer defect; treating it as usable would be
            // trusting a value nobody vouched for.
            _ => return Reading::Unavailable,
        }

        let observed = feature
            .value
            .as_ref()
            .and_then(|value| value.kind.as_ref())
            .and_then(extract);

        // A type mismatch is a catalogue defect, not a zero.
        let Some(observed) = observed else {
            return Reading::Unavailable;
        };

        if feature.freshness == FeatureFreshness::Stale as i32 {
            Reading::Stale(observed)
        } else {
            Reading::Fresh(observed)
        }
    }

    fn find(&self, name: &str, window: FeatureWindow) -> Option<&'a Feature> {
        self.set?
            .features
            .iter()
            .find(|feature| feature.name == name && feature.window == window as i32)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use legion_proto::legion::risk::v1::FeatureValue;

    fn feature(
        name: &str,
        window: FeatureWindow,
        count: i64,
        freshness: FeatureFreshness,
    ) -> Feature {
        Feature {
            name: name.to_owned(),
            window: window as i32,
            value: Some(FeatureValue {
                kind: Some(feature_value::Kind::Count(count)),
            }),
            freshness: freshness as i32,
            ..Feature::default()
        }
    }

    fn set(features: Vec<Feature>) -> FeatureSet {
        FeatureSet {
            features,
            ..FeatureSet::default()
        }
    }

    #[test]
    fn a_missing_feature_is_unknown_rather_than_zero() {
        let empty = set(vec![]);
        let features = Features::new(Some(&empty));
        assert_eq!(
            features.count("transactions", FeatureWindow::FeatureWindow5m),
            Reading::Unavailable
        );
    }

    #[test]
    fn absent_is_distinguishable_from_unavailable() {
        let supplied = set(vec![feature(
            "transactions",
            FeatureWindow::FeatureWindow5m,
            0,
            FeatureFreshness::Absent,
        )]);
        let features = Features::new(Some(&supplied));

        let reading = features.count("transactions", FeatureWindow::FeatureWindow5m);
        assert_eq!(reading, Reading::Absent);
        assert_eq!(reading.absent_as(0), Reading::Fresh(0));
    }

    #[test]
    fn unavailable_is_never_substituted_for_zero() {
        let empty = set(vec![]);
        let features = Features::new(Some(&empty));

        let reading = features.count("transactions", FeatureWindow::FeatureWindow5m);
        assert_eq!(reading.absent_as(0), Reading::Unavailable);
    }

    #[test]
    fn a_stale_value_is_usable_but_marked() {
        let supplied = set(vec![feature(
            "transactions",
            FeatureWindow::FeatureWindow1h,
            7,
            FeatureFreshness::Stale,
        )]);
        let features = Features::new(Some(&supplied));

        let reading = features.count("transactions", FeatureWindow::FeatureWindow1h);
        assert_eq!(reading, Reading::Stale(7));
        assert!(reading.is_stale());
        assert_eq!(reading.value(), Some(7));
    }

    #[test]
    fn the_window_is_part_of_the_lookup() {
        let supplied = set(vec![feature(
            "transactions",
            FeatureWindow::FeatureWindow5m,
            9,
            FeatureFreshness::Fresh,
        )]);
        let features = Features::new(Some(&supplied));

        assert_eq!(
            features.count("transactions", FeatureWindow::FeatureWindow5m),
            Reading::Fresh(9)
        );
        assert_eq!(
            features.count("transactions", FeatureWindow::FeatureWindow24h),
            Reading::Unavailable
        );
    }

    #[test]
    fn a_type_mismatch_is_a_defect_not_a_value() {
        let supplied = set(vec![feature(
            "amount",
            FeatureWindow::FeatureWindow5m,
            100,
            FeatureFreshness::Fresh,
        )]);
        let features = Features::new(Some(&supplied));

        assert_eq!(
            features.number("amount", FeatureWindow::FeatureWindow5m),
            Reading::Unavailable
        );
    }
}
