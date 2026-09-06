//! Bounded numeric types for the decision model.
//!
//! Every value here is integral and range-checked at construction. Floating
//! point is absent deliberately: two runs of the same input must produce the
//! same bits on different machines, in different languages, months apart, or
//! the replay engine proves nothing.

/// A normalised risk score on the closed interval `[0, 100]`.
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

/// An agent's self-reported confidence on the closed interval `[0, 100]`.
///
/// Confidence may exclude a signal or leave it untouched. It never raises a
/// score: a confident agent does not get to matter more than its weight.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Confidence(u8);

impl Confidence {
    /// No confidence at all.
    pub const MIN: Self = Self(0);

    /// Full confidence.
    pub const MAX: Self = Self(100);

    /// Creates a confidence, returning `None` if `value` is outside `[0, 100]`.
    #[must_use]
    pub const fn new(value: u8) -> Option<Self> {
        if value <= 100 {
            Some(Self(value))
        } else {
            None
        }
    }

    /// Creates a confidence, clamping `value` into range.
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

/// A configured weight in basis points, on the closed interval `[0, 10000]`.
///
/// Basis points rather than percentages so that the arithmetic stays integral
/// at the precision the decision model needs. A weight of zero is legitimate
/// and meaningful: it is how an agent is registered and observed before it is
/// allowed to affect a decision.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Weight(u16);

impl Weight {
    /// No weight. The agent contributes nothing to the aggregate.
    pub const ZERO: Self = Self(0);

    /// The whole of the weight budget.
    pub const FULL: Self = Self(10_000);

    /// Basis points in one whole unit.
    pub const SCALE: u32 = 10_000;

    /// Creates a weight, returning `None` if `basis_points` exceeds 10000.
    #[must_use]
    pub const fn new(basis_points: u16) -> Option<Self> {
        if basis_points <= 10_000 {
            Some(Self(basis_points))
        } else {
            None
        }
    }

    /// Returns the weight in basis points.
    #[must_use]
    pub const fn get(self) -> u16 {
        self.0
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
    fn confidence_rejects_values_above_one_hundred() {
        assert_eq!(Confidence::new(101), None);
        assert_eq!(Confidence::saturating_new(200), Confidence::MAX);
    }

    #[test]
    fn weight_rejects_more_than_the_whole_budget() {
        assert_eq!(Weight::new(10_001), None);
        assert_eq!(Weight::new(10_000), Some(Weight::FULL));
    }
}
