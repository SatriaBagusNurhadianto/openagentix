"""Minimal MCP streamable-HTTP client (stdlib only). Usage: mcp_check.py URL
initialize -> tools/list -> tools/call (plain) -> tools/call execute_tool_script."""
import json, sys, urllib.request

URL = sys.argv[1]
sid = None
_id = 0

def rpc(method, params=None, notify=False):
    global sid, _id
    body = {"jsonrpc": "2.0", "method": method}
    if params is not None: body["params"] = params
    if not notify:
        _id += 1; body["id"] = _id
    h = {"content-type": "application/json", "accept": "application/json, text/event-stream"}
    if sid: h["mcp-session-id"] = sid
    r = urllib.request.urlopen(urllib.request.Request(URL, json.dumps(body).encode(), h), timeout=90)
    sid = r.headers.get("mcp-session-id") or sid
    raw = r.read().decode()
    if notify or not raw.strip(): return None
    if "data:" in raw and r.headers.get("content-type", "").startswith("text/event-stream"):
        raw = [l[5:].strip() for l in raw.splitlines() if l.startswith("data:")][-1]
    return json.loads(raw)

def show(t, o): print("== %s\n%s\n" % (t, json.dumps(o, indent=1)[:1500]))

show("initialize", rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
     "clientInfo": {"name": "mcp_check", "version": "0"}}))
rpc("notifications/initialized", notify=True)
tl = rpc("tools/list")
print("== tools/list:", [t["name"] for t in tl["result"]["tools"]], "\n")
for t in tl["result"]["tools"]:
    if t["name"] == "execute_tool_script": show("execute_tool_script schema", t)
show("tools/call osv_query_vulnerability(lodash 4.17.20)", rpc("tools/call", {"name": "osv_query_vulnerability",
     "arguments": {"package_name": "lodash", "ecosystem": "npm", "version": "4.17.20"}}))
script = sys.argv[2] if len(sys.argv) > 2 else None
if script:
    show("tools/call execute_tool_script", rpc("tools/call", {"name": "execute_tool_script", "arguments": {"script": script}}))
