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
        """S1: the limit is 20000."""
        self.assertEqual(limit(), 20000)

    def test_the_name_is_kept(self):
        # S2: the name is limit.
        self.assertTrue(callable(limit))
'''
TASK = "Cut the file at 20000 characters.\nKeep the name of the function."

PY_OUTPUT = '''test_the_limit_is_20000 (test_tester_probe.ProbeTest.test_the_limit_is_20000)
S1: the limit is 20000. ... FAIL
test_the_name_is_kept (test_tester_probe.ProbeTest.test_the_name_is_kept) ... ok
test_it_runs (test_tester_probe.ProbeTest.test_it_runs) ... ERROR

======================================================================
ERROR: test_it_runs (test_tester_probe.ProbeTest.test_it_runs)
----------------------------------------------------------------------
Traceback (most recent call last):
  File "t.py", line 9, in test_it_runs
    limit(1)
TypeError: limit() takes 0 positional arguments but 1 was given

======================================================================
FAIL: test_the_limit_is_20000 (test_tester_probe.ProbeTest.test_the_limit_is_20000)
S1: the limit is 20000.
----------------------------------------------------------------------
Traceback (most recent call last):
  File "t.py", line 6, in test_the_limit_is_20000
    self.assertEqual(limit(), 20000)
AssertionError: 8000 != 20000

----------------------------------------------------------------------
Ran 3 tests in 0.000s

FAILED (failures=1, errors=1)
'''

GO_OUTPUT = '''=== RUN   TestProbeImport
    tester_probe_test.go:31: S3: a failed import gave no error
--- FAIL: TestProbeImport (0.00s)
=== RUN   TestProbeCount
--- PASS: TestProbeCount (0.00s)
FAIL
'''

GO_PANIC = '''=== RUN   TestProbeCount
--- PASS: TestProbeCount (0.00s)
=== RUN   TestProbeNil
panic: runtime error: invalid memory address or nil pointer dereference [recovered]

goroutine 7 [running]:
github.com/example/magi/compose.ImportBaseKnowledge(...)
	/work/repo/magi/compose/baseknowledge.go:41 +0x1d
github.com/example/magi/compose.TestProbeNil(0xc000102000)
	/work/repo/magi/compose/tester_probe_test.go:12 +0x2a
