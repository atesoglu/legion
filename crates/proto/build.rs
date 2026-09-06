//! Compiles the authoritative contracts in `protocol/protobuf` into Rust.
//!
//! Go bindings are committed under `protocol/gen/go` because the Go build must
//! work without the protobuf toolchain installed. Cargo has no equivalent
//! problem: `protoc` arrives as an ordinary build dependency, so the Rust
//! bindings are generated at build time and never committed.

use std::error::Error;
use std::path::{Path, PathBuf};

const PROTO_ROOT: &str = "../../protocol/protobuf";

const PROTOS: &[&str] = &[
    "legion/common/v1/types.proto",
    "legion/common/v1/failure.proto",
    "legion/risk/v1/transaction.proto",
    "legion/risk/v1/feature.proto",
    "legion/risk/v1/signal.proto",
    "legion/risk/v1/reason_code.proto",
    "legion/risk/v1/decision.proto",
    "legion/risk/v1/lineage.proto",
    "legion/agent/v1/capability.proto",
    "legion/agent/v1/agent_service.proto",
    "legion/agent/v1/capability_service.proto",
    "legion/dataplane/v1/sentinel_service.proto",
    "legion/gateway/v1/decision_service.proto",
];

fn main() -> Result<(), Box<dyn Error>> {
    let root = Path::new(PROTO_ROOT);
    let paths: Vec<PathBuf> = PROTOS.iter().map(|p| root.join(p)).collect();

    for path in &paths {
        println!("cargo:rerun-if-changed={}", path.display());
    }

    let mut config = prost_build::Config::new();
    config.protoc_executable(protoc_bin_vendored::protoc_bin_path()?);

    tonic_prost_build::configure()
        .build_client(true)
        .build_server(true)
        .compile_with_config(config, &paths, &[root.to_path_buf()])?;

    Ok(())
}
