"""Workflow worker: claim agent tasks, run them, report, complete."""
from __future__ import annotations

import json
import logging
import threading
import time
from typing import Any, Callable

from ._clients import RpcError, workflow as wf
from .agent import Steering, Task
from .config import Limits, ModelConfig
from .model import Model
from .runner import apply_model_override, run_task

log = logging.getLogger("magi_agent.worker")

CLAIM_WAIT_MS = 20000
LEASE_MS = 120000
NOTE_MAX = 1000
DEFAULT_CHECK = "./tools/check.sh"


class Heartbeat:
    """Extends the claim lease on a background thread. lost is set when the
    service rejects the claim (for example, the run was cancelled)."""

    def __init__(self, client: Any, a: Any, lease_ms: int):
        self.client, self.a, self.lease_ms = client, a, lease_ms
        self.stop = threading.Event()
        self.lost: str | None = None
        self._t = threading.Thread(target=self._loop, daemon=True, name="heartbeat")

    def __enter__(self) -> "Heartbeat":
        self._t.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.stop.set()
        self._t.join(timeout=5)

    def _loop(self) -> None:
        interval = max(1.0, self.lease_ms / 3000)
        while not self.stop.wait(interval):
            try:
                ack = self.client.heartbeat(wf.HeartbeatRequest(
                    run=self.a.run, step=self.a.step, claim=self.a.claim, lease_ms=self.lease_ms))
                if not ack.ok:
                    self.lost = "heartbeat not acknowledged"
            except RpcError as e:
                if e.code in (404, 409, 403):
                    self.lost = f"claim lost: {e.message}"
                    return
                log.warning("heartbeat failed: %s", e)
            except Exception as e:  # network trouble: keep trying until the lease ends
                log.warning("heartbeat failed: %s", e)


class Worker:
    def __init__(self, *, workflow: Any, execution: Any, artifacts: Any | None,
                 model_cfg: ModelConfig, limits: Limits, queue: str = "agent",
                 worker_id: str = "agent", model: Model | None = None,
                 steering_for: Callable[[Any], Steering | None] | None = None,
                 extra_secrets: Callable[[], tuple[str | None, ...]] = lambda: (),
                 lease_ms: int = LEASE_MS, claim_wait_ms: int = CLAIM_WAIT_MS,
                 check_command: str | None = DEFAULT_CHECK):
        self.wf, self.execution, self.artifacts = workflow, execution, artifacts
        self.model_cfg, self.limits, self.queue, self.worker_id = model_cfg, limits, queue, worker_id
        self.model = model
        self.steering_for = steering_for or (lambda a: None)
        self.extra_secrets = extra_secrets
        self.lease_ms, self.claim_wait_ms = lease_ms, claim_wait_ms
        self.check_command = check_command
        self.stopping = threading.Event()

    def serve(self, max_tasks: int | None = None) -> int:
        done, backoff = 0, 1.0
        while not self.stopping.is_set() and (max_tasks is None or done < max_tasks):
            try:
                resp = self.wf.claim(wf.ClaimRequest(queue=self.queue, worker=self.worker_id,
                                                     wait_ms=self.claim_wait_ms, lease_ms=self.lease_ms))
                backoff = 1.0
            except Exception as e:
                log.warning("claim failed: %s; retry in %.0f s", e, backoff)
                self.stopping.wait(backoff)
                backoff = min(60.0, backoff * 2)
                continue
            if resp.assignment is None:
                continue
            self.handle(resp.assignment)
            done += 1
        return done

    def _progress(self, a: Any, note: str, ref: str | None = None) -> None:
        try:
            self.wf.progress(wf.ProgressRequest(run=a.run, step=a.step, claim=a.claim,
                                                note=note[:NOTE_MAX], ref=ref))
        except Exception as e:
            log.warning("progress failed: %s", e)

    def _complete(self, a: Any, outcome: str, outputs: dict[str, str], error: str | None = None) -> None:
        for attempt in range(5):
            try:
                self.wf.complete(wf.CompleteRequest(run=a.run, step=a.step, claim=a.claim,
                                                    outcome=outcome, outputs=outputs, error=error))
                return
            except RpcError as e:
                if e.code < 500:
                    log.error("complete rejected: %s", e)
                    return
                log.warning("complete failed: %s", e)
            except Exception as e:
                log.warning("complete failed: %s", e)
            time.sleep(2 ** attempt)

    def handle(self, a: Any) -> None:
        v = dict(a.vars or {})
        log.info("claimed run=%s step=%s attempt=%s", a.run, a.step, a.attempt)
        env_id = v.get("environment", "")
        instructions = v.get("instructions", "")
        if not env_id or not instructions.strip():
            self._complete(a, "fail", {}, "assignment needs vars environment and instructions")
            return
        try:
            model_cfg, limits = apply_model_override(self.model_cfg, self.limits, v.get("model"))
        except (ValueError, TypeError) as e:
            self._complete(a, "fail", {}, f"invalid model var: {e}")
            return
        task = Task(instructions=instructions, workdir=v.get("workdir") or "/work/repo",
                    check_command=v.get("check_command") or self.check_command or None,
                    check_output=v.get("check_output") or None, title=v.get("title") or None,
                    review_comment=v.get("review.comment") or None)
        with Heartbeat(self.wf, a, self.lease_ms) as hb:
            self._progress(a, f"agent started (attempt {a.attempt}, model {model_cfg.name})")
            out = run_task(
                task, execution=self.execution, environment=env_id, model_cfg=model_cfg,
                limits=limits, artifacts=self.artifacts,
                labels={"run": a.run, "step": a.step, "attempt": str(a.attempt)},
                model=self.model, steering=self.steering_for(a),
                should_stop=lambda: hb.lost or ("worker stopping" if self.stopping.is_set() else None),
                on_progress=lambda note: self._progress(a, note),
                extra_secrets=self.extra_secrets())
            r = out.result
            if hb.lost:
                log.warning("claim lost for run=%s; not completing", a.run)
                return
            outputs = {"agent.summary": r.summary, "agent.steps": str(r.steps),
                       "agent.status": r.status, "agent.tool_calls": str(r.tool_calls),
                       "agent.transcript": out.transcript_digest or ""}
            if r.check_passed is not None:
                outputs["agent.check_passed"] = json.dumps(r.check_passed)
            if out.upload_error:
                outputs["agent.transcript_error"] = out.upload_error[:500]
            self._progress(a, f"agent {r.status} after {r.steps} steps, {r.tool_calls} tool calls: "
                              f"{r.summary}", ref=out.transcript_digest)
        if r.status == "error":
            self._complete(a, "fail", outputs, r.error or r.summary)
        elif r.status == "stopped":  # worker shutdown: let another worker retry
            self._complete(a, "retry", outputs, r.summary)
        else:
            self._complete(a, "done", outputs)
