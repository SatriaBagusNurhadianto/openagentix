import importlib.util, json, os, unittest
HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("tb", os.path.join(HERE, "trace-breakdown.py")); TB = importlib.util.module_from_spec(spec); spec.loader.exec_module(TB)

def sp(sid, op, start, dur, svc, parent=None, tags=None):
    return {"spanID": sid, "operationName": op, "startTime": start, "duration": dur, "processID": svc,
            "references": [{"refType": "CHILD_OF", "spanID": parent}] if parent else [], "tags": [{"key": k, "value": v} for k, v in (tags or {}).items()]}

def trace(spans):
    svcs = {s["processID"] for s in spans}
    return {"data": [{"traceID": "t", "processes": {s: {"serviceName": s} for s in svcs}, "spans": spans}]}

class Kagent(unittest.TestCase):
    def test_real_trace_fixture(self):
        b = TB.kagent_breakdown(json.load(open(os.path.join(HERE, "testdata", "kagent-trace-plain.json"))))
        self.assertAlmostEqual(b["total_s"], 12.760745, 6)
        self.assertAlmostEqual(b["model_s"], 1.045272, 6)
        self.assertAlmostEqual(b["embed_s"], 1.156062, 6)
        self.assertEqual(b["tools_s"], 0.0)
        self.assertAlmostEqual(b["pre_dispatch_s"], 9.40099, 5)       # controller + actor resume, before the agent sees the request
        self.assertAlmostEqual(b["remainder_s"], 12.760745 - 1.045272 - 1.156062, 6)
        self.assertEqual(b["model_calls"], 1)
        self.assertEqual(b["prompt_tokens"], [7998])
        self.assertEqual(b["cache_read_tokens"], 0)
    def test_tools_and_multiple_model_calls(self):
        t = trace([sp("r", "POST /agents/", 0, 10_000_000, "kagent-controller"),
                   sp("a", "a2a.request", 1_000_000, 8_000_000, "assistant", "r"),
                   sp("m1", "POST /v1/chat/completions", 1_500_000, 2_000_000, "model-gateway", "a", {"gen_ai.usage.input_tokens": 5000, "gen_ai.usage.cache_read.input_tokens": 0}),
                   sp("t1", "tools/call osv_query_vulnerability", 3_600_000, 1_000_000, "tools", "a"),
                   sp("t0", "tools/list", 1_200_000, 5_000, "tools", "a"),
                   sp("m2", "POST /v1/chat/completions", 4_700_000, 3_000_000, "model-gateway", "a", {"gen_ai.usage.input_tokens": 5600, "gen_ai.usage.cache_read.input_tokens": 4800}),
                   sp("g", "generate_content claude-haiku", 4_650_000, 3_100_000, "assistant", "a", {"gen_ai.usage.input_tokens": 5600})])
        b = TB.kagent_breakdown(t)
        self.assertEqual((b["total_s"], b["model_s"], b["tools_s"], b["embed_s"]), (10.0, 5.0, 1.0, 0.0))
        self.assertEqual(b["model_calls"], 2)
        self.assertEqual(b["prompt_tokens"], [5000, 5600])
        self.assertEqual(b["cache_read_tokens"], 4800)
        self.assertEqual(b["remainder_s"], 4.0)
        self.assertEqual(b["pre_dispatch_s"], 1.0)
    def test_empty_trace_is_none(self):
        self.assertIsNone(TB.kagent_breakdown({"data": []}))
        self.assertIsNone(TB.kagent_breakdown({"data": [{"spans": [], "processes": {}}]}))

