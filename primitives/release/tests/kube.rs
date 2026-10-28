//! Live test against Kubernetes. Runs only with MAGI_TEST_KUBE=1.
//! It uses kube context "default" (override: MAGI_TEST_KUBE_CONTEXT) and
//! changes only namespace magi-release-test.

mod common;

use std::time::Duration;

use common::*;
use k8s_openapi::api::apps::v1::Deployment;
use kube::api::{Api, DeleteParams, PostParams};
use magi_release::cluster::{ANN_ID, ANN_PREVIOUS, KubeCluster};
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
    cluster.wait_rollout(&probe, Duration::from_secs(150)).await.expect("initial deployment is ready");

    let s = start(cluster.clone(), vec![probe, strict], 150, 8).await;
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
}
