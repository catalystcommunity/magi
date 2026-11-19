"""Run the cases of the coding eval through the code-change workflow of Magi.

  python3 evals/coding/run.py --url URL --token-file F --model JSON --label L
      [--cases pr2,pr5] [--max-minutes 240] [--out evals/coding/results]

Each case starts one task: kind code-change, repo magi, the title of the
case, the instructions of cases/<id>.md, the base of the case, and the
model of the candidate. The script polls the task until it waits for a
decision or ends, then writes result.json and the diff of the task. It
never decides: a person grades the run and rejects it.
"""
import argparse
import json
import os
import re
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
                                "clients", "python"))
from magi_rpc import HttpTransport, RpcError  # noqa: E402
from magi_magi.client import MagiClient  # noqa: E402
from magi_magi import types as T  # noqa: E402

POLL_SECONDS = 60
MAX_POLL_ERRORS = 5

# The runner ends each work step with a note of this shape (magi/runner/steps.go):
# "apprentice finished after 42 steps, 87 tool calls (cost $2.647): ...".
# A provider that reports no cost leaves the "(cost ...)" part out.
WORK_NOTE = re.compile(r"^apprentice (\S+) after (\d+) steps, (\d+) tool calls(?: \(cost \$([0-9.]+)\))?:")


class TokenExpired(Exception):
    """The server answered 401. The token is not valid any more."""


def log(msg):
    print(time.strftime("%H:%M:%S"), msg, flush=True)


def load_cases(path):
    with open(path) as f:
        return [json.loads(line) for line in f if line.strip()]


def pick_cases(cases, want):
    """The cases to run: all of them, or the ones that --names lists."""
    if not want:
        return cases
    ids = [i.strip() for i in want.split(",") if i.strip()]
    by_id = {c["id"]: c for c in cases}
    missing = [i for i in ids if i not in by_id]
    if missing:
        raise SystemExit("no case %s in cases.jsonl" % ", ".join(missing))
    return [by_id[i] for i in ids]


def work_totals(task):
    """Model calls, tool calls, and cost of the apprentice of one task.

    Each work step ends with one note that has the numbers, so a task that
    went back from the check step adds the notes of its work steps.
    """
    out = {"status": "", "steps": 0, "tool_calls": 0, "cost": None}
    for st in task.steps:
        if st.step != "work":
            continue
        for note in st.notes:
            m = WORK_NOTE.match(note)
            if not m:
                continue
            out["status"] = m.group(1)
            out["steps"] += int(m.group(2))
            out["tool_calls"] += int(m.group(3))
            if m.group(4):
                out["cost"] = round((out["cost"] or 0) + float(m.group(4)), 3)
    return out


def check_view(task):
    """The check of one task: pass or fail, and how many times it ran.

    Each attempt of the check step is one entry in the history of the task,
    so the count of those entries is the number of attempts.
    """
    attempts, last = 0, ""
    for st in task.steps:
        if st.step != "check":
            continue
        attempts += 1
        if st.outcome:
            last = st.outcome
    if not attempts:
        return None
    return {"outcome": "pass" if last == "pass" else "fail", "attempts": attempts, "step_outcome": last}


def at_end(task):
    """True when the task waits for a human decision or the run is over.

    The workflow has no other state between running and those two, and the
    task view shows the result of a succeeded run in its status.
    """
    return task.status != "running"


def call(client, op, *args):
    """One RPC operation. A 401 stops the whole run: the token expired."""
    try:
        return getattr(client, op)(*args)
    except RpcError as e:
        if e.code == 401:
            raise TokenExpired(e.message) from None
        raise


def poll(client, run_id, max_minutes, sleep=time.sleep, now=time.monotonic):
    """Poll one task until it waits for a decision, ends, or the time is over.

    An error of one poll is logged and the poll repeats. Five errors in a
    row stop the case. Returns the last task view (None when no poll
    answered), and whether the time ran out.
    """
    deadline = now() + max_minutes * 60
    errors, last = 0, None
    while True:
        try:
            last = call(client, "get_task", T.GetTaskRequest(id=run_id))
            errors = 0
        except RpcError as e:
            errors += 1
            log("poll %s failed (%d of %d): %s" % (run_id, errors, MAX_POLL_ERRORS, e))
            if errors >= MAX_POLL_ERRORS:
                raise
        if last is not None and at_end(last):
            return last, False
        if now() >= deadline:
            return last, True
        sleep(POLL_SECONDS)


