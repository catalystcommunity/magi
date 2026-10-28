"""Test doubles: a scripted model, an in-memory execution client, and a
transport-level workflow and artifact service (so the generated CBOR
encoding is exercised)."""
from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from magi_agent._clients import RpcError, artifacts as art, execution as ex, workflow as wf  # noqa: E402
from magi_agent.local import LocalExecution  # noqa: E402
from magi_agent.model import Reply, ToolCall  # noqa: E402


def call(name: str, **args: Any) -> ToolCall:
    return ToolCall(id=f"c_{name}_{len(json.dumps(args))}", name=name, arguments=json.dumps(args))


def reply(*calls: ToolCall, content: str = "", finish_reason: str = "tool_calls") -> Reply:
    return Reply(content=content, tool_calls=list(calls), finish_reason=finish_reason,
                 usage={"prompt_tokens": 100, "completion_tokens": 10})


class FakeModel:
    """Returns scripted replies. A script item can be a Reply or a function
    of the messages that returns a Reply."""

    def __init__(self, script: list[Reply | Callable[[list[dict]], Reply]]):
        self.script = list(script)
        self.requests: list[list[dict]] = []

    def complete(self, messages: list[dict], tools: list[dict]) -> Reply:
        self.requests.append(json.loads(json.dumps(messages)))
        if not self.script:
            return reply(content="nothing more")
        item = self.script.pop(0)
        return item(messages) if callable(item) else item


class MemExecution:
    """Files in a dict. exec runs real bash in a temp dir only when given."""

    def __init__(self, files: dict[str, str] | None = None, file_ops: bool = True,
                 local: LocalExecution | None = None):
        self.files = {k: v.encode() for k, v in (files or {}).items()}
        self.file_ops = file_ops
        self.local = local
        self.execs: list[ex.ExecRequest] = []

    def exec(self, req: ex.ExecRequest) -> ex.ExecResult:
        self.execs.append(req)
        if self.local:
            return self.local.exec(req)
        script = req.argv[-1]
        if not self.file_ops and script.startswith("cat -- "):
            path = script[len("cat -- "):].strip("'")
            if path not in self.files:
                return _res(1, b"", b"no such file")
            return _res(0, self.files[path], b"")
        if not self.file_ops and " && cat > " in script:
            self.files[script.split(" && cat > ")[1].strip("'")] = req.stdin or b""
        return _res(0, b"", b"")

    def _ops(self) -> None:
        if not self.file_ops:
            raise RpcError(404, "unknown operation ExecutionService/read-file")

    def read_file(self, req: ex.ReadFileRequest) -> ex.FileContent:
        self._ops()
        if req.path not in self.files:
            raise RpcError(404, f"no such file {req.path}")
        b = self.files[req.path]
        return ex.FileContent(path=req.path, body=b, size=len(b), truncated=False)

    def write_file(self, req: ex.WriteFileRequest) -> ex.WriteFileResponse:
        self._ops()
        self.files[req.path] = req.body
        return ex.WriteFileResponse(path=req.path, size=len(req.body))


def _res(code: int, out: bytes, err: bytes) -> ex.ExecResult:
    return ex.ExecResult(exit_code=code, stdout=out, stderr=err, truncated=False,
                         timed_out=False, duration_ms=1)


class FakeServices:
    """Transport for WorkflowClient and ArtifactClient. Decodes requests with
    the generated types, as a server would."""

    def __init__(self, assignments: list[wf.Assignment]):
        self.pending = list(assignments)
        self.calls: list[tuple[str, Any]] = []
        self.artifacts: dict[str, tuple[art.PutRequest, str]] = {}
        self.lost = False

    def call(self, service: str, method: str, req: bytes) -> bytes:
        if service == "WorkflowService":
            return self._wf(method, req)
        if service == "ArtifactService" and method == "put":
            p = art.PutRequest.from_cbor(req)
            d = "sha256:" + hashlib.sha256(p.body).hexdigest()
            self.artifacts[d] = (p, d)
            self.calls.append(("put", p))
            return art.Artifact(digest=d, media_type=p.media_type, size=len(p.body), owner="service:agent",
                                labels=p.labels or {}, created_ms=1).to_cbor()
        raise RpcError(404, f"unknown operation {service}/{method}")

    def _wf(self, method: str, req: bytes) -> bytes:
        types = {"claim": wf.ClaimRequest, "heartbeat": wf.HeartbeatRequest,
                 "progress": wf.ProgressRequest, "complete": wf.CompleteRequest}
        r = types[method].from_cbor(req)
        self.calls.append((method, r))
        if method == "claim":
            return wf.ClaimResponse(assignment=self.pending.pop(0) if self.pending else None).to_cbor()
        if method == "heartbeat":
            if self.lost:
                raise RpcError(409, "claim expired")
            return wf.Ack(ok=True).to_cbor()
        if method == "progress":
            return wf.Ack(ok=True).to_cbor()
        return wf.Run(id=r.run, definition="d", revision=1, owner="o", status="running",
                      current_step="", vars={}, history=[], created_ms=1, updated_ms=2).to_cbor()

    def of(self, method: str) -> list[Any]:
        return [r for m, r in self.calls if m == method]
