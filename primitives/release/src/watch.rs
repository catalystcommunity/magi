//! The watch. The controller looks at the workloads of the installation:
//! the instances of the database and the pods of the services. It records a
//! change of the primary of the database, and it restarts a workload that
//! stays not ready (docs/decisions/0015-health.md).
//!
//! The watch is in this controller because the controller has the access to
//! the cluster, and because it keeps its records in a file. Thus the watch
//! works while the database does not.

use std::collections::{BTreeMap, HashMap};
use std::fs::{File, OpenOptions};
use std::future::Future;
use std::io::{BufRead, BufReader, Write};
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use k8s_openapi::api::core::v1::{Event, Pod};
use kube::api::{Api, DeleteParams, ListParams, LogParams};
use serde::{Deserialize, Serialize};

use crate::api::{Diagnosis, Incident, PodFacts, Workload};
use crate::cluster::LABEL_PREFLIGHT;

pub const DATABASE: &str = "database";
pub const SERVICE: &str = "service";
const APP: &str = "app.kubernetes.io/name";
/// A pod that the watch deletes gets this many seconds to stop. The
/// default of a database pod is 30 minutes, and the operator of the
/// database does not promote the standby before the old primary is gone.
const GRACE_SECONDS: u32 = 30;
const LOG_LINES: i64 = 40;

/// The subject of an incident: the kind and the name of the workload.
pub fn subject(w: &Workload) -> String {
    format!("{}/{}", w.kind, w.name)
}

/// Access to the workloads of the installation.
pub trait Workloads: Send + Sync + 'static {
    fn list(&self) -> impl Future<Output = anyhow::Result<Vec<Workload>>> + Send;
    /// Deletes the pods of the workload that are not ready. The platform
    /// makes them again with the same data. It returns their names.
    fn restart(&self, subject: &str) -> impl Future<Output = anyhow::Result<Vec<String>>> + Send;
    fn diagnose(&self, subject: &str) -> impl Future<Output = anyhow::Result<Diagnosis>> + Send;
}

/// The pods of one namespace: the instances of a CloudNativePG cluster and
/// the pods of the Deployments with a label.
#[derive(Clone)]
pub struct KubeWorkloads {
    pub client: kube::Client,
    pub namespace: String,
    /// The name of the database cluster.
    pub database: Option<String>,
    /// The label (name, value) of the pods of the services.
    pub services: Option<(String, String)>,
}

fn label<'a>(p: &'a Pod, key: &str) -> Option<&'a str> {
    p.metadata.labels.as_ref()?.get(key).map(String::as_str)
}

fn pod_ready(p: &Pod) -> bool {
    p.metadata.deletion_timestamp.is_none()
        && p.status.as_ref().and_then(|s| s.conditions.as_ref()).is_some_and(|c| c.iter().any(|c| c.type_ == "Ready" && c.status == "True"))
}

fn pod_restarts(p: &Pod) -> u64 {
    p.status.as_ref().and_then(|s| s.container_statuses.as_ref()).map_or(0, |c| c.iter().map(|c| c.restart_count.max(0) as u64).sum())
}

/// A pod that ended (a job, an evicted pod) is not a part of a workload.
fn ended(p: &Pod) -> bool {
    matches!(p.status.as_ref().and_then(|s| s.phase.as_deref()), Some("Succeeded") | Some("Failed"))
}

impl KubeWorkloads {
    fn pods(&self) -> Api<Pod> {
        Api::namespaced(self.client.clone(), &self.namespace)
    }

    /// The subject of the workload of a pod, or `None` for a pod that the
    /// watch does not look at (a job of the database operator, a preflight
    /// pod, a pod of another application).
    pub fn subject_of(&self, p: &Pod) -> Option<String> {
        if ended(p) {
            return None;
        }
        if let Some(db) = &self.database {
            if label(p, "cnpg.io/cluster") == Some(db.as_str()) {
                return label(p, "cnpg.io/instanceName").map(|n| format!("{DATABASE}/{n}"));
            }
        }
        let (k, v) = self.services.as_ref()?;
        if label(p, k) != Some(v.as_str()) || label(p, LABEL_PREFLIGHT).is_some() {
            return None;
        }
        label(p, APP).map(|n| format!("{SERVICE}/{n}"))
    }

