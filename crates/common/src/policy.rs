//! Decision authority: thresholds, policy, and the outcome type itself.
//!
//! [`Decision`] is only reachable through [`Thresholds::classify`] or an
//! explicit fallback, and [`Thresholds`] cannot be constructed without an
//! ascending pair. There is no path from a score to a decision that skips
//! policy.

use crate::score::{Confidence, Score, Weight};

/// The authoritative outcome of an evaluation.
///
/// Only `legion-sentinel` constructs this. No agent and no model produces a
/// `Decision`.
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

    /// The score at or above which a decision becomes [`Decision::Review`].
    #[must_use]
    pub const fn review_at(&self) -> Score {
        self.review_at
    }

    /// The score at or above which a decision becomes [`Decision::Decline`].
    #[must_use]
    pub const fn decline_at(&self) -> Score {
        self.decline_at
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

/// The complete policy applied to one evaluation.
///
/// Every field is configuration, versioned and recorded on the outcome. None of
/// it is a claim about correct fraud policy; the values are tuned against the
/// evaluation framework and every change ships with a measured comparison.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Policy {
    /// Score bands mapping an aggregate onto a decision.
    pub thresholds: Thresholds,

    /// Signals below this confidence are excluded rather than down-weighted.
    pub minimum_confidence: Confidence,

    /// The share of *total configured* weight that must be present for the
    /// normal policy to apply. Below it, [`Policy::fallback`] decides.
    pub minimum_included_weight: Weight,

    /// The decision taken when there is too little evidence to apply the
    /// thresholds. There is no safe default here, so it is always explicit.
    pub fallback: Decision,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn score(value: u8) -> Score {
        Score::saturating_new(value)
    }

    /// The documented starting point: 0-39 allow, 40-69 review, 70-100 decline.
    pub(crate) fn illustrative_thresholds() -> Thresholds {
        match Thresholds::new(score(40), score(70)) {
            Ok(thresholds) => thresholds,
            Err(_) => Thresholds {
                review_at: Score::MIN,
                decline_at: Score::MAX,
            },
        }
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

    #[test]
    fn classify_is_inclusive_at_the_lower_edge_of_each_band() {
        let thresholds = illustrative_thresholds();

        assert_eq!(thresholds.classify(Score::MIN), Decision::Allow);
        assert_eq!(thresholds.classify(score(39)), Decision::Allow);
        assert_eq!(thresholds.classify(score(40)), Decision::Review);
        assert_eq!(thresholds.classify(score(69)), Decision::Review);
        assert_eq!(thresholds.classify(score(70)), Decision::Decline);
        assert_eq!(thresholds.classify(Score::MAX), Decision::Decline);
    }
}
