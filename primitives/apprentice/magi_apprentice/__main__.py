"""Command line: `python -m magi_apprentice worker`, `run`, or `review`."""
from __future__ import annotations

import argparse
import json
import logging
import os
import signal
import sys
import time
from dataclasses import replace

from ._clients import artifacts as art, execution as ex, token_source, transport, workflow as wf
from .apprentice import Task
from .config import Limits, ModelConfig, ServiceConfig
from .guards import outcome_of
from .local import LocalExecution
from .model import ChatModel, ModelError
from .prereview import pre_review, summary
from .runner import apply_model_override, run_task
from .tools import Tools, Workspace
from .worker import DEFAULT_CHECK, Worker


def _need(value: str, name: str) -> str:
    if not value:
        sys.exit(f"magi_apprentice: {name} is not set")
    return value


def cmd_worker(args: argparse.Namespace) -> int:
    sc = ServiceConfig.from_env()
    if args.queue:
        sc.queue = args.queue
    tok = token_source(sc.token_file)
    worker = Worker(
        workflow=wf.WorkflowClient(transport(_need(sc.workflow_url, "MAGI_WORKFLOW_URL"), sc.token_file)),
        execution=ex.ExecutionClient(transport(_need(sc.execution_url, "MAGI_EXECUTION_URL"), sc.token_file,
                                               timeout=Limits().bash_timeout_max_s + 120)),
        artifacts=art.ArtifactClient(transport(sc.artifacts_url, sc.token_file)) if sc.artifacts_url else None,
        model_cfg=ModelConfig.from_env(), limits=Limits.from_env(), queue=sc.queue,
        worker_id=sc.worker_id, extra_secrets=lambda: (tok(),),
        check_command=os.environ.get("MAGI_APPRENTICE_CHECK_COMMAND", DEFAULT_CHECK))

    def stop(*_: object) -> None:
        logging.info("stopping after the current task")
        worker.stopping.set()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    logging.info("worker %s claiming from queue %s", sc.worker_id, sc.queue)
    worker.serve(max_tasks=args.max_tasks)
    return 0


def cmd_run(args: argparse.Namespace) -> int:
    sc = ServiceConfig.from_env()
    model_cfg, limits = apply_model_override(ModelConfig.from_env(), Limits.from_env(), args.model)
    if args.max_steps:
        limits.max_steps = args.max_steps
    if args.tester:
        limits.tester = True
    instructions = args.instructions
    if instructions.startswith("@"):
        with open(instructions[1:]) as f:
            instructions = f.read()
    if args.local:
        execution, env_id = LocalExecution(), "local"
        workdir = args.workdir or args.local
    else:
        execution = ex.ExecutionClient(transport(_need(sc.execution_url, "MAGI_EXECUTION_URL"), sc.token_file,
                                                 timeout=limits.bash_timeout_max_s + 120))
        env_id = _need(args.environment or "", "--environment")
        workdir = args.workdir or "/work/repo"
    artifacts = art.ArtifactClient(transport(sc.artifacts_url, sc.token_file)) if sc.artifacts_url and not args.no_upload else None
    task = Task(instructions=instructions, workdir=workdir, check_command=args.check_command,
                check_output=args.check_output, title=args.title, review_comment=args.review_comment,
                lessons=args.lessons)
    t0 = time.monotonic()
    out = run_task(task, execution=execution, environment=env_id, model_cfg=model_cfg, limits=limits,
                   artifacts=artifacts, labels={"run": "cli", "step": "cli"},
                   on_progress=lambda n: logging.info("progress: %s", n),
                   transcript_path=args.transcript, extra_secrets=(token_source(sc.token_file)(),),
                   supervisor_model=args.supervisor_model or None,
                   review_model=args.review_model or None)
    r = out.result
    print(json.dumps({"status": r.status, "outcome": outcome_of(r.status), "summary": r.summary, "steps": r.steps,
                      "tool_calls": r.tool_calls, "check_passed": r.check_passed, "error": r.error,
                      "transcript": out.transcript_digest or args.transcript,
                      "upload_error": out.upload_error, "usage": r.usage,
                      "seconds": round(time.monotonic() - t0, 1)}, indent=2))
    return 0 if r.status == "finished" and r.check_passed is not False else 1


