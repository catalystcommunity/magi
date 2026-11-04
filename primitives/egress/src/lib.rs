//! Magi egress proxy: the only route out for LLM tooling. It ends TLS,
//! removes secrets, puts secrets in place of placeholders, runs WASM
//! plugins that can ask Magi, and blocks flows for review by an admin.

pub mod api;
pub mod auth;
pub mod ca;
pub mod config;
pub mod detect;
pub mod dns;
pub mod flow;
pub mod records;
pub mod magi;
pub mod net;
pub mod plugins;
pub mod policy;
pub mod proxy;
pub mod scan;
pub mod server;
pub mod ssh;

/// Unix time in milliseconds.
pub fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}
