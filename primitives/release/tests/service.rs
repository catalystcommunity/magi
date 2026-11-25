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

/// Workloads that the test sets. It records the restarts.
#[derive(Clone, Default)]
struct FakeWorkloads {
    workloads: Arc<Mutex<Vec<api::Workload>>>,
    restarted: Arc<Mutex<Vec<String>>>,
    refuse: bool,
}

impl FakeWorkloads {
    /// (subject, role, ready)
    fn set(&self, v: &[(&str, &str, bool)]) {
        *self.workloads.lock().unwrap() = v.iter().map(|(s, r, ok)| {
            let (kind, name) = s.split_once('/').unwrap();
            api::Workload { name: name.into(), kind: kind.into(), role: (!r.is_empty()).then(|| r.to_string()), ready: *ok, restarts: 0 }
        }).collect();
    }
    fn do_restart(&self, subject: &str) -> anyhow::Result<Vec<String>> {
        if self.refuse {
            anyhow::bail!("forbidden");
        }
        self.restarted.lock().unwrap().push(subject.into());
        Ok(vec![format!("{}-pod", subject.split_once('/').unwrap().1)])
    }
    fn facts(&self, subject: &str) -> anyhow::Result<api::Diagnosis> {
        Ok(api::Diagnosis { subject: subject.into(), pods: vec![api::PodFacts {
            name: "web-a".into(), phase: "Running".into(), ready: false, restarts: 7, state: "waiting: CrashLoopBackOff".into(),
            last_exit: Some("Error exit code 1".into()), log: "panic: no config\n".into(), events: vec!["Warning BackOff: restarting".into()],
        }] })
    }
}

impl magi_release::watch::Workloads for FakeWorkloads {
    async fn list(&self) -> anyhow::Result<Vec<api::Workload>> {
        Ok(self.workloads.lock().unwrap().clone())
    }
    async fn restart(&self, subject: &str) -> anyhow::Result<Vec<String>> {
        self.do_restart(subject)
    }
    async fn diagnose(&self, subject: &str) -> anyhow::Result<api::Diagnosis> {
        self.facts(subject)
    }
}

impl magi_release::watch::Operator for FakeWorkloads {
    fn diagnose(&self, subject: &str) -> anyhow::Result<api::Diagnosis> {
        self.facts(subject)
    }
    fn restart(&self, subject: &str) -> anyhow::Result<Vec<String>> {
        self.do_restart(subject)
    }
}

