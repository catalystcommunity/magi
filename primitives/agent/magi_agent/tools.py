"""Agent tools. Every effect goes through an ExecutionService client."""
from __future__ import annotations

import json
import posixpath
import shlex
from dataclasses import dataclass
from typing import Any, Callable

from ._clients import RpcError, execution as ex
from .config import Limits

READ_MAX_BYTES = 4 * 1024 * 1024
READ_DEFAULT_LINES = 400
LIST_LIMIT = 400
SEARCH_LIMIT = 200


def truncate(text: str, limit: int) -> str:
    """Keep the head and the tail; errors are usually at the end."""
    if len(text) <= limit:
        return text
    head = limit * 2 // 5
    tail = limit - head
    cut = len(text) - head - tail
    return f"{text[:head]}\n... [{cut} chars cut] ...\n{text[-tail:]}"


class ToolError(Exception):
    """A failure the model can see and react to."""


@dataclass
class ToolResult:
    content: str
    ok: bool = True
    finished: bool = False


class Workspace:
    """Tool operations on one execution environment.

    client is an ExecutionClient or any object with the same exec,
    read_file, and write_file methods.
    """

    def __init__(self, client: Any, environment: str, workdir: str, limits: Limits):
        self.client = client
        self.environment = environment
        self.workdir = workdir.rstrip("/") or "/"
        self.limits = limits
        self._file_ops = True  # falls back to exec if the service lacks file ops

    def path(self, p: str | None) -> str:
        p = (p or ".").strip()
        return posixpath.normpath(p if p.startswith("/") else posixpath.join(self.workdir, p))

    def exec(self, argv: list[str], timeout_s: int, stdin: bytes | None = None) -> Any:
        return self.client.exec(ex.ExecRequest(
            environment=self.environment, argv=argv, cwd=self.workdir, stdin=stdin,
            timeout_ms=timeout_s * 1000, max_output_bytes=self.limits.exec_output_bytes))

    def sh(self, script: str, timeout_s: int = 60, stdin: bytes | None = None) -> Any:
        return self.exec(["bash", "-lc", script], timeout_s, stdin)

    def read(self, path: str) -> str:
        if self._file_ops:
            try:
                fc = self.client.read_file(ex.ReadFileRequest(
                    environment=self.environment, path=path, max_bytes=READ_MAX_BYTES))
                if fc.truncated:
                    raise ToolError(f"{path} is larger than {READ_MAX_BYTES} bytes")
                return fc.body.decode("utf-8", "replace")
            except RpcError as e:
                if not _unknown_op(e):
                    raise ToolError(f"cannot read {path}: {e.message}") from None
                self._file_ops = False
        r = self.sh(f"cat -- {shlex.quote(path)}")
        if r.exit_code != 0:
            raise ToolError(f"cannot read {path}: {_text(r.stderr).strip()}")
        return _text(r.stdout)

    def write(self, path: str, body: str) -> int:
        data = body.encode()
        parent = posixpath.dirname(path)
        if self._file_ops:
            try:
                self.sh(f"mkdir -p -- {shlex.quote(parent)}")
                self.client.write_file(ex.WriteFileRequest(
                    environment=self.environment, path=path, body=data))
                return len(data)
            except RpcError as e:
                if not _unknown_op(e):
                    raise ToolError(f"cannot write {path}: {e.message}") from None
                self._file_ops = False
        q = shlex.quote(path)
        r = self.sh(f"mkdir -p -- {shlex.quote(parent)} && cat > {q}", stdin=data)
        if r.exit_code != 0:
            raise ToolError(f"cannot write {path}: {_text(r.stderr).strip()}")
        return len(data)


def _unknown_op(e: RpcError) -> bool:
    return e.code == 404 and "unknown operation" in (e.message or "")


def _text(b: bytes | str | None) -> str:
    if b is None:
        return ""
    return b if isinstance(b, str) else b.decode("utf-8", "replace")


def _fn(name: str, desc: str, props: dict, required: list[str]) -> dict:
    return {"type": "function", "function": {
        "name": name, "description": desc,
        "parameters": {"type": "object", "properties": props, "required": required}}}


S = {"type": "string"}
I = {"type": "integer"}
B = {"type": "boolean"}

