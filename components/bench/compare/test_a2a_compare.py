import json, os, unittest, importlib.util
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("c", os.path.join(HERE, "a2a_compare.py")); C = importlib.util.module_from_spec(spec); spec.loader.exec_module(C)
TD = os.path.join(HERE, "../../../bridges/telegram/internal/a2a/testdata")

class T(unittest.TestCase):
    def test_parse_kagent_completed(self):
        r = C.parse_response(json.load(open(TD + "/a2a-sample-response.json")))
        self.assertEqual(r["state"], "TASK_STATE_COMPLETED")
        self.assertEqual(r["text"], "MOCK-LLM reply (2 messages seen). 2+2 is 4.")
        self.assertEqual(r["usage"]["totalTokens"], 15)
        self.assertEqual(r["model_calls"], 1)
    def test_parse_kagent_failure(self):
        r = C.parse_response(json.load(open(TD + "/a2a-sample-response-gemini-402-failure.json")))
        self.assertEqual(r["state"], "TASK_STATE_FAILED")
        self.assertIn("agent run failed", r["text"])
    def test_parse_adapter_num_turns(self):
        obj = {"result": {"task": {"contextId": "c", "status": {"state": "TASK_STATE_COMPLETED"},
               "artifacts": [{"parts": [{"text": "hi"}]}], "metadata": {C.CLAUDE: {"num_turns": 3}}}}}
        self.assertEqual(C.parse_response(obj)["model_calls"], 3)
    def test_parse_skips_tool_call_artifacts(self):
        obj = {"result": {"task": {"status": {"state": "TASK_STATE_COMPLETED"}, "artifacts": [
            {"parts": [{"text": "final"}]}, {"parts": [{"data": {"name": "x"}}]}]}}}
        self.assertEqual(C.parse_response(obj)["text"], "final")
    def test_check_b(self):
        self.assertTrue(C.check("b", 1, "Yes, lodash 4.17.20 has 4 known vulnerabilities, e.g. GHSA-35jh-r3h4-6jhm."))
        self.assertFalse(C.check("b", 1, "lodash 4.17.20 has vulnerabilities, e.g. GHSA-35jh-r3h4-6jhm."))
        self.assertFalse(C.check("b", 1, "There are 7 vulnerabilities, e.g. GHSA-35jh-r3h4-6jhm (4.17.21 fixes)."))
        self.assertFalse(C.check("b", 1, "It has 4 vulnerabilities."))
    def test_check_others(self):
        self.assertTrue(C.check("a", 1, "Nairobi."))
        self.assertTrue(C.check("c", 1, "PELICAN-7731-OPENAGENTIX-SKILL-LOADED"))
        self.assertFalse(C.check("c", 1, "I don't know"))
        self.assertTrue(C.check("d", 3, "Your lucky number is 7319 and your favourite colour is Teal."))
        self.assertFalse(C.check("d", 3, "I don't know."))
if __name__ == "__main__":
    unittest.main()
