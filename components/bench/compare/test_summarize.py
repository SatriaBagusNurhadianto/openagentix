import importlib.util, os, unittest
spec = importlib.util.spec_from_file_location("s", os.path.join(os.path.dirname(os.path.abspath(__file__)), "summarize.py")); S = importlib.util.module_from_spec(spec); spec.loader.exec_module(S)
def row(g, r, b, t, lat, ok, tok="", calls="", thr=0, mode="sequential", k="", batch="", state="TASK_STATE_COMPLETED", head=""):
    return dict(group=g, run=str(r), backend=b, turn=str(t), wall_s=str(lat), correct=str(ok), total_tokens=str(tok), model_calls=str(calls),
                throttled=str(thr), mode=mode, k=str(k), batch_wall_s=str(batch), state=state, answer_head=head)
class T(unittest.TestCase):
    def test_d_sums_turns_and_needs_all_correct(self):
        rows = [row("d", 1, "x", t, 2, 1, 10, 1) for t in (1, 2, 3)] + [row("d", 2, "x", t, 2, 1 if t < 3 else 0, 10, 1) for t in (1, 2, 3)]
        rs = S.runs(rows)
        self.assertEqual([(r["lat"], r["ok"], r["tok"], r["calls"]) for r in rs], [(6.0, True, 30.0, 3.0), (6.0, False, 30.0, 3.0)])
    def test_d_incomplete_is_failure(self):
        self.assertFalse(S.runs([row("d", 1, "x", 1, 2, 1), row("d", 1, "x", 2, 2, 1)])[0]["ok"])
    def test_e_uses_turn_two_only(self):
        rs = S.runs([row("e", 1, "x", 1, 9, 1, 100, 1), row("e", 1, "x", 2, 3, 1, 40, 1)])
        self.assertEqual([(r["lat"], r["ok"], r["tok"], r["calls"]) for r in rs], [(3.0, True, 40.0, 1.0)])
    def test_e_failed_first_turn_is_failure(self):
        self.assertFalse(S.runs([row("e", 1, "x", 1, 9, 0), row("e", 1, "x", 2, 3, 1)])[0]["ok"])
    def test_median_p90_min_max(self):
        t = S.table(S.runs([row("a", i, "kagent", 1, l, 1, 100, 1) for i, l in enumerate([1, 9, 5], 1)]))
        self.assertIn("| a | kagent | 3 | 3/3 | 0 | 5.0 | n/a | 1.0 | 9.0 | 100 | 1 |", t)   # n=3: no p90
    def test_p90_shown_when_n_allows(self):
        t = S.table(S.runs([row("a", i, "kagent", 1, i, 1) for i in range(1, 11)]))
        self.assertIn("| 10 | 10/10 | 0 | 5.5 | 9.0 | 1.0 | 10.0 |", t)
    def test_throttled_runs_are_counted_apart(self):
        a = row("a", 1, "kagent", 1, 5, 0, thr=1, state="TASK_STATE_FAILED")
        b = row("a", 2, "kagent", 1, 5, 1)
        rs = S.runs([a, b])
        self.assertTrue(rs[0]["throttled"]); self.assertFalse(rs[1]["throttled"])
        self.assertIn("| 1 | 1/1 | 1 |", S.table(rs))
    def test_legacy_columns_still_read(self):
        a = dict(group="a", run="1", backend="kagent", turn="1", latency_s="5", correct="0", total_tokens="", model_calls="", state="TASK_STATE_FAILED",
                 answer_head="agent run failed: OpenAI API error: 429 T")
        self.assertTrue(S.runs([a])[0]["throttled"])
    def test_arbitrary_backend_names_keep_csv_order(self):
        rows = [row("a", 1, n, 1, 1, 1) for n in ("zeta", "alpha")]
        t = S.table(S.runs(rows))
        self.assertLess(t.index("zeta"), t.index("alpha"))
    def test_concurrency_table(self):
        rows = [row("a", rnd, "ax-api", i, lat, 1, mode="concurrency", k=3, batch=b) for rnd, b, lats in ((1, 6.0, (3, 4, 6)), (2, 8.0, (5, 7, 8))) for i, lat in enumerate(lats, 1)]
        rows.append(row("a", 1, "ax-api", 1, 1, 1))   # a sequential row must not leak into the concurrency table
        t = S.concurrency_table(rows)
        self.assertIn("| ax-api | 3 | 2 | 6/6 | 0 | 5.5 | 8.0 | 7.0 |", t)   # k, rounds, success, throttled, median per-request, max, median batch wall
        self.assertEqual(len(S.runs(rows)), 1)
unittest.main() if __name__ == "__main__" else None
