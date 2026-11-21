"""Probes of case pr7. Target: primitives/apprentice/tests"""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice import apprentice as ap
from magi_apprentice.apprentice import Apprentice, Context, Task
from magi_apprentice.config import Limits
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

STUB = "[compacted 1978 chars]"


class CheckExec(MemExecution):
    """Counts the runs of the check command, which fails each time."""

    def __init__(self):
        super().__init__()
        self.checks = 0

    def exec(self, req):
        r = super().exec(req)
        if "./check.sh" in req.argv[-1]:
            self.checks += 1
            r.exit_code = 1
        return r


def run(script, lim, e=None, **task):
    model = FakeModel(script)
    e = e or MemExecution()
    a = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, Transcript(Redactor()))
    return a.run(Task("t", **task)), model, e


class Pr7(unittest.TestCase):
    def test_compaction_keeps_a_finish_summary(self):
        """_shrink does not change the arguments of finish calls."""
        summary = "I changed a.py and b.py. " * 60
        msgs = [{"role": "system", "content": "s"}, {"role": "user", "content": "task"}]
        for i in range(20):
            name = "finish" if i == 0 else "bash"
            args = {"summary": summary} if i == 0 else {"command": "echo " + "x" * 1500}
            msgs.append({"role": "assistant", "content": "", "tool_calls": [
                {"id": f"c{i}", "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}]})
            msgs.append({"role": "tool", "tool_call_id": f"c{i}", "_name": name, "content": "x" * 4000})
        ctx = Context(msgs, budget=6000)
        ctx.compact()
        found = [json.loads(c["function"]["arguments"]) for m in ctx.messages if m["role"] == "assistant"
                 for c in m["tool_calls"] if c["function"]["name"] == "finish"]
        self.assertEqual(found, [{"summary": summary}])

    def test_compaction_still_shrinks_other_calls(self):
        """Only finish calls are kept: the arguments of other old calls are compacted."""
        msgs = [{"role": "system", "content": "s"}, {"role": "user", "content": "task"}]
        for i in range(20):
            msgs.append({"role": "assistant", "content": "", "tool_calls": [
                {"id": f"c{i}", "type": "function",
                 "function": {"name": "bash", "arguments": json.dumps({"command": "echo " + "x" * 1500})}}]})
            msgs.append({"role": "tool", "tool_call_id": f"c{i}", "_name": "bash", "content": "x" * 4000})
        ctx = Context(msgs, budget=6000)
        ctx.compact()
        first = next(m for m in ctx.messages if m["role"] == "assistant")
        self.assertIn("compacted", first["tool_calls"][0]["function"]["arguments"])

    def test_stub_summary_is_rejected_and_the_next_finish_is_accepted(self):
        """A stub summary is rejected and the next finish is accepted."""
        r, model, _ = run([reply(call("finish", summary=STUB)), reply(call("finish", summary="real"))],
                          Limits(self_review=False))
        self.assertEqual((r.status, r.summary, r.steps), ("finished", "real", 2))

    def test_the_rejection_asks_for_the_full_summary(self):
        """Reject that finish with a tool result that asks for the full summary."""
        r, model, _ = run([reply(call("finish", summary=STUB)), reply(call("finish", summary="real"))],
                          Limits(self_review=False))
        result = model.requests[1][-1]
        self.assertEqual(result["role"], "tool")
        self.assertIn("summary", result["content"].lower())

    def test_the_stub_does_not_run_the_check(self):
        """This does not count as a failed check: the check does not run for the stub."""
        e = CheckExec()
        run([reply(call("finish", summary=STUB))], Limits(self_review=False, max_steps=1), e,
            check_command="./check.sh")
        self.assertEqual(e.checks, 0)

    def test_the_stub_does_not_use_up_a_check_retry(self):
        """This does not count as a failed check: the retries of the check stay."""
        lim = Limits(self_review=False, check_retries=2)
        script = [reply(call("finish", summary="one")), reply(call("finish", summary="two")),
                  reply(call("finish", summary="three"))]
        _, _, plain = run(list(script), lim, CheckExec(), check_command="./check.sh")
        _, _, stub = run([reply(call("finish", summary=STUB))] + script, lim, CheckExec(),
                         check_command="./check.sh")
        self.assertEqual(stub.checks, plain.checks)

    def test_the_script_ends_with_the_reset_after_the_diff_commands(self):
        """The script ends with the reset after the diff commands."""
        s = ap.DIFF_SCRIPT
        self.assertIn("git reset -q", s)
        self.assertGreater(s.index("git reset -q"), s.rindex("git diff"))


class Pr7Git(unittest.TestCase):
    def setUp(self):
        from magi_apprentice.local import LocalExecution
        self.dir = tempfile.TemporaryDirectory()
        self.d = self.dir.name
        self.env = {k: v for k, v in os.environ.items() if "GIT_" not in k}
        Path(self.d, "src").mkdir()
        Path(self.d, "src", "a.py").write_text("x = 1\n")
        self.git("init", "-q")
        self.git("add", "src/a.py")
        self.git("-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "first")
        Path(self.d, "src", "a.py").write_text("x = 2\n")
        Path(self.d, "src", "b.py").write_text("y = 2\n")
        lim = Limits()
        self.diff = ap.workdir_diff(Tools(Workspace(LocalExecution(), "local", self.d, lim)))

    def tearDown(self):
        self.dir.cleanup()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.d, env=self.env, check=True,
                              capture_output=True, text=True).stdout

    def test_the_diff_still_shows_a_new_file(self):
        """The reset is after the diff: the review diff still shows new files."""
        self.assertIn("+x = 2", self.diff)
        self.assertIn("+y = 2", self.diff)

    def test_a_new_file_is_untracked_after_the_diff(self):
        """After workdir_diff, a new file is still untracked (git status --short shows ??)."""
        self.assertEqual(self.git("status", "--short"), " M src/a.py\n?? src/b.py\n")

    def test_git_stash_works_after_the_diff(self):
        """git stash -u works."""
        self.assertIn("Saved working directory", self.git("stash", "-u"))
        self.assertFalse(Path(self.d, "src", "b.py").exists())
