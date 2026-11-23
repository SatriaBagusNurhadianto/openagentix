#!/usr/bin/env python3
"""Split the wall time of benchmark turns into where it went. Read-only (kubectl get --raw, kubectl logs). Stdlib only.

  trace-breakdown.py results.csv [more.csv ...] [--context kind-openagentix] [--rows-out breakdown.csv]

kagent rows (backend name starting with "kagent"): the row's trace_id (the W3C trace id the harness sent) is looked up in the bundled
Jaeger (namespace openagentix-observability, service jaeger, UI/API port 16686) through the Kubernetes API proxy, so no port-forward is
started. Per turn: total (the controller's POST /agents/ span), pre-dispatch (that span's start to the agent's a2a.request: controller
work and the sandbox resume), model (gateway spans `POST /v1/chat/completions`), embeddings (`POST /v1/embeddings`, the memory recall),
tools (vMCP `tools/call`; if a trace has none, the agent's `execute_tool` spans instead) and the remainder (runtime overhead and hops),
plus the prompt tokens of every model call and whether any cache-read tokens were reported.

AX rows (backend name starting with "ax"): no spans exist. The egress proxy log (namespace ate-system, deployment atenet-egress) has one
line per outbound request of a sandbox, joined by the actor name (the adapter names tasks turn-<id>), and the ax-server log says when the
task was resumed. Joined to a CSV row by time (the row's ts is the END of the request). Derived: request start to "resumed", model
(POST api.anthropic.com /v1/messages: sum of durations and count), tools (POST to the vMCP host), other egress (e.g. Claude Code's own
settings calls), remainder. NOT splittable without content or spans: sandbox start-up, the agent process's own CPU time, JSON building,
and the gaps between calls. Concurrent rows cannot be joined by time and are skipped.
"""
import argparse, csv, json, re, statistics, subprocess, sys, time
from collections import defaultdict
from datetime import datetime, timezone

JAEGER_PATH = "/api/v1/namespaces/%s/services/jaeger:ui/proxy"


def _us(v):
    return v / 1e6


def kagent_breakdown(trace):
    data = (trace or {}).get("data") or []
    if not data or not data[0].get("spans"):
        return None
    t = data[0]
    procs = t.get("processes") or {}
    spans = t["spans"]
    svc = lambda s: (procs.get(s["processID"]) or {}).get("serviceName", s["processID"])
    ids = {s["spanID"] for s in spans}
    roots = [s for s in spans if not any(r["spanID"] in ids for r in s.get("references") or [])]
    cand = [s for s in roots if s["operationName"].startswith("POST /agents/")] or roots or spans
    root = max(cand, key=lambda s: s["duration"])
    tag = lambda s, k: next((x["value"] for x in s.get("tags") or [] if x["key"] == k), None)
    sumd = lambda pred: sum(_us(s["duration"]) for s in spans if pred(s))
    chat = sorted([s for s in spans if svc(s) == "model-gateway" and s["operationName"] == "POST /v1/chat/completions"], key=lambda s: s["startTime"])
    model = sum(_us(s["duration"]) for s in chat)
    embed = sumd(lambda s: svc(s) == "model-gateway" and s["operationName"] == "POST /v1/embeddings")
    tools = sumd(lambda s: s["operationName"].startswith("tools/call")) or sumd(lambda s: s["operationName"].startswith("execute_tool"))
    a2a = next((s for s in spans if s["operationName"] == "a2a.request"), None)
    total = _us(root["duration"])
    return {"total_s": total, "pre_dispatch_s": _us(a2a["startTime"] - root["startTime"]) if a2a else None,
            "model_s": model, "embed_s": embed, "tools_s": tools, "remainder_s": total - model - embed - tools,
            "model_calls": len(chat), "prompt_tokens": [int(tag(s, "gen_ai.usage.input_tokens") or 0) for s in chat],
            "cache_read_tokens": sum(int(tag(s, "gen_ai.usage.cache_read.input_tokens") or 0) for s in chat)}


# ---- AX: egress log and ax-server log -----------------------------------------------------------------------------------------
KV = re.compile(r'([\w.]+)=("[^"]*"|\S+)')


def parse_ts(s):
    s = s.strip().replace("Z", "+00:00")
    if "." in s:
        head, rest = s.split(".", 1)
        frac = re.match(r"\d+", rest).group(0)[:6]
        s = head + "." + frac + rest[len(re.match(r"\d+", rest).group(0)):]
    return datetime.fromisoformat(s).astimezone(timezone.utc).timestamp()


def _dur(v):
    m = re.match(r"^([\d.]+)(ms|s|us|µs)$", v)
    if not m:
        return 0.0
    n = float(m.group(1))
    return n / 1000 if m.group(2) == "ms" else n if m.group(2) == "s" else n / 1e6


