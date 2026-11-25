//! State machine and transport tests with a fake cluster.

mod common;

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use common::*;
use magi_release::api;
use magi_release::config::TargetConfig;
use magi_release::controller::Cluster;

/// Images named "*:bad-preflight" fail the preflight, "*:bad-rollout" fail
/// the rollout, and "*:unhealthy" fail the health check. With `break_restore`, restoring any image fails.
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
    async fn preflight(&self, t: &TargetConfig, image: &str, _release: &str) -> anyhow::Result<()> {
        self.log.lock().unwrap().push(format!("preflight {}={image}", t.deployment));
        if image.ends_with(":bad-preflight") { anyhow::bail!("exit code 1: workflow: missing field") }
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

    let b = s.call(&op, "rollback", api::encode_rollback_request(&api::RollbackRequest { id: r.id.clone() }).unwrap()).await.unwrap();
    let rb = api::decode_release(&b).unwrap();
    assert_eq!(rb.items[0].image, "web:1");
    assert_eq!(s.wait(&op, &rb.id, LONG).await.status, "succeeded");
    assert_eq!(f.image("web"), "web:1");
    assert_eq!(s.wait(&op, &r.id, LONG).await.status, "rolled-back");
    // A rolled-back release cannot be rolled back again.
    let e = s.call(&op, "rollback", api::encode_rollback_request(&api::RollbackRequest { id: r.id.clone() }).unwrap()).await.unwrap_err();
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
    assert_eq!(*f.log.lock().unwrap(), ["preflight web=web:2", "web=web:2", "preflight web=web:3", "web=web:3"]);
}

#[tokio::test]
async fn items_roll_out_in_order_with_preflight() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("api", "api:2"), ("web", "web:2")]).await.unwrap();
    assert_eq!(s.wait(&op, &r.id, LONG).await.status, "succeeded");
    assert_eq!(*f.log.lock().unwrap(), ["preflight api=api:2", "api=api:2", "preflight web=web:2", "web=web:2"]);
}

#[tokio::test]
async fn failed_preflight_changes_nothing() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("web", "web:bad-preflight"), ("api", "api:2")]).await.unwrap();
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "rolled-back");
    assert!(r.items[0].message.as_deref().unwrap().contains("preflight: exit code 1"));
    assert!(r.items[1].message.as_deref().unwrap().starts_with("not applied"));
    assert_eq!(*f.log.lock().unwrap(), ["preflight web=web:bad-preflight"]);
}

