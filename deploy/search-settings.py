#!/usr/bin/env python3
"""Write settings.yml of the SearXNG service (deploy/render.py, SEARCH=true).

    deploy/search-settings.py --namespace magi --env-key env.key --out settings.yml
    kubectl -n magi create secret generic magi-search-settings --from-file=settings.yml

SearXNG sends the queries to the engines through the egress proxy as the
proxy user svc_searxng. The proxy URL holds the credential of that user
(HMAC-SHA256 of "magi-egress-env:svc_searxng" with the environment key), so
the file is a Secret. The script does not print the file.
"""
import argparse
import hashlib
import hmac
import json
import os
import secrets

# The engines, and the hosts that they call. The egress policy must permit
# these hosts for svc_searxng (deploy/README.md).
ENGINES = {
    "duckduckgo": ["duckduckgo.com", "html.duckduckgo.com"],
    "brave": ["search.brave.com"],
    "bing": ["www.bing.com"],
    "mojeek": ["www.mojeek.com"],
    "wikipedia": ["*.wikipedia.org"],
    "wikidata": ["www.wikidata.org", "query.wikidata.org"],
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--namespace", required=True)
    ap.add_argument("--env-key", required=True, help="the environment key file of the egress proxy")
    ap.add_argument("--out", required=True)
    ap.add_argument("--hosts", action="store_true", help="print the policy destinations for svc_searxng and stop")
    a = ap.parse_args()
    if a.hosts:
        hosts = sorted({h for hs in ENGINES.values() for h in hs})
        print(json.dumps([{"host": h, "environments": ["svc_searxng"]} for h in hosts], indent=1))
        return
    key = open(a.env_key, "rb").read().strip()
    user = "svc_searxng"
    cred = hmac.new(key, b"magi-egress-env:" + user.encode(), hashlib.sha256).hexdigest()
    proxy = f"http://{user}:{cred}@egress.{a.namespace}.svc:3128"
    # JSON is YAML; it needs no YAML library.
    settings = {
        "use_default_settings": {"engines": {"keep_only": list(ENGINES)}},
        "general": {"instance_name": "magi-search", "enable_metrics": False},
        "search": {"safe_search": 1, "autocomplete": "", "formats": ["html", "json"]},
        "server": {"secret_key": secrets.token_hex(32), "limiter": False, "public_instance": False,
                   "image_proxy": False, "method": "GET"},
        "outgoing": {"request_timeout": 6.0, "max_request_timeout": 12.0, "verify": "/etc/magi/egress-ca/ca.crt",
                     "proxies": {"all://": [proxy]}},
    }
    fd = os.open(a.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(settings, f, indent=1)
    print(f"wrote {a.out}")


if __name__ == "__main__":
    main()
