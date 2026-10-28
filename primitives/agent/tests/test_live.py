"""Live smoke test with a real model. Runs only when MAGI_TEST_MODEL=1.

Uses MAGI_MODEL_BASE_URL, MAGI_MODEL_NAME, and MAGI_MODEL_API_KEY_FILE.
Tools run as local subprocesses in a temporary directory.
"""
import os
import subprocess
import sys
import tempfile
import time
import unittest

from fakes import LocalExecution

from magi_agent.agent import Task
from magi_agent.config import Limits, ModelConfig
from magi_agent.runner import run_task

EXISTING = '''def slugify(text):
    """Return a lowercase slug: words joined by "-"."""
    return text.lower().replace(" ", "_")
'''


@unittest.skipUnless(os.environ.get("MAGI_TEST_MODEL") == "1", "set MAGI_TEST_MODEL=1 to run")
class LiveModelTest(unittest.TestCase):
    def test_small_python_task(self):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "textutil.py"), "w") as f:
                f.write(EXISTING)
            task = Task(
                instructions=("In textutil.py, slugify has a bug: it must join words with '-', not '_'. "
                              "Fix it. Then add a function word_count(text) that returns the number of "
                              "whitespace-separated words. Add test_textutil.py with unittest tests for "
                              "both functions. Make the tests pass."),
                workdir=d, check_command=f"{sys.executable} -m unittest -q")
            t0 = time.monotonic()
            out = run_task(task, execution=LocalExecution(), environment="local",
                           model_cfg=ModelConfig.from_env(), limits=Limits(max_steps=30, max_seconds=900),
                           transcript_path=os.environ.get("MAGI_TEST_TRANSCRIPT"))
            secs = time.monotonic() - t0
            r = out.result
            print(f"\nlive: status={r.status} steps={r.steps} tool_calls={r.tool_calls} "
                  f"check_passed={r.check_passed} seconds={secs:.1f} usage={r.usage}", file=sys.stderr)
            self.assertEqual(r.status, "finished", r.summary)
            self.assertTrue(r.check_passed)
            check = subprocess.run([sys.executable, "-m", "unittest", "-q"], cwd=d, capture_output=True)
            self.assertEqual(check.returncode, 0, check.stderr)
            self.assertTrue(os.path.exists(os.path.join(d, "test_textutil.py")))
            sys.path.insert(0, d)
            try:
                import textutil
                self.assertEqual(textutil.slugify("Hello Big World"), "hello-big-world")
                self.assertEqual(textutil.word_count(" a  b c "), 3)
            finally:
                sys.path.remove(d)
                sys.modules.pop("textutil", None)


if __name__ == "__main__":
    unittest.main()
