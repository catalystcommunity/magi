//! Kubernetes implementation of `Cluster`.

use std::collections::{BTreeMap, HashMap};
use std::time::Duration;

use anyhow::{Context, anyhow, bail};
use k8s_openapi::api::apps::v1::{Deployment, ReplicaSet};
use k8s_openapi::api::core::v1::Pod;
use kube::api::{Api, DeleteParams, ListParams, LogParams, Patch, PatchParams, PostParams};
use tokio::time::{Instant, sleep};

use crate::config::TargetConfig;
use crate::controller::Cluster;

pub const ANN_ID: &str = "magi.release/id";
pub const ANN_PREVIOUS: &str = "magi.release/previous-image";
/// Label of preflight pods. It is not a label of the Deployment, so no
/// Service sends requests to a preflight pod.
pub const LABEL_PREFLIGHT: &str = "magi.release/preflight";
const REVISION: &str = "deployment.kubernetes.io/revision";
const POLL: Duration = Duration::from_secs(2);
/// Waiting reasons that do not recover without a change.
const FATAL: &[&str] = &["ImagePullBackOff", "InvalidImageName", "CrashLoopBackOff", "CreateContainerConfigError", "ErrImageNeverPull"];

#[derive(Clone)]
pub struct KubeCluster {
    pub client: kube::Client,
    /// Probe pods through the API server pod proxy instead of the pod IP.
    pub via_proxy: bool,
    http: reqwest::Client,
}

impl KubeCluster {
    pub fn new(client: kube::Client, via_proxy: bool) -> Self {
        let http = reqwest::Client::builder().timeout(Duration::from_secs(3)).build().expect("http client");
        KubeCluster { client, via_proxy, http }
    }

    /// In-cluster configuration, else the kubeconfig (optionally a named context).
    pub async fn connect(context: Option<String>, via_proxy: bool) -> anyhow::Result<Self> {
        let cfg = match context {
            Some(c) => kube::Config::from_kubeconfig(&kube::config::KubeConfigOptions { context: Some(c), ..Default::default() }).await?,
            None => kube::Config::infer().await?,
        };
        Ok(Self::new(kube::Client::try_from(cfg)?, via_proxy))
    }

    fn deployments(&self, ns: &str) -> Api<Deployment> {
        Api::namespaced(self.client.clone(), ns)
    }

    /// The ReplicaSet of the current Deployment revision and its pods.
    async fn current_pods(&self, t: &TargetConfig) -> anyhow::Result<(Deployment, Vec<Pod>)> {
        let d = self.deployments(&t.namespace).get(&t.deployment).await?;
        let uid = d.metadata.uid.clone().unwrap_or_default();
        let rev = d.metadata.annotations.as_ref().and_then(|a| a.get(REVISION)).cloned().unwrap_or_default();
        let sel = selector(d.spec.as_ref().and_then(|s| s.selector.match_labels.as_ref()));
        let lp = ListParams::default().labels(&sel);
        let rss: Api<ReplicaSet> = Api::namespaced(self.client.clone(), &t.namespace);
        let rs = rss.list(&lp).await?.items.into_iter().find(|rs| {
            owned_by(&rs.metadata, &uid)
                && rs.metadata.annotations.as_ref().and_then(|a| a.get(REVISION)) == Some(&rev)
        });
        let Some(rs) = rs else { return Ok((d, vec![])) };
        let rs_uid = rs.metadata.uid.clone().unwrap_or_default();
        let pods: Api<Pod> = Api::namespaced(self.client.clone(), &t.namespace);
        let pods = pods.list(&lp).await?.items.into_iter().filter(|p| owned_by(&p.metadata, &rs_uid)).collect();
        Ok((d, pods))
    }

    async fn probe(&self, t: &TargetConfig, pod: &Pod, path: &str, port: u64) -> anyhow::Result<()> {
        let name = pod.metadata.name.clone().unwrap_or_default();
        if self.via_proxy {
            let req = http::Request::get(format!("/api/v1/namespaces/{}/pods/{name}:{port}/proxy{path}", t.namespace)).body(vec![])?;
            self.client.request_text(req).await.map_err(|e| match e {
                kube::Error::Api(s) => anyhow!("pod {name}: GET {path}: status {}", s.code),
                e => anyhow!("pod {name}: GET {path}: {e}"),
            })?;
            return Ok(());
        }
        let ip = pod.status.as_ref().and_then(|s| s.pod_ip.clone()).ok_or_else(|| anyhow!("pod {name} has no IP"))?;
        let host = if ip.contains(':') { format!("[{ip}]") } else { ip };
        let resp = self.http.get(format!("http://{host}:{port}{path}")).send().await.map_err(|e| anyhow!("pod {name}: GET {path}: {e}"))?;
        if !resp.status().is_success() {
            bail!("pod {name}: GET {path}: status {}", resp.status().as_u16());
        }
        Ok(())
    }
}