def cmd_review(args: argparse.Namespace) -> int:
    """The model review of a change whose checks passed. Prints one JSON
    object; a failed review is a result with an error, not a failed command,
    because the review of a person goes on without it."""
    sc = ServiceConfig.from_env()
    model_cfg, limits = apply_model_override(ModelConfig.from_env(), Limits.from_env(), args.model)
    # The lens answers are long: each lens writes out what it checked.
    model_cfg = replace(model_cfg, max_tokens=max(model_cfg.max_tokens, 12000), temperature=0.6)
    task = args.instructions
    if task.startswith("@"):
        with open(task[1:]) as f:
            task = f.read()
    if args.local:
        execution, env_id, workdir = LocalExecution(), "local", args.workdir or args.local
    else:
        execution = ex.ExecutionClient(transport(_need(sc.execution_url, "MAGI_EXECUTION_URL"), sc.token_file,
                                                 timeout=limits.bash_timeout_max_s + 120))
        env_id, workdir = _need(args.environment or "", "--environment"), args.workdir or "/work/repo"
    tools = Tools(Workspace(execution, env_id, workdir, limits))
    try:
        out = pre_review(ChatModel(model_cfg), tools, task, args.base, args.lessons or "", samples=args.samples)
    except (ModelError, OSError) as e:
        out = {"verdict": "revise", "blockers": [], "error": str(e)}
    out["summary"] = summary(out)
    print(json.dumps(out, indent=2))
    return 0


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, stream=sys.stderr,
                        format='{"time":"%(asctime)s","level":"%(levelname)s","msg":%(message)r}')
    p = argparse.ArgumentParser(prog="magi_apprentice")
    sub = p.add_subparsers(dest="cmd", required=True)
    w = sub.add_parser("worker", help="claim tasks from the workflow queue")
    w.add_argument("--queue", help="queue name (default MAGI_APPRENTICE_QUEUE or 'apprentice')")
    w.add_argument("--max-tasks", type=int, help="exit after this many tasks")
    r = sub.add_parser("run", help="run one task without the workflow service")
    r.add_argument("--instructions", required=True, help="task text, or @file")
    r.add_argument("--environment", help="execution environment id")
    r.add_argument("--local", metavar="DIR", help="run tools on this host in DIR (no isolation)")
    r.add_argument("--workdir")
    r.add_argument("--check-command")
    r.add_argument("--check-output")
    r.add_argument("--title")
    r.add_argument("--review-comment")
    r.add_argument("--lessons", help="review lessons of this repository, one line for each")
    r.add_argument("--model", help="model name or JSON override")
    r.add_argument("--max-steps", type=int)
    r.add_argument("--supervisor-model", help="model name of the supervisor (default: no supervisor)")
    r.add_argument("--tester", action="store_true",
                   help="with a review model: tests from the task text, run before the review")
    r.add_argument("--review-model", help="model name of the lens review at the first finish "
                                          "(default: the checklist self-review)")
    r.add_argument("--transcript", help="also write the JSONL transcript to this file")
    r.add_argument("--no-upload", action="store_true", help="do not upload the transcript")
    v = sub.add_parser("review", help="the model review of a change whose checks passed")
    v.add_argument("--instructions", required=True, help="task text, or @file")
    v.add_argument("--base", required=True, help="the commit that the change starts from")
    v.add_argument("--environment", help="execution environment id")
    v.add_argument("--local", metavar="DIR", help="run tools on this host in DIR (no isolation)")
    v.add_argument("--workdir")
    v.add_argument("--lessons", help="review lessons of this repository")
    v.add_argument("--model", help="model name or JSON override")
    v.add_argument("--samples", type=int, default=1, help="each lens is asked this many times")
    args = p.parse_args(argv)
    return {"worker": cmd_worker, "run": cmd_run, "review": cmd_review}[args.cmd](args)


if __name__ == "__main__":
    sys.exit(main())
