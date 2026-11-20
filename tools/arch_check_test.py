#!/usr/bin/env python3
"""Tests of tools/arch-check.py on a throwaway tree."""
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("arch_check", os.path.join(HERE, "arch-check.py"))
ac = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ac)

MOD = "github.com/catalystcommunity/magi/"


class ArchCheck(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = self.tmp.name
        os.makedirs(os.path.join(self.root, "tools"))

    def tearDown(self):
        self.tmp.cleanup()

    def put(self, rel, text):
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(text)

    def run_check(self):
        return subprocess.run([sys.executable, os.path.join(HERE, "arch-check.py"), "--root", self.root],
                              capture_output=True, text=True)

    def rules(self):
        return sorted((r, p) for r, p, _ in ac.find(self.root))

    def test_clean_tree_passes(self):
        self.put("primitives/a/x.go", f'import (\n\t"{MOD}primitives/a/api"\n\t"{MOD}lib/rpc"\n)\n')
        self.put("magi/compose/x.go", f'import "{MOD}primitives/a/api"\n')
        self.put("magi/web/src/app.ts", "export {}\n")
        self.assertEqual(self.rules(), [])
        self.assertEqual(self.run_check().returncode, 0)

    def test_primitive_imports_other_primitive(self):
        self.put("primitives/a/x.go", f'import "{MOD}primitives/b/api"\n')
        self.put("primitives/a/y.go", f'import "{MOD}magi/compose"\n')
        self.put("lib/rpc/z.go", f'import "{MOD}primitives/b"\n')
        self.assertEqual(self.rules(), [("imports", "lib/rpc/z.go"), ("imports", "primitives/a/x.go"),
                                        ("imports", "primitives/a/y.go")])
        self.assertEqual(self.run_check().returncode, 1)

    def test_cmd_can_import_the_client_of_other_primitive_only(self):
        self.put("primitives/a/cmd/a/main.go", f'import "{MOD}primitives/b/api"\n')
        self.put("primitives/a/cmd/a/bad.go", f'import "{MOD}primitives/b/server"\n')
        self.put("primitives/a/cmd/a/worse.go", f'import "{MOD}primitives/b/api/internal"\n')
        self.assertEqual(self.rules(), [("imports", "primitives/a/cmd/a/bad.go"),
                                        ("imports", "primitives/a/cmd/a/worse.go")])

    def test_private_name_is_found_by_hash_only(self):
        self.put(ac.PRIVATE, ac.sha("secret-host") + "\n")
        self.put("docs/a.md", "Connect to Secret-Host.example.local now.\n")
        self.put("docs/b.md", "Connect to other-host.example.local now.\n")
        self.assertEqual(self.rules(), [("private", "docs/a.md")])
        out = self.run_check().stdout
        self.assertIn("docs/a.md:1", out)
        self.assertNotIn("secret-host", out.lower())

    def test_forbidden_name(self):
        bad = "op" + "al"
        self.put("lib/x/a.go", f"var {bad.capitalize()}Store int\n")
        self.put("lib/x/b.go", "func stopAll() {}\nvar PropAliases int\n")
        self.assertEqual(self.rules(), [(bad, "lib/x/a.go")])

    def test_node_out_of_web_app(self):
        self.put("primitives/a/package.json", "{}\n")
        self.put("tools/x.js", "1\n")
        self.put("magi/web/package.json", "{}\n")
        self.assertEqual(self.rules(), [("node", "primitives/a/package.json"), ("node", "tools/x.js")])

    def test_baseline_permits_known_and_fails_stale(self):
        self.put("primitives/a/x.go", f'import "{MOD}primitives/b/api"\n')
        self.put(ac.BASELINE, "# known\nimports\tprimitives/a/x.go\tprimitives/b/api\n")
        self.assertEqual(self.run_check().returncode, 0)
        self.put("primitives/a/x.go", "package a\n")
        r = self.run_check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("delete this line", r.stdout)


if __name__ == "__main__":
    unittest.main()
