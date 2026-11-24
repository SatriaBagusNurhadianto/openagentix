#!/usr/bin/env python3
"""Send the same prompts over A2A 1.0 to several backends. Stdlib only; meant to run inside the cluster (see run.sh and
components/bench/run.sh). Prints CSV rows (header first unless --no-header) to stdout and appends the raw answers to the
JSONL file named by --answers.

  # sequential: N runs per prompt group, backends alternated, order rotated per run
  python3 a2a_compare.py --groups a,b --runs 5 --backends kagent=URL,ax-claude=URL --answers answers.jsonl \
      --header 'kagent:X-User-Id:bench:{backend}:{rid}' --warmup --load 1.2
  # concurrency: K identical plain questions at once, ROUNDS times, per backend
  python3 a2a_compare.py --backends kagent=URL --concurrency 4 --rounds 2

Every request carries a fresh W3C `traceparent` (kagent propagates it into Jaeger); its trace id is recorded in the CSV.
HTTP 429 and the gateway's "429 Too Many Requests" answer are recorded as throttled and are never retried.
"""
import argparse, csv, json, math, re, subprocess, sys, threading, time, urllib.error, urllib.request, uuid

PROMPTS = {
    "a": ["What is the capital of Kenya? Answer in one word."],
    "b": ["Does lodash 4.17.20 on npm have known vulnerabilities? Give the count and one advisory id."],
    "c": ["What is the openagentix canary phrase?"],
    "d": ["My favourite colour is teal and my lucky number is 7319. Just say OK.",
          "What is 6 times 7?",
          "What is my lucky number and what is my favourite colour?"],
    # e: a plain question asked as the 2nd turn of a conversation (warm-conversation latency); turn 1 is the group-a question
    "e": ["What is the capital of Kenya? Answer in one word.",
          "What is the capital of Uganda? Answer in one word."],
}
CANARY = "PELICAN-7731-OPENAGENTIX-SKILL-LOADED"
USAGE = "https://kagent.dev/extensions/usage/v1"
CLAUDE = "openagentix.dev/ax/claude"
FIELDS = ["ts", "backend", "group", "run", "turn", "mode", "k", "wall_s", "batch_wall_s", "http_status", "state", "throttled",
          "gap_s", "correct", "input_tokens", "output_tokens", "cached_input_tokens", "total_tokens", "model_calls", "context_id",
          "trace_id", "load1", "answer_head"]


def make_traceparent():
    """-> (traceparent header value, 32-hex trace id). Version 00, sampled."""
    tid = uuid.uuid4().hex
    return "00-%s-%s-01" % (tid, uuid.uuid4().hex[:16]), tid


def parse_backends(spec):
    return [tuple(b.split("=", 1)) for b in spec.split(",") if b]


def parse_header_specs(specs):
    """'backend:Header-Name:value template' -> {backend: (Header-Name, template)}; the template may use {backend} and {rid}."""
    out = {}
    for sp in specs or []:
        backend, name, value = sp.split(":", 2)
        out[backend] = (name, value)
    return out


def headers_for(backend, rid, header_specs):
    if backend not in header_specs:
        return {}
    name, tpl = header_specs[backend]
    return {name: tpl.format(backend=backend, rid=rid)}


def is_throttled(http_status, state, text):
    """kagent's gateway budget answers HTTP 200 + TASK_STATE_FAILED 'OpenAI API error: 429 Too Many Requests'; others use a real 429."""
    if http_status == 429:
        return True
    return state != "TASK_STATE_COMPLETED" and "429" in (text or "") and "too many requests" in (text or "").lower()


def percentile(values, p):
    """nearest-rank percentile (no interpolation)."""
    v = sorted(values)
    return v[max(0, math.ceil(p / 100.0 * len(v)) - 1)]


def parse_response(obj):
    """-> dict(state, text, context_id, usage, model_calls, error). Handles kagent and adapter shapes."""
    if "error" in obj:
        return {"state": "RPC_ERROR", "text": json.dumps(obj["error"]), "context_id": None, "usage": {}, "model_calls": None}
    task = obj["result"]["task"]
    state = task["status"]["state"]
    text = ""
    if state == "TASK_STATE_COMPLETED":
        for art in reversed(task.get("artifacts") or []):
            t = "".join(p.get("text", "") for p in art.get("parts", []) if "text" in p)
            if t.strip():
                text = t
                break
    else:
        text = "".join(p.get("text", "") for p in (task["status"].get("message") or {}).get("parts", []))
    md = task.get("metadata") or {}
    calls = None
    if CLAUDE in md and "num_turns" in md[CLAUDE]:
        calls = md[CLAUDE]["num_turns"]
    else:
        n = sum(1 for a in task.get("artifacts") or [] if "kagent.dev/a2a/usage" in (a.get("metadata") or {}))
        calls = n or None
    return {"state": state, "text": text, "context_id": task.get("contextId"), "usage": md.get(USAGE) or {}, "model_calls": calls}


