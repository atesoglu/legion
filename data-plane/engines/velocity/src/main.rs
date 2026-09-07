//! Command velocity serves the velocity signal.
//!
//! One of three deterministic engines, each in its own process (ADR-006). A
//! fault here costs one signal: the orchestrator excludes it, renormalises the
//! remaining weights and continues.
//!
//! Thresholds are configured defaults awaiting Phase 5 measurement. They are
//! not a claim about correct fraud detection.

use std::error::Error;
use std::time::Instant;

use legion_platform::{ServiceConfig, init_tracing, shutdown_signal};
use legion_proto::legion::agent::v1::agent_service_server::{AgentService, AgentServiceServer};
use legion_proto::legion::agent::v1::{EvaluateRequest, EvaluateResponse};
use legion_velocity::{AGENT_ID, Thresholds, evaluate};
use tonic::transport::Server;
use tonic::{Request, Response, Status};

const SERVICE_NAME: &str = AGENT_ID;
const DEFAULT_LISTEN_ADDRESS: &str = "0.0.0.0:9300";

struct VelocityEngine {
    thresholds: Thresholds,
}

#[tonic::async_trait]
impl AgentService for VelocityEngine {
    async fn evaluate(
        &self,
        request: Request<EvaluateRequest>,
    ) -> Result<Response<EvaluateResponse>, Status> {
        let started = Instant::now();
        let evaluated = evaluate(&request.into_inner(), &self.thresholds, started.elapsed());
        Ok(Response::new(evaluated))
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
        .add_service(AgentServiceServer::new(VelocityEngine {
            thresholds: Thresholds::default(),
        }))
        .serve_with_shutdown(address, shutdown_signal())
        .await?;

    tracing::info!(service = config.name, "service stopped");
    Ok(())
}
