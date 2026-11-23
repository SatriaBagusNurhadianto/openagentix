---
title: Timeline
sidebar_position: 3
description: Dated diary of OpenAgentix milestones, oldest first, with commit ids and lessons.
---

# Timeline

Dates are commit dates from `git log --date=short`. The whole history is 23 commits over two days. Commit ids are short hashes. Where a lesson comes from a spike note or a decision record rather than a commit message, the source is named.

## 2026-11-22

### MVP 0.1.0 (`e2e1580`, tag `v0.1.0`)

Added the first Helm chart `charts/openagentix`, `install.sh`, `uninstall.sh`, the Telegram bridge, the demo `assistant` agent, and the spike results for kagent, agentgateway, ToolHive and Google AX. Added ADR 0001 and `bridges/ax-adapter` (experimental).

Why: the goal in `WHAT.txt` was a demo of an agent on Kubernetes with tools, skills and a model, reachable from a chat app.

What was learned:
- kagent 1.0.1 and Substrate 1.0.1 run on a laptop, with gVisor sandboxes on arm64 (spikes/kagent/NOTES.md). A Gemini test key had no credits (HTTP 402), so a stub model proved the path first.
- The same agent was built on Google AX. It worked and was not slower, but needed an adapter, an agent loop and images that kagent supplies. Decision: kagent (ADR 0001).
- The first token budget of 100000 per hour was used up by the comparison runs and caused 429 answers. The default was raised to 1000000.
- Fixes before the tag: the installer runs component scripts through `bash`; the skill registry became its own container on the `kind` network because a registry sharing the node network broke after cluster recreation; the `kubectl-ate` download is checked by SHA-256.

### Knowledge base service (`6c70c6d`)

Added `services/kb`: MCP tools `kb_search`, `kb_get_document`, `kb_list_collections`, a token-protected ingest API, memory and PostgreSQL stores, optional pgvector hybrid search. Not deployed yet.

### Tool layer authentication and network policies (`0843360`)

Added NetworkPolicies for `mcp-tools` and for the kagent controller port 8083, and made the vMCP require an actor JWT (OIDC against the Substrate IdP). Added Cedar `toolAccess`.

What was learned: the vMCP could not fetch keys from the IdP until `jwksAllowPrivateIP: true` was set, because the IdP has a private ClusterIP. Substrate creates one actor per session in one atespace, so Cedar can tell sessions apart but not agents. Also tried and reverted: kagent `serviceAccount.authority: kubernetes`, which still answered requests with no token.

### Knowledge base deployment (`2c3bb5e`)

Deployed the kb with PostgreSQL and pgvector and a CPU embedding server (llama.cpp, `bge-small-en-v1.5`). Added the `embed-default` gateway model, the `MCPServerEntry`, `hack/kb-ingest.sh` and a sample corpus.

What was learned: Hugging Face text-embeddings-inference has no arm64 CPU image, so llama.cpp was used. An `MCPServerEntry` without an `MCPExternalAuthConfig` of type `unauthenticated` is silently dropped by the vMCP. A 503 came from the embedding pod before its model loaded, so readiness now waits for the model. A call to the kb could hang past its timeout because the MCP SDK does not pass the request deadline to tool handlers; each tool call now runs under its own timeout.

### Long-term memory (`e1c0e83`)

Enabled kagent memory per agent (`agents[].memory`), with a second embedding server (`bge-base-en-v1.5`, 768 dimensions).

What was learned: kagent stores `vector(768)` only, so the 384-dimension model of the kb cannot be reused. kagent allows one credential per destination origin, so the memory embedding path uses an `ExternalName` alias Service of the gateway. `ttlDays` is recorded but nothing prunes rows in kagent 1.0.1.

### Per-chat user id and Kourier spike (`540554b`)

The bridge now sends `X-User-Id: telegram:<chat id>`. Before that, every Telegram chat shared one memory space. Also added `spikes/kourier/` (Agent Kourier 0.6.0, webhook only).

What was learned: Kourier restarts mid-turn end the turn as `refused` on kagent 1.0.1 even though the task completes; a webhook has no reply route.

### Channels with Agent Kourier (`ba878d9`)

Added the `channels` block: AgentBackend, ChatConnections, Bindings, RBAC for Secrets, a controller NetworkPolicy peer, and install steps. Off by default. Verified live with a signed webhook. Slack was not run.

ADR 0002 was written the same day as a proposal. It found that kagent's own scheduler sends no caller identity, so memory-enabled agents fail, and that its runs hung on a cold agent.

## 2026-11-23

### Notify service (`60f5dae`, `1fb9b3b`, `e23a2f6`, `6091e41`, `c732e4e`, `ebf7544`)

Added `services/notify`, an MCP server with two tools. Then a hardening pass after a security review (`1fb9b3b`), the chart block with per-agent opt-in (`e23a2f6`), installer support that builds the image and creates an owner destination only on request (`6091e41`), demo wiring (`c732e4e`) and docs (`ebf7544`).

Why: agents started by a webhook or a schedule had nowhere to put their answer.

What was learned: the notify bot and the bridge can use the same bot token, because sending and long polling do not conflict (no 409 in the bridge log). Destinations must be named entries in a Secret, never ids chosen by the model.

### Schedules (`522d8cb`, `02d06fa`, `d1127eb`)

Added `tools/a2a-call` (Go, standard library only), the `schedules` and `scheduling` blocks that render CronJobs in `openagentix-schedules`, and accepted ADR 0002 (option B, CronJob).

What was learned: on kindnet the egress NetworkPolicy is enforced for cluster destinations but not for the internet. `backoffLimit` is 0 on purpose, so a failed run is not retried blindly.

### Observability (`18ce764`, `87e571a`, `26ee4c5`, `2afd943`, `069b82a`, `2442873`, `c82a4af`)

Added the `observability` block (gateway tracing, vMCP telemetry and audit, PodMonitors, an optional bundled VictoriaMetrics and Jaeger), installer wiring for kagent and Kourier telemetry, a W3C trace per Telegram message and per `a2a-call` run, `hack/trace-turn.sh`, demo values, and ADR 0003.

What was learned and what went wrong:
- The Go ADK and the vMCP write tool arguments and results into spans whatever the capture setting. The bundled Jaeger deletes those attributes before storage.
- The first roll of the kagent controller with the new telemetry values coincided with about 20 minutes of high load on the node. The API server timed out, `ate-idp-server` and the kagent controller crash-looped on lost leader election, and pods had to be deleted by hand. Cause not established (ADR 0003).
- VictoriaMetrics was killed once for memory at a 192 Mi limit; the limit is now 256 Mi.
- VictoriaMetrics counts the first value of a new counter as an increase, so the first hour after install reads high.

### In progress

The knowledge graph feature is being built (not committed). See [knowledge base](./features/knowledge-base.md).
