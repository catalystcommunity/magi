"""The tool-using reviewer: the reviewer of review.py, with read-only tools
on a checkout of the change.

The diff reviewer finds breaks of the review lessons, but it misses defects
that need the code around the change: a notice that now says a sentence
twice, a count that is wrong on the first import, a copied request type.
This reviewer gets the same task, lessons and diff, and it can read files,
search, and run read-only git commands in the work directory before it
gives its verdict. It cannot edit and it does not build or run tests: the
check step runs the tests, and on a desktop host a build stops the local
model engine.

The verdict goes through the same rules as a diff review (parse_sample): a
blocker or an unmet requirement is revise, and no verdict is invalid.
"""
from __future__ import annotations

import json
import time

from .model import Model, ModelError
from .review import REVIEW_SYSTEM, Sample, parse_sample, review_prompt
from .tools import TOOL_SPECS, ToolResult, Tools, format_exec, truncate

AGENT_SYSTEM = REVIEW_SYSTEM.replace(
    "Reply with your reasoning, then end with one JSON object in a ```json block:",
    """The change is applied in the work directory, so you can read the code around it. Use the \
tools before you decide:
- For each changed function, read the whole function and its callers (search for its name).
- For each text that a person reads (a notice, a log line, a doc), work out the full text that the \
code makes, with the strings around it, and read it as the person will.
- For each counter, condition or loop, follow one concrete input through the new code.
- Compare each requirement with the words of the task, not with the summary of the agent.
Do not build, run tests, or change files: the tests run in another step.
When you are done, call the tool verdict with:""").replace(
    "Do not invent defects: name only what the diff shows.",
    "Do not invent defects: name only what you saw in the diff or in a file that you read.")

GIT_READ = ("show", "log", "diff", "blame", "grep", "ls-files")

VERDICT_SPEC = {"type": "function", "function": {
    "name": "verdict",
    "description": "Give the review result. Call this once, at the end.",
    "parameters": {"type": "object", "properties": {
        "requirements": {"type": "array", "items": {"type": "object", "properties": {
            "text": {"type": "string"}, "met": {"type": "boolean"}, "evidence": {"type": "string"}}}},
        "defects": {"type": "array", "items": {"type": "object", "properties": {
            "file": {"type": "string"}, "line": {"type": ["integer", "null"]},
            "severity": {"type": "string", "enum": ["blocker", "minor"]}, "problem": {"type": "string"}}}},
        "verdict": {"type": "string", "enum": ["approve", "revise"]}},
        "required": ["requirements", "defects", "verdict"]}}}

GIT_SPEC = {"type": "function", "function": {
    "name": "git",
    "description": "Run a read-only git command in the work directory: " + ", ".join(GIT_READ)
                   + ". args are the arguments after git, for example [\"log\", \"-5\", \"--oneline\"].",
    "parameters": {"type": "object", "properties": {"args": {"type": "array", "items": {"type": "string"}}},
                   "required": ["args"]}}}

READ_TOOLS = ("read_file", "list_files", "search")
SPECS = [s for s in TOOL_SPECS if s["function"]["name"] in READ_TOOLS] + [GIT_SPEC, VERDICT_SPEC]

# Older tool results are cut to this size, so that a long review fits the
# context of a local engine.
KEEP_FULL = 6
OLD_RESULT_CHARS = 400


def git_command(tools: Tools, args: list) -> ToolResult:
    if not isinstance(args, list) or not args or not all(isinstance(a, str) for a in args):
        return ToolResult("error: args must be a list of strings", ok=False)
    if args[0] not in GIT_READ:
        return ToolResult(f"error: only these git commands are allowed: {', '.join(GIT_READ)}", ok=False)
    # --output writes a file, and -c / --exec-path change what git runs.
    if any(a.startswith(("--output", "--exec-path", "-c", "--ext-diff", "--textconv")) for a in args[1:]):
        return ToolResult("error: that option is not allowed in a review", ok=False)
    r = tools.ws.exec(["git", "--no-pager", *args], timeout_s=60)
    return ToolResult(format_exec(r, tools.ws.limits.tool_output_chars), ok=r.exit_code == 0)


def _verdict_sample(arguments: str) -> Sample:
    try:
        data = json.loads(arguments or "{}")
    except ValueError:
        return Sample("invalid", text=arguments or "")
    return parse_sample("```json\n" + json.dumps(data) + "\n```")


def _cut_old_results(messages: list[dict]) -> None:
    results = [m for m in messages if m["role"] == "tool"]
    for m in results[:-KEEP_FULL]:
        if len(m["content"]) > OLD_RESULT_CHARS:
            m["content"] = m["content"][:OLD_RESULT_CHARS] + "\n[older result cut; call the tool again to see it]"


def agent_review(model: Model, tools: Tools, task: str, patch: str, lessons: str = "",
                 max_steps: int = 25, log=None) -> Sample:
    """One review with tools. A model error, or no verdict within max_steps,
    is an invalid sample, so it cannot approve."""
    msgs = review_prompt(task, patch, lessons)
    msgs[0] = {"role": "system", "content": AGENT_SYSTEM}
    t0 = time.monotonic()
    for step in range(max_steps):
        last = step == max_steps - 1
        if last:
            msgs.append({"role": "user", "content": "This is the last step. Call verdict now."})
        try:
            reply = model.complete(msgs, [VERDICT_SPEC] if last else SPECS)
        except ModelError as e:
            return Sample("invalid", text=f"model error: {e}")
        msgs.append(reply.message())
        if not reply.tool_calls:
            # A verdict in the text is accepted too; otherwise ask for the tool.
            s = parse_sample(reply.content)
            if s.verdict != "invalid":
                return s
            msgs.append({"role": "user", "content": "Use the tools, or call verdict to finish."})
            continue
        for call in reply.tool_calls:
            if call.name == "verdict":
                s = _verdict_sample(call.arguments)
                # A verdict forced by the step limit comes from a review that
                # did not end, so it cannot approve.
                if last and s.verdict == "approve":
                    s.verdict = "revise"
                    s.defects.append({"file": "", "line": None, "severity": "blocker",
                                      "problem": f"the review did not end within {max_steps} steps"})
                s.text = f"steps {step + 1}, {int(time.monotonic() - t0)} s\n" + s.text
                return s
            if call.name == "git":
                try:
                    res = git_command(tools, json.loads(call.arguments or "{}").get("args"))
                except (ValueError, AttributeError):
                    res = ToolResult("error: invalid JSON arguments", ok=False)
            elif call.name in READ_TOOLS:
                res = tools.call(call.name, call.arguments)
            else:
                res = ToolResult(f"error: no tool {call.name!r} in a review. Tools: "
                                 + ", ".join([*READ_TOOLS, "git", "verdict"]), ok=False)
            if log:
                log(f"  step {step + 1}: {call.name} {truncate(call.arguments or '', 160)}")
            msgs.append({"role": "tool", "tool_call_id": call.id, "content": res.content})
        _cut_old_results(msgs)
    return Sample("invalid", text=f"no verdict in {max_steps} steps")
