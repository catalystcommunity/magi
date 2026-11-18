"""The code around a change, collected by the harness for a review.

A reviewer model that explores a repository with tools spends its steps on
test helpers and loses the change (evals/review: 25 steps, no verdict). The
harness collects the context instead, with fixed rules, in a few git calls:

- the diff with whole functions (git diff --function-context), so that the
  reviewer sees each changed function in full;
- the definitions of the functions that the added lines call, when they are
  defined in the repository and not shown in the diff already. A defect is
  often in how a changed line uses an unchanged function, for example a
  message that the called function extends with the same sentence again;
- the contract text of each RPC request type that the added lines use: the
  ";;;" comment and the fields in contracts/*.csil. The behavior of an
  operation is there, not in the generated client (evals/coding pr6: an
  import looked entities up by an id that put-entity never makes).
"""
from __future__ import annotations

import re
import shlex

from .review import drop_generated
from .tools import Tools, as_text

CALL = re.compile(r"\b([A-Za-z_][A-Za-z0-9_]*)\s*\(")
KEYWORDS = {"if", "for", "while", "switch", "return", "func", "def", "make", "len", "append", "cap", "new",
            "print", "range", "map", "chan", "go", "defer", "select", "case", "int", "str", "bool", "dict",
            "list", "set", "tuple", "isinstance", "super", "and", "or", "not", "in", "is", "lambda", "string",
            "byte", "error", "panic", "recover", "copy", "delete", "close", "uint64", "int64", "float64"}
MAX_DEFINITIONS = 15
MAX_DEFINITION_LINES = 80
MAX_CONTEXT_CHARS = 30_000
MAX_FUNCTION_DIFF_CHARS = 90_000
# A generated request type in Go (knowledgeapi.PutEntityRequest) or Python
# (T.PutEntityRequest).
REQUEST_TYPE = re.compile(r"\b([A-Za-z]\w*)\.([A-Z]\w*Request)\b")
MAX_CONTRACTS = 8
MAX_CONTRACT_LINES = 40


def _git(tools: Tools, args: str) -> str:
    r = tools.ws.sh("git --no-pager " + args + " 2>/dev/null", timeout_s=60)
    return as_text(r.stdout) if r.exit_code == 0 else ""


def called_names(patch: str) -> list[str]:
    """The names that the added lines call, in the order of first use."""
    seen: dict[str, None] = {}
    for line in patch.splitlines():
        if not line.startswith("+") or line.startswith("+++"):
            continue
        for name in CALL.findall(line[1:]):
            if name not in KEYWORDS and len(name) > 2:
                seen.setdefault(name, None)
    return list(seen)


def request_types(patch: str) -> list[tuple[str, str]]:
    """(contract, type) of each RPC request type that the added lines use, in
    the order of first use. A Go import name "knowledgeapi" gives the
    contract "knowledge"; another qualifier gives "" (any contract)."""
    seen: dict[tuple[str, str], None] = {}
    for line in patch.splitlines():
        if line.startswith("+") and not line.startswith("+++"):
            for qual, name in REQUEST_TYPE.findall(line[1:]):
                contract = qual[:-3] if qual.endswith("api") and len(qual) > 3 else ""
                seen.setdefault((contract, name), None)
    return list(seen)[:MAX_CONTRACTS]


def contract_block(text: str, name: str) -> str:
    """The ";;;" comment above "name = {" in a CSIL file, and the type up to
    its closing brace. Empty when the file does not define name."""
    lines = text.splitlines()
    for i, ln in enumerate(lines):
        if re.match(rf"^{re.escape(name)}\s*=", ln):
            start = i
            while start > 0 and lines[start - 1].startswith(";;;"):
                start -= 1
            end = i
            while end < len(lines) - 1 and "}" not in lines[end] and end - i < MAX_CONTRACT_LINES:
                end += 1
            return "\n".join(lines[start:end + 1])
    return ""


def _contracts(tools: Tools, patch: str) -> list[str]:
    types = request_types(patch)
    if not types:
        return []
    pat = shlex.quote("^(" + "|".join(sorted({n for _, n in types})) + r")\s*=")
    files = sorted({h for h in _git(tools, f"grep -l -E {pat} -- 'contracts/*.csil'").splitlines() if h})
    texts = {path: _git(tools, f"show HEAD:{shlex.quote(path)}") for path in files}
    out = []
    for contract, name in types:
        own = f"contracts/{contract}.csil"
        for path in ([own] if own in texts else files):
            if block := contract_block(texts[path], name):
                out.append(f"// {path} ({name})\n{block}")
    return out


def _definition(tools: Tools, path: str, line: int) -> str:
    """The text of the definition at path:line: a Go func up to its closing
    brace at the start of a line, or a Python def up to the next line with
    the same or less indentation."""
    text = _git(tools, f"show HEAD:{shlex.quote(path)}") or as_text(
        tools.ws.sh(f"cat -- {shlex.quote(path)}", timeout_s=30).stdout)
    lines = text.splitlines()[line - 1:line - 1 + MAX_DEFINITION_LINES]
    if not lines:
        return ""
    out = [lines[0]]
    if path.endswith(".go"):
        for ln in lines[1:]:
            out.append(ln)
            if ln.startswith("}"):
                break
    else:
        indent = len(lines[0]) - len(lines[0].lstrip())
        for ln in lines[1:]:
            if ln.strip() and len(ln) - len(ln.lstrip()) <= indent:
                break
            out.append(ln)
    return "\n".join(out)


def review_context(tools: Tools, base: str = "HEAD") -> tuple[str, str]:
    """(the diff with whole functions, the definitions of called functions).
    base is the commit that the change is compared with; the work directory
    holds the change. An empty diff means that the caller uses its own."""
    b = shlex.quote(base)
    _git(tools, "add --intent-to-add --all")
    diff = drop_generated(_git(tools, f"diff --function-context {b}"))
    tools.ws.sh("git reset -q 2>/dev/null", timeout_s=30)
    if len(diff) > MAX_FUNCTION_DIFF_CHARS:
        return "", ""
    shown = diff
    # Only the directories of the changed files: a common name such as Do or
    # New is defined in many modules, and a far copy is noise.
    dirs = sorted({f.rsplit("/", 1)[0] if "/" in f else "." for f in re.findall(r"^\+\+\+ b/(\S+)", diff, re.M)})
    where = " ".join(shlex.quote(d + "/*" + ext) for d in dirs for ext in (".go", ".py")) or "'*.go' '*.py'"
    parts, size = [], 0
    for name in called_names(diff):
        if len(parts) >= MAX_DEFINITIONS:
            break
        # A function that the diff shows in full needs no second copy.
        if re.search(rf"^[ +-]\s*(func (\([^)]*\) )?{name}\(|def {name}\()", shown, re.M):
            continue
        pat = shlex.quote(rf"^(func (\([^)]*\) )?{name}\(|\s*def {name}\()")
        hits = _git(tools, f"grep -n -E {pat} -- {where}").splitlines()
        hits = [h for h in hits if "_test.go:" not in h and "/tests/" not in h][:1]
        for h in hits:
            path, line, _ = h.split(":", 2)
            body = _definition(tools, path, int(line))
            if body and size + len(body) <= MAX_CONTEXT_CHARS:
                parts.append(f"// {path}:{line}\n{body}")
                size += len(body)
    for block in _contracts(tools, diff):
        if size + len(block) <= MAX_CONTEXT_CHARS:
            parts.append(block)
            size += len(block)
    return diff, "\n\n".join(parts)
