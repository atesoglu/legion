//! Value types shared by every Legion Rust crate.
//!
//! This crate exists to make the decision model unrepresentable in an invalid
//! state. A [`Score`] cannot be 137. A [`Decision`] is reachable only through
//! [`Thresholds::classify`] or an explicit policy fallback, so no agent and no
//! model can produce one.
//!
//! It performs no I/O and has no dependencies. Everything here is a pure
//! function of its arguments, which is what makes a decision reproducible from
//! lineage alone (ADR-013).

mod aggregate;
mod policy;
mod score;

pub use aggregate::{
    AgentInput, Aggregation, Contribution, Degradation, ExclusionReason, FeatureState, Signal,
    aggregate,
};
pub use policy::{Decision, Policy, ThresholdError, Thresholds};
pub use score::{Confidence, Score, Weight};
