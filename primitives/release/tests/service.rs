//! State machine and transport tests with a fake cluster.

mod common;

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use common::*;
use magi_release::api;
use magi_release::config::TargetConfig;
use magi_release::controller::Cluster;

/// Images named "*:bad-rollout" fail the rollout; "*:unhealthy" fail the
/// health check. With `break_restore`, restoring any image fails.
#[derive(Clone, Default)]
struct Fake {
    images: Arc<Mutex<HashMap<String, String>>>,
    log: Arc<Mutex<Vec<String>>>,
    break_restore: bool,
}

impl Fake {
    fn with(images: &[(&str, &str)]) -> Self {
        let f = Fake::default();
        for (d, i) in images {
            f.images.lock().unwrap().insert(d.to_string(), i.to_string());
        }
        f
    }
    fn image(&self, d: &str) -> String {
        self.images.lock().unwrap()[d].clone()
    }
}

impl Cluster for Fake {
    async fn current_image(&self, t: &TargetConfig) -> anyhow::Result<String> {
        self.images.lock().unwrap().get(&t.deployment).cloned().ok_or_else(|| anyhow::anyhow!("no deployment"))
    }
    async fn set_image(&self, t: &TargetConfig, image: &str, _release: &str, previous: &str) -> anyhow::Result<()> {
        if self.break_restore && image == previous {
            anyhow::bail!("api unavailable");
        }
        self.log.lock().unwrap().push(format!("{}={image}", t.deployment));
        self.images.lock().unwrap().insert(t.deployment.clone(), image.into());
        Ok(())
    }
    async fn wait_rollout(&self, t: &TargetConfig, _: Duration) -> anyhow::Result<()> {
        if self.image(&t.deployment).ends_with(":bad-rollout") { anyhow::bail!("ImagePullBackOff") }
        Ok(())
    }
    async fn verify(&self, t: &TargetConfig, _: Duration) -> anyhow::Result<()> {
        if self.image(&t.deployment).ends_with(":unhealthy") { anyhow::bail!("GET /healthz: status 500") }
        Ok(())
    }
}

fn targets() -> Vec<TargetConfig> {
    vec![target("web", "magi", "web", "web", Some("/healthz"), Some(8080)), target("api", "magi", "api", "api", None, None)]
}

const LONG: Duration = Duration::from_secs(10);

#[tokio::test]
async fn success_then_manual_rollback() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("web", "web:2"), ("api", "api:2")]).await.unwrap();
    assert!(r.id.starts_with("rel_") && r.status == "pending" && r.owner == "service:magi");
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "succeeded");
    assert_eq!(r.items[0].previous_image, "web:1");
    assert_eq!((f.image("web"), f.image("api")), ("web:2".into(), "api:2".into()));

    let b = s.call(&op, "rollback", api::encode_rollback_request(&api::RollbackRequest { id: r.id.clone() })).await.unwrap();
    let rb = api::decode_release(&b).unwrap();
    assert_eq!(rb.items[0].image, "web:1");
    assert_eq!(s.wait(&op, &rb.id, LONG).await.status, "succeeded");
    assert_eq!(f.image("web"), "web:1");
    assert_eq!(s.wait(&op, &r.id, LONG).await.status, "rolled-back");
    // A rolled-back release cannot be rolled back again.
    let e = s.call(&op, "rollback", api::encode_rollback_request(&api::RollbackRequest { id: r.id.clone() })).await.unwrap_err();
    assert_eq!(e.0, 409);
}

#[tokio::test]
async fn failed_rollout_restores_all_items() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("web", "web:2"), ("api", "api:bad-rollout")]).await.unwrap();
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "rolled-back");
    assert!(r.items[1].message.as_deref().unwrap().contains("ImagePullBackOff"));
    assert_eq!((f.image("web"), f.image("api")), ("web:1".into(), "api:1".into()));
}

#[tokio::test]
async fn failed_health_restores() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("wildcard");
    let r = s.create(&op, &[("web", "web:unhealthy")]).await.unwrap();
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "rolled-back");
    assert!(r.items[0].message.as_deref().unwrap().contains("health"));
    assert_eq!(f.image("web"), "web:1");
}

#[tokio::test]
async fn failed_restore_is_failed() {
    let mut f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    f.break_restore = true;
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("web", "web:bad-rollout")]).await.unwrap();
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "failed");
    assert!(r.items[0].message.as_deref().unwrap().contains("restore failed"));
}

