"""An in-process stand-in for ExecutionService, backed by local subprocesses.

For tests and for one-shot runs without the execution service. It has no
isolation: commands run as the current user on this host.
"""
from __future__ import annotations

import os
import re
import signal
import subprocess
import time
from pathlib import Path

from ._clients import RpcError, execution as ex
from .transcript import SECRET_NAME


# git reads GIT_CONFIG_COUNT pairs of GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n.
# The secret rule removes the KEY names, and git stops with an error when the
# count stays without them. So the three go together.
GIT_CONFIG_PAIRS = re.compile(r"^GIT_CONFIG_(COUNT$|KEY_|VALUE_)")


def scrubbed_env() -> dict[str, str]:
    return {k: v for k, v in os.environ.items()
            if not SECRET_NAME.search(k) and not GIT_CONFIG_PAIRS.search(k)}


class LocalExecution:
    def __init__(self, env: dict[str, str] | None = None):
        self.env = scrubbed_env() if env is None else env

    def exec(self, req: ex.ExecRequest) -> ex.ExecResult:
        cap = req.max_output_bytes or 1 << 20
        t0 = time.monotonic()
        p = subprocess.Popen(req.argv, cwd=req.cwd or None, env={**self.env, **(req.env or {})},
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             start_new_session=True)
        timed_out = False
        try:
            out, err = p.communicate(req.stdin or b"", timeout=(req.timeout_ms or 600000) / 1000)
        except subprocess.TimeoutExpired:
            timed_out = True
            os.killpg(p.pid, signal.SIGKILL)
            out, err = p.communicate()
        truncated = len(out) > cap or len(err) > cap
        return ex.ExecResult(exit_code=p.returncode if not timed_out else -1,
                             stdout=out[:cap], stderr=err[:cap], truncated=truncated,
                             timed_out=timed_out, duration_ms=int((time.monotonic() - t0) * 1000))

    def read_file(self, req: ex.ReadFileRequest) -> ex.FileContent:
        p = Path(req.path)
        try:
            data = p.read_bytes()
        except FileNotFoundError:
            raise RpcError(404, f"no such file: {req.path}") from None
        except OSError as e:
            raise RpcError(400, str(e)) from None
        cap = req.max_bytes or len(data)
        return ex.FileContent(path=req.path, body=data[:cap], size=len(data), truncated=len(data) > cap)

    def write_file(self, req: ex.WriteFileRequest) -> ex.WriteFileResponse:
        p = Path(req.path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(req.body)
        if req.mode:
            p.chmod(req.mode)
        return ex.WriteFileResponse(path=req.path, size=len(req.body))
