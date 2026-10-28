#![allow(dead_code)]
//! Shared test helpers: a server on a random port and a CBOR client.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use magi_release::api;
use magi_release::auth::Verifier;
use magi_release::config::TargetConfig;
use magi_release::controller::{App, Cluster, Worker, is_terminal};
use magi_release::server::{Server, router};
use magi_release::store::Store;

pub fn vector() -> serde_json::Value {
    serde_json::from_str(include_str!("../../testdata/authn-vector.json")).unwrap()
}

pub fn token(name: &str) -> String {
    vector()[name].as_str().unwrap().to_string()
}

pub struct Running {
    pub base: String,
    pub http: reqwest::Client,
    pub _dir: tempfile::TempDir,
}

/// Starts the controller with `cluster`. Durations are in seconds.
pub async fn start<C: Cluster>(cluster: C, targets: Vec<TargetConfig>, timeout: u64, stabilize: u64) -> Running {
    let dir = tempfile::tempdir().unwrap();
    let store = Arc::new(Store::open(dir.path()).unwrap());
    let targets: Arc<HashMap<_, _>> = Arc::new(targets.into_iter().map(|t| (t.name.clone(), t)).collect());
    let (tx, rx) = tokio::sync::mpsc::unbounded_channel();
    let w = Worker { store: store.clone(), targets: targets.clone(), cluster, timeout: Duration::from_secs(timeout), stabilize: Duration::from_secs(stabilize) };
    tokio::spawn(w.run(rx));
    let mut v = Verifier::default();
    v.add(serde_json::from_value(vector()["key"].clone()).unwrap()).unwrap();
    let srv = Arc::new(Server { app: App { store, targets, queue: tx }, verifier: Some(v) });
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", l.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(l, router(srv)).await.unwrap() });
    Running { base, http: reqwest::Client::new(), _dir: dir }
}

impl Running {
    /// Returns the CBOR body, or (status, JSON error body).
    pub async fn call(&self, tok: &str, op: &str, body: Vec<u8>) -> Result<Vec<u8>, (u16, serde_json::Value)> {
        let r = self.http.post(format!("{}/rpc/ReleaseService/{op}", self.base))
            .header("Content-Type", "application/cbor").bearer_auth(tok).body(body).send().await.unwrap();
        let status = r.status().as_u16();
        let b = r.bytes().await.unwrap().to_vec();
        if status == 200 { Ok(b) } else { Err((status, serde_json::from_slice(&b).unwrap())) }
    }

    pub async fn create(&self, tok: &str, images: &[(&str, &str)]) -> Result<api::Release, (u16, serde_json::Value)> {
        let req = api::CreateReleaseRequest {
            images: images.iter().map(|(t, i)| api::TargetImage { target: t.to_string(), image: i.to_string() }).collect(),
            reason: Some("test".into()),
        };
        self.call(tok, "create", api::encode_create_release_request(&req)).await.map(|b| api::decode_release(&b).unwrap())
    }

    pub async fn get(&self, tok: &str, id: &str) -> api::Release {
        let b = self.call(tok, "get", api::encode_get_release_request(&api::GetReleaseRequest { id: id.into() })).await.unwrap();
        api::decode_release(&b).unwrap()
    }

    pub async fn wait(&self, tok: &str, id: &str, limit: Duration) -> api::Release {
        let end = std::time::Instant::now() + limit;
        loop {
            let r = self.get(tok, id).await;
            if is_terminal(&r.status) || std::time::Instant::now() > end {
                return r;
            }
            tokio::time::sleep(Duration::from_millis(200)).await;
        }
    }
}

pub fn target(name: &str, ns: &str, dep: &str, container: &str, path: Option<&str>, port: Option<u64>) -> TargetConfig {
    TargetConfig {
        name: name.into(), namespace: ns.into(), deployment: dep.into(), container: container.into(),
        health_path: path.map(Into::into), health_port: port, timeout_seconds: None, stabilize_seconds: None,
    }
}
