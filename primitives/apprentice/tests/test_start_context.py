"""The start context: project instructions and a repository picture in the
first user message."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import (Apprentice, INSTRUCTIONS_MAX_CHARS, Task,
                                        build_prompt, repository_picture, workdir_diff)
from magi_apprentice.config import Limits
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript


def apprentice(script, files=None, limits=None, **kw):
    lim = limits or Limits(self_review=False)
    m = MemExecution(files)
    model = FakeModel(script)
    tr = Transcript(Redactor())
    return Apprentice(model, Tools(Workspace(m, "env", "/work/repo", lim)), lim, tr, **kw), model, m, tr


def first_user(model):
    return model.requests[0][1]["content"]


class ProjectInstructionsTest(unittest.TestCase):
    def test_agents_md_is_first(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    files={"/work/repo/AGENTS.md": "Use Go modules.\n",
                                           "/work/repo/CLAUDE.md": "Use Rust.\n"})
        a.run(Task("t"))
        user = first_user(model)
        self.assertIn("Project instructions (from AGENTS.md):\nUse Go modules.", user)
        self.assertNotIn("CLAUDE.md", user)
        # before the task instructions
        self.assertLess(user.index("Project instructions"), user.index("Task instructions:"))

    def test_a_known_secret_in_the_instructions_does_not_reach_the_model(self):
        a, model, _, tr = apprentice([reply(call("finish", summary="s"))],
                                     files={"/work/repo/AGENTS.md": "Deploy with the key sk-test-abcdef123456.\n"})
        tr.redactor.add("sk-test-abcdef123456")
        a.run(Task("t"))
        user = first_user(model)
        self.assertIn("Deploy with the key ", user)
        self.assertNotIn("sk-test-abcdef123456", user)

    def test_claude_md_when_no_agents_md(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    files={"/work/repo/CLAUDE.md": "Use Rust.\n"})
        a.run(Task("t"))
        self.assertIn("Project instructions (from CLAUDE.md):\nUse Rust.", first_user(model))

    def test_no_instructions_file_is_not_an_error(self):
        a, model, m, tr = apprentice([reply(call("finish", summary="s"))], files={})
        r = a.run(Task("t"))
        self.assertEqual(r.status, "finished")
        self.assertNotIn("Project instructions", first_user(model))
        self.assertIn("start_context", [json.loads(l)["kind"] for l in tr.records])

    def test_long_file_is_cut(self):
        body = "x" * (INSTRUCTIONS_MAX_CHARS + 5000)
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    files={"/work/repo/AGENTS.md": body})
        a.run(Task("t"))
        user = first_user(model)
        self.assertIn("Project instructions (from AGENTS.md):", user)
        self.assertIn("was cut here; 5000 more characters", user)
        self.assertLess(len(user), INSTRUCTIONS_MAX_CHARS + 2000)

    def test_empty_file_is_skipped(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    files={"/work/repo/AGENTS.md": "  \n",
                                           "/work/repo/CLAUDE.md": "Use Rust.\n"})
        a.run(Task("t"))
        self.assertIn("from CLAUDE.md", first_user(model))

    def test_system_prompt_names_the_owner(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    files={"/work/repo/AGENTS.md": "rules\n"})
        a.run(Task("t"))
        system = model.requests[0][0]["content"]
        self.assertIn("rules of the repository owner", system)
        self.assertIn("unless the task instructions say otherwise", system)


class RepositoryPictureTest(unittest.TestCase):
    def setUp(self):
        self.lim = Limits()

    def tools(self, files=None, **kw):
        m = MemExecution(files, **kw)
        return Tools(Workspace(m, "env", "/work/repo", self.lim)), m

    def test_all_three_parts(self):
        out = ("--- git status --short ---\n M a.py\n?? b.py\n"
               "--- git log --oneline -5 ---\nabc123 first\ndef456 second\n"
               "--- top-level entries ---\nlib magi tools \n")
        t, m = self.tools()
        m.stdout_for = out
        pic = repository_picture(t)
        self.assertIn("git status --short\n M a.py\n?? b.py", pic)
        self.assertIn("git log --oneline -5\nabc123 first", pic)
        self.assertIn("top-level entries\nlib magi tools", pic)
        self.assertEqual(len(m.execs), 1)  # one exec, not three

    def test_failed_git_command_is_left_out(self):
        out = ("--- git status --short ---\n"
               "--- git log --oneline -5 ---\nabc123 first\n"
               "--- top-level entries ---\nlib magi \n")
        t, m = self.tools()
        m.stdout_for = out
        pic = repository_picture(t)
        self.assertNotIn("git status", pic)
        self.assertIn("git log --oneline -5\nabc123 first", pic)
        self.assertIn("top-level entries", pic)

    def test_no_output_at_all(self):
        t, _ = self.tools()
        self.assertEqual(repository_picture(t), "")

    def test_broken_execution_service_is_not_an_error(self):
        class Broken(MemExecution):
            def exec(self, req):
                raise RuntimeError("service down")

            def read_file(self, req):
                raise RuntimeError("service down")

        model = FakeModel([reply(call("finish", summary="s")), reply(call("finish", summary="s"))])
        a = Apprentice(model, Tools(Workspace(Broken(), "env", "/work/repo", self.lim)), self.lim,
                       Transcript(Redactor()))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("finished", "s"))
        self.assertNotIn("Project instructions", first_user(model))
        self.assertNotIn("Repository:", first_user(model))
        # the review still happens without the execution service
        self.assertIn("(no diff)", model.requests[1][-1]["content"])

    def test_picture_is_in_the_first_message(self):
        out = ("--- git status --short ---\n M a.py\n"
               "--- git log --oneline -5 ---\nabc123 first\n"
               "--- top-level entries ---\nlib magi \n")
        m = MemExecution({"/work/repo/AGENTS.md": "rules\n"})
        m.stdout_for = out
        model = FakeModel([reply(call("finish", summary="s"))])
        tr = Transcript(Redactor())
        a = Apprentice(model, Tools(Workspace(m, "env", "/work/repo", self.lim)), self.lim, tr)
        a.run(Task("t"))
        user = first_user(model)
        self.assertIn("Repository:\ngit status --short\n M a.py", user)
        self.assertIn("abc123 first", user)
        self.assertIn("lib magi", user)
        # the picture comes after the task instructions
        self.assertGreater(user.index("Repository:"), user.index("Task instructions:"))


class LocalStartContextTest(unittest.TestCase):
    """The whole start context against a real git repository on this host."""

    def setUp(self):
        from magi_apprentice.local import LocalExecution
        self.dir = tempfile.TemporaryDirectory()
        d = self.dir.name
        Path(d, "AGENTS.md").write_text("Test the module you change.\n")
        Path(d, "src").mkdir()
        Path(d, "src", "a.py").write_text("x = 1\n")
        env = {k: v for k, v in os.environ.items() if "GIT_" not in k}
        subprocess.run(["git", "init", "-q"], cwd=d, check=True, env=env)
        subprocess.run(["git", "add", "AGENTS.md"], cwd=d, check=True, env=env)
        subprocess.run(["git", "-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "first"],
                       cwd=d, check=True, env=env)
        Path(d, "src", "b.py").write_text("y = 2\n")
        lim = Limits()
        m = LocalExecution()
        self.model = FakeModel([reply(call("finish", summary="s"))])
        self.a = Apprentice(self.model, Tools(Workspace(m, "local", d, lim)), lim, Transcript(Redactor()))

    def tearDown(self):
        self.dir.cleanup()

    def test_real_repository(self):
        self.a.run(Task("t"))
        user = first_user(self.model)
        self.assertIn("Project instructions (from AGENTS.md):\nTest the module you change.", user)
        self.assertIn("Repository:", user)
        self.assertIn("?? src/", user)             # git status --short
        self.assertIn("first", user)                # git log --oneline -5
        self.assertIn("AGENTS.md", user)            # top-level entries
        self.assertIn("src", user)


class LocalDiffTest(unittest.TestCase):
    """The self-review diff script against a real git repository on this host."""

    def setUp(self):
        from magi_apprentice.local import LocalExecution
        self.dir = tempfile.TemporaryDirectory()
        self.d = self.dir.name
        self.env = {k: v for k, v in os.environ.items() if "GIT_" not in k}
        Path(self.d, "src").mkdir()
        Path(self.d, "src", "a.py").write_text("x = 1\n")
        subprocess.run(["git", "init", "-q"], cwd=self.d, check=True, env=self.env)
        subprocess.run(["git", "add", "src/a.py"], cwd=self.d, check=True, env=self.env)
        subprocess.run(["git", "-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "first"],
                       cwd=self.d, check=True, env=self.env)
        # One modified file and one new file, so the diff needs the index trick.
        Path(self.d, "src", "a.py").write_text("x = 2\n")
        Path(self.d, "src", "b.py").write_text("y = 2\n")
        self.lim = Limits()
        self.ws = Workspace(LocalExecution(), "local", self.d, self.lim)

    def tearDown(self):
        self.dir.cleanup()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.d, env=self.env,
                              check=True, capture_output=True, text=True).stdout

    def test_diff_shows_new_files_and_restores_the_index(self):
        diff = workdir_diff(Tools(self.ws))
        self.assertIn("git diff --stat", diff)
        self.assertIn("+x = 2", diff)               # the modified file
        self.assertIn("+y = 2", diff)               # the new file
        # the new file is untracked again, as before the review
        self.assertEqual(self.git("status", "--short"), " M src/a.py\n?? src/b.py\n")
        # git stash works, as the system prompt tells the model
        self.assertIn("Saved working directory", self.git("stash", "-u"))
        self.git("stash", "pop")
        self.assertEqual(Path(self.d, "src", "b.py").read_text(), "y = 2\n")


class BuildPromptTest(unittest.TestCase):
    def test_order_of_sections(self):
        t = Task("do it", title="T", project_instructions="rules\n", project_instructions_file="AGENTS.md",
                 repository="git status --short\n M a\n", check_command="./c.sh")
        user = build_prompt(t, Limits())[1]["content"]
        order = [user.index(s) for s in ("Title: T", "Project instructions", "Task instructions:",
                                         "Work directory:", "Repository:", "Check command:")]
        self.assertEqual(order, sorted(order))

    def test_lessons_after_the_project_instructions(self):
        t = Task("do it", title="T", project_instructions="rules\n", project_instructions_file="AGENTS.md",
                 lessons="- rl-0001 Keep the state of one run: Set the flags at the start.")
        user = build_prompt(t, Limits())[1]["content"]
        self.assertIn("Lessons from earlier code reviews of this repository. Follow them:\n"
                      "- rl-0001 Keep the state of one run: Set the flags at the start.", user)
        # after the project instructions, before the task instructions
        self.assertLess(user.index("Project instructions"), user.index("Lessons from earlier"))
        self.assertLess(user.index("Lessons from earlier"), user.index("Task instructions:"))

    def test_no_lessons_section_without_lessons(self):
        for t in (Task("do it"), Task("do it", lessons=None), Task("do it", lessons="  ")):
            self.assertNotIn("Lessons from earlier", build_prompt(t, Limits())[1]["content"])


class RunnerStartContextTest(unittest.TestCase):
    """The start context through run_task, as the worker and one-shot mode
    call it."""

    def test_run_task_adds_the_start_context(self):
        from magi_apprentice.config import ModelConfig
        from magi_apprentice.runner import run_task
        m = MemExecution({"/work/repo/AGENTS.md": "Use Go modules.\n"})
        m.stdout_for = ("--- git status --short ---\n M a.py\n"
                        "--- git log --oneline -5 ---\nabc123 first\n"
                        "--- top-level entries ---\nlib magi \n")
        model = FakeModel([reply(call("finish", summary="s"))])
        out = run_task(Task("t"), execution=m, environment="env_1", model_cfg=ModelConfig(api_key="k-abcdef"),
                       limits=Limits(self_review=False), model=model)
        self.assertEqual(out.result.status, "finished")
        user = first_user(model)
        self.assertIn("Project instructions (from AGENTS.md):\nUse Go modules.", user)
        self.assertIn("Repository:\ngit status --short\n M a.py", user)
        self.assertIn("start_context", [json.loads(l)["kind"] for l in out.transcript.records])


if __name__ == "__main__":
    unittest.main()