FAIL	github.com/example/magi/compose	0.004s
'''


def git(d, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    return subprocess.run(["git", "-c", "user.email=a@example.local", "-c", "user.name=t", *args], cwd=d,
                          check=True, env=env, capture_output=True, text=True).stdout


def records(tr, kind):
    return [d for d in (json.loads(line) for line in tr.records) if d["kind"] == kind]


class PartsTest(unittest.TestCase):
    def test_the_sentences_of_the_task_without_the_acceptance_checks(self):
        text = ("# Title of it\n\n1. Cut it at 20000 characters with a note. A missing file is not an error.\n"
                "- Keep the change small: only one file.\n\nDone.\n\nAcceptance checks:\n\n1. \"Cut it\" Result: a fake Tools.")
        self.assertEqual(tester.sentences(text), ["Cut it at 20000 characters with a note.",
                                                  "A missing file is not an error.",
                                                  "Keep the change small: only one file."])

    def test_the_failed_python_tests_with_the_last_lines_of_each_failure(self):
        failed = tester.failed_tests("tests/test_tester_probe.py", PY_OUTPUT)
        self.assertEqual(sorted(failed), ["test_it_runs", "test_the_limit_is_20000"])
        self.assertIn("AssertionError: 8000 != 20000", failed["test_the_limit_is_20000"])
        self.assertIn("TypeError", failed["test_it_runs"])

    def test_the_failed_go_tests(self):
        failed = tester.failed_tests("magi/compose/tester_probe_test.go", GO_OUTPUT)
        self.assertEqual(list(failed), ["TestProbeImport"])
        self.assertIn("S3: a failed import gave no error", failed["TestProbeImport"])
        failed = tester.failed_tests("magi/compose/tester_probe_test.go", GO_PANIC)
        self.assertEqual(list(failed), ["TestProbeNil"])
        self.assertTrue(tester.crashed(failed["TestProbeNil"], ["magi/compose/baseknowledge.go"]))
        self.assertFalse(tester.crashed(failed["TestProbeNil"], ["magi/compose/other.go"]))

    def test_only_an_exception_through_the_changed_code_is_a_crash(self):
        failed = tester.failed_tests("tests/test_tester_probe.py", PY_OUTPUT)
        changed = ["pkg/a.py", "tests/test_a.py"]
        in_code = failed["test_it_runs"].replace('File "t.py", line 9, in test_it_runs',
                                                 'File "t.py", line 9, in test_it_runs\n    x\n  File "/w/pkg/a.py", line 2, in limit')
        self.assertTrue(tester.crashed(in_code, changed))
        # The exception is in the test itself.
        self.assertFalse(tester.crashed(failed["test_it_runs"], changed))
        # An assertion is a judgment of the tester.
        self.assertFalse(tester.crashed(failed["test_the_limit_is_20000"].replace("t.py", "/w/pkg/a.py"), changed))
        # The code that stopped is not a file of the change.
        self.assertFalse(tester.crashed(in_code, ["pkg/b.py"]))
        # The last frame is in a test file of the change: a helper of the tests stopped.
        self.assertFalse(tester.crashed(in_code + '\n  File "/w/tests/test_a.py", line 3, in helper', changed))

    def test_a_finding_has_the_sentence_the_failure_and_the_test(self):
        found = tester.blockers("tests/test_tester_probe.py", PROBE,
                                {"test_the_limit_is_20000": "AssertionError: 8000 != 20000"}, tester.sentences(TASK))
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0]["lens"], "tester")
        self.assertIn('"Cut the file at 20000 characters."', found[0]["problem"])
        self.assertIn("AssertionError: 8000 != 20000", found[0]["problem"])
        self.assertIn("stops with an exception in the changed code", found[0]["problem"])
        self.assertIn("self.assertEqual(limit(), 20000)", found[0]["problem"])

    def test_the_findings_have_a_limit(self):
        self.assertEqual(len(tester.blockers("t.py", "", {f"test_{i}": "" for i in range(9)}, [])), tester.MAX_ITEMS)

    def test_the_command_runs_only_the_file_of_the_tester(self):
        self.assertEqual(tester.run_command("primitives/apprentice/tests/test_tester_probe.py", ""),
                         "cd primitives/apprentice && python3 -m unittest discover -s tests -p test_tester_probe.py -v 2>&1")
        self.assertEqual(tester.run_command("magi/compose/tester_probe_test.go", "func TestA(t *testing.T) {\n}\n"),
                         "cd magi/compose && go test -count=1 -v -run '^(TestA)$' . 2>&1")


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
        Path(self.d, "a.py").write_text("def limit():\n    return 8000 + None\n")
        Path(self.d, "new.py").write_text("x = 1\n")
        self.tools = Tools(Workspace(LocalExecution(), "local", self.d, Limits()))
        self.tr = Transcript(Redactor())

    def run_tester(self, *replies):
        self.model = FakeModel(list(replies))
        return tester.run(self.model, self.tools, TASK, Limits(), self.tr)

    def test_a_failed_test_is_a_finding_and_the_work_directory_is_as_before(self):
        before = git(self.d, "status", "--short")
        # The summary says that all is well: the run of the harness decides.
        found = self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                                reply(call("write_file", path="new.py", content="x = 2\n")),
                                reply(call("finish", summary="All tests pass.")))
        self.assertEqual(len(found), 1)
        self.assertIn("self.assertEqual(limit(), 20000)", found[0]["problem"])
        self.assertIn("TypeError: unsupported operand", found[0]["problem"])
        # The tester cannot change the work: its test file is gone, and its
        # edit of the change is gone too.
        self.assertFalse(Path(self.d, "test_tester_probe.py").exists())
        self.assertEqual(Path(self.d, "a.py").read_text(), "def limit():\n    return 8000 + None\n")
        self.assertEqual(Path(self.d, "new.py").read_text(), "x = 1\n")
        self.assertEqual(git(self.d, "status", "--short"), before)
        rec = records(self.tr, "tester")[0]
        self.assertEqual((rec["status"], rec["crashed"], rec["sentences"], rec["restored"]),
                         ("finished", ["test_the_limit_is_20000"], 2, True))

    def test_a_test_that_fails_on_an_assertion_is_in_the_record_only(self):
        # The assertions of the tester were wrong too often to send them.
        Path(self.d, "a.py").write_text("def limit():\n    return 8000\n")
        found = self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                                reply(call("finish", summary="One finding.")))
        self.assertEqual(found, [])
        rec = records(self.tr, "tester")[0]
        self.assertEqual(rec["crashed"], [])
        self.assertIn("AssertionError: 8000 != 20000", rec["failed"]["test_the_limit_is_20000"])

    def test_the_tester_gets_the_sentences_with_numbers(self):
        self.run_tester(*[reply(call("finish", summary="x")) for _ in range(4)])
        first = self.model.requests[0][1]["content"]
        self.assertIn("S1. Cut the file at 20000 characters.\nS2. Keep the name of the function.", first)

    def test_a_sentence_with_no_test_sends_the_finish_back(self):
        one = PROBE.replace("# S2: the name is limit.", "# the name")
        self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=one)),
                        reply(call("finish", summary="done")),
                        reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                        reply(call("finish", summary="done")))
        self.assertIn("no test and no 'no test' line in test_tester_probe.py: S2", self.model.requests[2][-1]["content"])
        self.assertEqual(records(self.tr, "tester")[0]["crashed"], ["test_the_limit_is_20000"])

    def test_a_fake_in_the_test_file_sends_the_finish_back(self):
        fake = PROBE + "\n\nclass FakeTools:\n    pass\n"
        self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=fake)),
                        reply(call("finish", summary="done")),
                        reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                        reply(call("finish", summary="done")))
        note = self.model.requests[2][-1]["content"]
        self.assertIn("has a fake, a stub, or a mock of its own", note)
        self.assertIn("class FakeTools:", note)

    def test_the_diff_rules_do_not_run_on_the_finish_of_the_tester(self):
        # The diff is the change of the worker: a rule on it is not for the tester.
        Path(self.d, "test_new.py").write_text("class FakeTools:\n    pass\n")
        self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                        reply(call("finish", summary="done")))
        self.assertEqual(records(self.tr, "tester_diff_gate"), [])
        self.assertEqual(records(self.tr, "tester")[0]["status"], "finished")

    def test_the_records_of_the_tester_have_their_own_kinds(self):
        self.run_tester(reply(call("write_file", path="test_tester_probe.py", content=PROBE)),
                        reply(call("finish", summary="done")))
        ks = [json.loads(line)["kind"] for line in self.tr.records]
        self.assertIn("tester_result", ks)
        self.assertNotIn("result", ks)

    def test_a_tester_with_no_test_file_finds_nothing(self):
        found = self.run_tester(*[reply(content="I am done.", finish_reason="stop") for _ in range(8)])
        self.assertEqual(found, [])
        self.assertEqual(Path(self.d, "a.py").read_text(), "def limit():\n    return 8000 + None\n")

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
