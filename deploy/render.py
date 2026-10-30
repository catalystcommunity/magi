#!/usr/bin/env python3
"""Render the Kubernetes manifests of one Magi installation.

    deploy/render.py --images .build/images.env --values PRIVATE.env > out.yaml

The repository holds no private values. The values file supplies them:
  NAMESPACE, ENV_NAMESPACE, PUBLIC_HOST, MODEL_BASE_URL, MODEL_NAME,
  SPEECH_STT_URL, SPEECH_TTS_URL, ALLOWED_DOMAINS, GATEWAY_NAME,
  GATEWAY_NAMESPACE, GATEWAY_SECTION, ENV_IMAGE.
Optional: PERSONA_LOCAL_URL and PERSONA_LOCAL_MODEL (an inference host that
the persona tries first; it needs the secret magi-inference-token).
Secrets are created separately (see deploy/README.md).
"""
import argparse
import collections
import json


def load_env(path):
    out = {}
    for line in open(path):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            out[k] = v
    return out


LABELS = {"app.kubernetes.io/part-of": "magi"}


def db_env(name):
    """MAGI_DATABASE_URL for database `name`, built from the CNPG app secret."""
    ref = lambda key: {"valueFrom": {"secretKeyRef": {"name": "magi-db-app", "key": key}}}
    return [
        {"name": "DB_USER", **ref("username")},
        {"name": "DB_PASSWORD", **ref("password")},
        {"name": "MAGI_DATABASE_URL", "value": f"postgres://$(DB_USER):$(DB_PASSWORD)@magi-db-rw:5432/{name}?sslmode=require"},
    ]


