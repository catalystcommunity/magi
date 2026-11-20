#!/usr/bin/env python3
"""Tests of the comparison rules of verdict.py."""
import importlib.util
import os
import unittest

spec = importlib.util.spec_from_file_location("verdict", os.path.join(os.path.dirname(os.path.abspath(__file__)), "verdict.py"))
verdict = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verdict)


def results(table, errors=()):
    """table: {case: "110"}: one character for each run, 1 is a pass."""
    return {c: [{"case": c, "pass": ch == "1", "model_errors": ["x"] if (c, i) in errors else []}
                for i, ch in enumerate(runs)] for c, runs in table.items()}


class Compare(unittest.TestCase):
    def test_same_results_pass(self):
        t = {"a": "111", "b": "111", "c": "010"}
        v = verdict.compare(results(t), results(t), {"a"})
        self.assertEqual(v["verdict"], "pass")
        self.assertIn("no change", " ".join(v["reasons"]))

    def test_core_case_that_the_candidate_breaks_fails(self):
        base = {"a": "111", "b": "111", "c": "111", "d": "000"}
        cand = {"a": "001", "b": "111", "c": "111", "d": "111"}  # the total is better
        v = verdict.compare(results(base), results(cand), {"a"})
        self.assertEqual(v["verdict"], "fail")
        self.assertIn("core case a", " ".join(v["reasons"]))

    def test_core_floor_needs_a_baseline_that_passes_each_run(self):
        base = {"a": "110", "b": "111"}
        cand = {"a": "100", "b": "111"}
        self.assertEqual(verdict.compare(results(base), results(cand), {"a"})["verdict"], "pass")

    def test_one_missed_run_of_a_core_case_is_not_the_floor(self):
        base = {"a": "111", "b": "111"}
        cand = {"a": "110", "b": "111"}
        self.assertEqual(verdict.compare(results(base), results(cand), {"a"})["verdict"], "pass")

    def test_a_case_that_is_not_core_fails_only_through_the_total(self):
        base = {c: "111" for c in "abcdefgh"}
        cand = dict(base, a="000")  # 3 runs less than the baseline; the noise is 1 for a run
        self.assertEqual(verdict.compare(results(base), results(cand), set())["verdict"], "fail")
        cand = dict(base, a="110")
        self.assertEqual(verdict.compare(results(base), results(cand), set())["verdict"], "pass")

    def test_noise_of_the_baseline_widens_the_limit(self):
        # The totals of the baseline runs are 8, 5, 8: the noise is 3 for a run.
        base = {"a": "101", "b": "101", "c": "101", "d": "111", "e": "111", "f": "111", "g": "111", "h": "111"}
        cand = dict(base, d="000")  # 3 less
        self.assertEqual(verdict.compare(results(base), results(cand), set())["verdict"], "pass")

    def test_better_total_is_reported(self):
        base = {c: "000" for c in "abcd"}
        cand = {c: "111" for c in "abcd"}
        v = verdict.compare(results(base), results(cand), set())
        self.assertEqual(v["verdict"], "pass")
        self.assertIn("better", " ".join(v["reasons"]))

    def test_model_error_is_not_a_result(self):
        t = {"a": "111"}
        v = verdict.compare(results(t), results(t, errors={("a", 1)}), {"a"})
        self.assertEqual(v["verdict"], "error")

    def test_different_cases_are_an_error(self):
        v = verdict.compare(results({"a": "11"}), results({"a": "11", "b": "11"}), set())
        self.assertEqual(v["verdict"], "error")
        v = verdict.compare(results({"a": "11"}), results({"a": "111"}), set())
        self.assertEqual(v["verdict"], "error")


if __name__ == "__main__":
    unittest.main()
