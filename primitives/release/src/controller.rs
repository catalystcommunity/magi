//! Release state machine. `App` accepts calls; `Worker` runs one release at a
//! time against a `Cluster`.
//!
//! Release status: pending -> rolling -> verifying -> succeeded. On a failure:
//! rolling-back -> rolled-back, or failed if the restore fails too.

use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use base64::Engine;
use tokio::sync::mpsc::{UnboundedReceiver, UnboundedSender};

use crate::api::{self, Release, ReleaseItem, ServiceError};
use crate::auth::Principal;
use crate::config::TargetConfig;
use crate::now_ms;
use crate::store::{Record, Store};

pub const PENDING: &str = "pending";
pub const ROLLING: &str = "rolling";
pub const VERIFYING: &str = "verifying";
pub const SUCCEEDED: &str = "succeeded";
pub const ROLLING_BACK: &str = "rolling-back";
pub const ROLLED_BACK: &str = "rolled-back";
pub const FAILED: &str = "failed";

pub fn is_terminal(status: &str) -> bool {
    matches!(status, SUCCEEDED | ROLLED_BACK | FAILED)
}

/// Operations on the workload. Errors carry a short human-readable cause.
pub trait Cluster: Send + Sync + 'static {
    fn current_image(&self, t: &TargetConfig) -> impl Future<Output = anyhow::Result<String>> + Send;
    fn set_image(&self, t: &TargetConfig, image: &str, release: &str, previous: &str) -> impl Future<Output = anyhow::Result<()>> + Send;
    fn wait_rollout(&self, t: &TargetConfig, timeout: Duration) -> impl Future<Output = anyhow::Result<()>> + Send;
    fn verify(&self, t: &TargetConfig, window: Duration) -> impl Future<Output = anyhow::Result<()>> + Send;
}

fn err(code: i32, message: impl Into<String>) -> ServiceError {
    ServiceError { code, message: message.into() }
}

pub fn new_id() -> String {
    format!("rel_{}", base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(rand::random::<[u8; 12]>()))
}

/// Accepts this image reference form: `[registry/]repo[:tag][@sha256:digest]`.
pub fn valid_image(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 512
        && s.chars().all(|c| c.is_ascii_alphanumeric() || "._-/:@".contains(c))
        && !s.ends_with([':', '@', '/'])
        && !s.starts_with(['/', ':', '@', '-', '.'])
}

pub struct App {
    pub store: Arc<Store>,
    pub targets: Arc<HashMap<String, TargetConfig>>,
    pub queue: UnboundedSender<String>,
}

fn can_operate(p: &Principal) -> bool {
    p.has("release:operate") || p.has("release:admin")
}
fn can_read_all(p: &Principal) -> bool {
    can_operate(p) || p.has("release:read")
}

impl App {
    fn enqueue(&self, rec: Record) -> Result<Release, ServiceError> {
        self.store.put(rec.clone()).map_err(|e| {
            tracing::error!(error = %e, "store write");
            err(500, "internal error")
        })?;
        self.queue.send(rec.release.id.clone()).map_err(|_| err(503, "release worker stopped"))?;
        Ok(rec.release)
    }

    fn new_release(&self, owner: &str, images: Vec<(String, String)>, reason: Option<String>, rollback_of: Option<String>) -> Record {
        let now = now_ms();
        Record {
            release: Release {
                id: new_id(),
                owner: owner.to_string(),
                status: PENDING.into(),
                reason,
                items: images.into_iter().map(|(target, image)| ReleaseItem {
                    target, image, previous_image: String::new(), status: PENDING.into(), message: None,
                }).collect(),
                created_ms: now,
                updated_ms: now,
            },
            rollback_of,
        }
    }

    fn readable(&self, p: &Principal, id: &str) -> Result<Record, ServiceError> {
        let r = self.store.get(id).ok_or_else(|| err(404, format!("release {id} not found")))?;
        if r.release.owner != p.sub && !can_read_all(p) {
            return Err(err(404, format!("release {id} not found")));
        }
        Ok(r)
    }
}

impl api::ReleaseService for App {
    type Context = Principal;

