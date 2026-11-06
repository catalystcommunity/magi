//! Live test against Kubernetes. Runs only with MAGI_TEST_KUBE=1.
//! It uses kube context "default" (override: MAGI_TEST_KUBE_CONTEXT) and
//! changes only namespace magi-release-test.

mod common;

use std::time::Duration;

use common::*;
use k8s_openapi::api::apps::v1::Deployment;
use k8s_openapi::api::core::v1::Pod;
use kube::api::{Api, DeleteParams, ListParams, PostParams};
use magi_release::cluster::{ANN_ID, ANN_PREVIOUS, KubeCluster, LABEL_PREFLIGHT};
use magi_release::controller::Cluster;

const NS: &str = "magi-release-test";
const NAME: &str = "release-probe";
const GOOD_1: &str = "nginxinc/nginx-unprivileged:1.27-alpine";
const GOOD_2: &str = "nginxinc/nginx-unprivileged:1.28-alpine";
const MISSING: &str = "nginxinc/nginx-unprivileged:magi-release-no-such-tag";

fn deployment() -> Deployment {
    serde_json::from_value(serde_json::json!({
        "apiVersion": "apps/v1", "kind": "Deployment",
        "metadata": {"name": NAME, "namespace": NS, "labels": {"app": NAME}},
        "spec": {
            "replicas": 1,
            "progressDeadlineSeconds": 120,
            "selector": {"matchLabels": {"app": NAME}},
            "template": {
                "metadata": {"labels": {"app": NAME}},
                "spec": {
                    "enableServiceLinks": false,
                    "automountServiceAccountToken": false,
                    "nodeSelector": {"kubernetes.io/arch": "amd64"},
                    "securityContext": {"runAsNonRoot": true, "seccompProfile": {"type": "RuntimeDefault"}},
                    "containers": [{
                        "name": "web", "image": GOOD_1,
                        "ports": [{"containerPort": 8080}],
                        "readinessProbe": {"httpGet": {"path": "/", "port": 8080}, "periodSeconds": 2},
                        "resources": {"requests": {"cpu": "10m", "memory": "16Mi"}, "limits": {"memory": "64Mi"}},
                        "securityContext": {"allowPrivilegeEscalation": false, "capabilities": {"drop": ["ALL"]}},
                    }],
                },
            },
        },
    })).unwrap()
}

#[tokio::test]
async fn live_release_and_rollback() {
    if std::env::var("MAGI_TEST_KUBE").as_deref() != Ok("1") {
        eprintln!("skip: set MAGI_TEST_KUBE=1");
        return;
    }
    let _ = tracing_subscriber::fmt().with_test_writer().try_init();
    let ctx = std::env::var("MAGI_TEST_KUBE_CONTEXT").unwrap_or_else(|_| "default".into());
    let cluster = KubeCluster::connect(Some(ctx), true).await.unwrap();
    let api: Api<Deployment> = Api::namespaced(cluster.client.clone(), NS);
    let _ = api.delete(NAME, &DeleteParams::foreground()).await;
    while api.get_opt(NAME).await.unwrap().is_some() {
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    api.create(&PostParams::default(), &deployment()).await.unwrap();
    let res = tokio::spawn(scenario(cluster.clone(), api.clone())).await;
    api.delete(NAME, &DeleteParams::background()).await.unwrap();
    res.unwrap();
}

async fn scenario(cluster: KubeCluster, api: Api<Deployment>) {
    let mut probe = target("probe", NS, NAME, "web", Some("/"), Some(8080));
    probe.timeout_seconds = Some(150);
    probe.stabilize_seconds = Some(8);
    let mut strict = probe.clone();
    strict.name = "probe-strict".into();
    strict.health_path = Some("/magi-missing".into());
    // The nginx entrypoint runs arguments that are not "nginx" as a command.
    let mut pre_bad = probe.clone();
    pre_bad.name = "probe-preflight-bad".into();
    pre_bad.preflight_args = Some(vec!["sh".into(), "-c".into(), "echo preflight-says-no; exit 3".into()]);
    pre_bad.preflight_timeout_seconds = Some(90);
    let mut pre_ok = pre_bad.clone();
    pre_ok.name = "probe-preflight-ok".into();
    pre_ok.preflight_args = Some(vec!["true".into()]);
    cluster.wait_rollout(&probe, Duration::from_secs(150)).await.expect("initial deployment is ready");

    let s = start(cluster.clone(), vec![probe, strict, pre_bad, pre_ok], 150, 8).await;
    let op = token("operator");
    let image = |d: &Deployment| d.spec.as_ref().unwrap().template.spec.as_ref().unwrap().containers[0].image.clone().unwrap();

    // 1. A good image succeeds and annotates the deployment.
    let r = s.create(&op, &[("probe", GOOD_2)]).await.unwrap();
    let r = s.wait(&op, &r.id, Duration::from_secs(240)).await;
    eprintln!("good: {r:?}");
    assert_eq!(r.status, "succeeded");
    assert_eq!(r.items[0].previous_image, GOOD_1);
    let d = api.get(NAME).await.unwrap();
    assert_eq!(image(&d), GOOD_2);
    let ann = d.metadata.annotations.unwrap();
    assert_eq!(ann[ANN_ID], r.id);
    assert_eq!(ann[ANN_PREVIOUS], GOOD_1);

    // 2. A missing image fails the rollout and is rolled back.
    let r = s.create(&op, &[("probe", MISSING)]).await.unwrap();
    let r = s.wait(&op, &r.id, Duration::from_secs(400)).await;
    eprintln!("missing image: {r:?}");
    assert_eq!(r.status, "rolled-back");
    assert!(r.items[0].message.as_deref().unwrap().contains("rollout"));
    assert_eq!(image(&api.get(NAME).await.unwrap()), GOOD_2);

    // 3. A running image that fails the health check is rolled back.
    let r = s.create(&op, &[("probe-strict", GOOD_1)]).await.unwrap();
    let r = s.wait(&op, &r.id, Duration::from_secs(400)).await;
    eprintln!("unhealthy: {r:?}");
    assert_eq!(r.status, "rolled-back");
    assert!(r.items[0].message.as_deref().unwrap().contains("health"));
    assert_eq!(image(&api.get(NAME).await.unwrap()), GOOD_2);

    // 4. A preflight that fails changes nothing, and its pod is removed.
    let r = s.create(&op, &[("probe-preflight-bad", GOOD_1)]).await.unwrap();
    let r = s.wait(&op, &r.id, Duration::from_secs(300)).await;
    eprintln!("preflight fails: {r:?}");
    assert_eq!(r.status, "rolled-back");
    let msg = r.items[0].message.clone().unwrap();
    assert!(msg.contains("preflight") && msg.contains("preflight-says-no"), "{msg}");
    assert_eq!(image(&api.get(NAME).await.unwrap()), GOOD_2);

    // 5. A preflight that passes lets the release continue.
    let r = s.create(&op, &[("probe-preflight-ok", GOOD_1)]).await.unwrap();
    let r = s.wait(&op, &r.id, Duration::from_secs(300)).await;
    eprintln!("preflight passes: {r:?}");
    assert_eq!(r.status, "succeeded");
    assert_eq!(image(&api.get(NAME).await.unwrap()), GOOD_1);

    let pods: Api<Pod> = Api::namespaced(cluster.client.clone(), NS);
    tokio::time::sleep(Duration::from_secs(5)).await;
    let left = pods.list(&ListParams::default().labels(LABEL_PREFLIGHT)).await.unwrap().items;
    assert!(left.iter().all(|p| p.metadata.deletion_timestamp.is_some()), "preflight pods remain: {}", left.len());
}