    /// The workloads of a list of pods, in the order of their subjects.
    pub fn workloads(&self, pods: &[Pod]) -> Vec<Workload> {
        let mut by: BTreeMap<String, Vec<&Pod>> = BTreeMap::new();
        for p in pods {
            if let Some(s) = self.subject_of(p) {
                by.entry(s).or_default().push(p);
            }
        }
        by.into_iter().map(|(s, ps)| {
            let (kind, name) = s.split_once('/').unwrap_or(("", &s));
            Workload {
                name: name.to_string(),
                kind: kind.to_string(),
                role: ps.iter().find_map(|p| label(p, "cnpg.io/instanceRole")).map(str::to_string),
                ready: ps.iter().all(|p| pod_ready(p)),
                restarts: ps.iter().map(|p| pod_restarts(p)).sum(),
            }
        }).collect()
    }

    async fn pods_of(&self, subject: &str) -> anyhow::Result<Vec<Pod>> {
        let all = self.pods().list(&ListParams::default()).await?.items;
        Ok(all.into_iter().filter(|p| self.subject_of(p).as_deref() == Some(subject)).collect())
    }
}

/// The state of the first container of a pod, and how it stopped before.
fn container_state(p: &Pod) -> (String, Option<String>) {
    let Some(c) = p.status.as_ref().and_then(|s| s.container_statuses.as_ref()).and_then(|c| c.first()) else {
        let why = p.status.as_ref().and_then(|s| s.conditions.as_ref()).and_then(|c| c.iter().find(|c| c.status != "True"))
            .map(|c| format!("{}: {}", c.reason.clone().unwrap_or_default(), c.message.clone().unwrap_or_default()));
        return (why.unwrap_or_else(|| "no container".into()), None);
    };
    let state = match c.state.as_ref() {
        Some(s) if s.running.is_some() => "running".to_string(),
        Some(s) => {
            if let Some(w) = &s.waiting {
                format!("waiting: {} {}", w.reason.clone().unwrap_or_default(), w.message.clone().unwrap_or_default())
            } else if let Some(t) = &s.terminated {
                format!("stopped: {} exit code {}", t.reason.clone().unwrap_or_default(), t.exit_code)
            } else {
                "unknown".into()
            }
        }
        None => "unknown".into(),
    };
    let last = c.last_state.as_ref().and_then(|s| s.terminated.as_ref())
        .map(|t| format!("{} exit code {} {}", t.reason.clone().unwrap_or_default(), t.exit_code, t.message.clone().unwrap_or_default()).trim().to_string());
    (state.trim().to_string(), last)
}

impl Workloads for KubeWorkloads {
    async fn list(&self) -> anyhow::Result<Vec<Workload>> {
        Ok(self.workloads(&self.pods().list(&ListParams::default()).await?.items))
    }

    async fn restart(&self, subject: &str) -> anyhow::Result<Vec<String>> {
        let mut out = Vec::new();
        for p in self.pods_of(subject).await? {
            let name = p.metadata.name.clone().unwrap_or_default();
            if pod_ready(&p) || p.metadata.deletion_timestamp.is_some() {
                continue;
            }
            self.pods().delete(&name, &DeleteParams::default().grace_period(GRACE_SECONDS)).await?;
            out.push(name);
        }
        Ok(out)
    }

    async fn diagnose(&self, subject: &str) -> anyhow::Result<Diagnosis> {
        let events: Api<Event> = Api::namespaced(self.client.clone(), &self.namespace);
        let mut pods = Vec::new();
        for p in self.pods_of(subject).await? {
            let name = p.metadata.name.clone().unwrap_or_default();
            let (state, last_exit) = container_state(&p);
            // The log of the container that runs, or of the one before it
            // when this one did not start.
            let mut log = String::new();
            for previous in [false, true] {
                let lp = LogParams { tail_lines: Some(LOG_LINES), previous, ..Default::default() };
                if let Ok(l) = self.pods().logs(&name, &lp).await {
                    if !l.trim().is_empty() {
                        log = l;
                        break;
                    }
                }
            }
            let lp = ListParams::default().fields(&format!("involvedObject.name={name}"));
            let mut ev: Vec<_> = events.list(&lp).await.map(|l| l.items).unwrap_or_default();
            ev.sort_by_key(|e| e.last_timestamp.as_ref().map(|t| t.0));
            let ev = ev.iter().rev().take(10).rev()
                .map(|e| format!("{} {}: {}", e.type_.clone().unwrap_or_default(), e.reason.clone().unwrap_or_default(), e.message.clone().unwrap_or_default()))
                .collect();
            pods.push(PodFacts {
                name,
                phase: p.status.as_ref().and_then(|s| s.phase.clone()).unwrap_or_default(),
                ready: pod_ready(&p),
                restarts: pod_restarts(&p),
                state,
                last_exit,
                log,
                events: ev,
            });
        }
        anyhow::ensure!(!pods.is_empty(), "no pod of {subject}");
        Ok(Diagnosis { subject: subject.to_string(), pods })
    }
}

