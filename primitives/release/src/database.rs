//! The database watch. The controller looks at the instances of the
//! database of the installation. It records a change of the primary, and it
//! restarts an instance that stays not ready.
//!
//! The watch is in this controller because the controller has the access to
//! the cluster, and because it keeps its records in a file. Thus the watch
//! works while the database does not.

use std::collections::HashMap;
use std::fs::{File, OpenOptions};
use std::future::Future;
use std::io::{BufRead, BufReader, Write};
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use k8s_openapi::api::core::v1::Pod;
use kube::api::{Api, DeleteParams, ListParams};
use serde::{Deserialize, Serialize};

use crate::api::{DatabaseIncident, DatabaseInstance};

/// Access to the instances of one database cluster.
pub trait Database: Send + Sync + 'static {
    fn instances(&self) -> impl Future<Output = anyhow::Result<Vec<DatabaseInstance>>> + Send;
    /// Stops the instance. The operator of the database starts it again
    /// with the same data.
    fn restart(&self, name: &str) -> impl Future<Output = anyhow::Result<()>> + Send;
}

/// The pods of a CloudNativePG cluster.
#[derive(Clone)]
pub struct KubeDatabase {
    pub client: kube::Client,
    pub namespace: String,
    pub cluster: String,
}

impl KubeDatabase {
    fn pods(&self) -> Api<Pod> {
        Api::namespaced(self.client.clone(), &self.namespace)
    }
}

/// The instance of a pod of the operator. A pod with no instance name (a
/// job of the operator) is not an instance.
pub fn instance_of(p: &Pod) -> Option<DatabaseInstance> {
    let labels = p.metadata.labels.as_ref()?;
    labels.get("cnpg.io/instanceName")?;
    let st = p.status.as_ref();
    let ready = p.metadata.deletion_timestamp.is_none()
        && st.and_then(|s| s.conditions.as_ref()).is_some_and(|c| c.iter().any(|c| c.type_ == "Ready" && c.status == "True"));
    let restarts = st.and_then(|s| s.container_statuses.as_ref()).map_or(0, |c| c.iter().map(|c| c.restart_count.max(0) as u64).sum());
    Some(DatabaseInstance {
        name: p.metadata.name.clone().unwrap_or_default(),
        role: labels.get("cnpg.io/instanceRole").cloned().unwrap_or_default(),
        ready,
        restarts,
    })
}

impl Database for KubeDatabase {
    async fn instances(&self) -> anyhow::Result<Vec<DatabaseInstance>> {
        let lp = ListParams::default().labels(&format!("cnpg.io/cluster={}", self.cluster));
        let mut out: Vec<_> = self.pods().list(&lp).await?.items.iter().filter_map(instance_of).collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }

    async fn restart(&self, name: &str) -> anyhow::Result<()> {
        // Only a pod of this cluster: the name comes from `instances`.
        let p = self.pods().get(name).await?;
        let of = p.metadata.labels.as_ref().and_then(|l| l.get("cnpg.io/cluster")).cloned().unwrap_or_default();
        anyhow::ensure!(of == self.cluster, "pod {name} is not an instance of {}", self.cluster);
        self.pods().delete(name, &DeleteParams::default()).await?;
        Ok(())
    }
}

/// What the watch found in one look.
#[derive(Debug, Clone, PartialEq)]
pub enum Finding {
    /// An incident to record.
    Incident { kind: &'static str, instance: String, message: String },
    /// An instance to restart. The caller records the result.
    Restart { instance: String, not_ready: Duration },
}

#[derive(Default)]
struct Seen {
    not_ready_since: Option<u64>,
    restarted_at: Option<u64>,
    /// An "unhealthy" incident is recorded for this time of trouble.
    flagged: bool,
}

/// The rules of the watch. It keeps no clock: each look gets the time.
pub struct Watch {
    /// An instance that is not ready for this long is restarted.
    pub restart_after: Duration,
    /// An instance that is not ready this long after the restart needs a
    /// person. The watch restarts an instance one time only, until the
    /// instance was ready again.
    pub give_up_after: Duration,
    primary: Option<String>,
    seen: HashMap<String, Seen>,
}

impl Watch {
    pub fn new(restart_after: Duration, give_up_after: Duration) -> Self {
        Watch { restart_after, give_up_after, primary: None, seen: HashMap::new() }
    }

