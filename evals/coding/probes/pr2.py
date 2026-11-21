"""Probes of case pr2. Target: primitives/apprentice/tests"""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

FINISH = reply(call("finish", summary="s"))
TASK = "TASK-TEXT-4711"


def start(execution, workdir="/work/repo"):
    """Run one task. Returns the result, the first user message, and the system message."""
    lim = Limits()
    model = FakeModel([FINISH, FINISH, FINISH])
    a = Apprentice(model, Tools(Workspace(execution, "env", workdir, lim)), lim, Transcript(Redactor()))
    r = a.run(Task(TASK, workdir=workdir))
    return r, model.requests[0][1]["content"], model.requests[0][0]["content"]


class Pr2(unittest.TestCase):
    def test_agents_md_before_the_task(self):
        """Put the text under the heading "Project instructions (from AGENTS.md):", before the task instructions."""
        _, user, _ = start(MemExecution({"/work/repo/AGENTS.md": "Use Go modules.\n",
                                         "/work/repo/CLAUDE.md": "Use Rust.\n"}))
        self.assertIn("Project instructions (from AGENTS.md):", user)
        self.assertIn("Use Go modules.", user)
        self.assertNotIn("Use Rust.", user)
        self.assertLess(user.index("Use Go modules."), user.index(TASK))

    def test_claude_md_when_there_is_no_agents_md(self):
        """If there is no AGENTS.md, read CLAUDE.md."""
        _, user, _ = start(MemExecution({"/work/repo/CLAUDE.md": "Use Rust.\n"}))
        self.assertIn("Project instructions (from CLAUDE.md):", user)
        self.assertIn("Use Rust.", user)

    def test_a_missing_file_is_not_an_error(self):
        """A missing file is not an error."""
        r, user, _ = start(MemExecution({}))
        self.assertEqual(r.status, "finished")
        self.assertNotIn("Project instructions", user)
        self.assertIn(TASK, user)

    def test_a_long_file_is_cut_at_20000_characters(self):
        """Cut it at 20000 characters with a note."""
        _, user, _ = start(MemExecution({"/work/repo/AGENTS.md": "x" * 26000}))
        self.assertIn("x" * 20000, user)
        self.assertNotIn("x" * 20001, user)
        note = user[user.rindex("x" * 20000) + 20000:user.index(TASK)]
        self.assertRegex(note.lower(), r"cut|truncat|more characters|shortened")

    def test_the_system_prompt_names_the_owner(self):
        """Add a line to SYSTEM_PROMPT: the project instructions are rules of the repository owner."""
        _, _, system = start(MemExecution({"/work/repo/AGENTS.md": "rules\n"}))
        self.assertIn("project instructions", system.lower())
        self.assertIn("owner", system.lower())
        self.assertIn("unless", system.lower())

    def test_one_exec_for_the_picture(self):
        """Get them with one exec through the same Tools object."""
        m = MemExecution({"/work/repo/AGENTS.md": "rules\n"})
        seen = []
        lim = Limits()
        model = FakeModel([lambda _m: (seen.append(len(m.execs)), FINISH)[1], FINISH])
        Apprentice(model, Tools(Workspace(m, "env", "/work/repo", lim)), lim, Transcript(Redactor())).run(Task(TASK))
        self.assertEqual(seen[0], 1)


class Pr2Git(unittest.TestCase):
    """The picture against a real git repository."""

    def setUp(self):
        from magi_apprentice.local import LocalExecution
        self.local = LocalExecution()
        self.dir = tempfile.TemporaryDirectory()
        self.d = self.dir.name
        self.env = {k: v for k, v in os.environ.items() if "GIT_" not in k}

    def tearDown(self):
        self.dir.cleanup()

    def repo(self, new_files=1):
        for name in ("zeta.txt", "AGENTS.md", "mid.txt"):
            Path(self.d, name).write_text("Test the module you change.\n")
        Path(self.d, "src").mkdir()
        Path(self.d, "src", "a.py").write_text("x = 1\n")
        for args in (["init", "-q"], ["add", "-A"],
                     ["-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "the-first-commit"]):
            subprocess.run(["git", *args], cwd=self.d, check=True, env=self.env)
        Path(self.d, "src", "a.py").write_text("x = 2\n")
        for i in range(new_files):
            Path(self.d, f"new{i:02}.txt").write_text("n\n")

    def test_the_picture_has_the_status_and_the_log(self):
        """A section "Repository:" with the output of git status --short and git log --oneline -5."""
        self.repo()
        _, user, _ = start(self.local, self.d)
        self.assertIn("Repository:", user)
        self.assertIn(" M src/a.py", user)
        self.assertIn("?? new00.txt", user)
        self.assertIn("the-first-commit", user)
        self.assertIn("Test the module you change.", user)

    def test_the_top_level_entries_are_one_sorted_line(self):
        """The top-level entries of the work directory (one line, sorted)."""
        self.repo()
        _, user, _ = start(self.local, self.d)
        line = next((x for x in user.split("\n") if "zeta.txt" in x and "mid.txt" in x and "src" in x), "")
        self.assertTrue(line, "no line with all the entries")
        names = [w.strip(",") for w in line.split() if w.strip(",") in ("AGENTS.md", "mid.txt", "new00.txt", "src", "zeta.txt")]
        self.assertEqual(names, ["AGENTS.md", "mid.txt", "new00.txt", "src", "zeta.txt"])

    def test_at_most_50_lines_of_status(self):
        """The output of git status --short (at most 50 lines)."""
        self.repo(new_files=70)
        _, user, _ = start(self.local, self.d)
        n = len([x for x in user.split("\n") if x.startswith(("?? ", " M "))])
        self.assertGreater(n, 40)
        self.assertLessEqual(n, 50)

    def test_a_failed_command_leaves_its_part_out(self):
        """If a command fails, leave that part out."""
        Path(self.d, "only.txt").write_text("n\n")
        r, user, _ = start(self.local, self.d)
        self.assertEqual(r.status, "finished")
        self.assertIn("only.txt", user)
        self.assertNotIn("not a git repository", user)
        self.assertNotIn("git status", user)
        self.assertNotIn("git log", user)
