#!/usr/bin/env python3
"""Local server and query checker for hack/dashboard/index.html. Python 3 standard library only.

  serve.py serve --listen 127.0.0.1:8765 --upstream http://127.0.0.1:8428 [--jaeger http://127.0.0.1:16687]
      Serves the page and proxies a short allowlist of read-only Prometheus API paths to the upstream (the port-forwarded
      VictoriaMetrics). The page only ever talks to this origin, so no CORS headers are needed or sent.

  serve.py check --url http://127.0.0.1:8428 [--window 1h]
      Runs every PromQL query of the page against a Prometheus-compatible API and exits 1 if any query is an error
      (a bad query, an unreachable API). An empty result is fine: it means "no data yet".
"""
import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
PAGE = os.path.join(HERE, "index.html")

# Read-only query endpoints only. Nothing that writes (admin, import, delete) is reachable through the proxy.
ALLOWED = re.compile(r"^/api/v1/(query|query_range|series|labels|label/[A-Za-z0-9_:]+/values|targets|status/buildinfo)$")
CSP = ("default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; "
       "img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")


def load_queries(page=PAGE):
    with open(page, encoding="utf-8") as f:
        html = f.read()
    m = re.search(r'<script type="application/json" id="queries">(.*?)</script>', html, re.S)
    if not m:
        raise SystemExit("serve.py: no queries block in " + page)
    return json.loads(m.group(1))


def expand(expr, window):
    return expr.replace("{{W}}", window).replace("{{R}}", "5m")


def make_handler(upstream, jaeger, port_hosts):
    class H(BaseHTTPRequestHandler):
        server_version = "oa-dashboard"
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):  # quiet: the query strings hold only PromQL, but keep the terminal clean
            pass

        def _send(self, code, body, ctype, extra=None):
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Referrer-Policy", "no-referrer")
            for k, v in (extra or {}).items():
                self.send_header(k, v)
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)

        def _deny(self, code, msg):
            self._send(code, (msg + "\n").encode(), "text/plain; charset=utf-8")

        def _route(self):
            # DNS rebinding guard: only answer to our own loopback host names.
            host = (self.headers.get("Host") or "").lower()
            if host not in port_hosts:
                return self._deny(403, "forbidden host")
            u = urllib.parse.urlsplit(self.path)
            if u.path in ("/", "/index.html"):
                with open(PAGE, "rb") as f:
                    return self._send(200, f.read(), "text/html; charset=utf-8", {"Content-Security-Policy": CSP})
            if u.path == "/config.json":
                return self._send(200, json.dumps({"jaeger": jaeger}).encode(), "application/json")
            if ALLOWED.match(u.path):
                target = upstream.rstrip("/") + u.path + (("?" + u.query) if u.query else "")
                try:
                    with urllib.request.urlopen(urllib.request.Request(target, headers={"Accept": "application/json"}), timeout=20) as r:
                        return self._send(r.status, r.read(), r.headers.get("Content-Type", "application/json"))
                except urllib.error.HTTPError as e:
                    return self._send(e.code, e.read(), e.headers.get("Content-Type", "application/json"))
                except Exception as e:  # upstream down: answer in the API's own error shape so the page shows it
                    body = json.dumps({"status": "error", "errorType": "unavailable", "error": "upstream unreachable: " + type(e).__name__}).encode()
                    return self._send(502, body, "application/json")
            return self._deny(404, "not found")

        def do_GET(self):
            self._route()

        do_HEAD = do_GET

        def _method_not_allowed(self):
            self._deny(405, "read-only")

        do_POST = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = _method_not_allowed

    return H


def cmd_serve(a):
    host, _, port = a.listen.rpartition(":")
    if host not in ("127.0.0.1", "localhost", "::1"):
        raise SystemExit("serve.py: --listen must be a loopback address, got " + a.listen)
    srv = ThreadingHTTPServer((host, int(port)), None)
    real = srv.server_address[1]
    hosts = {"127.0.0.1:%d" % real, "localhost:%d" % real, "[::1]:%d" % real}
    srv.RequestHandlerClass = make_handler(a.upstream, a.jaeger, hosts)
    print("listening http://127.0.0.1:%d" % real, flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        srv.server_close()


def cmd_check(a):
    base = a.url.rstrip("/")
    failed = 0
    now = int(time.time())
    secs = 86400 if a.window == "24h" else 3600
    for q in load_queries():
        expr = expand(q["expr"], a.window)
        if q["kind"] == "range":
            path, params = "/api/v1/query_range", {"query": expr, "start": now - secs, "end": now, "step": max(secs // 60, 1)}
        else:
            path, params = "/api/v1/query", {"query": expr, "time": now}
        url = base + path + "?" + urllib.parse.urlencode(params)
        try:
            with urllib.request.urlopen(url, timeout=20) as r:
                body = json.loads(r.read())
        except urllib.error.HTTPError as e:
            try:
                body = json.loads(e.read())
            except Exception:
                body = {"status": "error", "error": "HTTP %d" % e.code}
        except Exception as e:
            body = {"status": "error", "error": "%s: %s" % (type(e).__name__, e)}
        if body.get("status") != "success":
            failed += 1
            print("FAIL  %-17s %s\n      %s" % (q["id"], expr, body.get("error", "unknown error")))
            continue
        n = len(body.get("data", {}).get("result", []))
        print("ok    %-17s %s" % (q["id"], ("%d series" % n) if n else "no data yet"))
    total = len(load_queries())
    print("%d of %d queries ok" % (total - failed, total))
    return 1 if failed else 0


def main(argv):
    p = argparse.ArgumentParser(prog="serve.py")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("serve")
    s.add_argument("--listen", default="127.0.0.1:8765")
    s.add_argument("--upstream", required=True)
    s.add_argument("--jaeger", default=None)
    c = sub.add_parser("check")
    c.add_argument("--url", required=True)
    c.add_argument("--window", default="1h", choices=["1h", "24h"])
    a = p.parse_args(argv)
    if a.cmd == "serve":
        return cmd_serve(a) or 0
    return cmd_check(a)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