def service(v, name, image, env=(), secrets=(), volumes=(), args=None, sa=None, port=8080, cpu="500m", mem="256Mi", extra_ports=()):
    """A Deployment and Service for one Magi process."""
    mounts = [{"name": "auth-public", "mountPath": "/etc/magi/auth", "readOnly": True}]
    vols = [{"name": "auth-public", "secret": {"secretName": "magi-auth-public"}}]
    for sname, path in secrets:
        mounts.append({"name": sname, "mountPath": path, "readOnly": True})
        vols.append({"name": sname, "secret": {"secretName": sname, "defaultMode": 0o440}})
    for vname, path, claim in volumes:
        mounts.append({"name": vname, "mountPath": path})
        vols.append({"name": vname, "persistentVolumeClaim": {"claimName": claim}})
    labels = {**LABELS, "app.kubernetes.io/name": name}
    container = {
        "name": name, "image": image,
        "env": [{"name": "MAGI_LISTEN", "value": f":{port}"}, {"name": "MAGI_AUTH_KEYS", "value": "/etc/magi/auth/issuer.json"}, *env],
        "ports": [{"name": "http", "containerPort": port}, *extra_ports],
        "readinessProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 10},
        "resources": {"requests": {"cpu": "20m", "memory": "32Mi"}, "limits": {"cpu": cpu, "memory": mem}},
        "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
        "volumeMounts": mounts,
    }
    if args:
        container["args"] = args
    spec = {
        "automountServiceAccountToken": sa is not None,
        "nodeSelector": {"kubernetes.io/arch": "amd64"},
        "imagePullSecrets": [{"name": "magi-registry"}],
        "securityContext": {"runAsNonRoot": True, "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [container], "volumes": vols,
    }
    if sa:
        spec["serviceAccountName"] = sa
    strategy = {"type": "Recreate"} if volumes else {"type": "RollingUpdate"}
    return [
        {"apiVersion": "apps/v1", "kind": "Deployment",
         "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": labels},
         "spec": {"replicas": 1, "strategy": strategy, "selector": {"matchLabels": {"app.kubernetes.io/name": name}},
                  "template": {"metadata": {"labels": labels}, "spec": spec}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": labels},
         "spec": {"selector": {"app.kubernetes.io/name": name}, "ports": [{"name": "http", "port": 8080, "targetPort": "http"}]}},
    ]


def pvc(v, name, size):
    return {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": LABELS},
            "spec": {"accessModes": ["ReadWriteOnce"], "storageClassName": "longhorn", "resources": {"requests": {"storage": size}}}}


def render(img, v):
    ns = v["NAMESPACE"]
    urls = {k: f"http://{k}.{ns}.svc:8080" for k in ["artifacts", "conversation", "execution", "knowledge", "source", "speech", "workflow", "release"]}
    model = [
        {"name": "MAGI_MODEL_BASE_URL", "value": v["MODEL_BASE_URL"]},
        {"name": "MAGI_MODEL_NAME", "value": v["MODEL_NAME"]},
        {"name": "MAGI_MODEL_API_KEY_FILE", "value": "/etc/magi/model/api-key"},
    ]
    out = [pvc(v, "artifacts", "20Gi"), pvc(v, "source", "10Gi"), pvc(v, "corndogs", "2Gi")]
    # Corndogs holds workflow task state. Only the workflow service talks to it.
    cd_labels = {**LABELS, "app.kubernetes.io/name": "corndogs"}
    out += [
        {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "corndogs", "namespace": ns, "labels": cd_labels},
         "spec": {"replicas": 1, "strategy": {"type": "Recreate"}, "selector": {"matchLabels": {"app.kubernetes.io/name": "corndogs"}},
                  "template": {"metadata": {"labels": cd_labels}, "spec": {
                      "automountServiceAccountToken": False, "nodeSelector": {"kubernetes.io/arch": "amd64"},
                      "securityContext": {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
                      "containers": [{"name": "corndogs", "image": img["IMAGE_CORNDOGS"],
                                      "env": [{"name": "STORAGE_BACKEND", "value": "file"}, {"name": "CORNDOGS_FILESTORE_DIR", "value": "/data"},
                                              {"name": "CORNDOGS_SUBMISSION_KEY_POLICY", "value": "required"}, {"name": "CORNDOGS_TASK_GUARD_POLICY", "value": "required"}],
                                      "ports": [{"name": "rpc", "containerPort": 5080}],
                                      "readinessProbe": {"tcpSocket": {"port": "rpc"}, "periodSeconds": 10},
                                      "resources": {"requests": {"cpu": "20m", "memory": "32Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}},
                                      "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
                                      "volumeMounts": [{"name": "data", "mountPath": "/data"}]}],
                      "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "corndogs"}}]}}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "corndogs", "namespace": ns, "labels": cd_labels},
         "spec": {"selector": {"app.kubernetes.io/name": "corndogs"}, "ports": [{"name": "rpc", "port": 5080, "targetPort": "rpc"}]}},
        {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "corndogs", "namespace": ns, "labels": LABELS},
         "spec": {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "corndogs"}}, "policyTypes": ["Ingress"],
                  "ingress": [{"from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "workflow"}}}]}]}},
    ]
    out += service(v, "artifacts", img["IMAGE_ARTIFACTS"], db_env("artifacts") + [{"name": "MAGI_ARTIFACT_DIR", "value": "/data"}],
                   volumes=[("data", "/data", "artifacts")], mem="512Mi")
    out += service(v, "conversation", img["IMAGE_CONVERSATION"], db_env("conversation"))
    out += service(v, "knowledge", img["IMAGE_KNOWLEDGE"], db_env("knowledge"))
    out += service(v, "workflow", img["IMAGE_WORKFLOW"], db_env("workflow") + [{"name": "MAGI_CORNDOGS_URL", "value": f"corndogs.{ns}.svc:5080"}])
    out += service(v, "source", img["IMAGE_SOURCE"], [{"name": "MAGI_SOURCE_DIR", "value": "/data"}], volumes=[("data", "/data", "source")], mem="512Mi")
    out += service(v, "speech", img["IMAGE_SPEECH"], [
        {"name": "MAGI_SPEECH_STT_URL", "value": v["SPEECH_STT_URL"]}, {"name": "MAGI_SPEECH_TTS_URL", "value": v["SPEECH_TTS_URL"]},
        {"name": "MAGI_SPEECH_ENGINE_TOKEN_FILE", "value": "/etc/magi/speech/token"}], secrets=[("magi-speech-engine", "/etc/magi/speech")])
    out += service(v, "execution", img["IMAGE_EXECUTION"], db_env("execution") + [
        {"name": "MAGI_EXEC_BACKEND", "value": "kubernetes"}, {"name": "MAGI_EXEC_NAMESPACE", "value": v["ENV_NAMESPACE"]},
        {"name": "MAGI_EXEC_PUBLIC_URL", "value": urls["execution"]}, {"name": "MAGI_EXEC_PULL_SECRET", "value": "magi-registry"}],
        sa="execution", mem="512Mi")
    out += service(v, "identity", img["IMAGE_IDENTITY"], db_env("identity") + [
        {"name": "MAGI_ISSUER_KEY", "value": "/etc/magi/issuer/issuer.json"},
        {"name": "MAGI_PUBLIC_URL", "value": f"https://{v['PUBLIC_HOST']}"},
        {"name": "MAGI_ALLOWED_DOMAINS", "value": v["ALLOWED_DOMAINS"]},
        {"name": "MAGI_LINKKEYS_RUNTIME", "value": "/etc/magi/linkkeys/runtime.json"}],
        secrets=[("magi-issuer", "/etc/magi/issuer"), ("magi-linkkeys", "/etc/magi/linkkeys")])
    # The release controller changes the magi Deployment; magi releases itself.
    targets = {"targets": [{"name": "magi", "namespace": ns, "deployment": "magi", "container": "magi", "health_path": "/healthz", "health_port": 8080}]}
    out += [
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "release-targets", "namespace": ns, "labels": LABELS},
         "data": {"targets.json": json.dumps(targets)}},
        pvc(v, "release-data", "1Gi"),
    ]
    rel = service(v, "release", img["IMAGE_RELEASE"], [
        {"name": "MAGI_RELEASE_TARGETS", "value": "/etc/magi/release/targets.json"},
        {"name": "MAGI_RELEASE_DATA", "value": "/var/lib/magi-release"}],
        volumes=[("data", "/var/lib/magi-release", "release-data")], sa="release", mem="128Mi")
    pod = rel[0]["spec"]["template"]["spec"]
    pod["containers"][0]["volumeMounts"].append({"name": "targets", "mountPath": "/etc/magi/release", "readOnly": True})
    pod["volumes"].append({"name": "targets", "configMap": {"name": "release-targets"}})
    out += rel
    svc_token = [{"name": "MAGI_SERVICE_TOKEN_FILE", "value": "/etc/magi/token/token"}]
    # Optional local inference host for the persona. The provider in
    # MODEL_* is the fallback when the host is busy, starting, or down.
    persona, persona_secrets = [], []
    if v.get("PERSONA_LOCAL_URL"):
        chain = [
            {"base_url": v["PERSONA_LOCAL_URL"], "model": v.get("PERSONA_LOCAL_MODEL", "local"),
             "key_file": "/etc/magi/inference/token", "attempts": 1, "timeout_s": 120},
            {"base_url": v["MODEL_BASE_URL"], "model": v["MODEL_NAME"], "key_file": "/etc/magi/model/api-key"},
        ]
        persona = [{"name": "MAGI_PERSONA_MODELS", "value": json.dumps(chain)}]
        persona_secrets = [("magi-inference-token", "/etc/magi/inference")]
    out += service(v, "magi", img["IMAGE_MAGI"], [
        *model, *svc_token, *persona,
        *[{"name": f"MAGI_{k.upper()}_URL", "value": u} for k, u in urls.items()],
        {"name": "MAGI_ENV_IMAGE", "value": v["ENV_IMAGE"]},
        # RUNNER: code changes run on this runner (RunnerService).
        {"name": "MAGI_RUNNER", "value": v.get("RUNNER", "")},
        {"name": "MAGI_IMAGE_REGISTRY", "value": f"registry.{ns}.svc:5000"},
        {"name": "MAGI_IMAGE_CREDENTIALS_FILE", "value": "/etc/magi/registry/registry.json"}],
        secrets=[("magi-model", "/etc/magi/model"), ("magi-token-magi", "/etc/magi/token"), ("magi-registry-push", "/etc/magi/registry"), *persona_secrets], mem="512Mi")
    # The agent worker has no Service; it only claims work.
    agent = service(v, "agent", img["IMAGE_AGENT"], [
        *model, {"name": "MAGI_TOKEN_FILE", "value": "/etc/magi/token/token"},
        {"name": "MAGI_WORKFLOW_URL", "value": urls["workflow"]}, {"name": "MAGI_EXECUTION_URL", "value": urls["execution"]},
        {"name": "MAGI_ARTIFACTS_URL", "value": urls["artifacts"]}],
        secrets=[("magi-model", "/etc/magi/model"), ("magi-token-agent", "/etc/magi/token")], mem="512Mi")[0]
    c = agent["spec"]["template"]["spec"]["containers"][0]
    c["args"] = ["worker"]
    c.pop("readinessProbe")
    c["ports"] = []
    out.append(agent)
    # Public routes: only identity login, the Magi API, and the web client.
    # Long polls wait up to 30 s; the gateway default timeout is shorter.
    timeouts = {"request": "120s", "backendRequest": "120s"}
    route_rules = [
        {"matches": [{"path": {"type": "PathPrefix", "value": "/login/"}}], "backendRefs": [{"name": "identity", "port": 8080}], "timeouts": timeouts},
        {"matches": [{"path": {"type": "PathPrefix", "value": "/rpc/IdentityService/"}}], "backendRefs": [{"name": "identity", "port": 8080}], "timeouts": timeouts},
        {"matches": [{"path": {"type": "PathPrefix", "value": "/"}}], "backendRefs": [{"name": "magi", "port": 8080}], "timeouts": timeouts},
    ]
    if v.get("PUBLIC_ROUTE") == "true":
        out.append({"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
                    "metadata": {"name": "magi", "namespace": ns, "labels": LABELS},
                    "spec": {"parentRefs": [{"name": v["GATEWAY_NAME"], "namespace": v["GATEWAY_NAMESPACE"], "sectionName": v["GATEWAY_SECTION"]}],
                             "hostnames": [v["PUBLIC_HOST"]], "rules": route_rules}})
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--images", required=True)
    ap.add_argument("--values", required=True)
    ap.add_argument("--only", default="", help="comma-separated object names to keep")
    a = ap.parse_args()
    # A missing image renders as MISSING so --only can deploy part of the set.
    images = collections.defaultdict(lambda: "MISSING", load_env(a.images))
    objs = render(images, load_env(a.values))
    if a.only:
        keep = set(a.only.split(","))
        objs = [o for o in objs if o["metadata"]["name"] in keep]
    print(json.dumps({"apiVersion": "v1", "kind": "List", "items": objs}, indent=1))


if __name__ == "__main__":
    main()
