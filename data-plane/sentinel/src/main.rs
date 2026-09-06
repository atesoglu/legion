//! Command sentinel serves the only contract that can produce a decision.
//!
//! It runs in its own process deliberately (ADR-006): the engines are permitted
//! to fail and be excluded, the sentinel is not, and under `panic = "abort"`
//! those two properties cannot share a process.
//!
//! The policy is the documented default from `docs/decision-model.md`. Loading
//! versioned policy artefacts is Phase 5 work; the shape of the type does not
//! change when it arrives.

use std::error::Error;

use legion_platform::{ServiceConfig, init_tracing, shutdown_signal};
use legion_proto::legion::dataplane::v1::sentinel_service_server::{
    SentinelService, SentinelServiceServer,
};
use legion_proto::legion::dataplane::v1::{DecideRequest, DecideResponse};
use legion_sentinel::{SentinelPolicy, decide};
use tonic::transport::Server;
use tonic::{Request, Response, Status};

const SERVICE_NAME: &str = "sentinel";
const DEFAULT_LISTEN_ADDRESS: &str = "0.0.0.0:9600";

struct Sentinel {
    policy: SentinelPolicy,
}

#[tonic::async_trait]
impl SentinelService for Sentinel {
    async fn decide(
        &self,
        request: Request<DecideRequest>,
    ) -> Result<Response<DecideResponse>, Status> {
        Ok(Response::new(decide(&request.into_inner(), &self.policy)))
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    let config = ServiceConfig::load(SERVICE_NAME, DEFAULT_LISTEN_ADDRESS)?;
    init_tracing(&config);

    let address = config.listen_address.parse()?;
    let policy = SentinelPolicy::default();
    tracing::info!(
        service = config.name,
        listen_address = %config.listen_address,
        policy_id = policy.policy_id,
        policy_version = policy.version,
        "service started"
    );

    Server::builder()
        .add_service(SentinelServiceServer::new(Sentinel { policy }))
        .serve_with_shutdown(address, shutdown_signal())
        .await?;

    tracing::info!(service = config.name, "service stopped");
    Ok(())
}