fn selector(labels: Option<&BTreeMap<String, String>>) -> String {
    labels.into_iter().flatten().map(|(k, v)| format!("{k}={v}")).collect::<Vec<_>>().join(",")
}

fn owned_by(m: &k8s_openapi::apimachinery::pkg::apis::meta::v1::ObjectMeta, uid: &str) -> bool {
    m.owner_references.iter().flatten().any(|o| o.uid == uid)
}

fn ready(p: &Pod) -> bool {
    p.metadata.deletion_timestamp.is_none()
        && p.status.as_ref().and_then(|s| s.conditions.as_ref()).into_iter().flatten().any(|c| c.type_ == "Ready" && c.status == "True")
}

fn restarts(p: &Pod) -> i32 {
    p.status.as_ref().and_then(|s| s.container_statuses.as_ref()).into_iter().flatten().map(|c| c.restart_count).sum()
}

/// A fatal waiting reason of a container in the pod, if any.
fn fatal_reason(p: &Pod) -> Option<String> {
    let st = p.status.as_ref()?;
    st.init_container_statuses.iter().flatten().chain(st.container_statuses.iter().flatten()).find_map(|c| {
        let w = c.state.as_ref()?.waiting.as_ref()?;
        let r = w.reason.as_deref()?;
        FATAL.contains(&r).then(|| format!("container {}: {r}: {}", c.name, w.message.clone().unwrap_or_default()))
    })
}

/// The pod of a preflight run: the pod spec of the Deployment, only the
/// target container, the new image and the preflight arguments, no probes,
/// no restart, and a time limit.
fn preflight_pod(d: &Deployment, t: &TargetConfig, image: &str, release: &str, args: &[String], limit: Duration) -> anyhow::Result<Pod> {
    let mut spec = d.spec.as_ref().and_then(|s| s.template.spec.clone())
        .ok_or_else(|| anyhow!("deployment {}/{} has no pod spec", t.namespace, t.deployment))?;
    let mut c = spec.containers.iter().find(|c| c.name == t.container).cloned()
        .ok_or_else(|| anyhow!("deployment {}/{} has no container {}", t.namespace, t.deployment, t.container))?;
    c.image = Some(image.into());
    c.args = Some(args.to_vec());
    c.readiness_probe = None;
    c.liveness_probe = None;
    c.startup_probe = None;
    c.ports = None;
    spec.containers = vec![c];
    spec.restart_policy = Some("Never".into());
    spec.active_deadline_seconds = Some(limit.as_secs() as i64);
    let mut pod = Pod::default();
    pod.metadata.generate_name = Some(format!("{}-preflight-", t.deployment));
    pod.metadata.namespace = Some(t.namespace.clone());
    pod.metadata.labels = Some(BTreeMap::from([(LABEL_PREFLIGHT.to_string(), t.deployment.clone())]));
    pod.metadata.annotations = Some(BTreeMap::from([(ANN_ID.to_string(), release.to_string())]));
    pod.spec = Some(spec);
    Ok(pod)
}

fn container_port(d: &Deployment, container: &str) -> Option<u64> {
    let c = d.spec.as_ref()?.template.spec.as_ref()?.containers.iter().find(|c| c.name == container)?;
    Some(c.ports.as_ref()?.first()?.container_port as u64)
}

impl Cluster for KubeCluster {
    async fn current_image(&self, t: &TargetConfig) -> anyhow::Result<String> {
        let d = self.deployments(&t.namespace).get(&t.deployment).await
            .with_context(|| format!("deployment {}/{}", t.namespace, t.deployment))?;
        d.spec.and_then(|s| s.template.spec)
            .and_then(|s| s.containers.into_iter().find(|c| c.name == t.container))
            .and_then(|c| c.image)
            .ok_or_else(|| anyhow!("deployment {}/{} has no container {}", t.namespace, t.deployment, t.container))
    }

    async fn set_image(&self, t: &TargetConfig, image: &str, release: &str, previous: &str) -> anyhow::Result<()> {
        let patch = serde_json::json!({
            "metadata": {"annotations": {ANN_ID: release, ANN_PREVIOUS: previous}},
            "spec": {"template": {"spec": {"containers": [{"name": t.container, "image": image}]}}},
        });
        self.deployments(&t.namespace)
            .patch(&t.deployment, &PatchParams { field_manager: Some("magi-release".into()), ..Default::default() }, &Patch::Strategic(&patch))
            .await?;
        Ok(())
    }

