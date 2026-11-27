"""Probes of case long3. Target: clients/python"""
import re
import unittest
from pathlib import Path

from magi_inference import client as inference_client
from magi_inference.codec import cbor_encode

ROOT = Path(__file__).resolve().parents[2]


def text(path):
    p = ROOT / path
    return p.read_text() if p.exists() else ""


def one_line(s):
    return " ".join(s.split())


def section(markdown, title):
    """The text of the section with the heading title, to the next heading of the same level."""
    m = re.search(r"^(#+) " + re.escape(title) + r"\s*$", markdown, re.M)
    if not m:
        return ""
    rest = markdown[m.end():]
    end = re.search(r"^#{1," + str(len(m.group(1))) + r"} ", rest, re.M)
    return rest[:end.start()] if end else rest


class Recorder:
    """A transport that records the call and gives one status, as the host sends it."""

    def __init__(self):
        self.calls = []

    def call(self, service, method, req):
        self.calls.append((service, method))
        return cbor_encode({"state": "starting", "model": "careful", "in_flight": 0,
                            "models": ["careful", "careful-notes"], "loadable": ["usual", "careful"],
                            "hold_until_ms": 1795800000000})


class Long3(unittest.TestCase):
    def test_the_contract_has_the_operation_and_the_fields(self):
        """Add to contracts/inference.csil: LoadRequest = { model: text, ? hold_seconds: uint }; load: LoadRequest -> Status; Status: ? loadable: [* text], ? hold_until_ms: uint."""
        c = one_line(text("contracts/inference.csil"))
        self.assertRegex(c, r"LoadRequest = \{ model: text, \? hold_seconds: uint,? \}")
        self.assertRegex(c, r"load: LoadRequest -> Status")
        status = re.search(r"Status = \{(.*?)\}", c)
        self.assertIsNotNone(status)
        self.assertRegex(status.group(1), r"\? loadable: \[\* text\]")
        self.assertRegex(status.group(1), r"\? hold_until_ms: uint")

    def test_the_python_client_has_the_operation(self):
        """Then run tools/gen.sh: the generated Python client has the operation load, and reads the new fields of the status."""
        from magi_inference import types
        self.assertTrue(hasattr(types, "LoadRequest"), "the generated types have no LoadRequest")
        t = Recorder()
        st = inference_client.InferenceClient(t).load(types.LoadRequest(model="careful", hold_seconds=60))
        self.assertEqual(t.calls, [("InferenceService", "load")])
        self.assertEqual((st.model, list(st.loadable), st.hold_until_ms), ("careful", ["usual", "careful"], 1795800000000))

    def test_the_typescript_types_have_the_request(self):
        """The generated TypeScript client has the type LoadRequest and the new fields of the status."""
        ts = text("clients/typescript/inference/types.gen.ts")
        self.assertIn("LoadRequest", ts)
        self.assertIn("hold_until_ms", ts)
        self.assertIn("loadable", ts)

    def test_the_readme_has_the_operation_and_the_refusal(self):
        """primitives/inference/README.md: the operation in the API table, the error type not_loaded in the table of refusals."""
        readme = text("primitives/inference/README.md")
        rows = [line for line in readme.splitlines() if line.strip().startswith("|")]
        self.assertTrue(any("InferenceService/load" in r for r in rows), "no row for InferenceService/load")
        self.assertTrue(any("not_loaded" in r for r in rows), "no row for not_loaded")

    def test_the_readme_has_the_section(self):
        """A section "More than one engine" with a configuration example and the rules of items 2 to 6."""
        s = section(text("primitives/inference/README.md"), "More than one engine")
        self.assertTrue(s, 'no section "More than one engine"')
        self.assertIn('"engines"', s)
        self.assertIn("hold_seconds", s)
        self.assertIn("not_loaded", s)
        self.assertIn("keep_warm", s)

    def test_the_readme_report_has_the_new_fields(self):
        """loadable and hold_until_ms in "Report for a placement"."""
        s = section(text("primitives/inference/README.md"), "Report for a placement")
        self.assertIn("loadable", s)
        self.assertIn("hold_until_ms", s)


if __name__ == "__main__":
    unittest.main()
