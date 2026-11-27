"""Probes of case long1. Target: clients/python"""
import re
import unittest
from pathlib import Path

from magi_inference.client import InferenceClient
from magi_inference.codec import cbor_encode
from magi_inference.types import StatusRequest

ROOT = Path(__file__).resolve().parents[2]
RECORD = ROOT / "docs" / "decisions" / "0016-placement.md"


def text(path):
    p = ROOT / path
    return p.read_text() if p.exists() else ""


def one_line(s):
    return " ".join(s.split())


def rows(markdown):
    """The cells of each table row."""
    return [[c.strip() for c in line.strip().strip("|").split("|")]
            for line in markdown.splitlines() if line.strip().startswith("|")]


class OneAnswer:
    """A transport that gives one status, as the host sends it."""

    def call(self, service, method, req):
        return cbor_encode({"state": "ready", "model": "local", "in_flight": 0,
                            "gpus": [{"name": "AMD 7480", "total_mib": 8176, "used_mib": 7069}, {"name": "Intel 46a6"}],
                            "engine_mib": 7000, "models": ["local", "local-notes"]})


class Long1(unittest.TestCase):
    def test_the_contract_has_the_gpu_type_and_the_status_fields(self):
        """In contracts/inference.csil, add the type Gpu and three optional fields to Status: gpus, engine_mib, models."""
        csil = one_line(text("contracts/inference.csil"))
        self.assertRegex(csil, r"Gpu = \{ name: text, \? total_mib: uint, \? used_mib: uint, \? util: uint,? \}")
        status = csil[csil.index("Status = {"):]
        status = status[:status.index("}") + 1]
        for field in ("? gpus: [* Gpu]", "? engine_mib: uint", "? models: [* text]"):
            self.assertIn(field, status)

    def test_the_python_client_reads_the_report(self):
        """Then run tools/gen.sh: the Python client reads the new fields."""
        st = InferenceClient(OneAnswer()).status(StatusRequest(detail=True))
        self.assertEqual([(g.name, g.total_mib, g.used_mib, g.util) for g in st.gpus],
                         [("AMD 7480", 8176, 7069, None), ("Intel 46a6", None, None, None)])
        self.assertEqual((st.engine_mib, st.models), (7000, ["local", "local-notes"]))

    def test_the_typescript_client_has_the_report(self):
        """Then run tools/gen.sh: the TypeScript client has the new fields."""
        ts = one_line(text("clients/typescript/inference/types.gen.ts"))
        self.assertRegex(ts, r"export interface Gpu \{ name: string; totalMib\?: number; usedMib\?: number; util\?: number; \}")
        for field in ("gpus?: Gpu[];", "engineMib?: number;", "models?: string[];"):
            self.assertIn(field, ts)

    def test_the_record_has_the_parts_of_a_record(self):
        """Add docs/decisions/0016-placement.md with the same parts as the other records. The status is accepted."""
        self.assertTrue(RECORD.exists(), "no docs/decisions/0016-placement.md")
        lines = RECORD.read_text().splitlines()
        self.assertEqual(lines[0], "# 0016 Magi places the models on the hosts")
        self.assertIn("Status: accepted", lines)
        heads = [line for line in lines if line.startswith("## ")]
        self.assertEqual([h for h in heads if h in ("## Decision", "## Reason", "## Done when")],
                         ["## Decision", "## Reason", "## Done when"])

    def test_done_when_has_one_complete_item(self):
        """ "Done when" has these six items. Mark only the first item as complete."""
        done = text("docs/decisions/0016-placement.md").partition("## Done when")[2]
        items = re.findall(r"^- \[([ x])\] ", done, re.M)
        self.assertEqual(items, ["x", " ", " ", " ", " ", " "])
        first = one_line(done.partition("- [ ]")[0])
        self.assertIn("GPUs", first)
        self.assertIn("detail", first)

    def test_the_record_is_in_the_table_of_records(self):
        """docs/decisions/README.md: add the record to the table of records."""
        row = [r for r in rows(text("docs/decisions/README.md")) if r[0] == "[0016](0016-placement.md)"]
        self.assertEqual(row, [["[0016](0016-placement.md)", "Magi places the models on the hosts", "accepted"]])

    def test_who_decides_has_the_placement(self):
        """Add this row to the table "Who decides", directly after the row "Which model serves a role?"."""
        table = rows(text("docs/decisions/README.md").partition("## Who decides")[2])
        first = [r[0] for r in table]
        self.assertIn("Which host runs a model?", first)
        i = first.index("Which host runs a model?")
        self.assertEqual(first[i - 1], "Which model serves a role?")
        self.assertEqual(table[i][1:], ["Code, from the roles, the memory that a model needs, and the reports of the hosts. "
                                        "The rules of the owner of a machine come first", "record 0016"])

    def test_the_readme_of_the_primitive_has_the_report(self):
        """primitives/inference/README.md: a section "Report for a placement": what status with detail adds, and where the GPU values come from."""
        readme = text("primitives/inference/README.md")
        self.assertIn("\n## Report for a placement\n", readme)
        section = readme.partition("\n## Report for a placement\n")[2].partition("\n## ")[0]
        for word in ("detail", "nvidia-smi", "/sys/class/drm"):
            self.assertIn(word, section)


if __name__ == "__main__":
    unittest.main()