    pub fn look(&mut self, now_ms: u64, instances: &[DatabaseInstance]) -> Vec<Finding> {
        let mut out = Vec::new();
        // The primary is the ready instance with that role. During a
        // failover no instance has it for a moment.
        if let Some(p) = instances.iter().find(|i| i.ready && i.role == "primary") {
            if let Some(old) = self.primary.as_ref().filter(|old| **old != p.name) {
                out.push(Finding::Incident {
                    kind: "failover",
                    instance: p.name.clone(),
                    message: format!("The primary of the database changed from {old} to {}.", p.name),
                });
            }
            self.primary = Some(p.name.clone());
        }
        for i in instances {
            let s = self.seen.entry(i.name.clone()).or_default();
            if i.ready {
                if s.restarted_at.is_some() || s.flagged {
                    out.push(Finding::Incident { kind: "recovered", instance: i.name.clone(), message: format!("Database instance {} is ready again.", i.name) });
                }
                *s = Seen::default();
                continue;
            }
            let since = *s.not_ready_since.get_or_insert(now_ms);
            match s.restarted_at {
                None if now_ms.saturating_sub(since) >= self.restart_after.as_millis() as u64 => {
                    s.restarted_at = Some(now_ms);
                    out.push(Finding::Restart { instance: i.name.clone(), not_ready: Duration::from_millis(now_ms - since) });
                }
                Some(at) if !s.flagged && now_ms.saturating_sub(at) >= self.give_up_after.as_millis() as u64 => {
                    s.flagged = true;
                    out.push(Finding::Incident {
                        kind: "unhealthy",
                        instance: i.name.clone(),
                        message: format!(
                            "Database instance {} is still not ready {} minutes after its restart. A person must look at it.",
                            i.name,
                            self.give_up_after.as_secs() / 60
                        ),
                    });
                }
                _ => {}
            }
        }
        out
    }

    /// The restart of `instance` failed: a person must look.
    pub fn restart_failed(&mut self, instance: &str) {
        if let Some(s) = self.seen.get_mut(instance) {
            s.flagged = true;
        }
    }
}

#[derive(Serialize, Deserialize)]
struct IncidentJson {
    seq: u64,
    kind: String,
    instance: String,
    message: String,
    at_ms: u64,
}

struct Log {
    file: Option<File>,
    items: Vec<DatabaseIncident>,
}

/// The incidents of the watch, in a file of the data directory, and the
/// last state that the watch saw.
pub struct Incidents {
    log: Mutex<Log>,
    status: Mutex<Option<Vec<DatabaseInstance>>>,
}

impl Incidents {
    /// With no directory, the incidents are in memory only (tests).
    pub fn open(dir: Option<&Path>) -> anyhow::Result<Self> {
        let mut items = Vec::new();
        let mut file = None;
        if let Some(dir) = dir {
            std::fs::create_dir_all(dir)?;
            let path = dir.join("database-incidents.jsonl");
            if let Ok(f) = File::open(&path) {
                for line in BufReader::new(f).lines() {
                    if let Ok(i) = serde_json::from_str::<IncidentJson>(&line?) {
                        items.push(DatabaseIncident { seq: i.seq, kind: i.kind, instance: i.instance, message: i.message, at_ms: i.at_ms });
                    }
                }
            }
            file = Some(OpenOptions::new().create(true).append(true).open(&path)?);
        }
        Ok(Incidents { log: Mutex::new(Log { file, items }), status: Mutex::new(None) })
    }