/// The calls of the API that act on the cluster. The API is not async, so
/// the calls block.
pub trait Operator: Send + Sync {
    fn diagnose(&self, subject: &str) -> anyhow::Result<Diagnosis>;
    fn restart(&self, subject: &str) -> anyhow::Result<Vec<String>>;
}

/// An `Operator` from `Workloads`, for a runtime with more than one thread.
pub struct Blocking<W>(pub W);

impl<W: Workloads> Operator for Blocking<W> {
    fn diagnose(&self, subject: &str) -> anyhow::Result<Diagnosis> {
        tokio::task::block_in_place(|| tokio::runtime::Handle::current().block_on(self.0.diagnose(subject)))
    }
    fn restart(&self, subject: &str) -> anyhow::Result<Vec<String>> {
        tokio::task::block_in_place(|| tokio::runtime::Handle::current().block_on(self.0.restart(subject)))
    }
}

/// What the watch found in one look.
#[derive(Debug, Clone, PartialEq)]
pub enum Finding {
    /// An incident to record.
    Incident { kind: &'static str, subject: String, message: String },
    /// A workload to restart. The caller records the result.
    Restart { subject: String, not_ready: Duration },
}

#[derive(Default)]
struct Seen {
    not_ready_since: Option<u64>,
    restarted_at: Option<u64>,
    /// An "unhealthy" incident is recorded for this time of trouble.
    flagged: bool,
}

/// The words for a workload in a message.
fn words(w: &Workload) -> String {
    match w.kind.as_str() {
        DATABASE => format!("Database instance {}", w.name),
        _ => format!("Service {}", w.name),
    }
}

/// The rules of the watch. It keeps no clock: each look gets the time.
pub struct Watch {
    /// A database instance that is not ready for this long is restarted.
    pub database_after: Duration,
    /// A service that is not ready for this long is restarted.
    pub service_after: Duration,
    /// A workload that is not ready this long after the restart is
    /// "unhealthy". The watch restarts a workload one time only, until the
    /// workload was ready again.
    pub give_up_after: Duration,
    primary: Option<String>,
    seen: HashMap<String, Seen>,
}

impl Watch {
    pub fn new(database_after: Duration, service_after: Duration, give_up_after: Duration) -> Self {
        Watch { database_after, service_after, give_up_after, primary: None, seen: HashMap::new() }
    }

    /// `releasing` is true while a release runs: the release has its own
    /// checks and its own way back.
    pub fn look(&mut self, now_ms: u64, workloads: &[Workload], releasing: bool) -> Vec<Finding> {
        let mut out = Vec::new();
        let databases: Vec<_> = workloads.iter().filter(|w| w.kind == DATABASE).collect();
        // The primary is the ready instance with that role. During a
        // failover no instance has it for a moment.
        let primary = databases.iter().find(|w| w.ready && w.role.as_deref() == Some("primary"));
        if let Some(p) = primary {
            if let Some(old) = self.primary.as_ref().filter(|old| **old != p.name) {
                out.push(Finding::Incident {
                    kind: "failover",
                    subject: subject(p),
                    message: format!("The primary of the database changed from {old} to {}.", p.name),
                });
            }
            self.primary = Some(p.name.clone());
        }
        // A service is not ready while the database has no primary. The
        // cause is the database, and a restart of the service does not help.
        let hold_services = releasing || (!databases.is_empty() && primary.is_none());
        for w in workloads {
            let key = subject(w);
            let s = self.seen.entry(key.clone()).or_default();
            if w.ready {
                if s.restarted_at.is_some() || s.flagged {
                    out.push(Finding::Incident { kind: "recovered", subject: key, message: format!("{} is ready again.", words(w)) });
                }
                *s = Seen::default();
                continue;
            }
            if w.kind != DATABASE && hold_services {
                // The time starts again when the cause is gone.
                if s.restarted_at.is_none() {
                    s.not_ready_since = None;
                }
                continue;
            }
            let after = if w.kind == DATABASE { self.database_after } else { self.service_after };
            let since = *s.not_ready_since.get_or_insert(now_ms);
            match s.restarted_at {
                None if now_ms.saturating_sub(since) >= after.as_millis() as u64 => {
                    s.restarted_at = Some(now_ms);
                    out.push(Finding::Restart { subject: key, not_ready: Duration::from_millis(now_ms - since) });
                }
                Some(at) if !s.flagged && now_ms.saturating_sub(at) >= self.give_up_after.as_millis() as u64 => {
                    s.flagged = true;
                    out.push(Finding::Incident {
                        kind: "unhealthy",
                        subject: key,
                        message: format!("{} is still not ready {} minutes after its restart.", words(w), self.give_up_after.as_secs() / 60),
                    });
                }
                _ => {}
            }
        }
        out
    }