    fn create(&self, p: &Principal, input: api::CreateReleaseRequest) -> Result<Release, ServiceError> {
        if !can_operate(p) {
            return Err(err(403, "scope release:operate required"));
        }
        if input.images.is_empty() {
            return Err(err(400, "images is empty"));
        }
        let mut seen = std::collections::HashSet::new();
        for ti in &input.images {
            if !self.targets.contains_key(&ti.target) {
                return Err(err(400, format!("unknown target {:?}", ti.target)));
            }
            if !seen.insert(ti.target.as_str()) {
                return Err(err(400, format!("target {:?} is listed twice", ti.target)));
            }
            if !valid_image(&ti.image) {
                return Err(err(400, format!("invalid image {:?}", ti.image)));
            }
        }
        if input.reason.as_ref().is_some_and(|r| r.len() > 4096) {
            return Err(err(400, "reason is too long"));
        }
        let images = input.images.into_iter().map(|t| (t.target, t.image)).collect();
        self.enqueue(self.new_release(&p.sub, images, input.reason, None))
    }

    fn get(&self, p: &Principal, input: api::GetReleaseRequest) -> Result<Release, ServiceError> {
        Ok(self.readable(p, &input.id)?.release)
    }

    fn list(&self, p: &Principal, input: api::ListReleasesRequest) -> Result<api::ListReleasesResponse, ServiceError> {
        let limit = input.limit.unwrap_or(50).clamp(1, 500) as usize;
        let all = can_read_all(p);
        Ok(api::ListReleasesResponse { releases: self.store.list(|r| all || r.owner == p.sub, limit) })
    }

    /// Starts a new release that restores the previous images of a succeeded release.
    fn rollback(&self, p: &Principal, input: api::RollbackRequest) -> Result<Release, ServiceError> {
        if !can_operate(p) {
            return Err(err(403, "scope release:operate required"));
        }
        let r = self.readable(p, &input.id)?;
        if r.release.status != SUCCEEDED {
            return Err(err(409, format!("release {} is {}, not succeeded", input.id, r.release.status)));
        }
        let images = r.release.items.iter().map(|i| (i.target.clone(), i.previous_image.clone())).collect();
        self.enqueue(self.new_release(&p.sub, images, Some(format!("rollback of {}", input.id)), Some(input.id)))
    }

    fn list_targets(&self, p: &Principal, input: api::ListTargetsRequest) -> Result<api::ListTargetsResponse, ServiceError> {
        if !can_read_all(p) {
            return Err(err(403, "scope release:read required"));
        }
        let mut targets: Vec<_> = self.targets.values()
            .filter(|t| input.name.as_ref().is_none_or(|n| *n == t.name))
            .map(TargetConfig::to_api).collect();
        targets.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(api::ListTargetsResponse { targets })
    }
}

pub struct Worker<C> {
    pub store: Arc<Store>,
    pub targets: Arc<HashMap<String, TargetConfig>>,
    pub cluster: C,
    pub timeout: Duration,
    pub stabilize: Duration,
}

impl<C: Cluster> Worker<C> {
    /// Recovers unfinished releases, then runs queued releases one at a time.
    pub async fn run(self, mut rx: UnboundedReceiver<String>) {
        for rec in self.store.all() {
            match rec.release.status.as_str() {
                PENDING => self.process(&rec.release.id).await,
                s if !is_terminal(s) => {
                    tracing::warn!(id = %rec.release.id, status = s, "release was interrupted; restoring previous images");
                    let mut rec = rec;
                    self.restore(&mut rec, "controller restarted during the release").await;
                }
                _ => {}
            }
        }
        while let Some(id) = rx.recv().await {
            self.process(&id).await;
        }
    }

    fn save(&self, rec: &mut Record) {
        rec.release.updated_ms = now_ms();
        if let Err(e) = self.store.put(rec.clone()) {
            tracing::error!(id = %rec.release.id, error = %e, "store write");
        }
    }

    fn target(&self, name: &str) -> anyhow::Result<&TargetConfig> {
        self.targets.get(name).ok_or_else(|| anyhow::anyhow!("target {name} is no longer configured"))
    }

