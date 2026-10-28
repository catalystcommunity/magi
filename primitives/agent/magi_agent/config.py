"""Configuration from MAGI_* environment variables."""
from __future__ import annotations

import os
from dataclasses import dataclass, field, replace
from pathlib import Path


def _env(name: str, default: str = "") -> str:
    return os.environ.get(name, default)


def _int(name: str, default: int) -> int:
    v = os.environ.get(name)
    return int(v) if v else default


def _float(name: str, default: float | None) -> float | None:
    v = os.environ.get(name)
    return float(v) if v else default


@dataclass
class ModelConfig:
    base_url: str = "http://127.0.0.1:8080/v1"
    name: str = "magi-coder"
    api_key: str | None = field(default=None, repr=False)
    timeout_s: float = 300.0
    max_retries: int = 5
    max_tokens: int = 4096
    temperature: float | None = 0.2
    # Extra fields merged into each request body (for example top_p).
    extra: dict = field(default_factory=dict)

    @classmethod
    def from_env(cls) -> "ModelConfig":
        key = None
        key_file = _env("MAGI_MODEL_API_KEY_FILE")
        if key_file:
            key = Path(key_file).read_text().strip() or None
        elif _env("MAGI_MODEL_API_KEY"):
            key = _env("MAGI_MODEL_API_KEY").strip()
        return cls(
            base_url=_env("MAGI_MODEL_BASE_URL", cls.base_url),
            name=_env("MAGI_MODEL_NAME", cls.name),
            api_key=key,
            timeout_s=float(_env("MAGI_MODEL_TIMEOUT", "300")),
            max_retries=_int("MAGI_MODEL_MAX_RETRIES", 5),
            max_tokens=_int("MAGI_MODEL_MAX_TOKENS", 4096),
            temperature=_float("MAGI_MODEL_TEMPERATURE", 0.2),
        )


@dataclass
class Limits:
    max_steps: int = 60               # model calls per task
    max_seconds: int = 1800           # wall clock per task
    context_tokens: int = 24000       # compaction threshold (estimate)
    tool_output_chars: int = 8000     # per tool result in context
    exec_output_bytes: int = 1048576  # cap requested from the environment
    bash_timeout_s: int = 300         # default bash timeout
    bash_timeout_max_s: int = 1200
    progress_every: int = 5           # progress note every N tool calls
    check_retries: int = 3            # finish attempts rejected by a failed check

    @classmethod
    def from_env(cls) -> "Limits":
        d = cls()
        return replace(
            d,
            max_steps=_int("MAGI_AGENT_MAX_STEPS", d.max_steps),
            max_seconds=_int("MAGI_AGENT_MAX_SECONDS", d.max_seconds),
            context_tokens=_int("MAGI_AGENT_CONTEXT_TOKENS", d.context_tokens),
            tool_output_chars=_int("MAGI_AGENT_TOOL_OUTPUT_CHARS", d.tool_output_chars),
            bash_timeout_s=_int("MAGI_AGENT_BASH_TIMEOUT", d.bash_timeout_s),
            progress_every=_int("MAGI_AGENT_PROGRESS_EVERY", d.progress_every),
            check_retries=_int("MAGI_AGENT_CHECK_RETRIES", d.check_retries),
        )


@dataclass
class ServiceConfig:
    workflow_url: str = ""
    execution_url: str = ""
    artifacts_url: str = ""
    token_file: str | None = None
    queue: str = "agent"
    worker_id: str = ""

    @classmethod
    def from_env(cls) -> "ServiceConfig":
        return cls(
            workflow_url=_env("MAGI_WORKFLOW_URL"),
            execution_url=_env("MAGI_EXECUTION_URL"),
            artifacts_url=_env("MAGI_ARTIFACTS_URL"),
            token_file=_env("MAGI_TOKEN_FILE") or None,
            queue=_env("MAGI_AGENT_QUEUE", "agent"),
            worker_id=_env("MAGI_AGENT_WORKER_ID") or f"agent-{os.uname().nodename}-{os.getpid()}",
        )
