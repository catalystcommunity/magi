"""Run one task end to end: agent, transcript, upload."""
from __future__ import annotations

import json
from dataclasses import dataclass, replace
from typing import Any, Callable

from ._clients import artifacts as art
from .agent import Agent, Result, Steering, Task
from .config import Limits, ModelConfig
from .model import ChatModel, Model
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
    temperature, max_tokens, max_steps. The base URL and key cannot change
    here, so task data cannot send the key to another host."""
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
    return cfg, limits


def run_task(task: Task, *, execution: Any, environment: str, model_cfg: ModelConfig,
             limits: Limits, artifacts: Any | None = None, labels: dict[str, str] | None = None,
             model: Model | None = None, steering: Steering | None = None,
             should_stop: Callable[[], str | None] | None = None,
             on_progress: Callable[[str], None] | None = None,
             transcript_path: str | None = None,
             extra_secrets: tuple[str | None, ...] = ()) -> Outcome:
    redactor = default_redactor(model_cfg.api_key, *extra_secrets)
    tr = Transcript(redactor, transcript_path)
    tr.record("config", model=model_cfg.name, base_url=model_cfg.base_url,
              max_tokens=model_cfg.max_tokens, temperature=model_cfg.temperature,
              environment=environment)
    agent = Agent(model or ChatModel(model_cfg), Tools(Workspace(execution, environment, task.workdir, limits)),
                  limits, tr, steering=steering, should_stop=should_stop, on_progress=on_progress)
    try:
        result = agent.run(task)
    except Exception as e:  # keep the evidence of a crashed task
        tr.record("crash", error=f"{type(e).__name__}: {e}")
        result = Result(status="error", summary="The agent crashed.", error=f"{type(e).__name__}: {e}")
    digest, upload_error = None, None
    if artifacts is not None:
        try:
            a = artifacts.put(art.PutRequest(media_type="application/x-ndjson", body=tr.body(),
                                             labels={"kind": "agent-transcript", **(labels or {})}))
            digest = a.digest
        except Exception as e:
            upload_error = f"{type(e).__name__}: {e}"
    tr.close()
    return Outcome(result, digest, tr, upload_error)
