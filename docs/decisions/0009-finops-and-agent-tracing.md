# ADR 0009: cost, FinOps and agent tracing

Status: Accepted as direction (2026-11-25). Nothing new is built yet. This extends ADR 0003 (observability). Items marked *unverified* must be proven in the slice that uses them.

## Context

The project owner asked for observability with cost and FinOps, tracing of agents "and more". ADR 0003 gave us cost and tokens per agent from the model gateway, one trace per turn across runtime, tools and gateway, tool-call counters, an audit line, and a privacy default of no content capture. Budgets are in tokens only.

## What a FinOps view needs, and where each number comes from

| Need | Source today | Gap |
|---|---|---|
| Cost per agent | gateway cost metric, labelled with the agent's key | none |
| Cost per model | same metric, model labels | none |
| Input and output tokens | gateway token metric | none |
| Cache read and write tokens | gateway spans; on the metric *unverified* | maybe a recording rule from spans |
| Cost per user or caller | nothing: the gateway sees only the agent | the end user id (hashed) must reach the gateway as a metric attribute; needs a consent setting and a cardinality cap |
| Cost per channel or schedule | the gate's audit line has caller and trace id | a join of audit and trace, or a caller tag on the turn |
| Cost per tool | tool-call counts only | derive from traces (model spans next to tool spans) |
| Cost per conversation | one trace at a time (`hack/trace-turn.sh`) | needs traces that persist; the bundled Jaeger keeps 2000 in memory |
| Budget against spend, forecast, alerts | spend yes; limit in tokens; no alert rules | USD limits are not reachable in the gateway's Kubernetes resources (ADR 0008); alert rules are not shipped |
| Infrastructure share (sandbox CPU and memory) | not scraped | scrape cAdvisor, multiply by a configured price. OpenCost is not worth it on one kind node; a candidate adapter for real clusters |
| Showback by team | nothing | a team label on agents, carried into the gateway metric |

## Decision

Two kinds and one label.

- **`ObservabilitySettings`** (named in ADR 0007): which signals, where they go (the bundled backends or an OTLP endpoint), retention, and content capture, which stays off unless the platform policy allows it.
- **`Budget`**: scope (agent, team, platform), limit (tokens now, US dollars when it can be enforced), window, what happens when it is exceeded (alert, or block), and a notify destination for alerts. Adapter today: the gateway's token rate limit per key, plus alert rules generated from the same object. A dollar block is done by the control plane reading spend and cutting the key's limit; that lags by a scrape interval and the documentation says so. When agentgateway exposes key budgets in its Kubernetes resources, the adapter uses them.
- **A `team` label** on `AgentDefinition`, written into the agent's gateway key metadata and added to the cost metric.

Stages, with what the console shows:

1. **Costs page.** Cost, tokens and model per agent; cache read and write share; spend today against the token budget; a link from a turn to its trace. Recording rules for daily totals.
2. **Budgets.** The `Budget` kind, showback by team, a forecast for the window, alert rules, cost per model, an estimate of what prompt caching saved.
3. **Deeper.** Cost per user behind an explicit consent setting; cost per conversation (needs persistent traces); the routing savings report of ADR 0008; infrastructure share.

## Agent tracing

All three layers already emit OpenTelemetry GenAI attributes: the gateway (`gen_ai.usage.*`, cost, model), the runtime (`invoke_agent`, `generate_content`, `execute_tool`, agent name, conversation id) and the tool gateway (tool name, MCP method). The GenAI conventions moved to their own repository and their stability status is *unverified*, so attribute mapping lives in the adapter and the platform's own pages use platform names.

The console's Traces page reads through the `Observability` adapter: a list of recent turns (agent, caller, duration, cost, outcome) and one turn as a timeline of model calls, tool calls, memory and knowledge lookups.

Optional backends, each as an adapter, none bundled by default:

| Backend | Licence | Adds over Jaeger | Note |
|---|---|---|---|
| Jaeger (bundled) | Apache-2.0 | - | 27 to 34 MiB, traces only, in memory |
| SigNoz | MIT outside its enterprise directory | metrics, traces, logs, alerts and dashboards in one place, persistence | ClickHouse; too heavy for the demo node |
| Langfuse | MIT outside its enterprise directories | sessions, per-user cost, evaluations, prompt management | PostgreSQL, ClickHouse, Redis and object storage; useful mostly with content capture on, which is off by default here |
| Grafana Tempo and Loki | AGPL-3.0 | long retention, logs | licence is a concern for bundling in an Apache-2.0 project |
| Arize Phoenix | Elastic License 2.0 | traces, evaluations, datasets | not an open-source licence; not bundled |

Anything that stores prompts and answers needs the content capture switch, which only the operator's policy can open.

## Follow-ups

- Check on a live gateway: cache token types on the token metric, a custom price catalog, key metadata as a metric attribute.
- Decide how the end user id reaches the gateway without widening what agents can claim (the gate sets `X-User-Id` toward the runtime, not toward the gateway).
