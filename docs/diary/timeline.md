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

Why: the goal in the original product brief was a demo of an agent on Kubernetes with tools, skills and a model, reachable from a chat app.

What was learned:
- kagent 1.0.1 and Substrate 1.0.1 run on a laptop, with gVisor sandboxes on arm64 (components/runtime/NOTES.md). A Gemini test key had no credits (HTTP 402), so a stub model proved the path first.
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

## 2026-11-23: knowledge graph

Added an optional knowledge graph inside the knowledge base service (`knowledge.kb.graph`, off by default). At ingest a background worker sends each document window to a model through the gateway, validates the answer and stores entities and relations as plain tables in the knowledge base PostgreSQL. Three tools read it: `knowledge_kb_graph_search`, `knowledge_kb_graph_entity`, `knowledge_kb_graph_paths`. See [knowledge graph](./features/knowledge-graph.md) and ADR 0004.

What was learned and what went wrong:
- The first run against a real model failed every job with HTTP 400. The model rejects `temperature`, and the gateway turned `json_object` into an open schema that the provider rejects. The request now carries a strict JSON schema and no sampling parameters. A fake model in tests could not have shown this.
- The second run extracted well (26 documents, 42,359 tokens) but the multi-hop question failed. The same thing was stored under several names (`Basalt` and `Team Basalt`, `Teal` and `cluster Teal`), so chains broke. Entities now merge by a canonical name, generic unnamed things are dropped, and chains that span more documents rank first.
- The third run passed: 47,073 tokens, 33 entities, 29 relations, and the path from the Vane-3 outage to the on-call engineer was found through five documents.
- The first canary question was ambiguous. The agent read "the service whose backend had the outage" as the API and refused to guess an owner. That was a fair reading. The question was reworded and the agent then answered correctly with all five citations.
- One wrong relation was stored (`Basalt owns Mira Okafor`) with a real but unrelated sentence as evidence. Literal evidence does not prove the relation is right.
- A VM restart changed the node address and broke Agent Kourier, whose network policy pins the API server address at install time. Re-running the installer fixed it.
- A test helper connected to a database it did not start. Executors may now only use databases they start themselves.

## 2026-11-23: curated graph facts and the Kourier address fix

An operator can now load a hand-written catalogue (services, teams, owners, escalation contacts) into a collection's graph as a named fact set: `hack/kb-graph-load.sh`. The same three graph tools return these facts, marked as curated, next to extracted ones. No model is involved. Re-ingesting or deleting documents never removes curated facts.

Proven live: the sample catalogue was loaded (19 entities, 12 relations) and the agent answered "which service calls Quill, which team owns it, who is the escalation manager" from two extracted facts and one curated fact. It named the fact set as the source of the curated fact and said it could not confirm it in the documents.

Agent Kourier's network policy now allows the node subnet for the API server port on kind, so a Docker restart that moves the node address no longer breaks it.

## 2026-11-23: metrics from kb and notify, and a dashboard without a new pod

Round 1 of observability gave cost per agent, one trace per turn and the tool audit, but our own two services emitted nothing and there was nothing to look at. Round 2 adds Prometheus metrics to the knowledge base and notify services and a local dashboard. The demo node is already full, so there is no Grafana and no new pod.

The services serve metrics on their own port, only when observability is on, and only the bundled VictoriaMetrics may reach it. Labels come from closed sets: no text, queries, ids, destination names or addresses. The first design question was destination names for notify. They are operator chosen and can name a person, so the label is the platform kind instead. The library is a small text-exposition package copied into both modules, because neither had a metrics client and a shared module would have complicated the builds. Tests plant canary strings and check that they never appear in the metrics or the logs.

The dashboard is one static page and a script that port-forwards VictoriaMetrics and proxies its read-only query API with python3, so the page never makes a cross-origin request. It has a check mode that runs every query of the page, so it can be pointed at the cluster later.

Built and tested offline only: Go tests, chart render tests, a fake Prometheus API and a jsdom run of the page. It has not run on the cluster. The queries have not met a real VictoriaMetrics, and the label names of the upstream gateway series are taken from round 1 notes.

## 2026-11-23: observability round 2 goes live, and the runtime speed test is repeated

The dashboard queries were checked against the live cluster: 14 of 14 valid, 12 scrape targets up.

The kagent versus AX speed test was repeated with traces and three kagent variants (as deployed, memory off, lean). AX was installed again for the test and removed afterwards.

What was learned:
- kagent is not slow. A plain question takes 2.5 to 2.9 s, against 3.4 s for the minimal AX loop and 7.6 s for AX with Claude Code. The 12 s of the first test came from an overloaded node.
- Memory recall is cheap. Memory saving costs an extra model call in the first turn.
- Some new conversations wait 5 to 14 s before the agent starts. The cause is not found yet.
- No call reported a prompt cache read.
- Four conversations at once pushed the node load to 89. Both runtimes did badly, kagent worse.
- AX sandboxes cannot authenticate to our tool layer yet: AX refuses to send a token to a plain-HTTP service.
- Decision record: ADR 0005. Recommendation: stay on kagent and tune it, keep AX as an optional second runtime.


## 2026-11-24: tuning kagent after the speed test