TOOL_SPECS: list[dict] = [
    _fn("bash", "Run a bash command in the work directory. Returns exit code, stdout, and stderr "
        "(long output is cut in the middle). Use for builds, tests, git status/diff, and small "
        "shell tasks. Do not start servers that do not exit.",
        {"command": S, "timeout_seconds": {**I, "description": "default 300"}}, ["command"]),
    _fn("read_file", "Read a text file with line numbers. Use offset (1-based line) and limit "
        "to read part of a large file.",
        {"path": S, "offset": I, "limit": I}, ["path"]),
    _fn("write_file", "Create or replace a whole file. Parent directories are created. "
        "Prefer edit_file to change part of an existing file.",
        {"path": S, "content": S}, ["path", "content"]),
    _fn("edit_file", "Replace an exact string in a file. old_string must match the file "
        "exactly (whitespace too, without line-number prefixes) and must be unique unless "
        "replace_all is true. Include enough context lines to make it unique.",
        {"path": S, "old_string": S, "new_string": S, "replace_all": B},
        ["path", "old_string", "new_string"]),
    _fn("list_files", "List files under a directory, recursively, skipping .git. "
        "pattern is an optional glob such as '*.go' or 'src/**/*.py'.",
        {"path": S, "pattern": S}, []),
    _fn("search", "Search file contents with a regular expression (ripgrep). Returns "
        "path:line:text matches. glob limits the files, such as '*.go'.",
        {"pattern": S, "path": S, "glob": S, "ignore_case": B}, ["pattern"]),
    _fn("finish", "Stop work and report. Call this once the task is complete (and checks "
        "pass), or when you cannot continue. summary: what you changed, how you verified it, "
        "and anything left undone.",
        {"summary": S}, ["summary"]),
]