EGRESS = """2026-11-23T12:13:04.130418Z\tinfo\trequest gateway=default/default endpoint=ax-server-runner.ax-system.svc.cluster.local:8081 src.addr=1.2.3.4:5 http.method=POST http.host=ax-server-runner.ax-system.svc.cluster.local http.path=/runner/v1/assignment:claim http.status=200 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=20ms substrate.connect.authority="10.0.0.1:8081"
2026-11-23T12:13:06.597073Z\tinfo\trequest endpoint=vmcp-bench-osv.mcp-tools.svc.cluster.local:4483 http.method=POST http.host=vmcp-bench-osv.mcp-tools.svc.cluster.local http.path=/mcp http.status=200 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=7ms
2026-11-23T12:13:08.461883Z\tinfo\trequest endpoint=api.anthropic.com:443 http.method=POST http.host=api.anthropic.com http.path=/v1/messages?beta=true http.status=200 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=1253ms
2026-11-23T12:13:09.287177Z\tinfo\trequest endpoint=vmcp-bench-osv.mcp-tools.svc.cluster.local:4483 http.method=POST http.host=vmcp-bench-osv.mcp-tools.svc.cluster.local http.path=/mcp http.status=200 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=730ms
2026-11-23T12:13:12.611801Z\tinfo\trequest endpoint=api.anthropic.com:443 http.method=POST http.host=api.anthropic.com http.path=/v1/messages?beta=true http.status=200 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=3202ms
2026-11-23T12:13:12.700000Z\tinfo\trequest endpoint=api.anthropic.com:443 http.method=GET http.host=api.anthropic.com http.path=/api/claude_code/settings http.status=404 protocol=http ate.actor.name=turn-aa ate.actor.uid=u1 ate.atespace=default duration=300ms
2026-11-23T12:13:13.000000Z\tinfo\trequest endpoint=x:80 http.method=POST http.host=x http.path=/y http.status=200 protocol=http ate.actor.name=session-zz ate.atespace=kagent duration=5ms
2026-11-23T12:13:13.100000Z\twarn\tproxy::gateway\tproxy error: error writing a body"""
SERVER = [json.dumps(x) for x in (
    {"time": "2026-11-23T12:13:03.798879051Z", "msg": "resuming actor on Substrate worker", "actor": "turn-aa"},
    {"time": "2026-11-23T12:13:04.045079884Z", "msg": "task successfully resumed", "name": "turn-aa", "actor": "turn-aa"},
    {"time": "2026-11-23T12:13:14.08973718Z", "msg": "deleting Substrate actor for task", "task": "turn-aa"})]

class AX(unittest.TestCase):
    def test_parse_egress_skips_non_request_lines_and_other_atespaces(self):
        ev = TB.parse_egress(EGRESS.splitlines())
        self.assertEqual(len(ev), 6)
        self.assertEqual(ev[2]["host"], "api.anthropic.com"); self.assertEqual(ev[2]["duration_s"], 1.253)
        self.assertEqual(ev[0]["actor"], "turn-aa")
    def test_ax_breakdown(self):
        ev = TB.parse_egress(EGRESS.splitlines()); srv = TB.parse_server(SERVER)
        # the request: started 12:13:03.700, ended 12:13:14.200 (the CSV ts is the END of the request)
        b = TB.ax_breakdown(TB.parse_ts("2026-11-23T12:13:14.200Z"), 10.5, ev, srv)
        self.assertEqual(b["actor"], "turn-aa")
        self.assertEqual(b["model_calls"], 2)
        self.assertAlmostEqual(b["model_s"], 1.253 + 3.202, 6)
        self.assertAlmostEqual(b["tools_s"], 0.007 + 0.730, 6)
        self.assertAlmostEqual(b["to_resumed_s"], 0.345, 2)                 # request start -> "task successfully resumed"
        self.assertAlmostEqual(b["other_egress_s"], 0.3, 6)                  # the claude settings GET
        self.assertAlmostEqual(b["remainder_s"], 10.5 - 0.345 - 4.455 - 0.737, 2)
    def test_ax_breakdown_ambiguous_or_missing(self):
        ev = TB.parse_egress(EGRESS.splitlines())
        self.assertIsNone(TB.ax_breakdown(TB.parse_ts("2026-11-23T11:00:00Z"), 5, ev, []))
        two = ev + [dict(e, actor="turn-bb", uid="u2") for e in ev]
        one_stray = ev + [dict(ev[1], actor="turn-bb", uid="u2")]   # one stray line of another actor does not make it ambiguous
        self.assertEqual(TB.ax_breakdown(TB.parse_ts("2026-11-23T12:13:14.200Z"), 10.5, one_stray, [])["actor"], "turn-aa")
        self.assertEqual(TB.ax_breakdown(TB.parse_ts("2026-11-23T12:13:14.200Z"), 10.5, two, [])["actor"], "ambiguous")

class Tables(unittest.TestCase):
    def test_median_table(self):
        rows = [{"backend": "kagent", "group": "a", "turn": 1, "total_s": t, "pre_dispatch_s": 1.0, "model_s": 2.0, "embed_s": 1.0, "tools_s": 0.0, "remainder_s": t - 3.0,
                 "model_calls": 1, "prompt_tokens": [8000], "cache_read_tokens": 0} for t in (10.0, 12.0, 20.0)]
        md = TB.kagent_table(rows)
        self.assertIn("| kagent | a | 1 | 3 | 12.0 | 1.0 | 2.0 | 1.0 | 0.0 | 9.0 | 1 | 8000 | no |", md)
if __name__ == "__main__":
    unittest.main()
