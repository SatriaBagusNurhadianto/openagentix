"""Deterministic OpenAI-compatible chat-completions stub that drives MCP tool calls.

- last message is a user message containing `use tool <name> <json-args>` and <name> is advertised
  in request.tools  -> assistant message with tool_calls
- last message is role=tool -> final assistant message quoting the tool result
- anything else -> plain canned reply (lists the advertised tool names)
Supports stream=false and stream=true (single-chunk SSE). Every request is logged to stdout.
"""
import json, re, time, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TRIGGER = re.compile(r"use tool\s+(\S+)\s*(\{.*\})?", re.S | re.I)

def text_of(m):
    c = m.get("content")
    if isinstance(c, list):
        return "".join(p.get("text", "") for p in c if isinstance(p, dict))
    return c or ""

def decide(body):
    msgs = body.get("messages", [])
    tools = [t.get("function", {}).get("name") for t in body.get("tools", []) or []]
    last = msgs[-1] if msgs else {}
    if last.get("role") == "tool":
        return {"content": "TOOL-RESULT-QUOTE: " + text_of(last)}
    if last.get("role") == "user":
        m = TRIGGER.search(text_of(last))
        if m:
            name, args = m.group(1), (m.group(2) or "{}")
            if name not in tools:
                return {"content": "tool %s not advertised; advertised=%s" % (name, tools)}
            try: json.loads(args)
            except ValueError: return {"content": "bad json args: " + args}
            return {"tool_calls": [{"id": "call_" + uuid.uuid4().hex[:8], "type": "function",
                                    "function": {"name": name, "arguments": args}}]}
    return {"content": "mock-llm-tools: no trigger; advertised tools=%s" % tools}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("content-length", 0))) or b"{}")
        d = decide(body)
        print("REQ msgs=%d tools=%s last=%s -> %s" % (len(body.get("messages", [])),
              [t.get("function", {}).get("name") for t in body.get("tools", []) or []],
              json.dumps(body.get("messages", [{}])[-1])[:300], json.dumps(d)[:300]), flush=True)
        msg = {"role": "assistant", "content": d.get("content")}
        fin = "stop"
        if "tool_calls" in d: msg["tool_calls"] = d["tool_calls"]; fin = "tool_calls"
        created = int(time.time())
        if body.get("stream"):
            self.send_response(200); self.send_header("content-type", "text/event-stream"); self.end_headers()
            delta = dict(msg)
            if "tool_calls" in delta:
                delta["tool_calls"] = [dict(tc, index=i) for i, tc in enumerate(delta["tool_calls"])]
            for dl, f in ((delta, None), ({}, fin)):
                c = {"id": "mock", "object": "chat.completion.chunk", "created": created, "model": "mock",
                     "choices": [{"index": 0, "delta": dl, "finish_reason": f}]}
                self.wfile.write(("data: " + json.dumps(c) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n"); return
        out = {"id": "mock", "object": "chat.completion", "created": created, "model": "mock",
               "choices": [{"index": 0, "message": msg, "finish_reason": fin}],
               "usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}}
        b = json.dumps(out).encode()
        self.send_response(200); self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass

if __name__ == "__main__":
    ThreadingHTTPServer(("", 8000), H).serve_forever()
