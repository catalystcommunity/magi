import json
import os
import tempfile
import unittest

import verdict


def run(**cases):
    """One run: case id -> (passed probes, failed probes)."""
    return {c: {"passed": set(p.split()), "failed": set(f.split())} for c, (p, f) in cases.items()}


class CompareTest(unittest.TestCase):
    def test_a_gain_larger_than_the_spread_of_the_baseline_is_a_pass(self):
        v = verdict.compare([run(pr2=("a b", "c d")), run(pr2=("a b c", "d"))],
                            [run(pr2=("a b c d", "")), run(pr2=("a b c d", ""))])
        self.assertEqual(v["verdict"], "pass")
        self.assertEqual((v["baseline"], v["candidate"], v["spread"]), ([0.5, 0.75], [1.0, 1.0], 0.25))

    def test_a_difference_inside_the_spread_is_no_change(self):
        v = verdict.compare([run(pr2=("a", "b c d")), run(pr2=("a b c", "d"))], [run(pr2=("a b c d", ""))])
        self.assertEqual(v["verdict"], "no change")

    def test_the_spread_is_by_case_and_of_the_side_that_has_more(self):
        # The totals of the baseline runs are equal: one case goes up and one goes down.
        base = [run(pr2=("a b", ""), pr3=("", "x y")), run(pr2=("", "a b"), pr3=("x y", ""))]
        v = verdict.compare(base, [run(pr2=("a b", ""), pr3=("x y", ""))])
        self.assertEqual((v["baseline"], v["spread"], v["verdict"]), ([1.0, 1.0], 2.0, "no change"))
        # The baseline runs are equal and the candidate runs are not.
        base = [run(pr2=("a", "b c d")), run(pr2=("a", "b c d"))]
        v = verdict.compare(base, [run(pr2=("a b c d", "")), run(pr2=("a", "b c d"))])
        self.assertEqual((v["spread"], v["verdict"]), (0.75, "no change"))

    def test_a_loss_larger_than_the_spread_is_a_fail(self):
        v = verdict.compare([run(pr2=("a b c", "d")), run(pr2=("a b d", "c"))], [run(pr2=("a", "b c d"))])
        self.assertEqual(v["verdict"], "fail")

    def test_a_probe_that_the_baseline_always_passes_and_the_candidate_never_is_a_fail(self):
        # The total of the candidate is higher, and the floor still decides.
        v = verdict.compare([run(pr2=("a", "b c d")), run(pr2=("a", "b c d"))],
                            [run(pr2=("b c d", "a")), run(pr2=("b c d", "a"))])
        self.assertEqual(v["verdict"], "fail")
        self.assertEqual(v["floor"], ["pr2:a"])

    def test_one_baseline_run_gives_no_verdict(self):
        v = verdict.compare([run(pr2=("a", "b"))], [run(pr2=("a b", ""))])
        self.assertEqual(v["verdict"], "no verdict")

    def test_each_case_counts_the_same_and_a_run_with_no_diff_scores_0(self):
        base = [run(pr2=("a", "b"), pr6=("x y z w", "")), run(pr2=("a", "b"), pr6=("", ""))]
        v = verdict.compare(base, [run(pr2=("a b", ""), pr6=("x y z w", ""))])
        self.assertEqual(v["baseline"], [1.5, 0.5])
        self.assertEqual(v["candidate"], [2.0])

    def test_only_the_cases_that_each_run_has(self):
        v = verdict.compare([run(pr2=("a", "b"), pr3=("a", "")), run(pr2=("a", "b"))], [run(pr2=("a", "b"))],)
        self.assertEqual(v["cases"], ["pr2"])
        v = verdict.compare([run(pr2=("a", "b"), pr3=("a", "")), run(pr2=("a", "b"), pr3=("a", ""))],
                            [run(pr2=("a", "b"), pr3=("a", ""))], cases=["pr3"])
        self.assertEqual(v["cases"], ["pr3"])


class CommandTest(unittest.TestCase):
    def test_the_command_reads_the_probe_results_and_states_the_runs(self):
        with tempfile.TemporaryDirectory() as d:
            for label, passed, failed in (("b1", ["a"], ["b"]), ("b2", ["a"], ["b"]), ("c1", ["a", "b"], [])):
                os.makedirs(os.path.join(d, label, "pr2"))
                with open(os.path.join(d, label, "pr2", "result.json"), "w") as f:
                    f.write("{}")
                with open(os.path.join(d, label, "pr2", "probe.json"), "w") as f:
                    json.dump({"passed": passed, "failed": [{"probe": p, "sentence": "s"} for p in failed]}, f)
            # A case with a result and no probe result: no diff.
            os.makedirs(os.path.join(d, "c1", "pr9"))
            with open(os.path.join(d, "c1", "pr9", "result.json"), "w") as f:
                f.write("{}")
            self.assertEqual(verdict.load(d, "c1")["pr9"], {"passed": set(), "failed": set()})
            v = verdict.compare([verdict.load(d, "b1"), verdict.load(d, "b2")], [verdict.load(d, "c1")])
            self.assertEqual(v["verdict"], "pass")
            text = verdict.report(v)
            self.assertIn("verdict: pass", text)
            self.assertIn("runs: 2 of the baseline, 1 of the candidate; cases: pr2", text)
            self.assertEqual(verdict.main(["--baseline", "b1,b2", "--candidate", "c1", "--results", d]), 0)
            self.assertEqual(verdict.main(["--baseline", "b1", "--candidate", "c1", "--results", d]), 3)


if __name__ == "__main__":
    unittest.main()
