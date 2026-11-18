import unittest

from fakes import FakeModel, reply

from magi_apprentice.model import ModelError
from magi_apprentice.review import LENSES, combine, drop_generated, lens_review, parse_sample, review, review_prompt


def answer(verdict: str, defects: str = "[]", met: str = "true") -> str:
    return ("The change looks at x.\n```json\n{\"requirements\": [{\"text\": \"x\", \"met\": " + met
            + ", \"evidence\": \"a.go:3\"}], \"defects\": " + defects + ", \"verdict\": \"" + verdict + "\"}\n```")


class ParseTest(unittest.TestCase):
    def test_verdicts(self):
        self.assertEqual(parse_sample(answer("approve")).verdict, "approve")
        self.assertEqual(parse_sample(answer("revise")).verdict, "revise")
        self.assertEqual(parse_sample("no json here").verdict, "invalid")
        self.assertEqual(parse_sample("```json\n{\"verdict\": \"maybe\"}\n```").verdict, "invalid")
        self.assertEqual(parse_sample("```json\n{broken\n```").verdict, "invalid")

    def test_a_bare_json_answer_counts(self):
        bare = '{"requirements": [{"text": "x", "met": true}], "defects": [], "verdict": "approve"}'
        self.assertEqual(parse_sample(" " + bare + "\n").verdict, "approve")
        self.assertEqual(parse_sample("I think " + bare).verdict, "invalid")

    def test_the_last_block_counts(self):
        text = answer("revise") + "\nOn second thought:\n" + answer("approve")
        self.assertEqual(parse_sample(text).verdict, "approve")

    def test_an_approve_with_a_blocker_or_an_unmet_requirement_is_revise(self):
        blocker = '[{"file": "a.go", "line": 3, "severity": "blocker", "problem": "nil map"}]'
        minor = '[{"file": "a.go", "line": 3, "severity": "minor", "problem": "name"}]'
        self.assertEqual(parse_sample(answer("approve", blocker)).verdict, "revise")
        self.assertEqual(parse_sample(answer("approve", minor)).verdict, "approve")
        self.assertEqual(parse_sample(answer("approve", met="false")).verdict, "revise")
        unchecked = '```json\n{"requirements": [{"text": "x"}], "defects": [], "verdict": "approve"}\n```'
        self.assertEqual(parse_sample(unchecked).verdict, "revise")
        none = '```json\n{"requirements": [], "defects": [], "verdict": "approve"}\n```'
        self.assertEqual(parse_sample(none).verdict, "revise")
        self.assertEqual(parse_sample(answer("approve", blocker)).defects[0]["problem"], "nil map")


class CombineTest(unittest.TestCase):
    def test_approve_only_when_all_samples_approve(self):
        a, r, i = parse_sample(answer("approve")), parse_sample(answer("revise")), parse_sample("x")
        self.assertEqual([combine(s).verdict for s in ([a, a, a], [a, a, r], [a, i], [])],
                         ["approve", "revise", "revise", "revise"])

    def test_review_asks_samples_times_and_a_model_error_cannot_approve(self):
        m = FakeModel([reply(content=answer("approve"))] * 3)
        rv = review(m, "do x", "diff", samples=3)
        self.assertEqual((rv.verdict, len(m.requests)), ("approve", 3))

        class Half:
            calls = 0

            def complete(self, messages, tools):
                Half.calls += 1
                if Half.calls == 2:
                    raise ModelError("HTTP 524", kind="unavailable")
                return reply(content=answer("approve"))
        rv = review(Half(), "do x", "diff", samples=3)
        self.assertEqual([s.verdict for s in rv.samples], ["approve", "invalid", "approve"])
        self.assertEqual(rv.verdict, "revise")