    async fn wait_rollout(&self, t: &TargetConfig, timeout: Duration) -> anyhow::Result<()> {
        let deadline = Instant::now() + timeout;
        loop {
            let (d, pods) = self.current_pods(t).await?;
            let want = d.spec.as_ref().and_then(|s| s.replicas).unwrap_or(1);
            let st = d.status.clone().unwrap_or_default();
            let observed = st.observed_generation.unwrap_or(0) >= d.metadata.generation.unwrap_or(0);
            if observed {
                if let Some(c) = st.conditions.iter().flatten().find(|c| c.type_ == "Progressing" && c.reason.as_deref() == Some("ProgressDeadlineExceeded")) {
                    bail!("progress deadline exceeded: {}", c.message.clone().unwrap_or_default());
                }
                if let Some(r) = pods.iter().find_map(fatal_reason) {
                    bail!("{r}");
                }
                let updated = st.updated_replicas.unwrap_or(0);
                if updated == want && st.replicas.unwrap_or(0) == want && st.available_replicas.unwrap_or(0) >= want {
                    return Ok(());
                }
            }
            if Instant::now() >= deadline {
                bail!("timed out after {}s: updated {}/{want}, available {}/{want}",
                    timeout.as_secs(), st.updated_replicas.unwrap_or(0), st.available_replicas.unwrap_or(0));
            }
            sleep(POLL).await;
        }
    }

    async fn preflight(&self, t: &TargetConfig, image: &str, release: &str) -> anyhow::Result<()> {
        let Some(args) = &t.preflight_args else { return Ok(()) };
        let limit = Duration::from_secs(t.preflight_timeout_seconds.unwrap_or(120));
        let d = self.deployments(&t.namespace).get(&t.deployment).await
            .with_context(|| format!("deployment {}/{}", t.namespace, t.deployment))?;
        let pods: Api<Pod> = Api::namespaced(self.client.clone(), &t.namespace);
        let pod = pods.create(&PostParams::default(), &preflight_pod(&d, t, image, release, args, limit)?).await
            .context("create preflight pod")?;
        let name = pod.metadata.name.clone().unwrap_or_default();
        let res = async {
            // The image pull counts too: wait a little longer than the
            // deadline of the pod.
            let deadline = Instant::now() + limit + Duration::from_secs(30);
            loop {
                let p = pods.get(&name).await?;
                match p.status.as_ref().and_then(|s| s.phase.as_deref()) {
                    Some("Succeeded") => return Ok(()),
                    Some("Failed") => {
                        let reason = p.status.as_ref().and_then(|s| s.reason.clone()).unwrap_or_else(|| "exit code not 0".into());
                        let tail = pods.logs(&name, &LogParams { tail_lines: Some(5), ..Default::default() }).await.unwrap_or_default();
                        bail!("pod {name}: {reason}: {}", tail.trim());
                    }
                    _ => {}
                }
                if let Some(r) = fatal_reason(&p) {
                    bail!("pod {name}: {r}");
                }
                if Instant::now() >= deadline {
                    bail!("pod {name}: no result after {}s", limit.as_secs());
                }
                sleep(POLL).await;
            }
        }.await;
        if let Err(e) = pods.delete(&name, &DeleteParams::background()).await {
            tracing::warn!(pod = %name, error = %e, "delete preflight pod");
        }
        res
    }

    async fn verify(&self, t: &TargetConfig, window: Duration) -> anyhow::Result<()> {
        let deadline = Instant::now() + window;
        let mut first: HashMap<String, i32> = HashMap::new();
        loop {
            let (d, pods) = self.current_pods(t).await?;
            let want = d.spec.as_ref().and_then(|s| s.replicas).unwrap_or(1) as usize;
            let ready_pods: Vec<&Pod> = pods.iter().filter(|p| ready(p)).collect();
            if ready_pods.len() < want {
                bail!("{} of {want} new pods are ready", ready_pods.len());
            }
            for p in &ready_pods {
                let name = p.metadata.name.clone().unwrap_or_default();
                let n = restarts(p);
                if *first.entry(name.clone()).or_insert(n) < n {
                    bail!("pod {name} restarted during the stabilization window");
                }
                if let Some(path) = &t.health_path {
                    let port = t.health_port.or_else(|| container_port(&d, &t.container)).unwrap_or(8080);
                    self.probe(t, p, path, port).await?;
                }
            }
            if Instant::now() >= deadline {
                return Ok(());
            }
            sleep(POLL).await;
        }
    }
}
