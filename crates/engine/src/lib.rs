//! Shared machinery for Legion's deterministic agents.
//!
//! An engine is a pure function of its request: features in, one bounded signal
//! out. This crate supplies the two things all three engines need — reading
//! features without erasing what is unknown, and combining rule outcomes into a
//! signal — so that each engine contains only its rules.
//!
//! It performs no I/O and holds no state between evaluations.

mod assess;
mod features;

pub use assess::{Assessment, Finding, RuleOutcome};
pub use features::{Features, Reading};

/// Readable aliases for the generated [`FeatureWindow`] variants.
///
/// Protobuf enum values beginning with a digit come through code generation as
/// `FeatureWindow5m`, which reads badly at every rule site.
pub mod window {
    use legion_proto::legion::risk::v1::FeatureWindow;

    /// Point-in-time attribute with no aggregation.
    pub const INSTANT: FeatureWindow = FeatureWindow::Instant;
    /// Five minutes.
    pub const M5: FeatureWindow = FeatureWindow::FeatureWindow5m;
    /// Ten minutes.
    pub const M10: FeatureWindow = FeatureWindow::FeatureWindow10m;
    /// One hour.
    pub const H1: FeatureWindow = FeatureWindow::FeatureWindow1h;
    /// Twenty-four hours.
    pub const H24: FeatureWindow = FeatureWindow::FeatureWindow24h;
    /// Seven days.
    pub const D7: FeatureWindow = FeatureWindow::FeatureWindow7d;
    /// Thirty days.
    pub const D30: FeatureWindow = FeatureWindow::FeatureWindow30d;
}
