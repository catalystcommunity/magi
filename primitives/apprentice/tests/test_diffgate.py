import os
import subprocess
import tempfile
import unittest

from fakes import FakeModel, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.diffgate import duplicated_blocks, problems
from magi_apprentice.local import LocalExecution
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript


def gate_out(summary="", name_status="", diff=""):
    return f"--- summary ---\n{summary}\n--- name-status ---\n{name_status}\n--- diff ---\n{diff}"


NOTE = ['        note(f"Budget: {left} of {total} calls are left.")',
        '        tr.record("budget_note", content=text)',
        '        ctx.messages.append({"role": "user", "content": text})',
        '        self.noted = self.noted + 1']


def added(path, *blocks):
    lines = [f"diff --git a/{path} b/{path}", f"--- a/{path}", f"+++ b/{path}", "@@ -1,0 +1,20 @@"]
    for b in blocks:
        lines += ["+" + x for x in b] + ["+    other_line_between_the_blocks()"]
    return "\n".join(lines) + "\n"


class ProblemsTest(unittest.TestCase):
    def test_a_lost_executable_bit(self):
        found = problems(gate_out(" mode change 100755 => 100644 tools/run.sh\n"), "edit run.sh")
        self.assertEqual(found, ["These files were executable and are not now: tools/run.sh. Run chmod 755 "
                                 "on each, and check with git diff --summary (no mode change)."])
        self.assertEqual(problems(gate_out(" mode change 100644 => 100755 tools/run.sh\n"), "x"), [])

    def test_a_deleted_test_file_unless_the_task_says_so(self):
        out = gate_out(name_status="M\ta.go\nD\ta_test.go\nD\tdocs/old.md\nD\ttests/test_x.py\n")
        found = problems(out, "Fix the parser.")
        self.assertEqual(len(found), 1)
        self.assertIn("a_test.go, tests/test_x.py.", found[0])
        self.assertNotIn("old.md", found[0])
        self.assertEqual(problems(out, "Remove the old parser tests."), [])

    def test_a_copied_block_of_code(self):
        found = problems(gate_out(diff=added("pkg/apprentice.py", NOTE, NOTE, NOTE)), "x")
        self.assertEqual(found, ["The change adds the same block of code in two or more places: pkg/apprentice.py "
                                 '(it starts with: note(f"Budget: {left} of {total} calls are left.")). Put the '
                                 "shared code in one helper or constant, or say in the summary why the copies "
                                 "must stay."])

    def test_copies_that_the_rule_does_not_count(self):
        go_err = ["p, err := rpc.Caller(ctx)", "if err != nil {", "return Resp{}, err", "}",
                  "if err := requireRoles(p); err != nil {", "return Resp{}, err", "}"]
        for diff in (added("pkg/a.py", NOTE),                      # one block
                     added("pkg/a_test.go", NOTE, NOTE),            # a test file
                     added("tests/test_a.py", NOTE, NOTE),
                     added("docs/a.md", NOTE, NOTE),                # not code
                     added("magi/api/a.gen.go", NOTE, NOTE),        # generated
                     added("pkg/a.go", go_err, go_err),             # error boilerplate
                     added("pkg/a.py", NOTE) + added("pkg/b.py", NOTE)):  # two files
            self.assertEqual(duplicated_blocks(diff), [], diff)

    def test_a_clean_change_has_no_problem(self):
        self.assertEqual(problems(gate_out(" create mode 100644 b.go\n", "A\tb.go\n"), "x"), [])


def git(d, *args):
    subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local", *args],
                   check=True, capture_output=True)


class GateInRunTest(unittest.TestCase):
    def test_a_finish_with_a_lost_mode_is_rejected_once_then_fixed(self):
        with tempfile.TemporaryDirectory() as d:
            git(d, "init", "-q")
            path = os.path.join(d, "run.sh")
            with open(path, "w") as f:
                f.write("#!/bin/sh\necho one\n")
            os.chmod(path, 0o755)
            git(d, "add", "run.sh")
            git(d, "commit", "-qm", "first")
            lim = Limits(self_review=False)
            model = FakeModel([
                # An old envd wrote each file with mode 644.
                reply(call("bash", command="printf '#!/bin/sh\\necho two\\n' > run.sh && chmod 644 run.sh")),
                reply(call("finish", summary="changed the echo")),
                reply(call("bash", command="chmod 755 run.sh")),
                reply(call("finish", summary="changed the echo, mode kept")),
            ])
            tr = Transcript(Redactor())
            r = Apprentice(model, Tools(Workspace(LocalExecution(), "local", d, lim)), lim, tr).run(
                Task("Make run.sh say two.", workdir=d))
            self.assertEqual((r.status, r.summary), ("finished", "changed the echo, mode kept"))
            rejected = [m for m in model.requests[2] if m["role"] == "tool"][-1]["content"]
            self.assertIn("finish rejected: the change breaks a rule of the harness.", rejected)
            self.assertIn("These files were executable and are not now: run.sh.", rejected)
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o755)

    def test_a_finish_with_a_copied_block_is_rejected_in_a_real_repository(self):
        with tempfile.TemporaryDirectory() as d:
            git(d, "init", "-q")
            with open(os.path.join(d, "a.py"), "w") as f:
                f.write("x = 1\n")
            git(d, "add", "a.py")
            git(d, "commit", "-qm", "first")
            block = "\n".join(x.strip() for x in NOTE)
            with open(os.path.join(d, "b.py"), "w") as f:  # a new file: untracked
                f.write(f"def one():\n{block}\n\ndef two():\n{block}\n")
            lim = Limits(self_review=False)
            model = FakeModel([reply(call("finish", summary="added b")),
                               reply(call("finish", summary="added b; the copies are two different notes"))])
            tr = Transcript(Redactor())
            r = Apprentice(model, Tools(Workspace(LocalExecution(), "local", d, lim)), lim, tr).run(
                Task("Add b.py.", workdir=d))
            self.assertEqual(r.summary, "added b; the copies are two different notes")
            rejected = [m for m in model.requests[1] if m["role"] == "tool"][-1]["content"]
            self.assertIn("The change adds the same block of code in two or more places: b.py", rejected)


if __name__ == "__main__":
    unittest.main()