    pub fn add(&self, kind: &str, instance: &str, message: &str, at_ms: u64) -> DatabaseIncident {
        let mut log = self.log.lock().unwrap();
        let seq = log.items.last().map_or(1, |i| i.seq + 1);
        let i = DatabaseIncident { seq, kind: kind.into(), instance: instance.into(), message: message.into(), at_ms };
        if let Some(f) = log.file.as_mut() {
            let line = serde_json::to_string(&IncidentJson { seq, kind: i.kind.clone(), instance: i.instance.clone(), message: i.message.clone(), at_ms }).unwrap_or_default();
            if let Err(e) = writeln!(f, "{line}").and_then(|_| f.sync_data()) {
                tracing::error!(error = %e, "write database incident");
            }
        }
        log.items.push(i.clone());
        i
    }

    /// The incidents with seq greater than `after`, the oldest first.
    pub fn after(&self, after: u64, limit: usize) -> Vec<DatabaseIncident> {
        self.log.lock().unwrap().items.iter().filter(|i| i.seq > after).take(limit).cloned().collect()
    }

    pub fn set_status(&self, instances: Vec<DatabaseInstance>) {
        *self.status.lock().unwrap() = Some(instances);
    }

    /// `None` when no watch looked at a database.
    pub fn status(&self) -> Option<Vec<DatabaseInstance>> {
        self.status.lock().unwrap().clone()
    }
}

/// One look of the watch: read the instances, record the findings, and
/// restart what the rules say.
pub async fn look_once<D: Database>(db: &D, watch: &mut Watch, incidents: &Incidents, now_ms: u64) {
    let instances = match db.instances().await {
        Ok(i) => i,
        Err(e) => {
            tracing::warn!(error = %e, "database watch: read instances");
            return;
        }
    };
    for f in watch.look(now_ms, &instances) {
        match f {
            Finding::Incident { kind, instance, message } => {
                tracing::warn!(kind, instance, message, "database incident");
                incidents.add(kind, &instance, &message, now_ms);
            }
            Finding::Restart { instance, not_ready } => {
                let mins = not_ready.as_secs() / 60;
                match db.restart(&instance).await {
                    Ok(()) => {
                        let m = format!("Database instance {instance} was not ready for {mins} minutes. The controller restarted it.");
                        tracing::warn!(instance, "database watch: restarted an instance");
                        incidents.add("restarted", &instance, &m, now_ms);
                    }
                    Err(e) => {
                        watch.restart_failed(&instance);
                        let m = format!("Database instance {instance} was not ready for {mins} minutes, and the restart failed: {e}. A person must look at it.");
                        tracing::error!(instance, error = %e, "database watch: restart failed");
                        incidents.add("unhealthy", &instance, &m, now_ms);
                    }
                }
            }
        }
    }
    incidents.set_status(instances);
}

/// Looks at the database each `every`, until the task is stopped.
pub async fn run<D: Database>(db: D, mut watch: Watch, incidents: Arc<Incidents>, every: Duration) {
    loop {
        look_once(&db, &mut watch, &incidents, crate::now_ms()).await;
        tokio::time::sleep(every).await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn inst(name: &str, role: &str, ready: bool) -> DatabaseInstance {
        DatabaseInstance { name: name.into(), role: role.into(), ready, restarts: 0 }
    }

    const MIN: u64 = 60_000;

    #[test]
    fn failover_is_one_incident() {
        let mut w = Watch::new(Duration::from_secs(180), Duration::from_secs(600));
        assert!(w.look(0, &[inst("db-1", "primary", true), inst("db-2", "replica", true)]).is_empty());
        // During the failover no ready instance is the primary.
        assert!(w.look(1000, &[inst("db-1", "primary", false), inst("db-2", "replica", true)]).is_empty());
        let f = w.look(2000, &[inst("db-1", "primary", false), inst("db-2", "primary", true)]);
        assert_eq!(f.len(), 1);
        assert!(matches!(&f[0], Finding::Incident { kind: "failover", instance, message } if instance == "db-2" && message.contains("from db-1 to db-2")));
        assert!(w.look(3000, &[inst("db-1", "replica", false), inst("db-2", "primary", true)]).is_empty());
    }

    #[test]
    fn restarts_one_time_then_asks_a_person() {
        let mut w = Watch::new(Duration::from_secs(180), Duration::from_secs(600));
        let down = [inst("db-1", "primary", false)];
        assert!(w.look(0, &down).is_empty());
        assert!(w.look(2 * MIN, &down).is_empty());
        assert_eq!(w.look(3 * MIN, &down), vec![Finding::Restart { instance: "db-1".into(), not_ready: Duration::from_secs(180) }]);
        // No second restart.
        assert!(w.look(8 * MIN, &down).is_empty());
        let f = w.look(13 * MIN, &down);
        assert!(matches!(&f[..], [Finding::Incident { kind: "unhealthy", .. }]));
        assert!(w.look(30 * MIN, &down).is_empty());
        // Ready again: one incident, and the rules start again.
        let f = w.look(31 * MIN, &[inst("db-1", "primary", true)]);
        assert!(matches!(&f[..], [Finding::Incident { kind: "recovered", .. }]));
        assert!(w.look(32 * MIN, &[inst("db-1", "primary", true)]).is_empty());
        assert!(w.look(33 * MIN, &down).is_empty());
        assert!(matches!(&w.look(36 * MIN, &down)[..], [Finding::Restart { .. }]));
    }

    #[test]
    fn a_short_stop_is_no_incident() {
        let mut w = Watch::new(Duration::from_secs(180), Duration::from_secs(600));
        assert!(w.look(0, &[inst("db-1", "primary", false)]).is_empty());
        assert!(w.look(MIN, &[inst("db-1", "primary", true)]).is_empty());
        // The time starts again with the next stop.
        assert!(w.look(2 * MIN, &[inst("db-1", "primary", false)]).is_empty());
        assert!(w.look(4 * MIN, &[inst("db-1", "primary", false)]).is_empty());
    }

    #[test]
    fn incidents_stay_in_the_file() {
        let dir = tempfile::tempdir().unwrap();
        let a = Incidents::open(Some(dir.path())).unwrap();
        assert_eq!(a.add("failover", "db-2", "m1", 5).seq, 1);
        assert_eq!(a.add("restarted", "db-1", "m2", 6).seq, 2);
        let b = Incidents::open(Some(dir.path())).unwrap();
        assert_eq!(b.after(1, 10).iter().map(|i| i.kind.as_str()).collect::<Vec<_>>(), ["restarted"]);
        assert_eq!(b.add("recovered", "db-1", "m3", 7).seq, 3);
        assert_eq!(b.after(0, 2).len(), 2);
        assert!(b.status().is_none());
    }

    #[test]
    fn an_operator_job_is_not_an_instance() {
        let pod = |labels: serde_json::Value, ready: &str| -> Pod {
            serde_json::from_value(serde_json::json!({
                "metadata": {"name": "db-1", "labels": labels},
                "status": {"conditions": [{"type": "Ready", "status": ready}], "containerStatuses": [{"name": "postgres", "restartCount": 25, "ready": false, "image": "i", "imageID": "i"}]},
            })).unwrap()
        };
        let i = instance_of(&pod(serde_json::json!({"cnpg.io/cluster": "db", "cnpg.io/instanceName": "db-1", "cnpg.io/instanceRole": "primary"}), "False")).unwrap();
        assert_eq!((i.name.as_str(), i.role.as_str(), i.ready, i.restarts), ("db-1", "primary", false, 25));
        assert!(instance_of(&pod(serde_json::json!({"cnpg.io/cluster": "db", "cnpg.io/jobRole": "join"}), "True")).is_none());
    }
}
