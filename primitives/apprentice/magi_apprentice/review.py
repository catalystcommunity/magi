"""The reviewer: a model reads the task and the change, and says approve or
revise, with the defects it found.

The reviewer is strict by design: a change is approved only when every
sample says approve, no sample finds a blocker, and every sample says each
requirement of the task is met. An answer that cannot be parsed counts as
revise. So the reviewer can wrongly ask for changes, which costs a work
round, but it approves only when it is sure, because a wrong approval merges
a defect. evals/review measures both rates against the verdicts of a person.
"""
from __future__ import annotations

import json
import re
from dataclasses import dataclass, field

from .model import Model, ModelError

REVIEW_SYSTEM = """You review a code change for the owner of this repository. You are strict: the \
change merges without a person when you approve it. Read the task, the review lessons, and the \
diff. Check each point:
1. Each requirement of the task is done, with the behavior that the task's words describe.
2. Tests call the code that they test, with literal expected values; they do not copy its logic. \
Each new behavior has a test.
3. No defect: wrong conditions, missing error paths, state that leaks between runs, retries or \
counters with side effects, lists or loops with no limit, file modes or permissions that change, \
cleanup that removes something that another part of the system still uses.
4. Text that a person reads (notices, docs, comments, summaries) is true for the code.
5. No duplicated blocks; no change that the task did not ask for; generated files are not edited \
by hand.
6. The review lessons are followed.

Reply with your reasoning, then end with one JSON object in a ```json block:
{"requirements": [{"text": "<requirement>", "met": true, "evidence": "<file:line or why>"}],
 "defects": [{"file": "<path>", "line": <number or null>, "severity": "blocker" or "minor", \
"problem": "<what is wrong and the failure it causes>"}],
 "verdict": "approve" or "revise"}
A blocker is a defect that must be fixed before the merge. Say "revise" when any requirement is not \
met or any blocker exists. Do not invent defects: name only what the diff shows."""

JSON_BLOCK = re.compile(r"```json\s*(\{.*?\})\s*```", re.S)

# Files that tools/gen.sh writes, and the compiled web scripts. Their diff
# follows from the contract and the source; in a large change it pushes the
# real code out of the window. The reviewer sees that they changed, not how.
GENERATED = re.compile(r"(\.gen\.(go|ts|js)$|^primitives/[^/]+/api/|^clients/|^magi/api/|^magi/web/static/js/)")
FILE_HEADER = re.compile(r"^diff --git a/(\S+) b/", re.M)


def drop_generated(patch: str) -> str:
    """The patch with the body of each generated file replaced by one line."""
    parts = re.split(r"(?m)^(?=diff --git )", patch)
    out, dropped = [], []
    for part in parts:
        m = FILE_HEADER.match(part)
        if m and GENERATED.search(m.group(1)):
            dropped.append(m.group(1))
            continue
        out.append(part)
    if dropped:
        out.append("\n[generated files changed, diff not shown: " + ", ".join(dropped) + "]\n")
    return "".join(out)


@dataclass
class Sample:
    verdict: str  # approve, revise, or invalid (the answer could not be parsed)
    requirements: list = field(default_factory=list)
    defects: list = field(default_factory=list)
    text: str = ""


@dataclass
class Review:
    verdict: str  # approve or revise
    samples: list[Sample]

    @property
    def defects(self) -> list:
        return [d for s in self.samples for d in s.defects]


def review_prompt(task: str, patch: str, lessons: str = "", max_patch_chars: int = 120_000,
                  context: str = "") -> list[dict]:
    """The messages of one review call. A patch over max_patch_chars is cut in
    the middle, and the cut is said, so that the reviewer does not approve
    what it did not see. Generated files are left out first."""
    patch = drop_generated(patch)
    if len(patch) > max_patch_chars:
        half = max_patch_chars // 2
        patch = (patch[:half] + f"\n\n[... {len(patch) - max_patch_chars} characters of the diff are not shown; "
                 "you cannot approve a change that you did not see completely ...]\n\n" + patch[-half:])
    user = f"## Task\n\n{task.strip()}\n\n"
    if lessons.strip():
        user += f"## Review lessons of this repository\n\n{lessons.strip()}\n\n"
    user += f"## Diff\n\n```diff\n{patch}\n```\n"
    if context.strip():
        user += ("\n## Functions that the change calls (not changed)\n\n"
                 "Check how each changed line uses them. For a text that a person reads, work out the "
                 "full text from the changed line and the function together.\n\n"
                 f"```\n{context.strip()}\n```\n")
    return [{"role": "system", "content": REVIEW_SYSTEM}, {"role": "user", "content": user}]


