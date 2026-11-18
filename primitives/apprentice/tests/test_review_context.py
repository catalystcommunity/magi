import os
import subprocess
import tempfile
import unittest

import fakes  # noqa: F401 (puts the package on the path)

from magi_apprentice.config import Limits
from magi_apprentice.local import LocalExecution
from magi_apprentice.review_context import called_names, contract_block, request_types, review_context
from magi_apprentice.tools import Tools, Workspace


def write(d, path, text):
    p = os.path.join(d, path)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(text)


def git(d, *args):
    subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local", *args],
                   check=True, capture_output=True)


class ReviewContextTest(unittest.TestCase):
    def test_called_names_of_added_lines(self):
        patch = "+++ b/a.go\n+\tif ok(x) { s.report(endNote(msg)) }\n-\told(y)\n context(z)\n"
        self.assertEqual(called_names(patch), ["report", "endNote"])

    def test_the_definition_of_a_called_function_in_the_same_directory(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "svc/note.go", "package svc\n\nfunc endNote(what string) string {\n"
                                    "\treturn what + \". I removed the environment.\"\n}\n")
            write(d, "svc/cancel.go", "package svc\n\nfunc cancel() string {\n\treturn \"\"\n}\n")
            write(d, "other/far.go", "package other\n\nfunc farAway() {}\n")
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "svc/cancel.go", "package svc\n\nfunc cancel() string {\n"
                                      "\treturn endNote(\"Stopped. I removed its environment\") + farAway()\n}\n")
            diff, ctx = review_context(Tools(Workspace(LocalExecution(), "l", d, Limits())))
            self.assertIn("+\treturn endNote(", diff)
            self.assertIn("// svc/note.go:3\nfunc endNote(what string) string {\n"
                          "\treturn what + \". I removed the environment.\"\n}", ctx)
            self.assertNotIn("farAway", ctx)

    def test_the_contract_of_a_request_type_that_the_change_uses(self):
        other = ";;; Gets an artifact.\nPutEntityRequest = { digest: text }\n"
        csil = (";;; Other text.\nOtherRequest = { a: text }\n\n"
                ";;; Makes an entity, or returns the entity with the same external system and\n"
                ";;; locator.\nPutEntityRequest = {\n  kind: text,\n  label: text,\n}\n\nNext = { b: text }\n")
        with tempfile.TemporaryDirectory() as d:
            write(d, "contracts/knowledge.csil", csil)
            write(d, "contracts/artifacts.csil", other)
            write(d, "svc/lessons.go", "package svc\n\nfunc run() {}\n")
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "svc/lessons.go", "package svc\n\nfunc run() {\n"
                                       "\t_, _ = k.PutEntity(ctx, knowledgeapi.PutEntityRequest{Kind: \"lesson\"})\n}\n")
            _, ctx = review_context(Tools(Workspace(LocalExecution(), "l", d, Limits())))
            self.assertIn("// contracts/knowledge.csil (PutEntityRequest)\n"
                          ";;; Makes an entity, or returns the entity with the same external system and\n"
                          ";;; locator.\nPutEntityRequest = {\n  kind: text,\n  label: text,\n}", ctx)
            self.assertNotIn("OtherRequest", ctx)
            self.assertNotIn("Next", ctx)
            self.assertNotIn("artifacts.csil", ctx)

    def test_request_types_and_a_one_line_contract(self):
        patch = "+++ b/a.py\n+r = c.get(T.GetRequest(id=x))\n-old(T.OldRequest())\n+s.Search(ctx, api.SearchRequest{})\n"
        self.assertEqual(request_types(patch), [("", "GetRequest"), ("", "SearchRequest")])
        self.assertEqual(request_types("+x(knowledgeapi.GetRequest{})\n"), [("knowledge", "GetRequest")])
        self.assertEqual(contract_block(";;; Gets one.\nGetRequest = { id: text }\n", "GetRequest"),
                         ";;; Gets one.\nGetRequest = { id: text }")
        self.assertEqual(contract_block("GetRequestX = { }\n", "GetRequest"), "")


if __name__ == "__main__":
    unittest.main()
