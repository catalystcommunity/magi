#!/usr/bin/env python3
"""Render the Kubernetes manifests of one Magi installation.

    deploy/render.py --images .build/images.env --values PRIVATE.env > out.json
    deploy/render.py --base --values PRIVATE.env > base.yaml   (PostgreSQL, registry)

The repository holds no private values. The values file supplies them:
  NAMESPACE, ENV_NAMESPACE, PUBLIC_HOST, MODEL_BASE_URL, MODEL_NAME,
  SPEECH_STT_URL, SPEECH_TTS_URL, ALLOWED_DOMAINS, GATEWAY_NAME,
  GATEWAY_NAMESPACE, GATEWAY_SECTION, ENV_IMAGE.
Optional: STORAGE_CLASS for the volumes (empty: the default class of the
cluster).
Optional: PERSONA_LOCAL_URL and PERSONA_LOCAL_MODEL (inference hosts that
the persona tries first, comma-separated in order, one model for each URL;
they need the secret magi-inference-token). http://<name>.revconn/v1 is a
host that connects out to Magi.
Optional: EGRESS=true adds the egress proxy (primitives/egress) and the
"proxy" network for task environments; see deploy/README.md for its secrets
and ConfigMaps. MODEL_EGRESS=true (with EGRESS) sends the model calls of
Magi and the apprentice through the proxy; the provider key is then the
proxy secret "model", and Magi holds only its placeholder.
Optional: DETECTOR_URL is the prompt injection detector engine (the engine
"detect" of primitives/speech/engine, with the speech engine token);
DETECTOR_MODE is "flag" (default) or "review", DETECTOR_THRESHOLD 0.98.
Optional: SEARCH=true (with EGRESS) adds SearXNG for web_search (image
SEARCH_IMAGE, Secret magi-search-settings); see deploy/README.md.
Optional: SPEECH_TURN_URL is the speech detection and turn classifier engine
(the engine "turn"), for detect-speech and classify-turn.
Secrets are created separately (see deploy/README.md).
"""
import argparse
import collections
import json
import sys
import pathlib


def load_env(path):
    out = {}
    for line in open(path):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            out[k] = v
    return out


LABELS = {"app.kubernetes.io/part-of": "magi"}

# The key placeholder of the model provider (MODEL_EGRESS).
MODEL_PLACEHOLDER = "magi-secret-model"


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
        # lib/rpc drains for 5 s + up to 35 s on SIGTERM (long polls finish).
        "terminationGracePeriodSeconds": 50,
    }
    if sa:
        spec["serviceAccountName"] = sa
    # A volume is ReadWriteOnce, so those pods cannot overlap. The others
    # start the new pod and wait for it to be ready before they stop the old one.
    strategy = {"type": "Recreate"} if volumes else {"type": "RollingUpdate", "rollingUpdate": {"maxUnavailable": 0, "maxSurge": 1}}
    return [
        {"apiVersion": "apps/v1", "kind": "Deployment",
         "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": labels},
         "spec": {"replicas": 1, "strategy": strategy, "selector": {"matchLabels": {"app.kubernetes.io/name": name}},
                  "template": {"metadata": {"labels": labels}, "spec": spec}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": labels},
         "spec": {"selector": {"app.kubernetes.io/name": name}, "ports": [{"name": "http", "port": 8080, "targetPort": "http"}]}},
    ]


def secret_mount(objs, name, path):
    """Mounts Secret `name` at `path` in the Deployment of `objs`."""
    pod = objs[0]["spec"]["template"]["spec"]
    pod["containers"][0]["volumeMounts"].append({"name": name, "mountPath": path, "readOnly": True})
    pod["volumes"].append({"name": name, "secret": {"secretName": name, "defaultMode": 0o440}})


def configmap(objs, name, path):
    """Mounts ConfigMap `name` at `path` in the Deployment of `objs`."""
    pod = objs[0]["spec"]["template"]["spec"]
    pod["containers"][0]["volumeMounts"].append({"name": name, "mountPath": path, "readOnly": True})
    pod["volumes"].append({"name": name, "configMap": {"name": name}})