def parse_egress(lines):
    out = []
    for ln in lines:
        if "\trequest " not in ln and " request " not in ln:
            continue
        kv = {k: v.strip('"') for k, v in KV.findall(ln)}
        if kv.get("ate.atespace") != "default" or "ate.actor.name" not in kv:
            continue
        try:
            ts = parse_ts(ln.split("\t", 1)[0].split(" ", 1)[0])
        except Exception:
            continue
        out.append({"ts": ts, "host": kv.get("http.host", ""), "path": kv.get("http.path", ""), "method": kv.get("http.method", ""),
                    "status": kv.get("http.status", ""), "duration_s": _dur(kv.get("duration", "")), "actor": kv["ate.actor.name"],
                    "uid": kv.get("ate.actor.uid", "")})
    return out


def parse_server(lines):
    """ax-server JSON log -> {actor: {resuming, resumed, deleting}} (epoch seconds)."""
    out = defaultdict(dict)
    for ln in lines:
        try:
            j = json.loads(ln)
        except Exception:
            continue
        key = {"resuming actor on Substrate worker": "resuming", "task successfully resumed": "resumed",
               "deleting Substrate actor for task": "deleting"}.get(j.get("msg"))
        actor = j.get("actor") or j.get("name") or j.get("task")
        if key and actor:
            out[actor][key] = parse_ts(j["time"])
    return dict(out)


def ax_breakdown(end_ts, wall_s, egress, server):
    start = end_ts - wall_s
    inwin = [e for e in egress if start - 0.2 <= e["ts"] <= end_ts + 0.2 and e["actor"].startswith("turn-")]
    counts = defaultdict(int)
    for e in inwin:
        counts[e["actor"]] += 1
    if not counts:
        return None
    ranked = sorted(counts.items(), key=lambda kv: -kv[1])
    if len(ranked) > 1 and ranked[1][1] * 2 >= ranked[0][1]:
        return {"actor": "ambiguous"}      # two actors with comparable activity in the window (concurrent requests)
    a = ranked[0][0]
    ev = [e for e in egress if e["actor"] == a]
    model = [e for e in ev if e["host"] == "api.anthropic.com" and e["method"] == "POST" and e["path"].startswith("/v1/messages")]
    tools = [e for e in ev if "vmcp" in e["host"] and e["method"] == "POST"]
    other = [e for e in ev if e["host"] == "api.anthropic.com" and e not in model]
    resumed = ((server or {}).get(a) or {}).get("resumed")
    to_resumed = max(0.0, resumed - start) if resumed else None
    m, t = sum(e["duration_s"] for e in model), sum(e["duration_s"] for e in tools)
    return {"actor": a, "to_resumed_s": to_resumed, "model_s": m, "model_calls": len(model), "tools_s": t,
            "other_egress_s": sum(e["duration_s"] for e in other), "remainder_s": wall_s - (to_resumed or 0.0) - m - t}


# ---- tables -------------------------------------------------------------------------------------------------------------------
def _med(v):
    v = [x for x in v if x is not None]
    return statistics.median(v) if v else None


def _f(v):
    return "n/a" if v is None else "%.1f" % v


def kagent_table(rows):
    head = ["| backend | group | turn | n | median total s | pre-dispatch s | model s | embeddings s | tools s | remainder s | median model calls | median prompt tokens/call | cache-read reported |",
            "|---|---|---|---|---|---|---|---|---|---|---|---|---|"]
    by = defaultdict(list)
    for r in rows:
        by[(r["backend"], r["group"], int(r["turn"]))].append(r)
    for (b, g, t), rs in by.items():
        toks = [x for r in rs for x in r["prompt_tokens"]]
        head.append("| %s | %s | %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            b, g, t, len(rs), _f(_med([r["total_s"] for r in rs])), _f(_med([r["pre_dispatch_s"] for r in rs])),
            _f(_med([r["model_s"] for r in rs])), _f(_med([r["embed_s"] for r in rs])), _f(_med([r["tools_s"] for r in rs])),
            _f(_med([r["remainder_s"] for r in rs])), "%g" % _med([r["model_calls"] for r in rs]),
            "%d" % statistics.median(toks) if toks else "n/a", "yes" if any(r["cache_read_tokens"] for r in rs) else "no"))
    return "\n".join(head)


