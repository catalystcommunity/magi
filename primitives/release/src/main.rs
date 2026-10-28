use std::collections::HashMap;
use std::sync::Arc;

use magi_release::auth::Verifier;
use magi_release::cluster::KubeCluster;
use magi_release::config::{Settings, load_targets};
use magi_release::controller::{App, Worker};
use magi_release::server::{Server, router};
use magi_release::store::Store;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt().json().with_env_filter(
        tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
    ).init();
    let args: Vec<String> = std::env::args().skip(1).collect();
    let cfg = Settings::from_args(&args)?;
    let verifier = if cfg.auth_keys.is_empty() {
        tracing::warn!("MAGI_AUTH_KEYS is empty; every call is rejected");
        None
    } else {
        Some(Verifier::load(&cfg.auth_keys)?)
    };
    let targets: HashMap<_, _> = load_targets(&cfg.targets)?.into_iter().map(|t| (t.name.clone(), t)).collect();
    let targets = Arc::new(targets);
    let store = Arc::new(Store::open(&cfg.data)?);
    let cluster = KubeCluster::connect(cfg.kube_context.clone(), cfg.health_via_proxy).await?;
    let (tx, rx) = tokio::sync::mpsc::unbounded_channel();
    let worker = Worker { store: store.clone(), targets: targets.clone(), cluster, timeout: cfg.timeout, stabilize: cfg.stabilize };
    let worker = tokio::spawn(worker.run(rx));
    let server = Arc::new(Server { app: App { store, targets, queue: tx }, verifier });
    let listener = tokio::net::TcpListener::bind(&cfg.listen).await?;
    tracing::info!(listen = %cfg.listen, "release controller started");
    axum::serve(listener, router(server)).with_graceful_shutdown(shutdown()).await?;
    // An interrupted release is restored at the next start.
    worker.abort();
    tracing::info!("release controller stopped");
    Ok(())
}

/// PID 1 in a container gets no default SIGTERM action, so handle it.
async fn shutdown() {
    let mut term = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).expect("SIGTERM handler");
    tokio::select! {
        _ = term.recv() => {}
        _ = tokio::signal::ctrl_c() => {}
    }
}