def egress_proxy(img, v):
    """The egress proxy: API on 8080 (Magi), proxy on 3128 (environments)."""
    ns = v["NAMESPACE"]
    objs = service(v, "egress", img["IMAGE_EGRESS"], [
        {"name": "MAGI_EGRESS_POLICY", "value": "/etc/magi/egress-policy/policy.json"},
        {"name": "MAGI_EGRESS_CA_CERT", "value": "/etc/magi/egress-ca/ca.crt"},
        {"name": "MAGI_EGRESS_CA_KEY", "value": "/etc/magi/egress-ca-key/ca.key"},
        {"name": "MAGI_EGRESS_ENV_KEY", "value": "/etc/magi/egress-key/key"},
        {"name": "MAGI_EGRESS_SECRETS_DIR", "value": "/etc/magi/egress-secrets"},
        {"name": "MAGI_URL", "value": f"http://magi.{ns}.svc:8080"},
        {"name": "MAGI_EGRESS_TOKEN_FILE", "value": "/etc/magi/token/token"}],
        secrets=[("magi-egress-ca-key", "/etc/magi/egress-ca-key"), ("magi-egress-env-key", "/etc/magi/egress-key"),
                 ("magi-egress-secrets", "/etc/magi/egress-secrets"), ("magi-token-egress", "/etc/magi/token")],
        extra_ports=[{"name": "proxy", "containerPort": 3128}], mem="512Mi")
    if v.get("EGRESS_SSH") == "true":
        # The proxy ends SSH with this host key (Secret magi-egress-ssh-key).
        objs[0]["spec"]["template"]["spec"]["containers"][0]["env"].append(
            {"name": "MAGI_EGRESS_SSH_HOST_KEY", "value": "/etc/magi/egress-ssh-key/key"})
        secret_mount(objs, "magi-egress-ssh-key", "/etc/magi/egress-ssh-key")
    configmap(objs, "magi-egress-ca", "/etc/magi/egress-ca")
    configmap(objs, "magi-egress-policy", "/etc/magi/egress-policy")
    objs[1]["spec"]["ports"].append({"name": "proxy", "port": 3128, "targetPort": "proxy"})
    # Environments reach the proxy port only; the API is for Magi.
    objs.append({"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "egress", "namespace": ns, "labels": LABELS},
                 "spec": {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "egress"}}, "policyTypes": ["Ingress"],
                          "ingress": [
                              {"from": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": v["ENV_NAMESPACE"]}}},
                                        {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "magi"}}},
                                        {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "apprentice"}}},
                                        # SearXNG (SEARCH=true) sends the queries to the engines.
                                        {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "search"}}}],
                               "ports": [{"protocol": "TCP", "port": 3128}]},
                              {"from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "magi"}}}],
                               "ports": [{"protocol": "TCP", "port": 8080}]}]}})
    return objs


def search(v):
    """SearXNG for web_search (magi/compose/search.go). Only Magi calls it.
    The Secret magi-search-settings has settings.yml: its outgoing proxy is
    the egress proxy as svc_searxng (the URL holds the credential), so the
    engine hosts must be in the policy for that user (deploy/README.md)."""
    ns = v["NAMESPACE"]
    labels = {**LABELS, "app.kubernetes.io/name": "search"}
    container = {
        "name": "search", "image": v["SEARCH_IMAGE"],
        # All its traffic goes through the egress proxy, so it trusts only
        # the proxy CA. SearXNG gives SSL_CERT_FILE to curl over the
        # "verify" setting.
        "env": [{"name": "SEARXNG_BASE_URL", "value": f"http://search.{ns}.svc:8080/"},
                {"name": "SSL_CERT_FILE", "value": "/etc/magi/egress-ca/ca.crt"}, {"name": "SSL_CERT_DIR", "value": ""}],
        "ports": [{"name": "http", "containerPort": 8080}],
        "readinessProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 10},
        "resources": {"requests": {"cpu": "20m", "memory": "96Mi"}, "limits": {"cpu": "1", "memory": "512Mi"}},
        "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
        "volumeMounts": [{"name": "settings", "mountPath": "/etc/searxng", "readOnly": True},
                         {"name": "cache", "mountPath": "/var/cache/searxng"},
                         {"name": "magi-egress-ca", "mountPath": "/etc/magi/egress-ca", "readOnly": True}],
    }
    spec = {"automountServiceAccountToken": False, "nodeSelector": {"kubernetes.io/arch": "amd64"},
            "imagePullSecrets": [{"name": "magi-registry"}],
            "securityContext": {"runAsNonRoot": True, "runAsUser": 977, "runAsGroup": 977, "fsGroup": 977, "seccompProfile": {"type": "RuntimeDefault"}},
            "containers": [container],
            "volumes": [{"name": "settings", "secret": {"secretName": "magi-search-settings", "defaultMode": 0o440}},
                        {"name": "cache", "emptyDir": {"sizeLimit": "256Mi"}},
                        {"name": "magi-egress-ca", "configMap": {"name": "magi-egress-ca"}}]}
    return [
        {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "search", "namespace": ns, "labels": labels},
         "spec": {"replicas": 1, "selector": {"matchLabels": {"app.kubernetes.io/name": "search"}},
                  "template": {"metadata": {"labels": labels}, "spec": spec}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "search", "namespace": ns, "labels": labels},
         "spec": {"selector": {"app.kubernetes.io/name": "search"}, "ports": [{"name": "http", "port": 8080, "targetPort": "http"}]}},
        {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": "search", "namespace": ns, "labels": LABELS},
         "spec": {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "search"}}, "policyTypes": ["Ingress"],
                  "ingress": [{"from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "magi"}}}],
                               "ports": [{"protocol": "TCP", "port": 8080}]}]}},
    ]


