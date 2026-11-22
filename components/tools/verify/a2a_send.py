"""Send one A2A SendMessage. Usage: a2a_send.py AGENT TEXT   (runs in a pod; stdlib only)"""
import json, sys, urllib.request, uuid
agent, text = sys.argv[1], sys.argv[2]
url = "http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/" + agent
body = {"jsonrpc": "2.0", "id": "1", "method": "SendMessage", "params": {"message": {
    "messageId": uuid.uuid4().hex, "role": "ROLE_USER", "parts": [{"text": text}]}}}
r = urllib.request.urlopen(urllib.request.Request(url, json.dumps(body).encode(),
    {"content-type": "application/json", "A2A-Version": "1.0"}), timeout=170)
print(json.dumps(json.loads(r.read()), indent=1))
