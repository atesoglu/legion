//! Generated Rust bindings for Legion's protobuf contracts.
//!
//! The module tree mirrors the protobuf package names, so
//! `legion.dataplane.v1.SentinelService` is [`legion::dataplane::v1`]. Nothing
//! here is hand-written; edit the `.proto` files instead.

/// Root of the generated contract tree.
pub mod legion {
    /// Types shared by every other package.
    pub mod common {
        /// Version 1.
        pub mod v1 {
            tonic::include_proto!("legion.common.v1");
        }
    }

    /// The risk domain: transactions, features, signals and decisions.
    pub mod risk {
        /// Version 1.
        pub mod v1 {
            tonic::include_proto!("legion.risk.v1");
        }
    }

    /// Agent and capability contracts.
    pub mod agent {
        /// Version 1.
        pub mod v1 {
            tonic::include_proto!("legion.agent.v1");
        }
    }

    /// The deterministic data plane.
    pub mod dataplane {
        /// Version 1.
        pub mod v1 {
            tonic::include_proto!("legion.dataplane.v1");
        }
    }

    /// The externally facing decision contract.
    pub mod gateway {
        /// Version 1.
        pub mod v1 {
            tonic::include_proto!("legion.gateway.v1");
        }
    }
}
