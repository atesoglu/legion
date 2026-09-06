//! Command sentinel serves the only contract that can produce a decision.
//!
//! It runs in its own process deliberately (ADR-006): the engines are permitted
//! to fail and be excluded, the sentinel is not, and under `panic = "abort"`
//! those two properties cannot share a process.
//!
//! Phase 0 scaffolding: the service starts, serves its contract as
//! `Unimplemented`, and shuts down cleanly. Behaviour arrives in Phase 1.

use std::error::Error;

use legion_platform::{ServiceConfig, init_tracing, shutdown_signal};
use legion_proto::legion::dataplane::v1::sentinel_service_server::{
    SentinelService, SentinelServiceServer,
};
use legion_proto::legion::dataplane::v1::{DecideRequest, DecideResponse};
use tonic::transport::Server;
use tonic::{Request, Response, Status};

const SERVICE_NAME: &str = "sentinel";
const DEFAULT_LISTEN_ADDRESS: &str = "0.0.0.0:9600";

struct Sentinel;

#[tonic::async_trait]
impl SentinelService for Sentinel {
    async fn decide(
        &self,
        _request: Request<DecideRequest>,
    ) -> Result<Response<DecideResponse>, Status> {
        Err(Status::unimplemented(
            "sentinel: aggregation and policy arrive in Phase 1",
        ))
    }
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    let config = ServiceConfig::load(SERVICE_NAME, DEFAULT_LISTEN_ADDRESS)?;
    init_tracing(&config);

    let address = config.listen_address.parse()?;
    tracing::info!(
        service = config.name,
        listen_address = %config.listen_address,
        "service started"
    );

    Server::builder()
        .add_service(SentinelServiceServer::new(Sentinel))
        .serve_with_shutdown(address, shutdown_signal())
        .await?;

    tracing::info!(service = config.name, "service stopped");
    Ok(())
}
