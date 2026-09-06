//! Command geo serves the geographic signal.
//!
//! One of three deterministic engines, each in its own process (ADR-006). A
//! fault here costs one signal: the orchestrator excludes it, renormalises the
//! remaining weights and continues.
//!
//! Phase 0 scaffolding: the service starts, serves its contract as
//! `Unimplemented`, and shuts down cleanly. Behaviour arrives in Phase 1.

use std::error::Error;

use legion_platform::{ServiceConfig, init_tracing, shutdown_signal};
use legion_proto::legion::agent::v1::agent_service_server::{AgentService, AgentServiceServer};
use legion_proto::legion::agent::v1::{EvaluateRequest, EvaluateResponse};
use tonic::transport::Server;
use tonic::{Request, Response, Status};

const SERVICE_NAME: &str = "geo";
const DEFAULT_LISTEN_ADDRESS: &str = "0.0.0.0:9500";

struct GeoEngine;

#[tonic::async_trait]
impl AgentService for GeoEngine {
    async fn evaluate(
        &self,
        _request: Request<EvaluateRequest>,
    ) -> Result<Response<EvaluateResponse>, Status> {
        Err(Status::unimplemented(
            "geo engine: signal computation arrives in Phase 1",
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
        .add_service(AgentServiceServer::new(GeoEngine))
        .serve_with_shutdown(address, shutdown_signal())
        .await?;

    tracing::info!(service = config.name, "service stopped");
    Ok(())
}
