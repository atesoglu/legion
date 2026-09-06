//! The sentinel: the only component permitted to produce a decision.
//!
//! # Responsibility
//!
//! Given a risk subject and the set of agent evaluations gathered for it, the
//! sentinel normalises the signals, applies configured weights, aggregates
//! them into a single score, applies policy thresholds, and emits a
//! [`legion_common::Decision`] together with the reason codes and the
//! contribution breakdown that justify it.
//!
//! # Non-responsibility
//!
//! The sentinel has no dependencies. It does not call the feature store, the
//! agents or the inference runtime, and it performs no I/O. Everything it may
//! consider arrives in its request. That closure is what makes a decision
//! reproducible from lineage alone, and it is why a replay of the same inputs
//! against the same versions must yield the same output.
//!
//! # Status
//!
//! Phase 0 defines the boundary only. Aggregation, weight renormalisation,
//! fallback policy and reason-code assembly arrive in Phase 1.