#[tokio::test]
async fn watch_restarts_and_reports() {
    use magi_release::watch::{Watch, look_once};
    let cluster = FakeWorkloads::default();
    let r = start_with(Fake::default(), vec![], 5, 0, Some(Arc::new(cluster.clone()))).await;
    let (read, op) = (token("reader"), token("operator"));
    let status = |b: Vec<u8>| api::decode_health_status_response(&b).unwrap();
    let list = |b: Vec<u8>| api::decode_list_incidents_response(&b).unwrap().incidents;
    let status_req = |kind: Option<&str>| api::encode_health_status_request(&api::HealthStatusRequest { kind: kind.map(str::to_string) }).unwrap();
    let list_req = |after: u64| api::encode_list_incidents_request(&api::ListIncidentsRequest { after: Some(after), limit: None }).unwrap();
    let restart_req = |s: &str| api::encode_restart_workload_request(&api::RestartWorkloadRequest { subject: s.into(), reason: Some("the log shows a lock that stays".into()) }).unwrap();
    let diagnose_req = |s: &str| api::encode_diagnose_request(&api::DiagnoseRequest { subject: s.into() }).unwrap();
    // No watch looked yet.
    assert!(!status(r.call(&read, "health-status", status_req(None)).await.unwrap()).watched);
    assert_eq!(r.call(&token("noscope"), "list-incidents", list_req(0)).await.unwrap_err().0, 403);
    assert_eq!(r.call(&read, "diagnose", diagnose_req("service/web")).await.unwrap_err().0, 404);

    let mut w = Watch::new(Duration::from_secs(90), Duration::from_secs(180), Duration::from_secs(600));
    let min = 60_000;
    cluster.set(&[("database/db-1", "primary", true), ("database/db-2", "replica", true), ("service/web", "", true)]);
    look_once(&cluster, &mut w, &r.incidents, 0, false).await;
    let st = status(r.call(&read, "health-status", status_req(None)).await.unwrap());
    assert!(st.watched && st.workloads.len() == 3 && st.workloads[0].role.as_deref() == Some("primary"));
    assert_eq!(status(r.call(&read, "health-status", status_req(Some("service"))).await.unwrap()).workloads.len(), 1);
    assert!(list(r.call(&read, "list-incidents", list_req(0)).await.unwrap()).is_empty());

    // The primary breaks: the old primary is restarted after 90 seconds,
    // the standby takes over, and the old primary joins again.
    cluster.set(&[("database/db-1", "primary", false), ("database/db-2", "replica", true), ("service/web", "", false)]);
    look_once(&cluster, &mut w, &r.incidents, min, false).await;
    look_once(&cluster, &mut w, &r.incidents, 3 * min, false).await;
    assert_eq!(*cluster.restarted.lock().unwrap(), ["database/db-1"]);
    cluster.set(&[("database/db-1", "replica", false), ("database/db-2", "primary", true), ("service/web", "", true)]);
    look_once(&cluster, &mut w, &r.incidents, 4 * min, false).await;
    cluster.set(&[("database/db-1", "replica", true), ("database/db-2", "primary", true), ("service/web", "", true)]);
    look_once(&cluster, &mut w, &r.incidents, 5 * min, false).await;
    let got = list(r.call(&read, "list-incidents", list_req(0)).await.unwrap());
    assert_eq!(got.iter().map(|i| (i.seq, i.kind.as_str(), i.subject.as_str())).collect::<Vec<_>>(),
        [(1, "restarted", "database/db-1"), (2, "failover", "database/db-2"), (3, "recovered", "database/db-1")]);
    assert!(got[0].message.contains("Database instance db-1 was not ready for 2.0 minutes"));
    assert_eq!(list(r.call(&read, "list-incidents", list_req(2)).await.unwrap()).len(), 1);

    // The API: the facts of a workload, and a restart on request. Only a
    // workload that the watch saw, and only with the scope.
    let d = api::decode_diagnosis(&r.call(&read, "diagnose", diagnose_req("service/web")).await.unwrap()).unwrap();
    assert!(d.pods[0].log.contains("no config") && d.pods[0].restarts == 7);
    assert_eq!(r.call(&read, "restart-workload", restart_req("service/web")).await.unwrap_err().0, 403);
    assert_eq!(r.call(&op, "restart-workload", restart_req("service/other")).await.unwrap_err().0, 404);
    let done = api::decode_restart_workload_response(&r.call(&op, "restart-workload", restart_req("service/web")).await.unwrap()).unwrap();
    assert_eq!(done.restarted, ["web-pod"]);
    let got = list(r.call(&read, "list-incidents", list_req(3)).await.unwrap());
    assert!(got.len() == 1 && got[0].kind == "restarted" && got[0].message.contains("on request of") && got[0].message.contains("a lock that stays"));

    // A restart that the cluster refuses is an incident.
    let cluster = FakeWorkloads { refuse: true, ..Default::default() };
    let mut w = Watch::new(Duration::from_secs(90), Duration::from_secs(180), Duration::from_secs(600));
    cluster.set(&[("service/web", "", false)]);
    look_once(&cluster, &mut w, &r.incidents, 0, false).await;
    look_once(&cluster, &mut w, &r.incidents, 3 * min, false).await;
    look_once(&cluster, &mut w, &r.incidents, 20 * min, false).await;
    let got = list(r.call(&read, "list-incidents", list_req(4)).await.unwrap());
    assert_eq!(got.len(), 1);
    assert!(got[0].kind == "unhealthy" && got[0].message.contains("the restart failed: forbidden"));
}
