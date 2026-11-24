#!/usr/bin/env python3
"""results.csv -> Markdown tables (sequential cells per backend and group; concurrency batches).

A run = one row (groups a-c), the 3 turns summed (d; correct only if all 3 are), or turn 2 of 2 (e, the warm-conversation latency;
correct only if both turns are). Throttled runs (429) are counted apart and excluded from n and from the success rate.
p90 (nearest rank) is shown only from n >= 10. Usage: summarize.py results.csv [more.csv ...]"""
import csv, math, statistics, sys
from collections import defaultdict

LABEL = {"a": "a plain question", "b": "b tool: lodash OSV", "c": "c skill: canary", "d": "d 3-turn memory (sum)", "e": "e plain, 2nd turn"}
NEED = {"d": 3, "e": 2}
P90_MIN_N = 10


def wall(r):
    return float(r.get("wall_s") or r.get("latency_s") or 0)


def is_throttled(r):
    if r.get("throttled") not in (None, ""):
        return r["throttled"] == "1"
    return "429" in r.get("answer_head", "")   # older CSVs


def percentile(values, p):
    v = sorted(values)
    return v[max(0, math.ceil(p / 100.0 * len(v)) - 1)]


def runs(rows):
    by, order = defaultdict(list), []
    for r in rows:
        if r.get("mode") == "concurrency":
            continue
        key = (r["group"], r["backend"], int(r["run"]))
        if key not in by:
            order.append(key)
        by[key].append(r)
    out = []
    for key in order:
        g, b, n = key
        rs = sorted(by[key], key=lambda x: int(x["turn"]))
        need = NEED.get(g, 1)
        ok = len(rs) == need and all(x["correct"] == "1" for x in rs)
        last = rs[-1] if g == "e" and len(rs) == need else None
        use = [last] if last else rs
        num = lambda k: sum(float(x[k]) for x in use if x.get(k, "") != "")
        have = lambda k: any(x.get(k, "") != "" for x in use)
        lat = wall(last) if last else sum(wall(x) for x in rs)
        out.append({"throttled": any(is_throttled(x) for x in rs), "group": g, "backend": b, "run": n, "lat": lat, "ok": ok,
                    "tok": num("total_tokens") if have("total_tokens") else None,
                    "calls": num("model_calls") if have("model_calls") else None})
    return out


def f1(v):
    return "%.1f" % v


def table(rs):
    lines = ["| group | backend | n | success | throttled (429, excluded) | median s | p90 s | min s | max s | median tokens | median model calls |",
             "|---|---|---|---|---|---|---|---|---|---|---|"]
    backends = list(dict.fromkeys(r["backend"] for r in rs))
    keys = sorted({(r["group"], r["backend"]) for r in rs}, key=lambda k: (k[0], backends.index(k[1])))
    for g, b in keys:
        allx = [r for r in rs if r["group"] == g and r["backend"] == b]
        x = [r for r in allx if not r["throttled"]]
        lat = [r["lat"] for r in x]
        toks = [r["tok"] for r in x if r["tok"] is not None]
        calls = [r["calls"] for r in x if r["calls"] is not None]
        if lat:
            cells = [f1(statistics.median(lat)), f1(percentile(lat, 90)) if len(lat) >= P90_MIN_N else "n/a", f1(min(lat)), f1(max(lat))]
        else:
            cells = ["n/a"] * 4
        lines.append("| %s | %s | %d | %d/%d | %d | %s | %s | %s |" % (
            g, b, len(x), sum(r["ok"] for r in x), len(x), len(allx) - len(x), " | ".join(cells),
            "%d" % statistics.median(toks) if toks else "n/a", "%g" % statistics.median(calls) if calls else "n/a"))
    return "\n".join(lines)


def concurrency_table(rows):
    rows = [r for r in rows if r.get("mode") == "concurrency"]
    lines = ["| backend | K | rounds | success | throttled | median request s | max request s | median batch wall s |", "|---|---|---|---|---|---|---|---|"]
    for b in dict.fromkeys(r["backend"] for r in rows):
        rb = [r for r in rows if r["backend"] == b]
        thr = [r for r in rb if is_throttled(r)]
        ok = [r for r in rb if r["correct"] == "1"]
        lat = [wall(r) for r in rb if not is_throttled(r)]
        rounds = sorted({r["run"] for r in rb})
        batch = [float(next(r["batch_wall_s"] for r in rb if r["run"] == n)) for n in rounds]
        lines.append("| %s | %s | %d | %d/%d | %d | %s | %s | %s |" % (
            b, rb[0]["k"], len(rounds), len(ok), len(rb), len(thr), f1(statistics.median(lat)) if lat else "n/a",
            f1(max(lat)) if lat else "n/a", f1(statistics.median(batch))))
    return "\n".join(lines)


if __name__ == "__main__":
    allrows = []
    for p in sys.argv[1:]:
        allrows += list(csv.DictReader(open(p, newline="")))
    seq = runs(allrows)
    if seq:
        print("### Sequential\n\n" + table(seq))
    if any(r.get("mode") == "concurrency" for r in allrows):
        print("\n### Concurrency (K identical plain questions at once)\n\n" + concurrency_table(allrows))