def pvc(v, name, size):
    return {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": name, "namespace": v["NAMESPACE"], "labels": LABELS},
            "spec": {"accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": size}},
                     **({"storageClassName": v["STORAGE_CLASS"]} if v.get("STORAGE_CLASS") else {})}}


def base(v):
    """deploy/base/postgres.yaml and deploy/registry/registry.yaml with the
    namespace and the storage class of the installation. A line with
    ${STORAGE_CLASS} goes when STORAGE_CLASS is empty."""
    here = pathlib.Path(__file__).parent
    out = []
    for f in [here / "base/postgres.yaml", here / "registry/registry.yaml"]:
        lines = []
        for line in f.read_text().splitlines():
            if "${STORAGE_CLASS}" in line and not v.get("STORAGE_CLASS"):
                continue
            lines.append(line.replace("${STORAGE_CLASS}", v.get("STORAGE_CLASS", "")).replace("${NAMESPACE}", v["NAMESPACE"]))
        out.append("\n".join(lines))
    return "\n---\n".join(out) + "\n"


def render(img, v):
    ns = v["NAMESPACE"]
    urls = {k: f"http://{k}.{ns}.svc:8080" for k in ["artifacts", "conversation", "execution", "knowledge", "source", "speech", "workflow", "release"]}
    model_egress = v.get("EGRESS") == "true" and v.get("MODEL_EGRESS") == "true"
    # With MODEL_EGRESS, the proxy puts the key (proxy secret "model") in
    # place of this placeholder; see deploy/README.md.
    model_key = ({"name": "MAGI_MODEL_API_KEY", "value": MODEL_PLACEHOLDER} if model_egress
                 else {"name": "MAGI_MODEL_API_KEY_FILE", "value": "/etc/magi/model/api-key"})
    model_secret = [] if model_egress else [("magi-model", "/etc/magi/model")]
    model = [
        {"name": "MAGI_MODEL_BASE_URL", "value": v["MODEL_BASE_URL"]},
        {"name": "MAGI_MODEL_NAME", "value": v["MODEL_NAME"]},
        model_key,
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
    out += service(v, "speech", img["IMAGE_SPEECH"], db_env("speech") + [
        {"name": "MAGI_SPEECH_STT_URL", "value": v["SPEECH_STT_URL"]}, {"name": "MAGI_SPEECH_TTS_URL", "value": v["SPEECH_TTS_URL"]},
        {"name": "MAGI_SPEECH_ENGINE_TOKEN_FILE", "value": "/etc/magi/speech/token"}] +
        ([{"name": "MAGI_SPEECH_TURN_URL", "value": v["SPEECH_TURN_URL"]}] if v.get("SPEECH_TURN_URL") else []),
        secrets=[("magi-speech-engine", "/etc/magi/speech")])
    egress = v.get("EGRESS") == "true"
    exec_env, exec_secrets = [], []
    if egress:
        exec_env = [{"name": "MAGI_EXEC_EGRESS_PROXY", "value": f"egress.{ns}.svc:3128"}, {"name": "MAGI_EXEC_EGRESS_NAMESPACE", "value": ns},
                    {"name": "MAGI_EXEC_EGRESS_ENV_KEY", "value": "/etc/magi/egress-key/key"}, {"name": "MAGI_EXEC_EGRESS_CA", "value": "/etc/magi/egress-ca/ca.crt"}]
        exec_secrets = [("magi-egress-env-key", "/etc/magi/egress-key")]
        if v.get("EGRESS_SSH") == "true":
            # Pods with network "proxy" trust the SSH host key of the proxy.
            exec_env.append({"name": "MAGI_EXEC_EGRESS_SSH_PUB", "value": "/etc/magi/egress-ca/ssh.pub"})
    ex = service(v, "execution", img["IMAGE_EXECUTION"], db_env("execution") + [
        {"name": "MAGI_EXEC_BACKEND", "value": "kubernetes"}, {"name": "MAGI_EXEC_NAMESPACE", "value": v["ENV_NAMESPACE"]},
        {"name": "MAGI_EXEC_PUBLIC_URL", "value": urls["execution"]}, {"name": "MAGI_EXEC_PULL_SECRET", "value": "magi-registry"}, *exec_env],
        secrets=exec_secrets, sa="execution", mem="512Mi")
    if egress:
        configmap(ex, "magi-egress-ca", "/etc/magi/egress-ca")
    out += ex
    if egress:
        out += egress_proxy(img, v)
    out += service(v, "identity", img["IMAGE_IDENTITY"], db_env("identity") + [
        {"name": "MAGI_ISSUER_KEY", "value": "/etc/magi/issuer/issuer.json"},
        {"name": "MAGI_PUBLIC_URL", "value": f"https://{v['PUBLIC_HOST']}"},
        {"name": "MAGI_ALLOWED_DOMAINS", "value": v["ALLOWED_DOMAINS"]},
        {"name": "MAGI_LINKKEYS_RUNTIME", "value": "/etc/magi/linkkeys/runtime.json"}],
        secrets=[("magi-issuer", "/etc/magi/issuer"), ("magi-linkkeys", "/etc/magi/linkkeys")])
    # The release controller changes the magi Deployment; magi releases itself.
    # Before the change, the new image runs "magi-server preflight": it calls
    # each service that Magi uses. During the stabilization window, each new
    # pod must answer /healthz/deps (the same calls). The readiness probe
    # stays /healthz, so that one service that stops does not stop Magi.
    targets = {"targets": [{"name": "magi", "namespace": ns, "deployment": "magi", "container": "magi",
                            "health_path": "/healthz/deps", "health_port": 8080,
                            "preflight_args": ["preflight"], "preflight_timeout_seconds": 90}]}
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
        # Comma-separated, in order of preference, with one model name for
        # each URL. http://<name>.revconn/v1 is a host that connects out to
        # Magi (primitives/inference, "Connect out").
        urls_l = [u.strip() for u in v["PERSONA_LOCAL_URL"].split(",") if u.strip()]
        names = [m.strip() for m in v.get("PERSONA_LOCAL_MODEL", "local").split(",")]
        if len(names) != len(urls_l):
            sys.exit("PERSONA_LOCAL_MODEL needs one model name for each URL of PERSONA_LOCAL_URL")
        local = [{"base_url": u, "model": m, "key_file": "/etc/magi/inference/token", "attempts": 1, "timeout_s": 120}
                 for u, m in zip(urls_l, names)]
        chain = [*local, {"base_url": v["MODEL_BASE_URL"], "model": v["MODEL_NAME"],
                          **({"key": MODEL_PLACEHOLDER} if model_egress else {"key_file": "/etc/magi/model/api-key"})}]
        # The librarian works in the background, so it waits for the local
        # hosts instead of using the provider.
        persona = [{"name": "MAGI_PERSONA_MODELS", "value": json.dumps(chain)},
                   {"name": "MAGI_LIBRARIAN_MODELS", "value": json.dumps(local)}]
        persona_secrets = [("magi-inference-token", "/etc/magi/inference")]
    egress_env = []
    if egress:
        # EGRESS_RULES=true: the ConfigMap magi-egress-rules has rules.json.
        # Runner sandboxes reach the proxy port through /runner/egress.
        egress_env = [{"name": "MAGI_EGRESS_URL", "value": f"http://egress.{ns}.svc:8080"},
                      {"name": "MAGI_EGRESS_PROXY", "value": f"egress.{ns}.svc:3128"},
                      {"name": "MAGI_EGRESS_ENV_KEY_FILE", "value": "/etc/magi/egress-key/key"},
                      {"name": "MAGI_EGRESS_CA", "value": "/etc/magi/egress-ca/ca.crt"}]
        if v.get("EGRESS_RULES") == "true":
            egress_env.append({"name": "MAGI_EGRESS_RULES", "value": "/etc/magi/egress-rules/rules.json"})
        if v.get("EGRESS_SSH") == "true":
            # The public SSH host key of the proxy, for task VMs.
            egress_env.append({"name": "MAGI_EGRESS_SSH_PUB", "value": "/etc/magi/egress-ca/ssh.pub"})
        if model_egress:
            egress_env.append({"name": "MAGI_MODEL_EGRESS", "value": "true"})
    detector_env, detector_secrets = [], []
    if v.get("DETECTOR_URL"):
        detector_env = [{"name": "MAGI_DETECTOR_URL", "value": v["DETECTOR_URL"]},
                        {"name": "MAGI_DETECTOR_TOKEN_FILE", "value": "/etc/magi/detector/token"},
                        {"name": "MAGI_DETECTOR_MODE", "value": v.get("DETECTOR_MODE", "flag")},
                        {"name": "MAGI_DETECTOR_THRESHOLD", "value": v.get("DETECTOR_THRESHOLD", "0.98")}]
        detector_secrets = [("magi-speech-engine", "/etc/magi/detector")]
    search_env = []
    if v.get("SEARCH") == "true":
        if not egress:
            sys.exit("SEARCH needs EGRESS: SearXNG and the pages go through the egress proxy")
        search_env = [{"name": "MAGI_SEARCH_URL", "value": f"http://search.{ns}.svc:8080"}]
        out += search(v)
    mg = service(v, "magi", img["IMAGE_MAGI"], [
        *model, *svc_token, *persona, *egress_env, *detector_env, *search_env,
        *[{"name": f"MAGI_{k.upper()}_URL", "value": u} for k, u in urls.items()],
        {"name": "MAGI_ENV_IMAGE", "value": v["ENV_IMAGE"]},
        # RUNNER: the default runner for code changes (RunnerService).
        # PUBLISHER: the runner that pushes; default: one that can.
        # ADMINS: user subjects that can make operator calls.
        # LEXICON_EDITORS: user subjects that can change the household
        # lexicon; empty means ADMINS.
        {"name": "MAGI_RUNNER", "value": v.get("RUNNER", "")},
        {"name": "MAGI_PUBLISHER", "value": v.get("PUBLISHER", "")},
        {"name": "MAGI_ADMINS", "value": v.get("ADMINS", "")},
        {"name": "MAGI_LEXICON_EDITORS", "value": v.get("LEXICON_EDITORS", "")},
        {"name": "MAGI_IMAGE_REGISTRY", "value": f"registry.{ns}.svc:5000"},
        {"name": "MAGI_IMAGE_CREDENTIALS_FILE", "value": "/etc/magi/registry/registry.json"}],
        secrets=[*model_secret, ("magi-token-magi", "/etc/magi/token"), ("magi-registry-push", "/etc/magi/registry"), *persona_secrets, *detector_secrets,
                 *([("magi-egress-env-key", "/etc/magi/egress-key")] if egress else [])], mem="512Mi")
    if egress:
        configmap(mg, "magi-egress-ca", "/etc/magi/egress-ca")
    if egress and v.get("EGRESS_RULES") == "true":
        configmap(mg, "magi-egress-rules", "/etc/magi/egress-rules")
    out += mg
    # The apprentice worker has no Service; it only claims work.
    # With MODEL_EGRESS, the apprentice is the proxy user svc_apprentice:
    # Secret magi-egress-apprentice has its proxy URL with the credential.
    # Calls to the services of the namespace do not use the proxy.
    proxy_env = []
    if model_egress:
        url = {"valueFrom": {"secretKeyRef": {"name": "magi-egress-apprentice", "key": "url"}}}
        proxy_env = [{"name": "HTTPS_PROXY", **url}, {"name": "HTTP_PROXY", **url},
                     {"name": "NO_PROXY", "value": ".svc,.svc.cluster.local,localhost,127.0.0.1"},
                     {"name": "SSL_CERT_FILE", "value": "/etc/magi/egress-ca/ca.crt"}]
    apprentice = service(v, "apprentice", img["IMAGE_APPRENTICE"], [
        *model, *proxy_env, {"name": "MAGI_TOKEN_FILE", "value": "/etc/magi/token/token"},
        {"name": "MAGI_WORKFLOW_URL", "value": urls["workflow"]}, {"name": "MAGI_EXECUTION_URL", "value": urls["execution"]},
        {"name": "MAGI_ARTIFACTS_URL", "value": urls["artifacts"]}],
        secrets=[*model_secret, ("magi-token-apprentice", "/etc/magi/token")], mem="512Mi")
    if model_egress:
        configmap(apprentice, "magi-egress-ca", "/etc/magi/egress-ca")
    apprentice = apprentice[0]
    c = apprentice["spec"]["template"]["spec"]["containers"][0]
    c["args"] = ["worker"]
    c.pop("readinessProbe")
    c["ports"] = []
    out.append(apprentice)
    # Public routes: only identity login, the Magi API, and the web client.
    # Long polls wait up to 30 s; the gateway default timeout is shorter.
    timeouts = {"request": "120s", "backendRequest": "120s"}
    route_rules = [
        {"matches": [{"path": {"type": "PathPrefix", "value": "/login/"}}], "backendRefs": [{"name": "identity", "port": 8080}], "timeouts": timeouts},
        {"matches": [{"path": {"type": "PathPrefix", "value": "/rpc/IdentityService/"}}], "backendRefs": [{"name": "identity", "port": 8080}], "timeouts": timeouts},
        {"matches": [{"path": {"type": "PathPrefix", "value": "/"}}], "backendRefs": [{"name": "magi", "port": 8080}], "timeouts": timeouts},
    ]
    if egress:
        # The egress tunnel of runner sandboxes: an upgraded connection that
        # stays open, so it has no request timeout.
        route_rules.insert(0, {"matches": [{"path": {"type": "Exact", "value": "/runner/egress"}}], "backendRefs": [{"name": "magi", "port": 8080}]})
    # Inference hosts connect out and keep idle connections (lib/revconn):
    # no request timeout.
    route_rules.insert(0, {"matches": [{"path": {"type": "Exact", "value": "/inference/connect"}}], "backendRefs": [{"name": "magi", "port": 8080}]})
    if v.get("PUBLIC_ROUTE") == "true":
        out.append({"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
                    "metadata": {"name": "magi", "namespace": ns, "labels": LABELS},
                    "spec": {"parentRefs": [{"name": v["GATEWAY_NAME"], "namespace": v["GATEWAY_NAMESPACE"], "sectionName": v["GATEWAY_SECTION"]}],
                             "hostnames": [v["PUBLIC_HOST"]], "rules": route_rules}})
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--images")
    ap.add_argument("--values", required=True)
    ap.add_argument("--only", default="", help="comma-separated object names to keep")
    ap.add_argument("--base", action="store_true", help="print the PostgreSQL cluster and the registry (YAML)")
    a = ap.parse_args()
    if a.base:
        print(base(load_env(a.values)), end="")
        return
    if not a.images:
        ap.error("--images is required")
    # A missing image renders as MISSING so --only can deploy part of the set.
    images = collections.defaultdict(lambda: "MISSING", load_env(a.images))
    objs = render(images, load_env(a.values))
    if a.only:
        keep = set(a.only.split(","))
        objs = [o for o in objs if o["metadata"]["name"] in keep]
    print(json.dumps({"apiVersion": "v1", "kind": "List", "items": objs}, indent=1))


if __name__ == "__main__":
    main()
