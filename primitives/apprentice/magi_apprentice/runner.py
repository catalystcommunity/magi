"""Run one task end to end: apprentice, transcript, upload."""
from __future__ import annotations

import json
import logging
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, replace
from typing import Any, Callable

from ._clients import artifacts as art
from .apprentice import Apprentice, Result, Steering, Task, model_wait_recorder
from .config import Limits, ModelConfig
from .model import ChatModel, Model
from .prereview import pre_review
from .spec import acceptance_checks, has_checks, with_checks
from . import tester
from .supervisor import Supervisor
from .tools import Tools, Workspace
from .transcript import Transcript, default_redactor


log = logging.getLogger("magi_apprentice.runner")


@dataclass
class Outcome:
    result: Result
    transcript_digest: str | None
    transcript: Transcript
    upload_error: str | None = None


# The part of the context window of the engine that the conversation can
# use before a compaction. The rest is room for the answer and for the
# error of the token estimate. 60000 of 65536 tokens was the measured value.
CONTEXT_SHARE = 0.915


def fetch_limits(cfg: ModelConfig, opener: Callable[..., Any] = urllib.request.urlopen) -> dict:
    """The limits that the model endpoint tells for the model name of cfg:
    GET <base_url>/models/<name>. Magi answers from the model catalog with
    `context` (the tokens of the context window of the engine) and
    `work_seconds` (the time limit of one work round). An endpoint that does
    not answer, or that answers something else, gives no limits."""
    if not cfg.base_url:
        return {}
    url = cfg.base_url.rstrip("/") + "/models/" + urllib.parse.quote(cfg.name, safe="")
    # The agent of the model calls (model.py): a gateway can refuse the
    # default agent of urllib, and the run then keeps limits that do not
    # fit its model, with no error.
    headers = {"User-Agent": "magi-apprentice/1"}
    if cfg.api_key:
        headers["Authorization"] = f"Bearer {cfg.api_key}"
    try:
        with opener(urllib.request.Request(url, headers=headers), timeout=15) as resp:
            d = json.loads(resp.read(65536))
    except urllib.error.HTTPError as e:
        e.close()
        if e.code != 404:
            # 404: an endpoint that has no such request (a plain provider).
            log.warning("no limits for model %s: the endpoint answered %d", cfg.name, e.code)
        return {}
    except (OSError, ValueError) as e:
        log.warning("no limits for model %s: %s", cfg.name, e)
        return {}
    return d if isinstance(d, dict) else {}


def _positive(v: Any) -> int:
    return v if isinstance(v, int) and not isinstance(v, bool) and v > 0 else 0


def apply_model_override(cfg: ModelConfig, limits: Limits, value: str | None,
                         endpoint_limits: Callable[[ModelConfig], dict] | None = None) -> tuple[ModelConfig, Limits]:
    """Apply a `model` assignment var. It is a model name, or JSON with name,
    temperature, max_tokens, max_steps, max_seconds, context_tokens. The base
    URL and key cannot change here, so task data cannot send the key to
    another host.

    endpoint_limits gives the limits of the model from the endpoint
    (fetch_limits). They set the context and the time of the run when the
    var does not: the limits of a run fit the model that does the run."""
    value = (value or "").strip()
    d: dict = {}
    if value.startswith("{"):
        d = json.loads(value)
    elif value:
        d = {"name": value}
    if "name" in d:
        cfg = replace(cfg, name=str(d["name"]))
    if "temperature" in d:
        cfg = replace(cfg, temperature=float(d["temperature"]))
    if "max_tokens" in d:
        cfg = replace(cfg, max_tokens=int(d["max_tokens"]))
    if "max_steps" in d:
        limits = replace(limits, max_steps=int(d["max_steps"]))
    if "max_seconds" in d:
        limits = replace(limits, max_seconds=int(d["max_seconds"]))
    if "context_tokens" in d:
        limits = replace(limits, context_tokens=int(d["context_tokens"]))
    told = endpoint_limits(cfg) if endpoint_limits else {}
    if "context_tokens" not in d and _positive(told.get("context")):
        limits = replace(limits, context_tokens=int(told["context"] * CONTEXT_SHARE))
    if "max_seconds" not in d and _positive(told.get("work_seconds")):
        limits = replace(limits, max_seconds=told["work_seconds"])
    return cfg, limits


WORK_REVIEW_SAMPLES = 2


def task_text(t: Task) -> str:
    """The task as the reviewer sees it: the title, the instructions, and the
    comment of the earlier round, as the check step of the runner gives it."""
    text = "\n\n".join(x for x in (t.title, t.instructions) if x)
    if t.review_comment:
        text += "\n\nThe review comment of the earlier round (each item must be done):\n" + t.review_comment
    return text