#[tokio::test]
async fn releases_run_one_at_a_time_in_order() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let a = s.create(&op, &[("web", "web:2")]).await.unwrap();
    let b = s.create(&op, &[("web", "web:3")]).await.unwrap();
    assert_eq!(s.wait(&op, &b.id, LONG).await.items[0].previous_image, "web:2");
    assert_eq!(s.wait(&op, &a.id, LONG).await.status, "succeeded");
    assert_eq!(*f.log.lock().unwrap(), ["web=web:2", "web=web:3"]);
}

#[tokio::test]
async fn validation_and_authorization() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f, targets(), 5, 0).await;
    let (op, rd, none) = (token("operator"), token("reader"), token("noscope"));
    assert_eq!(s.create(&op, &[("nope", "x:1")]).await.unwrap_err().0, 400);
    assert_eq!(s.create(&op, &[("web", "bad image")]).await.unwrap_err().0, 400);
    assert_eq!(s.create(&op, &[("web", "a:1"), ("web", "a:2")]).await.unwrap_err().0, 400);
    assert_eq!(s.create(&op, &[]).await.unwrap_err().0, 400);
    assert_eq!(s.create(&rd, &[("web", "web:2")]).await.unwrap_err().0, 403);
    assert_eq!(s.create(&token("expired"), &[("web", "web:2")]).await.unwrap_err(), (401, serde_json::json!({"code": 401, "message": "token expired"})));
    assert_eq!(s.call("", "get", vec![]).await.unwrap_err().0, 401);
    assert_eq!(s.call(&op, "nope", vec![]).await.unwrap_err().0, 404);
    assert_eq!(s.call(&op, "get", vec![0xff]).await.unwrap_err().0, 400);

    let r = s.create(&op, &[("web", "web:2")]).await.unwrap();
    // Reader sees everything; a principal without scopes sees only its own records.
    assert_eq!(s.get(&rd, &r.id).await.id, r.id);
    let e = s.call(&none, "get", api::encode_get_release_request(&api::GetReleaseRequest { id: r.id.clone() })).await.unwrap_err();
    assert_eq!(e.0, 404);
    let list = |t: String| {
        let s = &s;
        async move { api::decode_list_releases_response(&s.call(&t, "list", api::encode_list_releases_request(&api::ListReleasesRequest { limit: None })).await.unwrap()).unwrap() }
    };
    assert_eq!(list(rd.clone()).await.releases.len(), 1);
    assert_eq!(list(none.clone()).await.releases.len(), 0);
    let b = s.call(&rd, "list-targets", api::encode_list_targets_request(&api::ListTargetsRequest { name: Some("web".into()) })).await.unwrap();
    let t = api::decode_list_targets_response(&b).unwrap();
    assert_eq!(t.targets.len(), 1);
    assert_eq!(t.targets[0].health_port, Some(8080));
    assert_eq!(s.call(&none, "list-targets", api::encode_list_targets_request(&api::ListTargetsRequest { name: None })).await.unwrap_err().0, 403);
    let h = reqwest::get(format!("{}/healthz", s.base)).await.unwrap();
    assert_eq!(h.status(), 200);
}

#[tokio::test]
async fn interrupted_release_is_restored_at_start() {
    use magi_release::controller::Worker;
    use magi_release::store::{Record, Store};
    let dir = tempfile::tempdir().unwrap();
    let store = Arc::new(Store::open(dir.path()).unwrap());
    let rec = Record {
        release: api::Release {
            id: "rel_x".into(), owner: "service:magi".into(), status: "verifying".into(), reason: None,
            items: vec![api::ReleaseItem { target: "web".into(), image: "web:2".into(), previous_image: "web:1".into(), status: "verifying".into(), message: None }],
            created_ms: 1, updated_ms: 1,
        },
        rollback_of: None,
    };
    store.put(rec).unwrap();
    let f = Fake::with(&[("web", "web:2"), ("api", "api:1")]);
    let targets = Arc::new(targets().into_iter().map(|t| (t.name.clone(), t)).collect());
    let (tx, rx) = tokio::sync::mpsc::unbounded_channel();
    drop(tx);
    Worker { store: store.clone(), targets, cluster: f.clone(), timeout: LONG, stabilize: Duration::ZERO }.run(rx).await;
    assert_eq!(store.get("rel_x").unwrap().release.status, "rolled-back");
    assert_eq!(f.image("web"), "web:1");
}
