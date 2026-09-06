//! Device signal computation.
//!
//! # Responsibility
//!
//! Assesses the relationship between a device and an account: novelty, device
//! churn, sharing across accounts, and reported integrity compromise. Emits a
//! bounded score with reason codes and evidence.
//!
//! Channel matters here. Device novelty is meaningful for e-commerce and
//! nearly meaningless at a physical terminal, so the rules must be
//! channel-aware rather than uniformly suspicious of new devices.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and never receives raw
//! device fingerprints — only pseudonymous identifiers and derived features.
//!
//! # Status
//!
//! Phase 0 defines the boundary only. Rules arrive in Phase 1.