#[tokio::test]
async fn failed_preflight_restores_the_items_before() {
    let f = Fake::with(&[("web", "web:1"), ("api", "api:1")]);
    let s = start(f.clone(), targets(), 5, 0).await;
    let op = token("operator");
    let r = s.create(&op, &[("web", "web:2"), ("api", "api:bad-preflight")]).await.unwrap();
    let r = s.wait(&op, &r.id, LONG).await;
    assert_eq!(r.status, "rolled-back");
    assert_eq!((f.image("web"), f.image("api")), ("web:1".into(), "api:1".into()));
    assert_eq!(*f.log.lock().unwrap(), ["preflight web=web:2", "web=web:2", "preflight api=api:bad-preflight", "web=web:1"]);
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
    let e = s.call(&none, "get", api::encode_get_release_request(&api::GetReleaseRequest { id: r.id.clone() }).unwrap()).await.unwrap_err();
    assert_eq!(e.0, 404);
    let list = |t: String| {
        let s = &s;
        async move { api::decode_list_releases_response(&s.call(&t, "list", api::encode_list_releases_request(&api::ListReleasesRequest { limit: None }).unwrap()).await.unwrap()).unwrap() }
    };
    assert_eq!(list(rd.clone()).await.releases.len(), 1);
    assert_eq!(list(none.clone()).await.releases.len(), 0);
    let b = s.call(&rd, "list-targets", api::encode_list_targets_request(&api::ListTargetsRequest { name: Some("web".into()) }).unwrap()).await.unwrap();
    let t = api::decode_list_targets_response(&b).unwrap();
    assert_eq!(t.targets.len(), 1);
    assert_eq!(t.targets[0].health_port, Some(8080));
    assert_eq!(s.call(&none, "list-targets", api::encode_list_targets_request(&api::ListTargetsRequest { name: None }).unwrap()).await.unwrap_err().0, 403);
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

/// A database with instances that the test sets. It records the restarts.
#[derive(Clone, Default)]
struct FakeDb {
    instances: Arc<Mutex<Vec<api::DatabaseInstance>>>,
    restarted: Arc<Mutex<Vec<String>>>,
    refuse: bool,
}

impl FakeDb {
    fn set(&self, v: &[(&str, &str, bool)]) {
        *self.instances.lock().unwrap() = v.iter().map(|(n, r, ok)| api::DatabaseInstance { name: n.to_string(), role: r.to_string(), ready: *ok, restarts: 0 }).collect();
    }
}

impl magi_release::database::Database for FakeDb {
    async fn instances(&self) -> anyhow::Result<Vec<api::DatabaseInstance>> {
        Ok(self.instances.lock().unwrap().clone())
    }
    async fn restart(&self, name: &str) -> anyhow::Result<()> {
        if self.refuse {
            anyhow::bail!("forbidden");
        }
        self.restarted.lock().unwrap().push(name.into());
        Ok(())
    }
}

#[tokio::test]
async fn database_watch_restarts_and_reports() {
    use magi_release::database::{Watch, look_once};
    let r = start(Fake::default(), vec![], 5, 0).await;
    let read = token("reader");
    let status = |b: Vec<u8>| api::decode_database_status_response(&b).unwrap();
    let list = |b: Vec<u8>| api::decode_list_incidents_response(&b).unwrap().incidents;
    let status_req = api::encode_database_status_request(&api::DatabaseStatusRequest { cluster: None }).unwrap();
    let list_req = |after: u64| api::encode_list_incidents_request(&api::ListIncidentsRequest { after: Some(after), limit: None }).unwrap();
    // No watch looked yet.
    assert!(!status(r.call(&read, "database-status", status_req.clone()).await.unwrap()).watched);
    assert_eq!(r.call(&token("noscope"), "list-incidents", list_req(0)).await.unwrap_err().0, 403);

    let db = FakeDb::default();
    let mut w = Watch::new(Duration::from_secs(180), Duration::from_secs(600));
    let min = 60_000;
    db.set(&[("db-1", "primary", true), ("db-2", "replica", true)]);
    look_once(&db, &mut w, &r.incidents, 0).await;
    let st = status(r.call(&read, "database-status", status_req.clone()).await.unwrap());
    assert!(st.watched && st.instances.len() == 2 && st.instances[0].role == "primary");
    assert!(list(r.call(&read, "list-incidents", list_req(0)).await.unwrap()).is_empty());

    // The primary breaks: the standby takes over, and the old primary is
    // restarted after three minutes.
    db.set(&[("db-1", "primary", false), ("db-2", "primary", true)]);
    look_once(&db, &mut w, &r.incidents, min).await;
    look_once(&db, &mut w, &r.incidents, 4 * min).await;
    assert_eq!(*db.restarted.lock().unwrap(), ["db-1"]);
    db.set(&[("db-1", "replica", true), ("db-2", "primary", true)]);
    look_once(&db, &mut w, &r.incidents, 5 * min).await;
    let got = list(r.call(&read, "list-incidents", list_req(0)).await.unwrap());
    assert_eq!(got.iter().map(|i| (i.seq, i.kind.as_str(), i.instance.as_str())).collect::<Vec<_>>(),
        [(1, "failover", "db-2"), (2, "restarted", "db-1"), (3, "recovered", "db-1")]);
    assert!(got[1].message.contains("not ready for 3 minutes"));
    assert_eq!(list(r.call(&read, "list-incidents", list_req(2)).await.unwrap()).len(), 1);

    // A restart that the cluster refuses is an incident for a person.
    let db = FakeDb { refuse: true, ..Default::default() };
    let mut w = Watch::new(Duration::from_secs(180), Duration::from_secs(600));
    db.set(&[("db-1", "primary", false)]);
    look_once(&db, &mut w, &r.incidents, 0).await;
    look_once(&db, &mut w, &r.incidents, 3 * min).await;
    look_once(&db, &mut w, &r.incidents, 20 * min).await;
    let got = list(r.call(&read, "list-incidents", list_req(3)).await.unwrap());
    assert_eq!(got.len(), 1);
    assert!(got[0].kind == "unhealthy" && got[0].message.contains("the restart failed: forbidden"));
}
