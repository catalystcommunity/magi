import json
import os
import tempfile
import unittest
from pathlib import Path

from fakes import LocalExecution, MemExecution

from magi_agent.config import Limits
from magi_agent.tools import Tools, Workspace, truncate


def tools(files=None, **kw) -> tuple[Tools, MemExecution]:
    m = MemExecution(files, **kw)
    return Tools(Workspace(m, "env_1", "/work/repo", Limits())), m


def run(t: Tools, name: str, **args):
    return t.call(name, json.dumps(args))


class TruncateTest(unittest.TestCase):
    def test_short_text_unchanged(self):
        self.assertEqual(truncate("abc", 10), "abc")

    def test_keeps_head_and_tail(self):
        s = "H" * 500 + "M" * 5000 + "T" * 500
        out = truncate(s, 1000)
        self.assertTrue(out.startswith("H" * 400))
        self.assertTrue(out.endswith("T" * 500))
        self.assertIn("chars cut", out)
        self.assertLess(len(out), 1100)


class EditFileTest(unittest.TestCase):
    def setUp(self):
        self.t, self.m = tools({"/work/repo/a.py": "x = 1\ny = 2\nx = 1\n"})

    def body(self) -> str:
        return self.m.files["/work/repo/a.py"].decode()

    def test_unique_replace(self):
        r = run(self.t, "edit_file", path="a.py", old_string="y = 2", new_string="y = 3")
        self.assertTrue(r.ok, r.content)
        self.assertEqual(self.body(), "x = 1\ny = 3\nx = 1\n")

    def test_not_unique_is_rejected(self):
        r = run(self.t, "edit_file", path="a.py", old_string="x = 1", new_string="x = 9")
        self.assertFalse(r.ok)
        self.assertIn("occurs 2 times", r.content)
        self.assertEqual(self.body(), "x = 1\ny = 2\nx = 1\n")

    def test_replace_all(self):
        r = run(self.t, "edit_file", path="a.py", old_string="x = 1", new_string="x = 9", replace_all=True)
        self.assertTrue(r.ok)
        self.assertEqual(self.body(), "x = 9\ny = 2\nx = 9\n")

    def test_not_found(self):
        r = run(self.t, "edit_file", path="a.py", old_string="z = 1", new_string="z = 2")
        self.assertFalse(r.ok)
        self.assertIn("not found", r.content)

    def test_whitespace_hint(self):
        r = run(self.t, "edit_file", path="a.py", old_string="  y = 2  ", new_string="y = 3")
        self.assertIn("whitespace", r.content)

    def test_same_strings(self):
        self.assertFalse(run(self.t, "edit_file", path="a.py", old_string="y", new_string="y").ok)

    def test_missing_file(self):
        r = run(self.t, "edit_file", path="nope.py", old_string="a", new_string="b")
        self.assertFalse(r.ok)
        self.assertIn("cannot read", r.content)


class FileToolsTest(unittest.TestCase):
    def test_read_with_line_numbers_and_range(self):
        t, _ = tools({"/work/repo/f.txt": "\n".join(f"line{i}" for i in range(1, 11))})
        r = run(t, "read_file", path="f.txt", offset=3, limit=2)
        self.assertIn("     3\tline3", r.content)
        self.assertIn("     4\tline4", r.content)
        self.assertNotIn("line5", r.content)
        self.assertIn("lines 3-4 of 10", r.content)

    def test_absolute_and_relative_paths(self):
        t, m = tools()
        run(t, "write_file", path="sub/x.go", content="package x\n")
        run(t, "write_file", path="/tmp/y", content="y")
        self.assertIn("/work/repo/sub/x.go", m.files)
        self.assertIn("/tmp/y", m.files)

    def test_fallback_to_exec_without_file_ops(self):
        t, m = tools({"/work/repo/a.txt": "hello"}, file_ops=False)
        self.assertIn("hello", run(t, "read_file", path="a.txt").content)
        self.assertTrue(run(t, "write_file", path="b.txt", content="bye").ok)
        self.assertEqual(m.files["/work/repo/b.txt"], b"bye")

    def test_unknown_tool_and_bad_json(self):
        t, _ = tools()
        self.assertIn("unknown tool", t.call("rm_rf", "{}").content)
        self.assertIn("invalid JSON", t.call("bash", "{oops").content)
        self.assertIn("bad arguments", t.call("bash", '{"cmd": "ls"}').content)

    def test_finish(self):
        t, _ = tools()
        r = run(t, "finish", summary="done it")
        self.assertTrue(r.finished)
        self.assertEqual(r.content, "done it")


class LocalToolsTest(unittest.TestCase):
    """bash, list_files, and search through the subprocess-backed client."""

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        d = self.dir.name
        os.makedirs(f"{d}/pkg")
        Path(f"{d}/pkg/a.go").write_text("package pkg\n\nfunc Hello() string { return \"hi\" }\n")
        Path(f"{d}/b.py").write_text("def hello():\n    return 'hi'\n")
        os.makedirs(f"{d}/.git")
        Path(f"{d}/.git/config").write_text("hello")
        self.t = Tools(Workspace(LocalExecution(), "local", d, Limits(tool_output_chars=1000)))

    def tearDown(self):
        self.dir.cleanup()

    def test_bash_exit_code_and_output(self):
        r = run(self.t, "bash", command="echo out; echo err >&2; exit 3")
        self.assertFalse(r.ok)
        self.assertIn("exit_code: 3", r.content)
        self.assertIn("out", r.content)
        self.assertIn("err", r.content)

    def test_bash_output_is_capped(self):
        r = run(self.t, "bash", command="seq 1 30000")
        self.assertLess(len(r.content), 1200)
        self.assertIn("30000", r.content)  # the tail is kept
        self.t.ws.limits.exec_output_bytes = 1000
        r = run(self.t, "bash", command="seq 1 30000")
        self.assertIn("end is lost", r.content)

    def test_bash_timeout(self):
        r = run(self.t, "bash", command="sleep 5", timeout_seconds=1)
        self.assertIn("timed out", r.content)

    def test_list_files(self):
        r = run(self.t, "list_files")
        self.assertIn("pkg/a.go", r.content)
        self.assertIn("b.py", r.content)
        self.assertNotIn(".git/config", r.content)
        r = run(self.t, "list_files", pattern="*.go")
        self.assertIn("pkg/a.go", r.content)
        self.assertNotIn("b.py", r.content)

    def test_search(self):
        r = run(self.t, "search", pattern="hel+o", glob="*.py")
        self.assertIn("b.py:1:", r.content)
        self.assertNotIn("a.go", r.content)
        self.assertNotIn(".git", r.content)
        self.assertEqual(run(self.t, "search", pattern="zzzz_nomatch").content, "no matches")

    def test_secret_env_not_passed(self):
        os.environ["MAGI_TEST_API_KEY"] = "s3cr3t-value-123"
        try:
            r = run(self.t, "bash", command="env")
            self.assertNotIn("s3cr3t-value-123", LocalExecution().env.get("MAGI_TEST_API_KEY", ""))
            self.assertNotIn("MAGI_TEST_API_KEY", r.content)
        finally:
            del os.environ["MAGI_TEST_API_KEY"]


if __name__ == "__main__":
    unittest.main()