def collect(client, case, task, model, label, started, out_dir, timed_out=False):
    """Write result.json, the diff, and the check output of one case.

    The diff and the outputs come from the same RPC operations that the
    commands `magi diff` and `magi output` use.
    """
    run = task.id if task else ""
    diff, files, check_output = "", [], None
    if task:
        try:
            d = call(client, "get_task_diff", T.GetTaskDiffRequest(id=run))
            diff, files = d.patch, [f.path for f in d.files]
        except RpcError as e:
            log("diff of %s: %s" % (run, e))
        try:
            o = call(client, "get_task_output", T.GetTaskOutputRequest(id=run, name="check.output"))
            check_output = o.text
        except RpcError as e:
            log("check output of %s: %s" % (run, e))
    work = work_totals(task) if task else {"status": "", "steps": 0, "tool_calls": 0, "cost": None}
    result = {
        "id": case["id"],
        "label": label,
        "model": model,
        "run": run,
        "status": task.status if task else "timeout",
        "step": task.current_step if task else "",
        "error": task.error or ("the case did not end within the time limit" if timed_out else None),
        "check": check_view(task) if task else None,
        "apprentice": work,
        "minutes": round((time.monotonic() - started) / 60, 1),
        "files": sorted(set(files) | set(case.get("changed", []))),
        "diff_files": sorted(files),
    }
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "result.json"), "w") as f:
        json.dump(result, f, indent=2, sort_keys=True)
        f.write("\n")
    with open(os.path.join(out_dir, "diff.patch"), "w") as f:
        f.write(diff)
    if check_output is not None:
        with open(os.path.join(out_dir, "check.output.txt"), "w") as f:
            f.write(check_output)
    return result


def summary_line(r):
    check = "-"
    if r["check"]:
        check = "%s (%d)" % (r["check"]["outcome"], r["check"]["attempts"])
    cost = "$%.3f" % r["apprentice"]["cost"] if r["apprentice"]["cost"] is not None else "-"
    return "%-5s %-10s %-11s %4d steps  %s  %6.1f min" % (
        r["id"], r["status"], check, r["apprentice"]["steps"], cost, r["minutes"])


def with_checks(instructions: str, checks_dir: str, case_id: str) -> str:
    """The instructions with the acceptance checks of the case at the end.
    The checks come from a spec step that ran before the eval (a model
    that read only the instructions), so the eval measures whether checks
    in the task text help the first attempt."""
    if not checks_dir:
        return instructions
    path = os.path.join(checks_dir, case_id + ".md")
    if not os.path.exists(path):
        return instructions
    with open(path) as f:
        checks = f.read().strip()
    first, _, rest = checks.partition("\n")
    if first.strip().strip("#*: ").lower() == "acceptance checks":
        checks = rest.strip()
    return instructions.rstrip() + "\n\nAcceptance checks:\n\n" + checks + "\n"


def main(argv=None, client=None, sleep=time.sleep):
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--url", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--model", required=True,
                    help='the model field of StartTask, for example \'{"name": "qwen3.6-35b-a3b-studiodt"}\'')
    ap.add_argument("--label", required=True, help="names the candidate in the output path")
    ap.add_argument("--cases", default="", help="comma-separated case ids; the default is all cases")
    ap.add_argument("--max-minutes", type=float, default=240)
    ap.add_argument("--out", default=os.path.join(here, "results"))
    ap.add_argument("--checks-dir", default="",
                    help="a directory with <id>.md files of acceptance checks, added at the end of the instructions")
    args = ap.parse_args(argv)
    model = json.loads(args.model)
    if not isinstance(model, dict) or not model.get("name"):
        raise SystemExit('--model must be a JSON object with a "name" field')
    cases = pick_cases(load_cases(os.path.join(here, "cases.jsonl")), args.cases)
    if client is None:
        client = MagiClient(HttpTransport(args.url, open(args.token_file).read().strip()))
    out_root = os.path.join(args.out, args.label)
    results = []
    for case in cases:
        log("%s: starting (%s)" % (case["id"], case["title"]))
        with open(os.path.join(here, "cases", case["id"] + ".md")) as f:
            instructions = f.read()
        instructions = with_checks(instructions, args.checks_dir, case["id"])
        started = time.monotonic()
        task = call(client, "start_task", T.StartTaskRequest(
            kind="code-change", instructions=instructions, title=case["title"], repo="magi",
            model=args.model, base=case["base"]))
        log("%s: %s started" % (case["id"], task.id))
        try:
            task, timed_out = poll(client, task.id, args.max_minutes, sleep=sleep)
        except RpcError as e:
            log("%s: %s" % (case["id"], e))
            continue
        r = collect(client, case, task, model, args.label, started, os.path.join(out_root, case["id"]),
                    timed_out=timed_out)
        log("%s: %s%s" % (case["id"], summary_line(r), " (time limit)" if timed_out else ""))
        results.append(r)
    print()
    for r in results:
        print(summary_line(r))
    if not results:
        raise SystemExit("no case ended")


def cli():
    try:
        main()
    except TokenExpired as e:
        raise SystemExit("the token expired (401: %s). Sign in again and start a new run." % e) from None


if __name__ == "__main__":
    cli()