class _Noting:
    """A model whose calls write a status line when the run was quiet: one
    review is many model calls, and no step of the apprentice."""

    def __init__(self, model: Model):
        self.model = model
        self.alive: Callable[[str], None] | None = None

    def complete(self, messages: list[dict], tools: list[dict]) -> Any:
        if self.alive:
            self.alive("a model call of the review")
        return self.model.complete(messages, tools)

    def __getattr__(self, name: str) -> Any:
        return getattr(self.model, name)


def run_task(task: Task, *, execution: Any, environment: str, model_cfg: ModelConfig,
             limits: Limits, artifacts: Any | None = None, labels: dict[str, str] | None = None,
             model: Model | None = None, steering: Steering | None = None,
             should_stop: Callable[[], str | None] | None = None,
             on_progress: Callable[[str], None] | None = None,
             transcript_path: str | None = None,
             extra_secrets: tuple[str | None, ...] = (),
             supervisor_model: str | None = None,
             review_model: str | None = None) -> Outcome:
    redactor = default_redactor(model_cfg.api_key, *extra_secrets)
    tr = Transcript(redactor, transcript_path)
    tr.record("config", model=model_cfg.name, base_url=model_cfg.base_url,
              max_tokens=model_cfg.max_tokens, temperature=model_cfg.temperature,
              environment=environment, supervisor_model=supervisor_model, review_model=review_model)
    # The supervisor uses the route and the key of the worker, with its own
    # model name: another engine, so that its calls keep the worker's cache.
    supervisor = (Supervisor(ChatModel(replace(model_cfg, name=supervisor_model, max_tokens=4096, temperature=0.2)),
                             limits.max_steps) if supervisor_model else None)
    reviewer = None
    if review_model:
        # The lens answers are long: each lens writes out what it checked.
        rm = _Noting(ChatModel(replace(model_cfg, name=review_model, max_tokens=max(model_cfg.max_tokens, 12000),
                                       temperature=0.6)))

        def reviewer(tools: Tools, t: Task) -> dict:
            # The tester first: its findings come from a run, so they go
            # before the items of the review, which come from a reading.
            found = []
            if limits.tester:
                try:
                    found = tester.run(tester_model, tools, task_text(t), limits, tr)
                except Exception as e:  # the review works without the tester
                    tr.record("tester", error=f"{type(e).__name__}: {e}")
            # Two samples: the blockers of one review differ from run to run.
            rv = pre_review(rm, tools, task_text(t), "HEAD", t.lessons or "", samples=WORK_REVIEW_SAMPLES)
            if found:
                rv = {**rv, "verdict": "revise", "blockers": found + list(rv.get("blockers") or [])}
            return rv
        # The spec step uses the review model: it checks the task, as the
        # review checks the change. Checks that the task has already stay.
        if limits.spec_step and not has_checks(task.instructions):
            if on_progress:
                on_progress("working: the spec step")
            checks, rec = acceptance_checks(
                ChatModel(replace(model_cfg, name=review_model, max_tokens=12000, temperature=0.3)),
                task.instructions)
            task.instructions = with_checks(task.instructions, checks)
            tr.record("spec_step", model=review_model, **rec)
    worker_model = model or ChatModel(model_cfg, on_wait=model_wait_recorder(tr))
    # The tester is the worker model with an empty context.
    tester_model = _Noting(worker_model) if review_model else None
    apprentice = Apprentice(worker_model,
                  Tools(Workspace(execution, environment, task.workdir, limits)),
                  limits, tr, steering=steering, should_stop=should_stop, on_progress=on_progress,
                  supervisor=supervisor, reviewer=reviewer)
    if review_model:
        rm.alive = tester_model.alive = apprentice.alive
    try:
        result = apprentice.run(task)
    except Exception as e:  # keep the evidence of a crashed task
        tr.record("crash", error=f"{type(e).__name__}: {e}")
        result = Result(status="error", summary="The apprentice crashed.", error=f"{type(e).__name__}: {e}")
    digest, upload_error = None, None
    if artifacts is not None:
        try:
            a = artifacts.put(art.PutRequest(media_type="application/x-ndjson", body=tr.body(),
                                             labels={"kind": "apprentice-transcript", **(labels or {})}))
            digest = a.digest
        except Exception as e:
            upload_error = f"{type(e).__name__}: {e}"
    tr.close()
    return Outcome(result, digest, tr, upload_error)
