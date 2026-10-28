//! Magi release controller: applies images to Kubernetes Deployments, checks
//! health, and rolls back a failed release.

pub mod api;
pub mod auth;
pub mod cluster;
pub mod config;
pub mod controller;
pub mod server;
pub mod store;

/// Unix time in milliseconds.
pub fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}