def parse_sample(text: str, need_requirements: bool = True) -> Sample:
    """The verdict of one answer. The last JSON block counts, or the whole
    answer when it is one bare JSON object. A missing or broken block, or a
    verdict that is not approve or revise, is invalid."""
    blocks = JSON_BLOCK.findall(text or "")
    if not blocks:
        # A thinking model can send its reasoning in a separate field and
        # only the bare JSON object as the content.
        bare = (text or "").strip()
        if bare.startswith("{") and bare.endswith("}"):
            blocks = [bare]
        else:
            return Sample("invalid", text=text)
    try:
        data = json.loads(blocks[-1])
    except ValueError:
        return Sample("invalid", text=text)
    if not isinstance(data, dict):
        return Sample("invalid", text=text)
    verdict = data.get("verdict")
    reqs = data.get("requirements") if isinstance(data.get("requirements"), list) else []
    defects = data.get("defects") if isinstance(data.get("defects"), list) else []
    if verdict not in ("approve", "revise"):
        return Sample("invalid", reqs, defects, text)
    # The rules of the verdict hold even when the answer breaks them.
    # A requirement counts as met only when the answer says met: true, so an
    # answer that lists the requirements without checking them cannot approve.
    if verdict == "approve" and (any(isinstance(d, dict) and d.get("severity") == "blocker" for d in defects)
                                 or (need_requirements and not reqs)
                                 or any(not isinstance(r, dict) or r.get("met") is not True for r in reqs)):
        verdict = "revise"
    return Sample(verdict, reqs, defects, text)


def combine(samples: list[Sample]) -> Review:
    """Approve only when every sample approves."""
    ok = bool(samples) and all(s.verdict == "approve" for s in samples)
    return Review("approve" if ok else "revise", samples)


def review(model: Model, task: str, patch: str, lessons: str = "", samples: int = 3,
           context: str = "") -> Review:
    """Ask the model samples times. A failed model call is a sample that
    cannot approve. context holds the unchanged functions that the change
    calls (review_context.py)."""
    msgs = review_prompt(task, patch, lessons, context=context)
    out = []
    for _ in range(samples):
        try:
            reply = model.complete(msgs, [])
        except ModelError as e:
            out.append(Sample("invalid", text=f"model error: {e}"))
            continue
        out.append(parse_sample(reply.content))
    return combine(out)


# Lens reviews: one narrow question for each call. A small model that checks
# six points in one answer misses defects that it finds when it checks one
# point (evals/review: a notice that said one sentence twice was in the diff
# next to the function that made it, and three samples approved it).
LENS_FORMAT = """End with one JSON object in a ```json block:
{"requirements": [{"text": "<requirement>", "met": true, "evidence": "<file:line or why>"}],
 "defects": [{"file": "<path>", "line": <number or null>, "severity": "blocker" or "minor", \
"problem": "<what is wrong and the failure it causes>"}],
 "verdict": "approve" or "revise"}
A blocker is a defect that must be fixed before the merge. Say "revise" when there is a blocker. \
Name only defects that the text shows; do not invent them. Look only at your question: \
other reviewers check the rest."""

LENSES = {
    "requirements": "List each requirement of the task, in the task's words. For each one, quote the "
                    "code (file:line) that does it, and say met true or false. A requirement that the "
                    "change does differently from the task's words is not met. Put every requirement "
                    "in \"requirements\".",
    "text": "Find each text that a person reads and that the change makes or changes: notices, log "
            "lines, errors, docs, comments. For each one, build the full final text from the changed "
            "line and the functions it calls, with the strings around it, and write it out. A blocker "
            "is a text that says something twice, says something that the code does not do, or "
            "contradicts the code.",
    "logic": "For each changed condition, counter, loop, and error path, follow concrete inputs "
             "through the new code: the first run, a second run, an empty input, a failure. Write "
             "what the code gives and what it must give. A blocker is a wrong result, state that "
             "leaks between runs, or an error that is lost.",
    "tests": "For each new behavior, find its test. A test must call the real code and compare with "
             "literal expected values; a test that copies the logic of the code or checks only a "
             "copy is a blocker. A new behavior with no test is a blocker. A test that cannot fail "
             "is a blocker.",
    "scope": "Check the change against the review lessons and the task's limits: changes that the "
             "task did not ask for, a new type or helper that copies an existing one, duplicated "
             "blocks, generated files edited by hand, file modes that change. Each break of a review "
             "lesson that changes behavior or the contract is a blocker.",
}


def lens_prompt(lens: str, task: str, patch: str, lessons: str = "", context: str = "") -> list[dict]:
    msgs = review_prompt(task, patch, lessons, context=context)
    msgs[0] = {"role": "system", "content": "You review one question about a code change for the owner "
               "of this repository. The change merges without a person when all reviewers approve.\n\n"
               f"Your question: {LENSES[lens]}\n\n{LENS_FORMAT}"}
    return msgs


def lens_review(model: Model, task: str, patch: str, lessons: str = "", context: str = "",
                lenses: tuple[str, ...] = tuple(LENSES)) -> Review:
    """One call for each lens; approve only when every lens approves. Only
    the requirements lens must list requirements."""
    out = []
    for lens in lenses:
        try:
            reply = model.complete(lens_prompt(lens, task, patch, lessons, context), [])
        except ModelError as e:
            out.append(Sample("invalid", text=f"{lens}: model error: {e}"))
            continue
        s = parse_sample(reply.content, need_requirements=lens == "requirements")
        s.text = f"[{lens}]\n" + s.text
        for d in s.defects:
            if isinstance(d, dict):
                d["lens"] = lens
        out.append(s)
    return combine(out)
