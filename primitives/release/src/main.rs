use std::collections::HashMap;
use std::sync::Arc;

use magi_release::auth::Verifier;
use magi_release::cluster::KubeCluster;
use magi_release::config::{Settings, load_targets};
use magi_release::controller::{App, Worker, is_terminal};
use magi_release::watch::{Blocking, Incidents, KubeWorkloads, Operator, Watch};
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
    let incidents = Arc::new(Incidents::open(Some(&cfg.data))?);
    let mut operator: Option<Arc<dyn Operator>> = None;
    let watch = cfg.watch_namespace.clone().map(|namespace| {
        tracing::info!(namespace, database = ?cfg.watch_database, services = ?cfg.watch_services, "watch started");
        let k = KubeWorkloads { client: cluster.client.clone(), namespace, database: cfg.watch_database.clone(), services: cfg.watch_services.clone() };
        operator = Some(Arc::new(Blocking(k.clone())));
        let rules = Watch::new(cfg.database_restart_after, cfg.service_restart_after, cfg.give_up_after);
        let releases = store.clone();
        let releasing = move || releases.all().iter().any(|r| !is_terminal(&r.release.status));
        tokio::spawn(magi_release::watch::run(k, rules, incidents.clone(), std::time::Duration::from_secs(15), releasing))
    });
    let (tx, rx) = tokio::sync::mpsc::unbounded_channel();
    let worker = Worker { store: store.clone(), targets: targets.clone(), cluster, timeout: cfg.timeout, stabilize: cfg.stabilize };
    let worker = tokio::spawn(worker.run(rx));
    let server = Arc::new(Server { app: App { store, targets, queue: tx, incidents, operator }, verifier });
    let listener = tokio::net::TcpListener::bind(&cfg.listen).await?;
    tracing::info!(listen = %cfg.listen, "release controller started");
    axum::serve(listener, router(server)).with_graceful_shutdown(shutdown()).await?;
    // An interrupted release is restored at the next start.
    worker.abort();
    if let Some(w) = watch {
        w.abort();
    }
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