    /// The restart of `subject` failed: the rules have no more to do.
    pub fn restart_failed(&mut self, subject: &str) {
        if let Some(s) = self.seen.get_mut(subject) {
            s.flagged = true;
        }
    }
}

#[derive(Serialize, Deserialize)]
struct IncidentJson {
    seq: u64,
    kind: String,
    subject: String,
    message: String,
    at_ms: u64,
}

struct Log {
    file: Option<File>,
    items: Vec<Incident>,
}

/// The incidents of the watch, in a file of the data directory, and the
/// last state that the watch saw.
pub struct Incidents {
    log: Mutex<Log>,
    status: Mutex<Option<Vec<Workload>>>,
}

impl Incidents {
    /// With no directory, the incidents are in memory only (tests).
    pub fn open(dir: Option<&Path>) -> anyhow::Result<Self> {
        let mut items = Vec::new();
        let mut file = None;
        if let Some(dir) = dir {
            std::fs::create_dir_all(dir)?;
            let path = dir.join("incidents.jsonl");
            if let Ok(f) = File::open(&path) {
                for line in BufReader::new(f).lines() {
                    if let Ok(i) = serde_json::from_str::<IncidentJson>(&line?) {
                        items.push(Incident { seq: i.seq, kind: i.kind, subject: i.subject, message: i.message, at_ms: i.at_ms });
                    }
                }
            }
            file = Some(OpenOptions::new().create(true).append(true).open(&path)?);
        }
        Ok(Incidents { log: Mutex::new(Log { file, items }), status: Mutex::new(None) })
    }

    pub fn add(&self, kind: &str, subject: &str, message: &str, at_ms: u64) -> Incident {
        let mut log = self.log.lock().unwrap();
        let seq = log.items.last().map_or(1, |i| i.seq + 1);
        let i = Incident { seq, kind: kind.into(), subject: subject.into(), message: message.into(), at_ms };
        if let Some(f) = log.file.as_mut() {
            let line = serde_json::to_string(&IncidentJson { seq, kind: i.kind.clone(), subject: i.subject.clone(), message: i.message.clone(), at_ms }).unwrap_or_default();
            if let Err(e) = writeln!(f, "{line}").and_then(|_| f.sync_data()) {
                tracing::error!(error = %e, "write incident");
            }
        }
        log.items.push(i.clone());
        i
    }

    /// The incidents with seq greater than `after`, the oldest first.
    pub fn after(&self, after: u64, limit: usize) -> Vec<Incident> {
        self.log.lock().unwrap().items.iter().filter(|i| i.seq > after).take(limit).cloned().collect()
    }

    pub fn set_status(&self, workloads: Vec<Workload>) {
        *self.status.lock().unwrap() = Some(workloads);
    }

