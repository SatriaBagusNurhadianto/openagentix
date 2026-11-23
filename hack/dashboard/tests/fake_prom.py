#!/usr/bin/env python3
"""A tiny fake of the Prometheus HTTP API for offline tests of the dashboard. Standard library only.

  fake_prom.py --port 0 [--empty] [--fail SUBSTR] [--log FILE]

Prints "listening PORT" on the first line. Every request path+query is appended to --log. --empty returns no series for every
query; --fail makes queries whose text contains SUBSTR answer HTTP 400 with the Prometheus error shape.
"""
import argparse
import json
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

NOW = int(time.time())


def vec(*rows):
    return {"resultType": "vector", "result": [{"metric": m, "value": [NOW, str(v)]} for m, v in rows]}


def mat(name, base):
    return {"resultType": "matrix", "result": [{"metric": {}, "values": [[NOW - 60 * (30 - i), str(base * (1 + (i % 5) / 10))] for i in range(31)]}]}


def answer(expr, rng):
    if expr == "up":
        return vec(({"job": "kb", "namespace": "openagentix-knowledge", "pod": "kb-1"}, 1), ({"job": "vmcp", "namespace": "mcp-tools", "pod": "vmcp-1"}, 1),
                   ({"job": "notify", "namespace": "openagentix-notify", "pod": "notify-1"}, 0))
    if "cost_usd_total" in expr:
        return vec(({"user_id": "assistant"}, 0.0123), ({"user_id": "kb"}, 0.0004))
    if "token_usage_sum" in expr:
        return vec(({"user_id": "assistant", "gen_ai_token_type": "input"}, 30782), ({"user_id": "assistant", "gen_ai_token_type": "output"}, 263),
                   ({"user_id": "kb", "gen_ai_token_type": "input"}, 410))
    if "vector(0)" in expr:
        return vec(({}, 0.02))
    if "sum(rate(agentgateway_requests_total" in expr:
        return mat("rate", 0.05)
    if "sum(increase(agentgateway_requests_total" in expr:
        return vec(({}, 120))
    if "toolhive_mcp_tool_calls_total" in expr:
        return vec(({"tool": "osv_query_vulnerability", "status": "success"}, 7), ({"tool": "osv_query_vulnerability", "status": "error"}, 1))
    if "openagentix_kb_search_requests_total" in expr:
        return vec(({"mode": "hybrid", "outcome": "ok"}, 14), ({"mode": "lexical", "outcome": "ok"}, 3))
    if "histogram_quantile" in expr:
        return mat("p", 0.08 if "0.95" in expr else 0.03)
    if "openagentix_kb_graph_jobs_total" in expr:
        return vec(({"state": "done"}, 9), ({"state": "retry"}, 2))
    if "extraction_tokens_total" in expr:
        return vec(({}, 18400))
    if "graph_jobs_pending" in expr:
        return vec(({}, 3))
    if "openagentix_notify_sends_total" in expr:
        return vec(({"kind": "telegram", "outcome": "delivered"}, 4), ({"kind": "telegram", "outcome": "rate_limited"}, 1))
    return vec(({}, 1))


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=0)
    p.add_argument("--empty", action="store_true")
    p.add_argument("--fail", default=None)
    p.add_argument("--log", default=None)
    a = p.parse_args()

    class H(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            u = urllib.parse.urlsplit(self.path)
            if a.log:
                with open(a.log, "a") as f:
                    f.write(self.path + "\n")
            qs = urllib.parse.parse_qs(u.query)
            expr = (qs.get("query") or [""])[0]
            code, body = 200, None
            if u.path in ("/api/v1/query", "/api/v1/query_range"):
                if a.fail and a.fail in expr:
                    code, body = 400, {"status": "error", "errorType": "bad_data", "error": "parse error: unexpected token in " + a.fail}
                elif not expr:
                    code, body = 400, {"status": "error", "errorType": "bad_data", "error": "missing query"}
                else:
                    data = answer(expr, u.path.endswith("range"))
                    if a.empty:
                        data = {"resultType": data["resultType"], "result": []}
                    body = {"status": "success", "data": data}
            elif u.path == "/health":
                body = {"status": "OK"}
            else:
                code, body = 404, {"status": "error", "error": "not found"}
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    srv = ThreadingHTTPServer(("127.0.0.1", a.port), H)
    print("listening %d" % srv.server_address[1], flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    sys.exit(main())
