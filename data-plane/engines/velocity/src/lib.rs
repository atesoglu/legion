//! Velocity signal computation.
//!
//! # Responsibility
//!
//! Detects rate-of-activity patterns from windowed features: transaction
//! frequency, amount accumulation, failed-attempt bursts, card-testing shapes
//! and low-and-slow accumulation. Emits a bounded score with the reason codes
//! and evidence that produced it.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and does not see any
//! subject other than the one in its request.
//!
//! # Status
//!
//! Phase 0 defines the boundary only. Rules arrive in Phase 1.