def ax_table(rows):
    head = ["| backend | group | turn | n joined | median wall s | request start to resumed s | model s (egress) | tools s (egress) | other egress s | remainder s | median model calls |",
            "|---|---|---|---|---|---|---|---|---|---|---|"]
    by = defaultdict(list)
    for r in rows:
        by[(r["backend"], r["group"], int(r["turn"]))].append(r)
    for (b, g, t), rs in by.items():
        head.append("| %s | %s | %d | %d | %s | %s | %s | %s | %s | %s | %s |" % (
            b, g, t, len(rs), _f(_med([r["wall_s"] for r in rs])), _f(_med([r["to_resumed_s"] for r in rs])), _f(_med([r["model_s"] for r in rs])),
            _f(_med([r["tools_s"] for r in rs])), _f(_med([r["other_egress_s"] for r in rs])), _f(_med([r["remainder_s"] for r in rs])),
            "%g" % _med([r["model_calls"] for r in rs])))
    return "\n".join(head)


# ---- cluster access -----------------------------------------------------------------------------------------------------------
def kubectl(args, ctx, kc="kubectl", timeout=60):
    return subprocess.run([kc, "--context", ctx] + args, capture_output=True, text=True, timeout=timeout)


def fetch_trace(tid, ctx, ns, kc, wait):
    deadline = time.time() + wait
    while True:
        p = kubectl(["get", "--raw", (JAEGER_PATH % ns) + "/api/traces/" + tid], ctx, kc)
        if p.returncode == 0:
            try:
                j = json.loads(p.stdout)
                if (j.get("data") or [{}])[0].get("spans"):
                    return j
            except Exception:
                pass
        if time.time() >= deadline:
            return None
        time.sleep(1.5)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("csv", nargs="+")
    ap.add_argument("--context", default="kind-openagentix")
    ap.add_argument("--kubectl", default="kubectl")
    ap.add_argument("--obs-namespace", default="openagentix-observability")
    ap.add_argument("--wait", type=int, default=10, help="seconds to wait for a trace to appear in Jaeger")
    ap.add_argument("--rows-out", default="", help="write the per-turn numbers to this CSV")
    ap.add_argument("--no-ax", action="store_true")
    a = ap.parse_args(argv)
    rows = []
    for p in a.csv:
        rows += list(csv.DictReader(open(p, newline="")))
    out_k, out_a, per_row, missing = [], [], [], 0
    ps = kubectl(["get", "--raw", (JAEGER_PATH % a.obs_namespace) + "/api/v3/services"], a.context, a.kubectl)
    if ps.returncode != 0:
        print("trace-breakdown: Jaeger not reachable (%s); kagent rows skipped" % ps.stderr.strip()[:200], file=sys.stderr)
    for r in rows:
        if r["backend"].startswith("kagent") and r.get("trace_id") and ps.returncode == 0:
            b = kagent_breakdown(fetch_trace(r["trace_id"], a.context, a.obs_namespace, a.kubectl, a.wait))
            if b is None:
                missing += 1
                continue
            row = dict(b, backend=r["backend"], group=r["group"], turn=r["turn"], trace_id=r["trace_id"])
            out_k.append(row); per_row.append(row)
    ax_rows = [r for r in rows if r["backend"].startswith("ax") and r.get("mode", "sequential") != "concurrency" and not a.no_ax]
    if ax_rows:
        first = min(parse_ts(r["ts"]) - float(r["wall_s"]) for r in ax_rows) - 30
        since = datetime.fromtimestamp(first, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        eg = kubectl(["logs", "-n", "ate-system", "deploy/atenet-egress", "--since-time=" + since], a.context, a.kubectl, 180).stdout.splitlines()
        sv = kubectl(["logs", "-n", "ax-system", "deploy/ax-server", "--since-time=" + since], a.context, a.kubectl, 180).stdout.splitlines()
        egress, server = parse_egress(eg), parse_server(sv)
        for r in ax_rows:
            b = ax_breakdown(parse_ts(r["ts"]), float(r["wall_s"]), egress, server)
            if b is None or b["actor"] == "ambiguous":
                missing += 1
                continue
            row = dict(b, backend=r["backend"], group=r["group"], turn=r["turn"], wall_s=float(r["wall_s"]))
            out_a.append(row); per_row.append(row)
    if out_k:
        print("### kagent (Jaeger spans), medians per backend, group and turn\n\n" + kagent_table(out_k) + "\n")
    if out_a:
        print("### AX (egress proxy log joined by actor and time, no spans), medians\n\n" + ax_table(out_a) + "\n")
        print("Cannot be split for AX: sandbox start-up, the agent process's own time and the gaps between calls are all in `remainder`; "
              "prompt tokens and cache use per call are not visible without content.\n")
    if missing:
        print("%d row(s) had no trace / no unambiguous actor and are not in the tables.\n" % missing)
    if a.rows_out and per_row:
        keys = sorted({k for r in per_row for k in r})
        with open(a.rows_out, "w", newline="") as f:
            w = csv.DictWriter(f, keys)
            w.writeheader()
            w.writerows(per_row)


if __name__ == "__main__":
    main()
