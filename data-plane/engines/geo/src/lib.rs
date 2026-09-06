//! Geographic signal computation.
//!
//! # Responsibility
//!
//! Assesses location plausibility: impossible travel between consecutive
//! events, locations unusual for the account, and mismatch against the
//! account's country of record. Emits a bounded score with reason codes and
//! evidence.
//!
//! Location accuracy is part of the input, not an assumption. An
//! impossible-travel conclusion drawn from two coarse IP geolocations is a
//! false positive generator, so the rules must widen their tolerance by the
//! reported accuracy radius and say so through
//! `REASON_CODE_LOCATION_PRECISION_INSUFFICIENT`.
//!
//! # Non-responsibility
//!
//! Does not read the feature store, does not decide, and does not perform
//! geolocation lookups of its own.
//!
//! # Status
//!
//! Phase 0 defines the boundary only. Rules arrive in Phase 1.
