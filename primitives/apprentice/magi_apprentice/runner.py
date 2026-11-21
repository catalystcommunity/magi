"""Run one task end to end: apprentice, transcript, upload."""
from __future__ import annotations

import json
from dataclasses import dataclass, replace
from typing import Any, Callable

from ._clients import artifacts as art
from .apprentice import Apprentice, Result, Steering, Task, model_wait_recorder
from .config import Limits, ModelConfig
from .model import ChatModel, Model
from .prereview import pre_review
from .spec import acceptance_checks, has_checks, with_checks
from .supervisor import Supervisor
from .tools import Tools, Workspace
from .transcript import Transcript, default_redactor


@dataclass
class Outcome:
    result: Result
    transcript_digest: str | None
    transcript: Transcript
    upload_error: str | None = None


def apply_model_override(cfg: ModelConfig, limits: Limits, value: str | None) -> tuple[ModelConfig, Limits]:
    """Apply a `model` assignment var. It is a model name, or JSON with name,
    temperature, max_tokens, max_steps, context_tokens. The base URL and key
    cannot change here, so task data cannot send the key to another host."""
    if not value:
        return cfg, limits
    value = value.strip()
    if not value.startswith("{"):
        return replace(cfg, name=value), limits
    d = json.loads(value)
    if "name" in d:
        cfg = replace(cfg, name=str(d["name"]))
    if "temperature" in d:
        cfg = replace(cfg, temperature=float(d["temperature"]))
    if "max_tokens" in d:
        cfg = replace(cfg, max_tokens=int(d["max_tokens"]))
    if "max_steps" in d:
        limits = replace(limits, max_steps=int(d["max_steps"]))
    if "context_tokens" in d:
        limits = replace(limits, context_tokens=int(d["context_tokens"]))
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
            # Two samples: the blockers of one review differ from run to run.
            return pre_review(rm, tools, task_text(t), "HEAD", t.lessons or "", samples=WORK_REVIEW_SAMPLES)
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
    apprentice = Apprentice(model or ChatModel(model_cfg, on_wait=model_wait_recorder(tr)),
                  Tools(Workspace(execution, environment, task.workdir, limits)),
                  limits, tr, steering=steering, should_stop=should_stop, on_progress=on_progress,
                  supervisor=supervisor, reviewer=reviewer)
    if review_model:
        rm.alive = apprentice.alive
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
