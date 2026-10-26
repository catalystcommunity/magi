import unittest
from loop import action, run

class LoopTests(unittest.TestCase):
    def test_requests_precede_effects_and_keep_context(self):
        events = []
        def exchange(kind, payload, operation):
            events.append((kind, payload, operation))
            if kind == "tool":
                return {"outcome": "returned", "text": "observed file", "exit_code": 0}
            if operation == "model-0":
                return {"text": '{"tool":"read_file","arguments":{"path":"solution.py"}}'}
            self.assertIn("observed file", payload["messages"][-1]["content"])
            if operation == "model-1":
                self.assertFalse(payload["can_finish"])
                return {"text": '{"tool":"check","arguments":{}}'}
            self.assertTrue(payload["can_finish"])
            return {"text": '{"answer":"Done."}'}
        result = run("Read the file.", exchange)
        self.assertEqual(result["answer"], "Done.")
        self.assertEqual([e[0] for e in events], ["model", "tool", "model", "tool", "model"])

    def test_unknown_effect_stops_without_retry(self):
        seen = []
        def exchange(kind, payload, operation):
            seen.append(operation)
            return {"text": '{"tool":"read_file","arguments":{"path":"solution.py"}}'} if kind == "model" else {"outcome": "unknown"}
        self.assertTrue(run("Check.", exchange)["reconcile_required"])
        self.assertEqual(seen, ["model-0", "tool-0"])

    def test_strict_model_actions(self):
        for text in ['{"answer":"a","answer":"b"}', '```json\n{"answer":"ok"}\n```', '{"tool":"shell","arguments":{}}', '{"answer":"x","grant":"all"}', 'NaN', '[]']:
            with self.subTest(text=text), self.assertRaises(ValueError):
                action(text)

    def test_budget_is_enforced(self):
        calls = []
        def exchange(kind, payload, operation):
            calls.append(operation)
            return {"text": '{"tool":"read_file","arguments":{"path":"solution.py"}}'} if kind == "model" else {"outcome": "denied"}
        self.assertTrue(run("Check.", exchange, 2)["budget_exhausted"])
        self.assertEqual(len(calls), 4)

    def test_invalid_or_premature_final_never_runs_a_tool(self):
        for reply in ['```json\n{"answer":"Done"}\n```', '{"answer":"Done"}', '{"tool":"check","arguments":{}}']:
            seen = []
            def exchange(kind, payload, operation):
                seen.append(kind)
                self.assertEqual(payload["allowed_tools"], ["read_file"])
                self.assertFalse(payload["can_finish"])
                return {"text": reply}
            self.assertTrue(run("Change the file.", exchange, 2)["budget_exhausted"])
            self.assertEqual(seen, ["model", "model"])

    def test_write_invalidates_check(self):
        replies = [
            '{"tool":"read_file","arguments":{"path":"solution.py"}}',
            '{"tool":"check","arguments":{}}',
            '{"tool":"write_file","arguments":{"path":"solution.py","text":"changed"}}',
            '{"answer":"Done"}',
        ]
        def exchange(kind, payload, operation):
            if kind == "tool":
                return {"outcome": "returned", "exit_code": 0}
            index = int(operation.split("-")[1])
            self.assertEqual(payload["can_finish"], index == 2)
            return {"text": replies[index]}
        self.assertTrue(run("Change the file.", exchange, 4)["budget_exhausted"])

if __name__ == "__main__":
    unittest.main()