    /// `None` when no watch looked at the installation.
    pub fn status(&self) -> Option<Vec<Workload>> {
        self.status.lock().unwrap().clone()
    }
}

/// One look of the watch: read the workloads, record the findings, and
/// restart what the rules say.
pub async fn look_once<W: Workloads>(cluster: &W, watch: &mut Watch, incidents: &Incidents, now_ms: u64, releasing: bool) {
    let workloads = match cluster.list().await {
        Ok(w) => w,
        Err(e) => {
            tracing::warn!(error = %e, "watch: read workloads");
            return;
        }
    };
    for f in watch.look(now_ms, &workloads, releasing) {
        match f {
            Finding::Incident { kind, subject, message } => {
                tracing::warn!(kind, subject, message, "incident");
                incidents.add(kind, &subject, &message, now_ms);
            }
            Finding::Restart { subject, not_ready } => {
                let what = workloads.iter().find(|w| self::subject(w) == subject).map(words).unwrap_or_else(|| subject.clone());
                let mins = not_ready.as_secs_f64() / 60.0;
                match cluster.restart(&subject).await {
                    Ok(pods) => {
                        let m = format!("{what} was not ready for {mins:.1} minutes. The controller restarted it.");
                        tracing::warn!(subject, pods = pods.join(","), "watch: restarted a workload");
                        incidents.add("restarted", &subject, &m, now_ms);
                    }
                    Err(e) => {
                        watch.restart_failed(&subject);
                        let m = format!("{what} was not ready for {mins:.1} minutes, and the restart failed: {e}.");
                        tracing::error!(subject, error = %e, "watch: restart failed");
                        incidents.add("unhealthy", &subject, &m, now_ms);
                    }
                }
            }
        }
    }
    incidents.set_status(workloads);
}

/// Looks at the installation each `every`, until the task is stopped.
pub async fn run<W: Workloads>(cluster: W, mut watch: Watch, incidents: Arc<Incidents>, every: Duration, releasing: impl Fn() -> bool + Send + 'static) {
    loop {
        look_once(&cluster, &mut watch, &incidents, crate::now_ms(), releasing()).await;
        tokio::time::sleep(every).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn db(name: &str, role: &str, ready: bool) -> Workload {
        Workload { name: name.into(), kind: DATABASE.into(), role: Some(role.into()), ready, restarts: 0 }
    }

    fn svc(name: &str, ready: bool) -> Workload {
        Workload { name: name.into(), kind: SERVICE.into(), role: None, ready, restarts: 0 }
    }

    fn watch() -> Watch {
        Watch::new(Duration::from_secs(90), Duration::from_secs(180), Duration::from_secs(600))
    }

    const MIN: u64 = 60_000;

    #[test]
    fn failover_is_one_incident() {
        let mut w = watch();
        assert!(w.look(0, &[db("db-1", "primary", true), db("db-2", "replica", true)], false).is_empty());
        // During the failover no ready instance is the primary.
        assert!(w.look(1000, &[db("db-1", "primary", false), db("db-2", "replica", true)], false).is_empty());
        let f = w.look(2000, &[db("db-1", "primary", false), db("db-2", "primary", true)], false);
        assert_eq!(f.len(), 1);
        assert!(matches!(&f[0], Finding::Incident { kind: "failover", subject, message } if subject == "database/db-2" && message.contains("from db-1 to db-2")));
        assert!(w.look(3000, &[db("db-1", "replica", false), db("db-2", "primary", true)], false).is_empty());
    }

    #[test]
    fn restarts_one_time_then_gives_up() {
        let mut w = watch();
        let down = [db("db-1", "primary", false)];
        assert!(w.look(0, &down, false).is_empty());
        assert!(w.look(MIN, &down, false).is_empty());
        assert_eq!(w.look(MIN + MIN / 2, &down, false), vec![Finding::Restart { subject: "database/db-1".into(), not_ready: Duration::from_secs(90) }]);
        // No second restart.
        assert!(w.look(8 * MIN, &down, false).is_empty());
        let f = w.look(12 * MIN, &down, false);
        assert!(matches!(&f[..], [Finding::Incident { kind: "unhealthy", .. }]));
        assert!(w.look(30 * MIN, &down, false).is_empty());
        // Ready again: one incident, and the rules start again.
        let f = w.look(31 * MIN, &[db("db-1", "primary", true)], false);
        assert!(matches!(&f[..], [Finding::Incident { kind: "recovered", .. }]));
        assert!(w.look(32 * MIN, &[db("db-1", "primary", true)], false).is_empty());
        assert!(w.look(33 * MIN, &down, false).is_empty());
        assert!(matches!(&w.look(35 * MIN, &down, false)[..], [Finding::Restart { .. }]));
    }

    #[test]
    fn a_short_stop_is_no_incident() {
        let mut w = watch();
        assert!(w.look(0, &[svc("web", false)], false).is_empty());
        assert!(w.look(MIN, &[svc("web", true)], false).is_empty());
        // The time starts again with the next stop.
        assert!(w.look(2 * MIN, &[svc("web", false)], false).is_empty());
        assert!(w.look(4 * MIN, &[svc("web", false)], false).is_empty());
        assert_eq!(w.look(5 * MIN, &[svc("web", false)], false), vec![Finding::Restart { subject: "service/web".into(), not_ready: Duration::from_secs(180) }]);
    }

    #[test]
    fn a_service_waits_for_the_database_and_for_a_release() {
        let mut w = watch();
        // The database has no primary: the services are not the cause.
        let down = [db("db-1", "primary", false), svc("web", false)];
        assert!(w.look(0, &down, false).is_empty());
        let f = w.look(2 * MIN, &down, false);
        assert_eq!(f, vec![Finding::Restart { subject: "database/db-1".into(), not_ready: Duration::from_secs(120) }]);
        assert!(w.look(5 * MIN, &down, false).is_empty());
        // The database is back: the time of the service starts now.
        let up = [db("db-1", "primary", true), svc("web", false)];
        assert!(matches!(&w.look(6 * MIN, &up, false)[..], [Finding::Incident { kind: "recovered", .. }]));
        assert!(w.look(8 * MIN, &up, false).is_empty());
        // A release runs: it has its own checks.
        assert!(w.look(20 * MIN, &up, true).is_empty());
        assert!(w.look(21 * MIN, &up, false).is_empty());
        assert_eq!(w.look(24 * MIN, &up, false), vec![Finding::Restart { subject: "service/web".into(), not_ready: Duration::from_secs(180) }]);
    }

    #[test]
    fn incidents_stay_in_the_file() {
        let dir = tempfile::tempdir().unwrap();
        let a = Incidents::open(Some(dir.path())).unwrap();
        assert_eq!(a.add("failover", "database/db-2", "m1", 5).seq, 1);
        assert_eq!(a.add("restarted", "database/db-1", "m2", 6).seq, 2);
        let b = Incidents::open(Some(dir.path())).unwrap();
        assert_eq!(b.after(1, 10).iter().map(|i| i.kind.as_str()).collect::<Vec<_>>(), ["restarted"]);
        assert_eq!(b.add("recovered", "database/db-1", "m3", 7).seq, 3);
        assert_eq!(b.after(0, 2).len(), 2);
        assert!(b.status().is_none());
    }

    /// The pods of a namespace become workloads. This needs no cluster:
    /// the client is not used.
    #[tokio::test]
    async fn pods_become_workloads() {
        let pod = |name: &str, labels: serde_json::Value, ready: &str, phase: &str| -> Pod {
            serde_json::from_value(serde_json::json!({
                "metadata": {"name": name, "labels": labels},
                "status": {"phase": phase, "conditions": [{"type": "Ready", "status": ready}],
                    "containerStatuses": [{"name": "c", "restartCount": 2, "ready": false, "image": "i", "imageID": "i",
                        "state": {"waiting": {"reason": "CrashLoopBackOff", "message": "back-off"}},
                        "lastState": {"terminated": {"exitCode": 1, "reason": "Error"}}}]},
            })).unwrap()
        };
        let cfg = kube::Config::new("http://127.0.0.1:1".parse().unwrap());
        let k = KubeWorkloads { client: kube::Client::try_from(cfg).unwrap(), namespace: "magi".into(), database: Some("db".into()),
            services: Some(("app.kubernetes.io/part-of".into(), "magi".into())) };
        let part = |name: &str| serde_json::json!({"app.kubernetes.io/part-of": "magi", "app.kubernetes.io/name": name});
        let pods = [
            pod("db-1", serde_json::json!({"cnpg.io/cluster": "db", "cnpg.io/instanceName": "db-1", "cnpg.io/instanceRole": "primary"}), "False", "Running"),
            // A job of the database operator, a pod of another cluster, a
            // preflight pod, and a pod that ended are not workloads.
            pod("db-2-join", serde_json::json!({"cnpg.io/cluster": "db", "cnpg.io/jobRole": "join"}), "True", "Running"),
            pod("other-1", serde_json::json!({"cnpg.io/cluster": "other", "cnpg.io/instanceName": "other-1"}), "True", "Running"),
            pod("web-pre", serde_json::json!({"app.kubernetes.io/part-of": "magi", "app.kubernetes.io/name": "web", "magi.release/preflight": "rel_1"}), "False", "Running"),
            pod("web-old", part("web"), "False", "Failed"),
            pod("web-a", part("web"), "True", "Running"),
            pod("web-b", part("web"), "False", "Running"),
            pod("api-a", part("api"), "True", "Running"),
        ];
        let w = k.workloads(&pods);
        assert_eq!(w.iter().map(|w| (subject(w), w.ready, w.restarts)).collect::<Vec<_>>(),
            [("database/db-1".to_string(), false, 2), ("service/api".to_string(), true, 2), ("service/web".to_string(), false, 4)]);
        assert_eq!(w[0].role.as_deref(), Some("primary"));
        let (state, last) = container_state(&pods[0]);
        assert_eq!((state.as_str(), last.as_deref()), ("waiting: CrashLoopBackOff back-off", Some("Error exit code 1")));
    }
}