class Tools:
    def __init__(self, ws: Workspace):
        self.ws = ws
        self._table: dict[str, Callable[..., ToolResult]] = {
            "bash": self.bash, "read_file": self.read_file, "write_file": self.write_file,
            "edit_file": self.edit_file, "list_files": self.list_files, "search": self.search,
            "finish": self.finish}

    def call(self, name: str, arguments: str) -> ToolResult:
        fn = self._table.get(name)
        if fn is None:
            return ToolResult(f"error: unknown tool {name!r}. Tools: {', '.join(self._table)}", ok=False)
        try:
            args = json.loads(arguments or "{}")
            if not isinstance(args, dict):
                raise ValueError("arguments must be a JSON object")
        except ValueError as e:
            return ToolResult(f"error: invalid JSON arguments: {e}", ok=False)
        try:
            return fn(**args)
        except TypeError as e:
            return ToolResult(f"error: bad arguments for {name}: {e}", ok=False)
        except ToolError as e:
            return ToolResult(f"error: {e}", ok=False)
        except RpcError as e:
            return ToolResult(f"error: execution service: {e.message}", ok=False)

    # -- tools ---------------------------------------------------------

    def bash(self, command: str, timeout_seconds: int | None = None) -> ToolResult:
        lim = self.ws.limits
        t = max(1, min(int(timeout_seconds or lim.bash_timeout_s), lim.bash_timeout_max_s))
        r = self.ws.sh(command, timeout_s=t)
        return ToolResult(format_exec(r, lim.tool_output_chars), ok=r.exit_code == 0)

    def read_file(self, path: str, offset: int | None = None, limit: int | None = None) -> ToolResult:
        p = self.ws.path(path)
        lines = self.ws.read(p).splitlines()
        start = max(1, int(offset or 1))
        n = max(1, int(limit or READ_DEFAULT_LINES))
        part = lines[start - 1:start - 1 + n]
        if not lines:
            return ToolResult(f"{p} is empty")
        out = "\n".join(f"{i:6}\t{line}" for i, line in enumerate(part, start))
        end = start + len(part) - 1
        if start > 1 or end < len(lines):
            out += f"\n[lines {start}-{end} of {len(lines)}; use offset/limit for more]"
        return ToolResult(out)

    def write_file(self, path: str, content: str) -> ToolResult:
        p = self.ws.path(path)
        n = self.ws.write(p, content)
        return ToolResult(f"wrote {p} ({n} bytes, {len(content.splitlines())} lines)")

    def edit_file(self, path: str, old_string: str, new_string: str,
                  replace_all: bool = False) -> ToolResult:
        p = self.ws.path(path)
        if old_string == new_string:
            raise ToolError("old_string and new_string are the same")
        if old_string == "":
            raise ToolError("old_string is empty; use write_file to create a file")
        text = self.ws.read(p)
        count = text.count(old_string)
        if count == 0:
            hint = ""
            if old_string.strip() and old_string.strip() in text:
                hint = " (it matches after stripping whitespace; check indentation)"
            raise ToolError(f"old_string not found in {p}{hint}. Read the file and copy the text exactly.")
        if count > 1 and not replace_all:
            raise ToolError(f"old_string occurs {count} times in {p}. Add context to make it "
                            "unique, or set replace_all.")
        new = text.replace(old_string, new_string) if replace_all else text.replace(old_string, new_string, 1)
        self.ws.write(p, new)
        return ToolResult(f"edited {p}: {count if replace_all else 1} replacement(s)")

    def list_files(self, path: str | None = None, pattern: str | None = None) -> ToolResult:
        p = self.ws.path(path)
        q = shlex.quote(p)
        if pattern:
            g = shlex.quote(pattern)
            script = (f"cd {q} && if command -v rg >/dev/null; then rg --files --hidden -g '!.git' -g {g}; "
                      f"else find . -path ./.git -prune -o -type f -path {shlex.quote('./' + pattern.replace('**/', '*'))} -print "
                      f"-o -type f -name {g} -print | sed 's|^\\./||'; fi | sort | head -n {LIST_LIMIT + 1}")
        else:
            script = (f"cd {q} && if command -v rg >/dev/null; then rg --files --hidden -g '!.git'; "
                      f"else find . -path ./.git -prune -o -type f -print | sed 's|^\\./||'; fi "
                      f"| sort | head -n {LIST_LIMIT + 1}")
        r = self.ws.sh(script)
        if r.exit_code not in (0, 1):
            raise ToolError(_text(r.stderr).strip() or f"list failed with exit {r.exit_code}")
        files = [f for f in _text(r.stdout).splitlines() if f]
        more = len(files) > LIST_LIMIT
        out = "\n".join(files[:LIST_LIMIT]) or "(no files)"
        if more:
            out += f"\n[more than {LIST_LIMIT} files; narrow path or pattern]"
        return ToolResult(f"{p}:\n{truncate(out, self.ws.limits.tool_output_chars)}")

    def search(self, pattern: str, path: str | None = None, glob: str | None = None,
               ignore_case: bool = False) -> ToolResult:
        p = shlex.quote(self.ws.path(path))
        pat = shlex.quote(pattern)
        rg = ["rg", "-n", "--no-heading", "--color=never", "--hidden", "-g", "'!.git'", "-M", "300"]
        gr = ["grep", "-rnE", "--exclude-dir=.git"]
        if ignore_case:
            rg.append("-i")
            gr.append("-i")
        if glob:
            rg += ["-g", shlex.quote(glob)]
            gr.append(f"--include={shlex.quote(glob)}")
        script = (f"if command -v rg >/dev/null; then {' '.join(rg)} -e {pat} {p}; "
                  f"else {' '.join(gr)} -e {pat} {p}; fi | head -n {SEARCH_LIMIT + 1}; "
                  "exit ${PIPESTATUS[0]}")
        r = self.ws.sh(script)
        if r.exit_code == 1 and not _text(r.stdout).strip():
            return ToolResult("no matches")
        if r.exit_code not in (0, 1, 141):
            raise ToolError(_text(r.stderr).strip() or f"search failed with exit {r.exit_code}")
        lines = _text(r.stdout).splitlines()
        out = "\n".join(lines[:SEARCH_LIMIT])
        if len(lines) > SEARCH_LIMIT:
            out += f"\n[more than {SEARCH_LIMIT} matches; narrow the search]"
        return ToolResult(truncate(out, self.ws.limits.tool_output_chars))

    def finish(self, summary: str = "") -> ToolResult:
        return ToolResult(summary, finished=True)


def format_exec(r: Any, limit: int) -> str:
    out, err = _text(r.stdout), _text(r.stderr)
    head = f"exit_code: {r.exit_code}"
    if getattr(r, "timed_out", False):
        head += " (timed out)"
    if getattr(r, "truncated", False):
        head += (" (output was larger than the environment limit and its end is lost; "
                 "redirect to a file and use tail or search)")
    parts = [head]
    # Share the budget; give stderr at least a quarter when both are large.
    budget = max(200, limit - 100)
    if out and err:
        e_lim = max(budget // 4, budget - len(out)) if len(err) > budget // 4 else len(err)
        o_lim = budget - min(len(err), e_lim)
    else:
        o_lim = e_lim = budget
    if out:
        parts.append("stdout:\n" + truncate(out, o_lim))
    if err:
        parts.append("stderr:\n" + truncate(err, e_lim))
    if not out and not err:
        parts.append("(no output)")
    return "\n".join(parts)
