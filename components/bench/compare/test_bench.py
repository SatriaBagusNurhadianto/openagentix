"""Tests for the benchmark extensions of a2a_compare.py, against a throw-away local HTTP server (no cluster, no model)."""
import importlib.util, io, json, os, re, threading, time, unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("c", os.path.join(HERE, "a2a_compare.py"))
C = importlib.util.module_from_spec(spec)
spec.loader.exec_module(C)


class Fake:
    """A fake A2A backend. mode: ok | throttle-429 | throttle-text | slow."""

    def __init__(self, mode="ok", delay=0.0, answer_for=None):
        self.mode, self.delay, self.requests, self.lock = mode, delay, [], threading.Lock()
        self.answer_for = answer_for or (lambda text, n: "Nairobi" if "Kenya" in text else "Kampala" if "Uganda" in text else "OK")
        fake = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                msg = body["params"]["message"]
                with fake.lock:
                    fake.requests.append({"headers": {k.lower(): v for k, v in self.headers.items()}, "message": msg})
                    n = len(fake.requests)
                if fake.delay:
                    time.sleep(fake.delay)
                if fake.mode == "throttle-429":
                    self.send_response(429)
                    self.end_headers()
                    self.wfile.write(b"slow down")
                    return
                if fake.mode == "throttle-text":
                    task = {"contextId": "ctx-t", "status": {"state": "TASK_STATE_FAILED", "message": {"parts": [
                        {"text": "agent run failed: OpenAI API error: 429 Too Many Requests"}]}}}
                else:
                    task = {"contextId": msg.get("contextId") or "ctx-%d" % n, "status": {"state": "TASK_STATE_COMPLETED"},
                            "artifacts": [{"parts": [{"text": fake.answer_for(msg["parts"][0]["text"], n)}],
                                           "metadata": {"kagent.dev/a2a/usage": {}}}],
                            "metadata": {C.USAGE: {"inputTokens": 10, "outputTokens": 2, "cachedInputTokens": 0, "totalTokens": 12}}}
                out = json.dumps({"jsonrpc": "2.0", "id": "1", "result": {"task": task}}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(out)

        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = "http://127.0.0.1:%d/" % self.srv.server_address[1]
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


def run_main(argv):
    out = io.StringIO()
    old = C.sys.stdout
    C.sys.stdout = out
    try:
        C.main(argv)
    finally:
        C.sys.stdout = old
    return list(C.csv.DictReader(io.StringIO(out.getvalue())))


class Pure(unittest.TestCase):
    def test_traceparent_is_w3c_and_unique(self):
        tp, tid = C.make_traceparent()
        self.assertRegex(tp, r"^00-[0-9a-f]{32}-[0-9a-f]{16}-01$")
        self.assertEqual(tid, tp.split("-")[1])
        self.assertNotEqual(tid, C.make_traceparent()[1])
        self.assertNotEqual(tid, "0" * 32)

    def test_parse_backends_named(self):
        self.assertEqual(C.parse_backends("a=http://x:1/p,b-c=http://y/"), [("a", "http://x:1/p"), ("b-c", "http://y/")])

    def test_header_spec(self):
        h = C.parse_header_specs(["kagent:X-User-Id:bench:{backend}:{rid}"])
        self.assertEqual(C.headers_for("kagent", "r1-a2", h), {"X-User-Id": "bench:kagent:r1-a2"})
        self.assertEqual(C.headers_for("other", "r1-a2", h), {})

    def test_throttled_detection(self):
        self.assertTrue(C.is_throttled(429, "CLIENT_ERROR", ""))
        self.assertTrue(C.is_throttled(200, "TASK_STATE_FAILED", "agent run failed: OpenAI API error: 429 Too Many Requests"))
        self.assertFalse(C.is_throttled(200, "TASK_STATE_FAILED", "boom"))
        self.assertFalse(C.is_throttled(200, "TASK_STATE_COMPLETED", "Error 429 is a number"))

    def test_group_e_checks(self):
        self.assertIn("e", C.PROMPTS)
        self.assertEqual(len(C.PROMPTS["e"]), 2)
        self.assertTrue(C.check("e", 1, "Nairobi"))
        self.assertTrue(C.check("e", 2, "Kampala."))
        self.assertFalse(C.check("e", 2, "Nairobi"))

    def test_percentile_nearest_rank(self):
        self.assertEqual(C.percentile([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 90), 9)


class Sequential(unittest.TestCase):
    def test_rows_fields_headers_and_no_retry_on_429(self):
        ok, thr = Fake("ok"), Fake("throttle-429")
        try:
            rows = run_main(["--backends", "good=%s,bad=%s" % (ok.url, thr.url), "--groups", "a,e", "--runs", "2",
                             "--header", "good:X-User-Id:bench:{backend}:{rid}", "--run-id", "T1", "--load", "1.5"])
        finally:
            ok.close(); thr.close()
        # good: a (1 turn) + e (2 turns) per run = 3 rows x 2 runs; bad: 1 row per group-run (stops after failure) = 4
        self.assertEqual(len([r for r in rows if r["backend"] == "good"]), 6)
        self.assertEqual(len([r for r in rows if r["backend"] == "bad"]), 4)
        self.assertEqual(len(thr.requests), 4, "429 must never be retried")
        for r in rows:
            for col in ("ts", "backend", "group", "run", "turn", "wall_s", "http_status", "state", "correct", "total_tokens",
                        "model_calls", "context_id", "trace_id", "load1"):
                self.assertIn(col, r)
            self.assertRegex(r["trace_id"], r"^[0-9a-f]{32}$")
            self.assertEqual(r["load1"], "1.5")
        bad = [r for r in rows if r["backend"] == "bad"]
        self.assertTrue(all(r["http_status"] == "429" and r["throttled"] == "1" and r["correct"] == "0" for r in bad))
        good = [r for r in rows if r["backend"] == "good"]
        self.assertTrue(all(r["correct"] == "1" and r["http_status"] == "200" for r in good))
        self.assertEqual(good[0]["total_tokens"], "12")
        # distinct X-User-Id per (group, run); only the backend that has the header spec sends it
        users = [q["headers"].get("x-user-id") for q in ok.requests]
        self.assertEqual(users[0], "bench:good:T1-a1")
        self.assertEqual(len(set(users)), 4)                       # a1 a2 e1 e2 (two turns of one run share the id)
        self.assertTrue(all(q["headers"].get("x-user-id") is None for q in thr.requests))
        # traceparent header sent and equal to the recorded trace id
        tp = ok.requests[0]["headers"]["traceparent"]
        self.assertEqual(tp.split("-")[1], good[0]["trace_id"])
        # e: turn 2 reuses the contextId of turn 1
        e = [r for r in good if r["group"] == "e" and r["run"] == "1"]
        self.assertEqual([r["turn"] for r in e], ["1", "2"])
        self.assertEqual(e[0]["context_id"], e[1]["context_id"])
        self.assertEqual(ok.requests[3]["message"]["contextId"], e[0]["context_id"])
        # every request got its own trace id
        self.assertEqual(len({q["headers"]["traceparent"].split("-")[1] for q in ok.requests}), len(ok.requests))

    def test_text_throttle_is_flagged_not_retried(self):
        f = Fake("throttle-text")
        try:
            rows = run_main(["--backends", "k=%s" % f.url, "--groups", "a", "--runs", "3"])
        finally:
            f.close()
        self.assertEqual(len(f.requests), 3)
        self.assertTrue(all(r["throttled"] == "1" for r in rows))

    def test_warmup_is_untimed_and_not_recorded(self):
        f = Fake("ok")
        try:
            rows = run_main(["--backends", "k=%s" % f.url, "--groups", "a", "--runs", "1", "--warmup",
                             "--header", "k:X-User-Id:bench:{backend}:{rid}", "--run-id", "W"])
        finally:
            f.close()
        self.assertEqual(len(f.requests), 2)
        self.assertEqual(len(rows), 1)
        self.assertEqual(f.requests[0]["headers"]["x-user-id"], "bench:k:W-warmup")

    def test_header_row_can_be_suppressed(self):
        f = Fake("ok")
        out = io.StringIO()
        old = C.sys.stdout
        C.sys.stdout = out
        try:
            C.main(["--backends", "k=%s" % f.url, "--groups", "a", "--runs", "1", "--no-header"])
        finally:
            C.sys.stdout = old
            f.close()
        self.assertFalse(out.getvalue().startswith("ts,"))


class TurnGap(unittest.TestCase):
    def test_gap_between_turns_is_slept_outside_the_timing_and_recorded(self):
        f = Fake("ok")
        t0 = time.monotonic()
        try:
            rows = run_main(["--backends", "k=%s" % f.url, "--groups", "e", "--runs", "1", "--turn-gap", "0.6"])
        finally:
            f.close()
        self.assertGreaterEqual(time.monotonic() - t0, 0.6)
        self.assertEqual([r["gap_s"] for r in rows], ["0", "0.6"])      # turn 1 had no gap before it
        self.assertLess(float(rows[1]["wall_s"]), 0.5)                  # the sleep is not part of the turn's wall time


class Concurrency(unittest.TestCase):
    def test_k_parallel_requests_and_batch_wall(self):
        f = Fake("ok", delay=0.3)
        try:
            rows = run_main(["--backends", "k=%s" % f.url, "--concurrency", "4", "--rounds", "2",
                             "--header", "k:X-User-Id:bench:{backend}:{rid}", "--run-id", "C"])
        finally:
            f.close()
        self.assertEqual(len(rows), 8)
        self.assertTrue(all(r["mode"] == "concurrency" and r["k"] == "4" and r["group"] == "a" for r in rows))
        for rnd in ("1", "2"):
            rr = [r for r in rows if r["run"] == rnd]
            self.assertEqual(len(rr), 4)
            self.assertEqual(len({r["batch_wall_s"] for r in rr}), 1)
            bw = float(rr[0]["batch_wall_s"])
            self.assertGreaterEqual(bw, 0.3)
            self.assertLess(bw, 1.0, "requests must overlap (4 x 0.3 s sequential would be 1.2 s)")
            self.assertTrue(all(float(r["wall_s"]) <= bw + 0.05 for r in rr))
        self.assertEqual(len({q["headers"]["x-user-id"] for q in f.requests}), 8)


if __name__ == "__main__":
    unittest.main()
