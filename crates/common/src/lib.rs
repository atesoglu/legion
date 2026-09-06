//! Value types shared by every Legion Rust crate.
//!
//! This crate exists to make the decision model unrepresentable in an invalid
//! state. A [`Score`] cannot be 137. A [`Decision`] cannot be invented by an
//! agent, because agents do not depend on this crate's decision types being
//! constructible from a score without a [`Thresholds`] policy.
//!
//! Aggregation, weighting and policy loading are not here. They belong to
//! `legion-sentinel` and arrive in Phase 1.

/// A normalised risk score on the closed interval `[0, 100]`.
///
/// Scores are integers so that aggregation is reproducible across languages,
/// architectures and replays. Floating point would make two runs of the same
/// input differ in the last bit and defeat the point of a replay engine.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Score(u8);

impl Score {
    /// The lowest expressible score: no elevated risk observed.
    pub const MIN: Self = Self(0);

    /// The highest expressible score.
    pub const MAX: Self = Self(100);

    /// Creates a score, returning `None` if `value` is outside `[0, 100]`.
    ///
    /// Out-of-range input is a contract violation by the producer, not a value
    /// to be silently repaired; callers decide whether to reject the signal or
    /// record a failure.
    #[must_use]
    pub const fn new(value: u8) -> Option<Self> {
        if value <= 100 {
            Some(Self(value))
        } else {
            None
        }
    }

    /// Creates a score, clamping `value` into range.
    ///
    /// Use only where clamping is the documented behaviour, such as bounding
    /// the output of a model-derived signal that has already been rejected for
    /// being out of range once.
    #[must_use]
    pub const fn saturating_new(value: u8) -> Self {
        if value <= 100 { Self(value) } else { Self::MAX }
    }

    /// Returns the underlying value.
    #[must_use]
    pub const fn get(self) -> u8 {
        self.0
    }
}

/// The authoritative outcome of an evaluation.
///
/// Only `legion-sentinel` constructs this from a score and a policy. No agent
/// and no model produces a `Decision`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Decision {
    /// Proceed with the transaction.
    Allow,
    /// Hold the transaction for human or asynchronous adjudication.
    Review,
    /// Refuse the transaction.
    Decline,
}

/// Policy thresholds that map an aggregate score onto a [`Decision`].
///
/// These are configuration. The values are chosen per deployment and are not a
/// claim about universally correct fraud thresholds.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Thresholds {
    review_at: Score,
    decline_at: Score,
}

/// Reasons a [`Thresholds`] set was rejected at construction.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ThresholdError {
    /// `decline_at` was not strictly greater than `review_at`, which would
    /// leave `Review` unreachable or the ordering inverted.
    NotAscending,
}

impl Thresholds {
    /// Creates a threshold set.
    ///
    /// Scores below `review_at` are [`Decision::Allow`], scores at or above
    /// `decline_at` are [`Decision::Decline`], and the band between them is
    /// [`Decision::Review`].
    ///
    /// # Errors
    ///
    /// Returns [`ThresholdError::NotAscending`] unless `review_at < decline_at`.
    pub const fn new(review_at: Score, decline_at: Score) -> Result<Self, ThresholdError> {
        if review_at.get() < decline_at.get() {
            Ok(Self {
                review_at,
                decline_at,
            })
        } else {
            Err(ThresholdError::NotAscending)
        }
    }

    /// Maps an aggregate score onto a decision. Total and side-effect free.
    #[must_use]
    pub const fn classify(&self, aggregate: Score) -> Decision {
        if aggregate.get() >= self.decline_at.get() {
            Decision::Decline
        } else if aggregate.get() >= self.review_at.get() {
            Decision::Review
        } else {
            Decision::Allow
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn score_rejects_values_above_one_hundred() {
        assert_eq!(Score::new(101), None);
        assert_eq!(Score::new(255), None);
    }

    #[test]
    fn score_accepts_the_closed_boundaries() {
        assert_eq!(Score::new(0), Some(Score::MIN));
        assert_eq!(Score::new(100), Some(Score::MAX));
    }

    #[test]
    fn saturating_score_clamps_rather_than_wrapping() {
        assert_eq!(Score::saturating_new(200), Score::MAX);
    }

    #[test]
    fn thresholds_must_ascend() {
        assert_eq!(
            Thresholds::new(Score::MAX, Score::MIN),
            Err(ThresholdError::NotAscending)
        );
        assert_eq!(
            Thresholds::new(Score::MAX, Score::MAX),
            Err(ThresholdError::NotAscending)
        );
    }

    fn illustrative_thresholds() -> Thresholds {
        // The documented starting point: 0-39 allow, 40-69 review, 70-100 decline.
        match (Score::new(40), Score::new(70)) {
            (Some(review), Some(decline)) => match Thresholds::new(review, decline) {
                Ok(thresholds) => thresholds,
                Err(_) => unreachable!("40 < 70"),
            },
            _ => unreachable!("40 and 70 are within range"),
        }
    }

    #[test]
    fn classify_is_inclusive_at_the_lower_edge_of_each_band() {
        let thresholds = illustrative_thresholds();

        assert_eq!(thresholds.classify(Score::MIN), Decision::Allow);
        assert_eq!(
            thresholds.classify(Score::saturating_new(39)),
            Decision::Allow
        );
        assert_eq!(
            thresholds.classify(Score::saturating_new(40)),
            Decision::Review
        );
        assert_eq!(
            thresholds.classify(Score::saturating_new(69)),
            Decision::Review
        );
        assert_eq!(
            thresholds.classify(Score::saturating_new(70)),
            Decision::Decline
        );
        assert_eq!(thresholds.classify(Score::MAX), Decision::Decline);
    }
}
