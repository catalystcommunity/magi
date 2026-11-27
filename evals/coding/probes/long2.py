"""Probes of case long2. Target: docs"""
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def text(path):
    p = ROOT / path
    return p.read_text() if p.exists() else ""


def one_line(s):
    return " ".join(s.split())


class Long2(unittest.TestCase):
    def test_the_readme_of_the_primitive_has_the_decision_model(self):
        """primitives/inference/README.md, section "Decide": "decide": "head", the one call to /v1/systemone, and its limits."""
        section = one_line(text("primitives/inference/README.md").partition("\n## Decide\n")[2].partition("\n## ")[0])
        for word in ('"decide": "head"', "/v1/systemone", "noul", "chat"):
            self.assertIn(word, section)

    def test_the_egress_document_has_the_task_and_the_limit(self):
        """docs/egress-proxy.md 4.2, the row "model": true: the task decide.host, else the task decide; and the limit."""
        row = [line for line in text("docs/egress-proxy.md").splitlines() if line.startswith('| `"model": true` |')]
        self.assertEqual(len(row), 1)
        for word in ("decide.host", "limit", "egress-auto-limit", "0.97"):
            self.assertIn(word, row[0])
        # 0.95 was the default limit in the text of the base. It is not a default now.
        self.assertNotIn("0.95", row[0])

    def test_the_eval_document_tells_that_the_server_uses_the_head(self):
        """evals/egress/README.md says "The server does not use the head yet." Replace that item."""
        readme = one_line(text("evals/egress/README.md"))
        self.assertNotIn("The server does not use the head yet.", readme)
        section = one_line(text("evals/egress/README.md").partition("\n### A scoring head through llama.cpp\n")[2].partition("\n### ")[0])
        for word in ("decide.host", "egress-auto-limit", "0.1", "2.5"):
            self.assertIn(word, section)


if __name__ == "__main__":
    unittest.main()