    pub async fn process(&self, id: &str) {
        let Some(mut rec) = self.store.get(id) else { return };
        if rec.release.status != PENDING {
            return;
        }
        tracing::info!(id, "release start");
        rec.release.status = ROLLING.into();
        // Record previous images before any change.
        for i in 0..rec.release.items.len() {
            let res = match self.target(&rec.release.items[i].target) {
                Ok(t) => self.cluster.current_image(t).await,
                Err(e) => Err(e),
            };
            match res {
                Ok(img) => rec.release.items[i].previous_image = img,
                Err(e) => {
                    let item = &mut rec.release.items[i];
                    item.status = FAILED.into();
                    item.message = Some(format!("read current image: {e:#}"));
                    rec.release.status = FAILED.into();
                    self.save(&mut rec);
                    tracing::warn!(id, "release failed before any change");
                    return;
                }
            }
        }
        self.save(&mut rec);

        // Apply every image, then wait for every rollout.
        let mut failure = None;
        for i in 0..rec.release.items.len() {
            let item = rec.release.items[i].clone();
            rec.release.items[i].status = ROLLING.into();
            self.save(&mut rec);
            let t = match self.target(&item.target) { Ok(t) => t, Err(e) => { failure = Some((i, format!("{e:#}"))); break } };
            if let Err(e) = self.cluster.set_image(t, &item.image, &rec.release.id, &item.previous_image).await {
                failure = Some((i, format!("apply image: {e:#}")));
                break;
            }
        }
        if failure.is_none() {
            for i in 0..rec.release.items.len() {
                let t = self.target(&rec.release.items[i].target).unwrap();
                if let Err(e) = self.cluster.wait_rollout(t, Duration::from_secs(t.timeout_seconds.unwrap_or(self.timeout.as_secs()))).await {
                    failure = Some((i, format!("rollout: {e:#}")));
                    break;
                }
            }
        }
        if failure.is_none() {
            rec.release.status = VERIFYING.into();
            for item in &mut rec.release.items {
                item.status = VERIFYING.into();
            }
            self.save(&mut rec);
            let checks = rec.release.items.iter().map(|item| async {
                let t = self.target(&item.target)?;
                self.cluster.verify(t, Duration::from_secs(t.stabilize_seconds.unwrap_or(self.stabilize.as_secs()))).await
            });
            let results = futures::future::join_all(checks).await;
            failure = results.into_iter().enumerate().find_map(|(i, r)| r.err().map(|e| (i, format!("health: {e:#}"))));
        }
        match failure {
            None => {
                rec.release.status = SUCCEEDED.into();
                for item in &mut rec.release.items {
                    item.status = SUCCEEDED.into();
                }
                self.save(&mut rec);
                tracing::info!(id, "release succeeded");
                if let Some(orig) = rec.rollback_of.as_deref().and_then(|o| self.store.get(o)) {
                    let mut orig = orig;
                    orig.release.status = ROLLED_BACK.into();
                    for item in &mut orig.release.items {
                        item.status = ROLLED_BACK.into();
                        item.message = Some(format!("restored by {}", rec.release.id));
                    }
                    self.save(&mut orig);
                }
            }
            Some((i, msg)) => {
                tracing::warn!(id, target = %rec.release.items[i].target, error = %msg, "release item failed");
                rec.release.items[i].status = FAILED.into();
                rec.release.items[i].message = Some(msg.clone());
                let cause = format!("{} failed: {msg}", rec.release.items[i].target);
                self.restore(&mut rec, &cause).await;
            }
        }
    }

    /// Restores the previous image of every item, then waits for each rollout.
    async fn restore(&self, rec: &mut Record, cause: &str) {
        rec.release.status = ROLLING_BACK.into();
        self.save(rec);
        let mut ok = true;
        for i in 0..rec.release.items.len() {
            let item = rec.release.items[i].clone();
            if item.previous_image.is_empty() {
                continue;
            }
            let res = async {
                let t = self.target(&item.target)?;
                self.cluster.set_image(t, &item.previous_image, &rec.release.id, &item.previous_image).await?;
                self.cluster.wait_rollout(t, Duration::from_secs(t.timeout_seconds.unwrap_or(self.timeout.as_secs()))).await
            }.await;
            let it = &mut rec.release.items[i];
            match res {
                Ok(()) => {
                    if it.status != FAILED {
                        it.message = Some(format!("restored after: {cause}"));
                    }
                    it.status = ROLLED_BACK.into();
                }
                Err(e) => {
                    ok = false;
                    let prior = it.message.take().map(|m| format!("{m}; ")).unwrap_or_default();
                    it.status = FAILED.into();
                    it.message = Some(format!("{prior}restore failed: {e:#}"));
                }
            }
        }
        rec.release.status = if ok { ROLLED_BACK } else { FAILED }.into();
        self.save(rec);
        tracing::warn!(id = %rec.release.id, status = %rec.release.status, cause, "release ended");
    }
}
