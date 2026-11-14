"""Read and change the model catalog (primitives/models).

  tools/models.py --url URL --token-file F apply CATALOG.json
  tools/models.py --url URL --token-file F list
  tools/models.py --url URL --token-file F select [--task T] [--selector JSON] [--strict]
  tools/models.py --url URL --token-file F score NAME --scores JSON [--calibration JSON]

CATALOG.json: {"models": [Model...], "tasks": [{"task": ..., "selector": {...}}]}
(contracts/models.csil). apply puts each model and task; it removes
nothing. Changes need a token with the scope models:admin. Keep the
catalog file of an installation outside the repository: it has host names.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "clients", "python"))
from magi_rpc import HttpTransport  # noqa: E402
from magi_models.client import ModelClient  # noqa: E402
from magi_models import types as T  # noqa: E402


def selector(d):
    return T.Selector(**{k: d[k] for k in d})


def model(d):
    d = dict(d)
    d["endpoint"] = T.Endpoint(**d["endpoint"])
    d["cost"] = T.Cost(**d.get("cost", {"input_per_mtok": 0, "output_per_mtok": 0}))
    for k, v in (("tags", []), ("attributes", {}), ("scores", {}), ("calibration", {}), ("enabled", True), ("updated_ms", 0), ("description", "")):
        d.setdefault(k, v)
    return T.Model(**d)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--token-file", required=True)
    sub = ap.add_subparsers(dest="cmd", required=True)
    a = sub.add_parser("apply")
    a.add_argument("file")
    sub.add_parser("list")
    s = sub.add_parser("select")
    s.add_argument("--task", default="")
    s.add_argument("--selector", default="")
    s.add_argument("--strict", action="store_true")
    sc = sub.add_parser("score")
    sc.add_argument("name")
    sc.add_argument("--scores", default="{}")
    sc.add_argument("--calibration", default="{}")
    args = ap.parse_args()
    c = ModelClient(HttpTransport(args.url, open(args.token_file).read().strip()))
    if args.cmd == "apply":
        cat = json.load(open(args.file))
        for m in cat.get("models", []):
            c.put_model(T.PutModelRequest(model=model(m)))
            print("model", m["name"])
        for t in cat.get("tasks", []):
            c.put_task(T.PutTaskRequest(task=T.TaskDefault(task=t["task"], selector=selector(t["selector"]),
                                                            description=t.get("description"), updated_ms=0)))
            print("task", t["task"])
    elif args.cmd == "list":
        for m in c.list_models(T.ListModelsRequest(include_disabled=True)).models:
            print(f"{m.name:28} {'on ' if m.enabled else 'off'} {m.endpoint.base_url} {m.endpoint.model} tags={','.join(m.tags)}")
        for t in c.list_tasks(T.ListTasksRequest()).tasks:
            print(f"task {t.task:20} {t.selector}")
    elif args.cmd == "select":
        req = T.SelectRequest(task=args.task or None, selector=selector(json.loads(args.selector)) if args.selector else None,
                              strict=args.strict or None)
        for x in c.select(req).candidates:
            print(f"{x.model.name:28} {x.reason}")
    else:
        m = c.score(T.ScoreRequest(name=args.name, scores=json.loads(args.scores), calibration=json.loads(args.calibration)))
        print(m.name, m.scores, m.calibration)


if __name__ == "__main__":
    main()
