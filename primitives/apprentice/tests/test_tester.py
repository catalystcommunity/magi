import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice import tester
from magi_apprentice.apprentice import Task
from magi_apprentice.config import Limits, ModelConfig
from magi_apprentice.local import LocalExecution
from magi_apprentice.runner import run_task
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

PROBE = '''import unittest

from a import limit


class ProbeTest(unittest.TestCase):
    def test_the_limit_is_20000(self):
        self.assertEqual(limit(), 20000)

    def test_the_name_is_kept(self):
        self.assertTrue(True)
'''


def git(d, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    return subprocess.run(["git", "-c", "user.email=a@example.local", "-c", "user.name=t", *args], cwd=d,
                          check=True, env=env, capture_output=True, text=True).stdout


def kinds(tr):
    return [json.loads(line)["kind"] for line in tr.records]


class SummaryTest(unittest.TestCase):
    def test_the_summary_is_the_json_object_in_the_text(self):
        self.assertEqual(tester.parse_summary('Done.\n{"file": "t.py", "failing": []}\n'), {"file": "t.py", "failing": []})

    def test_a_summary_with_no_json_gives_nothing(self):
        for text in ("", "all tests pass", "{not json}", "[1, 2]"):
            self.assertEqual(tester.parse_summary(text), {})

    def test_a_finding_carries_the_sentence_and_the_test(self):
        report = {"file": "tests/test_tester_probe.py",
                  "failing": [{"test": "ProbeTest.test_the_limit_is_20000", "requirement": "Cut it at 20000.",
                               "why": "The code cuts at 8000."}]}
        found = tester.blockers(report, PROBE)
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0]["lens"], "tester")
        self.assertIn('"Cut it at 20000."', found[0]["problem"])
        self.assertIn("The code cuts at 8000.", found[0]["problem"])
        self.assertIn("self.assertEqual(limit(), 20000)", found[0]["problem"])

    def test_a_finding_with_no_test_in_the_file_is_dropped(self):
        report = {"file": "t.py", "failing": [{"test": "test_not_there", "requirement": "r", "why": "w"}, "text"]}
        self.assertEqual(tester.blockers(report, PROBE), [])

    def test_the_findings_have_a_limit(self):
        source = "class T:\n" + "".join(f"    def test_{i}(self):\n        self.fail()\n" for i in range(9))
        report = {"file": "t.py", "failing": [{"test": f"test_{i}"} for i in range(9)]}
        self.assertEqual(len(tester.blockers(report, source)), tester.MAX_ITEMS)


class RunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.d = self.tmp.name
        Path(self.d, "a.py").write_text("def limit():\n    return 1\n")
        git(self.d, "init", "-q")
        git(self.d, "add", "a.py")
        git(self.d, "commit", "-qm", "first")
        # The change of the worker: one changed file and one new file.
        Path(self.d, "a.py").write_text("def limit():\n    return 8000\n")
        Path(self.d, "new.py").write_text("x = 1\n")
        self.tools = Tools(Workspace(LocalExecution(), "local", self.d, Limits()))
        self.tr = Transcript(Redactor())

    def run_tester(self, *replies):
        return tester.run(FakeModel(list(replies)), self.tools, "Cut it at 20000.", Limits(), self.tr)

    def test_a_failed_test_is_a_finding_and_the_work_directory_is_as_before(self):
        before = git(self.d, "status", "--short")
        summary = json.dumps({"file": "test_tester_probe.py", "passed": 1,
                              "failing": [{"test": "test_the_limit_is_20000", "requirement": "Cut it at 20000.",
                                           "why": "The code returns 8000."}]})
        found = self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                                reply(call("write_file", path="a.py", content="def limit():\n    return 20000\n")),
                                reply(call("finish", summary=summary)))
        self.assertEqual(len(found), 1)
        self.assertIn("self.assertEqual(limit(), 20000)", found[0]["problem"])
        # The tester cannot change the work: its test file is gone, and its
        # edit of the code is gone too.
        self.assertFalse(Path(self.d, "test_tester_probe.py").exists())
        self.assertEqual(Path(self.d, "a.py").read_text(), "def limit():\n    return 8000\n")
        self.assertEqual(Path(self.d, "new.py").read_text(), "x = 1\n")
        self.assertEqual(git(self.d, "status", "--short"), before)

    def test_the_records_of_the_tester_have_their_own_kinds(self):
        self.run_tester(reply(call("finish", summary='{"file": "", "passed": 0, "failing": []}')))
        ks = kinds(self.tr)
        self.assertIn("tester_result", ks)
        self.assertNotIn("result", ks)
        rec = [json.loads(line) for line in self.tr.records if json.loads(line)["kind"] == "tester"][0]
        self.assertEqual((rec["status"], rec["claimed"], rec["findings"], rec["restored"]), ("finished", 0, [], True))

    def test_a_tester_that_does_not_finish_finds_nothing(self):
        self.assertEqual(self.run_tester(*[reply(content="I am done.", finish_reason="stop") for _ in range(8)]), [])
        self.assertEqual(Path(self.d, "a.py").read_text(), "def limit():\n    return 8000\n")

    def test_no_snapshot_means_no_tester_run(self):
        tools = Tools(Workspace(MemExecution(), "env", "/w", Limits()))
        model = FakeModel([])
        self.assertEqual(tester.run(model, tools, "t", Limits(), self.tr), [])
        self.assertEqual(model.requests, [])


class RunTaskTesterTest(unittest.TestCase):
    def run_task(self, limits):
        calls = []

        def fake_tester(model, tools, task, lim, tr):
            calls.append(task)
            return [{"lens": "tester", "file": "t.py", "line": None, "problem": "A test fails."}]

        def fake_pre_review(model, tools, task, base, lessons="", samples=1):
            return {"verdict": "approve", "seconds": 1.0,
                    "blockers": [{"lens": "tests", "file": "a_test.go", "line": 9, "problem": "No test of B."}]}
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        with mock.patch("magi_apprentice.runner.pre_review", fake_pre_review), \
                mock.patch("magi_apprentice.runner.tester.run", fake_tester):
            run_task(Task("make A return 3"), execution=MemExecution(), environment="env_1",
                     model_cfg=ModelConfig(name="worker", api_key="k-abcdef"), limits=limits,
                     model=model, review_model="qwen-review")
        return calls, model.requests[1][-1]["content"]

    def test_the_findings_go_before_the_items_of_the_review(self):
        calls, note = self.run_task(Limits(spec_step=False, tester=True))
        self.assertEqual(calls, ["make A return 3"])
        self.assertLess(note.index("- [tester] t.py: A test fails."), note.index("- [tests] a_test.go:9 No test of B."))

    def test_the_tester_is_off_by_default(self):
        calls, note = self.run_task(Limits(spec_step=False))
        self.assertEqual(calls, [])
        self.assertNotIn("[tester]", note)

    def test_the_limit_comes_from_the_environment(self):
        with mock.patch.dict(os.environ, {"MAGI_APPRENTICE_TESTER": "true"}):
            self.assertTrue(Limits.from_env().tester)
        self.assertFalse(Limits().tester)


if __name__ == "__main__":
    unittest.main()