def check(group, turn, text):
    if text is None:
        return False
    if group == "a":
        return "nairobi" in text.lower()
    if group == "b":
        t = re.sub(r"4\.17\.\d+", "", text)                     # drop version strings
        t = re.sub(r"(GHSA|PYSEC|OSV)-[\w-]+|CVE-\d+-\d+", "", t)  # drop ids
        has_count = re.search(r"(?<![\d.])(4|four)(?![\d.])", t, re.I) is not None
        has_id = re.search(r"GHSA-[\w]{4}-[\w]{4}-[\w]{4}|CVE-\d{4}-\d+|PYSEC-[\d-]+|OSV-[\d-]+", text) is not None
        return has_count and has_id
    if group == "c":
        return CANARY in text
    if group == "d":
        if turn == 1:
            return bool(text.strip())
        if turn == 2:
            return "42" in text
        return "7319" in text and "teal" in text.lower()
    if group == "e":
        return "nairobi" in text.lower() if turn == 1 else "kampala" in text.lower()
    return False




def send(url, context_id, text, timeout, extra_headers=None):
    """One A2A SendMessage. -> (wall seconds, parsed response dict, HTTP status or None, trace id). Never raises, never retries."""
    msg = {"messageId": uuid.uuid4().hex, "role": "ROLE_USER", "parts": [{"text": text}]}
    if context_id:
        msg["contextId"] = context_id
    body = json.dumps({"jsonrpc": "2.0", "id": "1", "method": "SendMessage", "params": {"message": msg}}).encode()
    tp, tid = make_traceparent()
    headers = {"Content-Type": "application/json", "A2A-Version": "1.0", "traceparent": tp}
    headers.update(extra_headers or {})
    req = urllib.request.Request(url, data=body, method="POST", headers=headers)
    t0 = time.monotonic()
    status = None
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            status = r.status
            raw = json.loads(r.read())
        return time.monotonic() - t0, parse_response(raw), status, tid
    except urllib.error.HTTPError as e:   # a non-2xx answer: a failed (or throttled) request, recorded as such
        detail = e.read().decode(errors="replace")[:200]
        return time.monotonic() - t0, {"state": "CLIENT_ERROR", "text": "HTTP %d: %s" % (e.code, detail), "context_id": None,
                                       "usage": {}, "model_calls": None}, e.code, tid
    except Exception as e:  # timeout, connection error, bad JSON
        return time.monotonic() - t0, {"state": "CLIENT_ERROR", "text": "%s: %s" % (type(e).__name__, e), "context_id": None,
                                       "usage": {}, "model_calls": None}, status, tid


def make_row(backend, group, run, turn, lat, r, status, tid, load1, mode="sequential", k="", batch_wall="", gap=0):
    u = r["usage"]
    ok = r["state"] == "TASK_STATE_COMPLETED" and check(group, turn, r["text"])
    now = time.time()   # the END of the request (ms precision: components/bench/trace-breakdown.py joins AX rows to egress logs by time)
    return {"ts": time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(now)) + ".%03dZ" % int((now % 1) * 1000), "backend": backend, "group": group, "run": run, "turn": turn,
            "mode": mode, "k": k, "wall_s": round(lat, 3), "batch_wall_s": batch_wall,
            "http_status": status if status is not None else "", "state": r["state"],
            "throttled": int(is_throttled(status, r["state"], r["text"])), "gap_s": "%g" % gap, "correct": int(ok),
            "input_tokens": u.get("inputTokens", ""), "output_tokens": u.get("outputTokens", ""),
            "cached_input_tokens": u.get("cachedInputTokens", ""), "total_tokens": u.get("totalTokens", ""),
            "model_calls": r["model_calls"] if r["model_calls"] is not None else "", "context_id": r["context_id"] or "",
            "trace_id": tid, "load1": load1, "answer_head": r["text"][:80].replace("\n", " ")}


