"""JSONL evidence of one agent task, with secret redaction."""
from __future__ import annotations

import json
import os
import re
import threading
import time
from typing import Any, Iterable, Mapping

SECRET_NAME = re.compile(r"TOKEN|KEY|SECRET|PASSWORD|PASSWD|CREDENTIAL", re.I)
MIN_SECRET_LEN = 6
REDACTED = "[REDACTED]"


class Redactor:
    """Replaces known secret values in text."""

    def __init__(self, secrets: Iterable[str] = ()):
        self._secrets: set[str] = set()
        self.add(*secrets)

    def add(self, *values: str | None) -> None:
        for v in values:
            if v and len(v.strip()) >= MIN_SECRET_LEN:
                self._secrets.add(v.strip())

    def add_env(self, env: Mapping[str, str]) -> None:
        self.add(*(v for k, v in env.items() if SECRET_NAME.search(k)))

    def text(self, s: str) -> str:
        # Longest first, so a secret that contains another is replaced whole.
        for v in sorted(self._secrets, key=len, reverse=True):
            if v in s:
                s = s.replace(v, REDACTED)
        return s

    def value(self, v: Any) -> Any:
        if isinstance(v, str):
            return self.text(v)
        if isinstance(v, bytes):
            return self.text(v.decode("utf-8", "replace"))
        if isinstance(v, dict):
            return {k: (REDACTED if isinstance(k, str) and SECRET_NAME.search(k)
                        and isinstance(x, str) and len(x) >= MIN_SECRET_LEN
                        else self.value(x)) for k, x in v.items()}
        if isinstance(v, (list, tuple)):
            return [self.value(x) for x in v]
        return v


def default_redactor(*extra: str | None) -> Redactor:
    r = Redactor()
    r.add_env(os.environ)
    r.add(*extra)
    return r


class Transcript:
    def __init__(self, redactor: Redactor, path: str | None = None):
        self.redactor = redactor
        self.records: list[str] = []
        self._lock = threading.Lock()
        self._file = open(path, "a", encoding="utf-8") if path else None

    def record(self, kind: str, **data: Any) -> None:
        rec = {"at_ms": int(time.time() * 1000), "kind": kind, **data}
        line = json.dumps(self.redactor.value(rec), ensure_ascii=False, default=str)
        with self._lock:
            self.records.append(line)
            if self._file:
                self._file.write(line + "\n")
                self._file.flush()

    def body(self) -> bytes:
        with self._lock:
            return ("\n".join(self.records) + "\n").encode() if self.records else b""

    def close(self) -> None:
        if self._file:
            self._file.close()
            self._file = None
