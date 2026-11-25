# ADR 0008: model routing, and one gateway or two

Status: Accepted as direction (2026-11-25). Nothing is built yet. Every capability below was read in the source of agentgateway v1.7.0 (the version we deploy) and ToolHive 0.67.1; none of the new ones has run on our cluster. Items marked *unverified* must be proven in the slice that uses them.

## Context

The project owner asked for model routing that is intelligent and cost-efficient, and whether one gateway could serve models and tools, since agentgateway does both. ADR 0007 makes each configurable area a resource in `openagentix.io` with the upstream tool behind an adapter. This record decides the shape of the model kinds and which gateway serves tools.

## What agentgateway 1.7.0 offers for routing

An `AgentgatewayModel` is either a concrete model (provider, base URL, credentials, health rules, request transformations) or a virtual model with exactly one of four strategies:

| Strategy | Behaviour |
|---|---|
| `failover` | targets with a priority; the next group is used only when the current one is unhealthy (we use this today) |
| `weighted` | traffic split by weight |
| `conditional` | ordered targets, each with a CEL condition on the request (headers, the parsed request body, the caller's key or token claims); first match wins, last target is the default |
| `callout` | the gateway asks an HTTP service which model to use, with a fallback model and a cache that keeps a session on one model |

Also present: retries with CEL conditions, health eviction, token and request rate limits per key (windows of seconds, minutes, hours), cost metrics with a price catalog, prompt guards (regex, webhook, provider moderation services).

Not present, or not reachable from Kubernetes resources at this version:

- No built-in learned or semantic model selection. `callout` is the extension point.
- No "remaining budget" value in conditions, and no time of day. Budget-aware routing needs something outside the gateway.
- Budgets in US dollars exist in the gateway's standalone configuration (rolling windows, audit or block), but the Kubernetes resources do not expose them. This is a candidate for an upstream request.
- No response cache or semantic cache in the gateway itself (provider prompt caching is there and is on).
- A model missing from the price catalog gets no cost. How to add prices from Kubernetes is *unverified*.

## Decision: model kinds

- **`ModelProvider`**: name, type (Anthropic, Gemini, OpenAI-compatible, a local server), endpoint, credential (write-only), health rules, optional prices. The adapter renders a concrete gateway model.
- **`ModelRoute`**: the alias that agents name (`fast`, `smart`, `auto`), with one strategy. The adapter renders a virtual gateway model.

Routing comes in three stages. Each is useful alone and each is a slice.

1. **Static.** Aliases, a fallback chain (with the 402, 429 and 5xx health rules the chart already has), weights. Per-agent token limits.
2. **Rules.** A cheap model by default, a stronger one when a rule matches. Rules use platform predicates that the adapter translates to conditions: the request has tools, the request is large, the agent's tier, an explicit hint from the caller. Budget-aware downgrade is done by the control plane, which watches spend and rewrites the route when a threshold is crossed. This lags by one scrape interval, and the documentation must say so.
3. **Router service.** A small in-cluster service called through `callout`: first a heuristic scorer (prompt length, code, tool need, conversation depth), later a small classifier that can reuse the embedding server we already run. It starts in shadow mode: it logs its decision and the gateway ignores it.

Projects considered instead of building the router:

| Project | Licence | Why not adopted now |
|---|---|---|
| vLLM Semantic Router | Apache-2.0, active | agentgateway documents an integration, but the example asks for 1 CPU and 3 GiB; too large for the 6 CPU / 8 GiB node. A candidate adapter for larger clusters |
| RouteLLM | Apache-2.0 | last change in 2024; trained on one old model pair |
| LiteLLM proxy | MIT with an enterprise directory | it is a second gateway: it would duplicate keys and budgets. Its "complexity router" idea is worth copying into our own service |
| semantic-router (Aurelio) | MIT | a library, usable inside our router service later |
| NVIDIA llm-router | Apache-2.0 | deprecated upstream, needs a GPU |
| Hosted routers | - | prompts leave the cluster |

**Proving that routing saves money without losing quality.** A routing rule is not switched on by default until it is measured:

- cost per agent and per model from the gateway metrics, cost per turn from traces;
- a task set per agent (synthetic, or recorded prompts only with explicit opt-in, since content capture is off by default), run on the best model and on the routed path, compared by task checks and a judged sample;
- shadow mode first, then a weighted split as a control group;
- watched alongside: escalation rate, fallback rate, retries, time to first token.

## Decision: tools stay on ToolHive, behind the `Tools` adapter

agentgateway can federate MCP servers and filter tools per caller with CEL. Compared with what we use:

| Feature | ToolHive | agentgateway | Consequence |
|---|---|---|---|
| Runs MCP server pods | yes (operator) | no, proxies to Services | we would run every server ourselves |
| Code mode (`execute_tool_script`) | yes | no | a real loss: it saves model round trips |
| Composite tools | yes | no | loss |
| Per-profile tool visibility | a virtual server per profile | CEL filter per caller | comparable, more expressive in CEL, no profile object |
| Authorization language | Cedar | CEL | our policies would be rewritten |
| Guardrails on MCP calls | no | yes | a gain |
| Audit log | built in | access log | our audit design is ToolHive's |
| Proven on our cluster | yes | no | |

Consolidating would save about 70 MiB of pods and give one policy surface, and it would cost code mode, composite tools and everything proven in ADR 0006 round 2. So ToolHive stays. agentgateway becomes a second `Tools` adapter later, for users who already run plain MCP servers behind it. A spike (the `knowledge` server through agentgateway with one tool filter) is the first step when that is wanted.

## Decision: the A2A gate stays

agentgateway's A2A support classifies calls and rewrites agent cards for telemetry. It has no per-caller rules for agents and user ids, no method allowlist read from the body, no memory RPC checks and no audit line of our kind. Rebuilding those as generic CEL policies would lose the tested fail-closed behaviour. `services/a2a-gate` stays. Possible later: agentgateway as transport with the gate as its external authorization service (*unverified*).

## Follow-ups

- Upstream request to agentgateway: expose key budgets (USD, rolling window) in the Kubernetes resources.
- Check on a live gateway: key metadata in `conditional` rules, per-agent limit sizes, custom prices, cache token types on the token metric.
