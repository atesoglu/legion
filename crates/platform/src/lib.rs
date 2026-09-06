//! Configuration and process lifecycle for Legion's Rust services.
//!
//! This is the Rust counterpart of `internal/platform` on the Go side, and it
//! deliberately reads the same environment variables so that one service is
//! configured like every other. It contains no risk logic: its only job is to
//! make a service start predictably and stop without truncating in-flight
//! decisions.

use std::env;
use std::time::Duration;

use thiserror::Error;

/// The end-to-end budget granted to a decision request when the caller does not
/// supply a shorter one. The platform target from ADR-009: a configured value,
/// not a measured claim.
pub const DEFAULT_REQUEST_DEADLINE: Duration = Duration::from_millis(80);

/// How long in-flight requests may finish after a termination signal.
pub const DEFAULT_SHUTDOWN_GRACE_PERIOD: Duration = Duration::from_secs(10);

/// Default verbosity when `LEGION_LOG_LEVEL` is unset.
pub const DEFAULT_LOG_LEVEL: &str = "info";

/// Why a service could not be configured.
#[derive(Debug, Error)]
pub enum ConfigError {
    /// An environment variable held a value that is not a duration.
    #[error("config: {key}: {source}")]
    Duration {
        /// The variable that could not be parsed.
        key: &'static str,
        /// The underlying parse failure.
        source: humantime::DurationError,
    },

    /// A value parsed but violates a documented rule.
    #[error("config: {0}")]
    Invalid(String),
}

/// Settings every Legion data plane service needs.
///
/// Service-specific settings live next to the service that owns them.
#[derive(Debug, Clone)]
pub struct ServiceConfig {
    /// Logical service name used in logs, traces and metrics.
    pub name: &'static str,
    /// The gRPC listen address.
    pub listen_address: String,
    /// Ceiling on the end-to-end decision budget. See `docs/deadline-model.md`.
    pub request_deadline: Duration,
    /// Bound on graceful shutdown. Must exceed [`Self::request_deadline`].
    pub shutdown_grace_period: Duration,
    /// One of `debug`, `info`, `warn`, `error`.
    pub log_level: String,
}

impl ServiceConfig {
    /// Reads configuration for the named service from the environment.
    ///
    /// Variables are prefixed with `LEGION_`, matching the Go services:
    /// `LEGION_LISTEN_ADDRESS`, `LEGION_LOG_LEVEL`, `LEGION_REQUEST_DEADLINE`
    /// and `LEGION_SHUTDOWN_GRACE_PERIOD`. Durations are written the way Go
    /// writes them, for example `80ms` or `10s`.
    ///
    /// # Errors
    ///
    /// Returns [`ConfigError`] if a duration cannot be parsed or a validation
    /// rule is violated.
    pub fn load(name: &'static str, default_listen_address: &str) -> Result<Self, ConfigError> {
        let config = Self {
            name,
            listen_address: env_string("LEGION_LISTEN_ADDRESS", default_listen_address),
            request_deadline: env_duration("LEGION_REQUEST_DEADLINE", DEFAULT_REQUEST_DEADLINE)?,
            shutdown_grace_period: env_duration(
                "LEGION_SHUTDOWN_GRACE_PERIOD",
                DEFAULT_SHUTDOWN_GRACE_PERIOD,
            )?,
            log_level: env_string("LEGION_LOG_LEVEL", DEFAULT_LOG_LEVEL),
        };
        config.validate()?;
        Ok(config)
    }

    fn validate(&self) -> Result<(), ConfigError> {
        if self.listen_address.is_empty() {
            return Err(ConfigError::Invalid(
                "listen address must not be empty".to_owned(),
            ));
        }
        if self.request_deadline.is_zero() {
            return Err(ConfigError::Invalid(
                "request deadline must be positive".to_owned(),
            ));
        }
        // A grace period inside the request deadline makes shutdown itself a
        // source of truncated decisions.
        if self.shutdown_grace_period <= self.request_deadline {
            return Err(ConfigError::Invalid(format!(
                "shutdown grace period ({:?}) must exceed request deadline ({:?})",
                self.shutdown_grace_period, self.request_deadline
            )));
        }
        if !matches!(self.log_level.as_str(), "debug" | "info" | "warn" | "error") {
            return Err(ConfigError::Invalid(format!(
                "unsupported log level {:?}",
                self.log_level
            )));
        }
        Ok(())
    }
}

/// Installs a JSON subscriber tagged with the service name.
///
/// Legion logs are operational, not evidential: transaction payloads,
/// identifiers and model output never reach them. Auditable detail belongs in
/// decision lineage, which has its own retention and access rules.
///
/// Calling this more than once in a process has no effect after the first.
pub fn init_tracing(config: &ServiceConfig) {
    let filter = tracing_subscriber::EnvFilter::try_from_env("LEGION_LOG_FILTER")
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new(&config.log_level));

    let _ = tracing_subscriber::fmt()
        .json()
        .with_env_filter(filter)
        .with_current_span(false)
        .try_init();
}

/// Resolves when the process receives SIGINT or SIGTERM.
///
/// Passed to `serve_with_shutdown`, this is what makes a rollout drain rather
/// than drop in-flight requests.
///
/// # Panics
///
/// Never in normal operation. On Unix it panics only if the process cannot
/// register a SIGTERM handler, which indicates a broken environment rather
/// than a recoverable condition.
pub async fn shutdown_signal() {
    let interrupt = async {
        let _ = tokio::signal::ctrl_c().await;
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut stream) => {
                stream.recv().await;
            }
            Err(error) => tracing::error!(%error, "cannot listen for SIGTERM"),
        }
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        () = interrupt => {},
        () = terminate => {},
    }

    tracing::info!("shutdown signal received");
}

fn env_string(key: &str, fallback: &str) -> String {
    match env::var(key) {
        Ok(value) if !value.is_empty() => value,
        _ => fallback.to_owned(),
    }
}

fn env_duration(key: &'static str, fallback: Duration) -> Result<Duration, ConfigError> {
    match env::var(key) {
        Ok(value) if !value.is_empty() => value
            .parse::<humantime::Duration>()
            .map(Into::into)
            .map_err(|source| ConfigError::Duration { key, source }),
        _ => Ok(fallback),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config() -> ServiceConfig {
        ServiceConfig {
            name: "test",
            listen_address: "0.0.0.0:1".to_owned(),
            request_deadline: DEFAULT_REQUEST_DEADLINE,
            shutdown_grace_period: DEFAULT_SHUTDOWN_GRACE_PERIOD,
            log_level: DEFAULT_LOG_LEVEL.to_owned(),
        }
    }

    #[test]
    fn defaults_are_valid() {
        assert!(config().validate().is_ok());
    }

    #[test]
    fn grace_period_must_exceed_the_request_deadline() {
        let mut c = config();
        c.shutdown_grace_period = c.request_deadline;
        assert!(c.validate().is_err());
    }

    #[test]
    fn log_level_is_closed() {
        let mut c = config();
        c.log_level = "trace".to_owned();
        assert!(c.validate().is_err());
    }

    #[test]
    fn listen_address_must_not_be_empty() {
        let mut c = config();
        c.listen_address = String::new();
        assert!(c.validate().is_err());
    }
}