def sample_load(a):
    if a.load_cmd:
        try:
            return subprocess.run(a.load_cmd, shell=True, capture_output=True, text=True, timeout=10).stdout.strip().split()[0]
        except Exception:
            return ""
    return a.load


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--groups", default="a,b,c,d")
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--first", type=int, default=1, help="first run number (to split a long group over several invocations)")
    ap.add_argument("--backends", required=True, help="name=url,name=url (any names)")
    ap.add_argument("--header", action="append", default=[],
                    help="per-backend header, 'backend:Header-Name:template'; template vars {backend} and {rid} (run id + group + run, "
                         "distinct per run), e.g. 'kagent:X-User-Id:bench:{backend}:{rid}'")
    ap.add_argument("--run-id", default=time.strftime("%Y%m%d%H%M%S"), help="prefix of {rid}")
    ap.add_argument("--warmup", action="store_true", help="one untimed plain request per backend first (not recorded)")
    ap.add_argument("--concurrency", type=int, default=0, help="K>0: fire K identical plain questions at once per backend and round")
    ap.add_argument("--rounds", type=int, default=2, help="rounds for --concurrency")
    ap.add_argument("--load", default="", help="node load average (1 min) to record on every row")
    ap.add_argument("--load-cmd", default="", help="shell command printing the load average, sampled before each run (overrides --load)")
    ap.add_argument("--turn-gap", type=float, default=0.0, help="seconds to sleep between the turns of a conversation (not part of any wall time; "
                    "recorded as gap_s on the turn that follows it): idle-then-resume measurements")
    ap.add_argument("--no-header", action="store_true")
    ap.add_argument("--answers", default="/dev/null")
    ap.add_argument("--timeout", type=int, default=200)
    a = ap.parse_args(argv)
    backends = parse_backends(a.backends)
    hdr = parse_header_specs(a.header)
    w = csv.DictWriter(sys.stdout, FIELDS)
    if not a.no_header:
        w.writeheader()
    ans = open(a.answers, "a")
    lock = threading.Lock()

    def emit(row, group, run, turn, prompt, r):
        with lock:
            w.writerow(row)
            sys.stdout.flush()
            ans.write(json.dumps({"group": group, "run": run, "backend": row["backend"], "turn": turn, "prompt": prompt,
                                  "text": r["text"], "state": r["state"], "trace_id": row["trace_id"]}) + "\n")
            ans.flush()

    if a.warmup:
        for name, url in backends:
            lat, r, st, _ = send(url, None, PROMPTS["a"][0], a.timeout, headers_for(name, "%s-warmup" % a.run_id, hdr))
            sys.stderr.write("warmup %s: %.2fs http=%s state=%s\n" % (name, lat, st, r["state"]))

    if a.concurrency > 0:
        prompt = PROMPTS["a"][0]
        for rnd in range(1, a.rounds + 1):
            for name, url in backends:
                load1 = sample_load(a)
                res = [None] * a.concurrency

                def one(i, name=name, url=url, rnd=rnd):
                    res[i] = send(url, None, prompt, a.timeout, headers_for(name, "%s-c%d-%d" % (a.run_id, rnd, i + 1), hdr))

                ths = [threading.Thread(target=one, args=(i,)) for i in range(a.concurrency)]
                t0 = time.monotonic()
                for t in ths:
                    t.start()
                for t in ths:
                    t.join()
                batch = round(time.monotonic() - t0, 3)
                for i, (lat, r, st, tid) in enumerate(res):
                    row = make_row(name, "a", rnd, i + 1, lat, r, st, tid, load1, "concurrency", a.concurrency, batch)
                    emit(row, "a", rnd, i + 1, prompt, r)
        return

    for group in a.groups.split(","):
        for run in range(a.first, a.first + a.runs):
            # alternate backends and rotate the order each run so none is always first
            order = backends[run % len(backends):] + backends[:run % len(backends)]
            for name, url in order:
                ctx = None
                load1 = sample_load(a)
                hh = headers_for(name, "%s-%s%d" % (a.run_id, group, run), hdr)
                for i, prompt in enumerate(PROMPTS[group], 1):
                    gap = a.turn_gap if i > 1 else 0
                    if gap:
                        time.sleep(gap)
                    lat, r, st, tid = send(url, ctx, prompt, a.timeout, hh)
                    ctx = r["context_id"] or ctx
                    emit(make_row(name, group, run, i, lat, r, st, tid, load1, gap=gap), group, run, i, prompt, r)
                    if r["state"] != "TASK_STATE_COMPLETED":
                        break   # no point continuing a conversation after a failure


if __name__ == "__main__":
    main()