class PromptTest(unittest.TestCase):
    def test_a_long_patch_is_cut_and_the_cut_is_said(self):
        msgs = review_prompt("do x", "a" * 300, lessons="- rl-0001 one", max_patch_chars=100)
        user = msgs[1]["content"]
        self.assertIn("[... 200 characters of the diff are not shown", user)
        self.assertIn("## Review lessons of this repository\n\n- rl-0001 one", user)
        self.assertIn("a" * 50 + "\n\n[...", user)


    def test_generated_files_are_named_but_not_shown(self):
        patch = ("diff --git a/magi/compose/admin.go b/magi/compose/admin.go\n+real code\n"
                 "diff --git a/magi/api/types.gen.go b/magi/api/types.gen.go\n+generated\n"
                 "diff --git a/clients/python/magi_magi/types.py b/clients/python/magi_magi/types.py\n+gen py\n")
        out = drop_generated(patch)
        self.assertIn("+real code", out)
        self.assertNotIn("+generated", out)
        self.assertNotIn("+gen py", out)
        self.assertIn("[generated files changed, diff not shown: magi/api/types.gen.go, "
                      "clients/python/magi_magi/types.py]", out)
        self.assertEqual(drop_generated("diff --git a/a.go b/a.go\n+x\n"), "diff --git a/a.go b/a.go\n+x\n")



class LensTest(unittest.TestCase):
    def test_each_lens_asks_its_own_question_and_one_blocker_is_revise(self):
        blocker = '[{"file": "a.go", "line": 3, "severity": "blocker", "problem": "said twice"}]'
        no_reqs = '```json\n{"requirements": [], "defects": [], "verdict": "approve"}\n```'
        script = [reply(content=answer("approve"))] + [reply(content=no_reqs)] * 3 + \
            [reply(content=answer("approve", blocker))]
        m = FakeModel(script)
        rv = lens_review(m, "do x", "diff")
        self.assertEqual(len(m.requests), len(LENSES))
        self.assertIn("Your question: " + LENSES["text"], m.requests[1][0]["content"])
        self.assertEqual([s.verdict for s in rv.samples], ["approve"] * 4 + ["revise"])
        self.assertEqual(rv.verdict, "revise")
        self.assertEqual((rv.defects[0]["lens"], rv.defects[0]["problem"]), ("scope", "said twice"))

    def test_only_the_requirements_lens_must_list_requirements(self):
        no_reqs = '```json\n{"requirements": [], "defects": [], "verdict": "approve"}\n```'
        m = FakeModel([reply(content=no_reqs)] * len(LENSES))
        self.assertEqual([s.verdict for s in lens_review(m, "do x", "diff").samples],
                         ["revise"] + ["approve"] * 4)

    def test_a_requirements_defect_is_revise_even_when_minor(self):
        req = '[{"text": "use AdminGetRunRequest", "met": true, "evidence": "a.csil:3"}]'
        minor = '[{"file": "a.csil", "line": 3, "severity": "minor", "problem": "a copy of the type"}]'
        ans = '```json\n{"requirements": %s, "defects": %s, "verdict": "approve"}\n```'
        m = FakeModel([reply(content=ans % (req, minor))] + [reply(content=ans % ("[]", minor))] * 4)
        rv = lens_review(m, "do x", "diff")
        self.assertEqual([s.verdict for s in rv.samples], ["revise"] + ["approve"] * 4)
        self.assertEqual(rv.verdict, "revise")

    def test_a_broken_lens_answer_is_asked_once_more(self):
        ok = '```json\n{"requirements": [{"text": "x", "met": true, "evidence": "a:1"}], "defects": [], ' \
             '"verdict": "approve"}\n```'
        broken = '```json\n{"requirements": [], "defects": [] "verdict": "approve"}\n```'
        # Lens 2 breaks twice and stays invalid; lens 3 breaks once.
        m = FakeModel([reply(content=ok)] + [reply(content=broken)] * 3 + [reply(content=ok)] * 3)
        rv = lens_review(m, "do x", "diff")
        self.assertEqual(len(m.requests), len(LENSES) + 2)
        self.assertEqual([s.verdict for s in rv.samples], ["approve", "invalid"] + ["approve"] * 3)

    def test_a_raw_newline_in_a_string_is_read(self):
        s = parse_sample('{"requirements": [{"text": "a\nb", "met": true}], "defects": [], "verdict": "approve"}')
        self.assertEqual(s.verdict, "approve")

    def test_context_goes_in_the_prompt(self):
        user = review_prompt("do x", "diff", context="func endNote() {}")[1]["content"]
        self.assertIn("## Functions that the change calls (not changed)", user)
        self.assertIn("func endNote() {}", user)


if __name__ == "__main__":
    unittest.main()