The owner decided to stay on kagent 1.0.1, so the three weak points of ADR 0005 were looked at one by one, with traces and the sources of kagent, Substrate and agentgateway.

- Memory saving: it is the model calling a tool, so it always costs a second model call. A prompt that saves only on request was tested: a mentioned fact takes 1 model call instead of 2 (turn 1 runtime median 3.96 s to 2.7 s). The owner kept automatic saving as the default, so this is an option, not the default. An explicit "remember" is recalled in a new conversation of the same user and not for another user.
- Prompt caching: the gateway adds Anthropic cache marks only when asked. `spec.backend.ai.promptCaching` is now in the chart policy and on by default. Cache reads of 6,635 tokens on every new conversation, 8,036 inside one, cost per call down 74%. Model time is unchanged. kagent itself still shows 0 cached tokens.
- Slow start: the 10 s wait is inside the kagent controller, before it sends to the router. Substrate, the restore, DNS and the node load are not the cause. Not solved; there is no setting for it. The demo node was also found in a restart loop (1 s probe timeouts on a full node), which explains part of the earlier concurrency result.
- The assistant key reached its 1,000,000 tokens per hour after about 125 test calls and the next call got a 429.

## 2026-11-24: public repository and documentation site

- The repository is public under Apache-2.0 at `https://github.com/openagentix/openagentix`. This diary is published at `https://openagentix.github.io/openagentix/` by the `docs` workflow on every push that touches `docs/` or `website/`.
- Layout change before publishing: the kagent and Substrate bring-up moved to `components/runtime/`, the benchmark harness to `components/bench/compare/`. Exploratory spike code and the original product brief are not part of the public repository.
- Memory saving stays automatic (owner decision). The save-on-request variant is documented in ADR 0005.
- Not verified: a fresh install from an empty cluster after the layout change. Syntax checks, chart tests (625) and a re-run of the installer on the existing cluster passed.

## 2026-11-24: the A2A gate (governance round 1)

ADR 0006 round 1 is built and run on the kind cluster. A small proxy, `a2a-gate`, now stands in front of the kagent controller's port 8083 when `governance.a2a.enabled` is true. It is off by default and the default and demo renders are byte-identical to before (20 of 20 `cmp` checks).

- What changes: the bridge and every schedule send a projected ServiceAccount token (one ServiceAccount per schedule); each Kourier binding sends its own static key. The gate checks the caller, allows only the agents and user ids in the caller's rule, answers 403 for gRPC and everything that is not A2A, sets `X-User-Id` itself and writes one audit line per request (user id hashed).
- Proven live, with two model calls: no token gives 401; a pod with the bridge's labels cannot reach port 8083 any more; a bridge token that claims `schedule:demo-brief` gets 403; a schedule Job completed through the gate; a signed webhook ended with `outcome=completed` through the gate.
- Kourier 0.6.0 sends the key from `identity.tokenSecretRef` unchanged as a bearer token, so no JWT was needed.
- One bug on the way: the gate pod had no `fsGroup`, so it could not read its mounted key hashes and every Kourier call got 401. Found from the audit reason, fixed, and the gate now warns about a key file it cannot read.
- Kourier re-checks a binding every 5 minutes, so the bindings turned Ready about 5 minutes after the fix.
- Not verified: a real Telegram turn through the gate (no message was sent), kubelet token rotation, streaming through the real controller. The `kagent` namespace can still call 8083 directly.
- The node load went above 20 for a few minutes during the run, from the node's own probes and other work on the host; the gate pod never restarted.

## 2026-11-24: review of the A2A gate, round 1 fixes

A security review of governance round 1 found a critical hole and several smaller ones. All are fixed and were run on the kind cluster.

- Critical: the kagent UI was an open relay around the gate. Nothing selected its pods, its nginx forwards `X-User-Id`, and the controller serves its whole API over gRPC-Web on `/api/`. A pod in `default` got 200 on the memory List call, with the gate on or off. The UI now has a deny-all NetworkPolicy (`networkPolicy.kagentUI.allowFrom` lists exceptions), and port 8083 admits the UI pods instead of the whole `kagent` namespace. Proven: the probe from `default`, `openagentix-gateway` and `kagent` timed out; `kubectl port-forward` to the UI still works under kindnet. The default render changed on purpose.
- The gate got memory and API limits: 16 KiB headers, a read deadline for the start of a body, a cap on requests in flight, a size limit and a payload pre-check before TokenReview, singleflight and a concurrency limit on reviews, and separate caches for good and bad tokens.
- Each caller has a method list (the chart sets what its callers really send; the default is `SendMessage` and `SendStreamingMessage`). A method outside the list, or a body whose method cannot be determined, gets 403 with `reason: method`.
- The claimed user id is audited only after authentication. The audit `context_id` was empty only because every live request was a first message: a follow-up showed its id.
- A wrong claim in the docs was corrected: a ServiceAccount per schedule does not stop someone who can create pods in the schedules namespace from acting as any schedule.
- One bug on the way: after a read timeout the server waited for the rest of the body, so the 408 never arrived. Fixed with `Connection: close`.
- Two model calls: a schedule turn through the gate and a follow-up in the same conversation. Not verified: a Kourier turn after the change.
