# ADR 0003: Observability (cost per agent, one trace per turn, tool audit)

- **Status:** Accepted for the MVP slice described here (cost per agent, one trace of one turn, tool-call counts and audit, trace start at the entry points, a correlation script). Dashboards, alerting, persistence and per-user attribution are deferred (see "Deferred").
- **Date:** 2026-11-23
- **Evidence:** everything marked "proven" below was run on the kind cluster of this repository (one arm64 node, kagent 1.0.1, Substrate 1.0.1, agentgateway v1.7.0, ToolHive 0.67.1, Agent Kourier 0.6.0, VictoriaMetrics v1.156.0, Jaeger v2.23.0). Numbers are from that run; model-call count for the whole task: 8.

## Context

The platform has many hops for one user message: bridge (or webhook, or schedule) -> kagent controller -> Substrate router -> agent sandbox -> egress proxy -> model gateway, vMCP, kb, notify. Before this change nothing could answer "what did agent X cost today?" or "where did the 40 seconds of that turn go?". Our own Go services (bridge, kb, notify, a2a-call) had no metrics and no tracing, and the bridge logged nothing for a successful message.

The upstream pieces already emit most of what is needed. The work is wiring them to one switch, adding the missing start of the trace, and not leaking content.

## What each component emits

| Component | Metrics (Prometheus) | Traces (OTLP) | Notes |
|---|---|---|---|
| Model gateway (agentgateway) | `agentgateway_gen_ai_client_cost_usd_total`, `agentgateway_gen_ai_client_token_usage_{bucket,sum,count}` (`gen_ai_token_type`), `agentgateway_gen_ai_server_request_duration_*`, `agentgateway_requests_total`, all labelled `user_id` = the virtual-key name (the AGENT: `assistant`, `kb`, `memory`) | one server span per request (`POST /v1/chat/completions`, `POST /v1/embeddings`) with model, token counts and per-request cost (`agw.ai.usage.cost.*`) and a client span to the provider; no prompt or completion | `trace.id` is also in the access log. Tracing is `AgentgatewayPolicy.spec.frontend.tracing` in the one existing policy |
| kagent controller + agent runtime (Go ADK) | none (controller metrics bind is off) | `POST /agents/`, `lf.a2a.v1.A2AService/SendMessage`, `a2a.request`, `invoke_agent <agent>`, `generate_content <model>`, `execute_tool <tool>`, task store and memory RPCs; `gen_ai.agent.name`, `gen_ai.conversation.id`, `a2a.task.id`, `user.id`, `gen_ai.usage.*` | configured by the kagent Helm values `config.telemetry` and `config.runtimeDefaults.telemetry` |
| ToolHive vMCP | `toolhive_mcp_tool_calls_total{server,tool,status}`, `toolhive_mcp_requests_total`, `mcp_server_operation_duration_seconds_*` (port 9464) | `tools/call <tool>` plus the aggregator spans | audit log (`AUDIT` lines in the pod log); `MCPTelemetryConfig` + `VirtualMCPServer.spec.telemetryConfigRef` |
| Substrate (router, egress, API server, atelet, controller) | scrape endpoints on 15020, 9090, 8080 (`ate_*`) | none of its own; the egress proxy logs `trace.id` and `ate.actor.name=session-<contextId>` on every sandbox-originated hop | no configuration needed |
| Agent Kourier | `agentkourier_*` on 8080 | OTLP http/protobuf through `extraEnv` | its NetworkPolicy needs the scraper and the collector; the installer adds both |
| Bridge, `a2a-call` | none | none exported: they START the trace (`traceparent` header) | one structured log line per message or run, with the trace id |
| kb, notify | none in round 1; round 2 adds them (below) | none yet | round 1 left their code out of scope |

## Decision: two layers, one switch

1. **Wiring (works with any OTLP/Prometheus stack).** A top-level chart block `observability` (default `enabled: false`; the default render is byte-identical to before). With it on, the chart: adds `spec.frontend.tracing` to the single gateway policy; renders an `MCPTelemetryConfig` and a metadata-only audit block for the vMCP; renders `PodMonitor` objects for every scrape target **only when the `monitoring.coreos.com/v1` API exists**; and renders a marker ConfigMap (`openagentix-observability`) with the public settings. Upstream releases the chart does not own (kagent, Agent Kourier) are configured by `install.sh` from that marker, through the files and scripts it already uses (`spikes/kagent/kagent-helm.sh`, `components/channels/kourier.sh`), only when the release differs from the wanted state, in either direction. One setting therefore turns the whole thing on and off.
2. **Optional bundled dev profile** (`observability.bundled.enabled`, default false): namespace `openagentix-observability` with VictoriaMetrics single node (PromQL, VMUI; scrape config from a ConfigMap, Kubernetes pod discovery with a read-only ClusterRole on pods) and Jaeger v2 all-in-one with in-memory storage capped at `maxTraces`. Images pinned by tag and digest (multi-arch indexes, arm64 confirmed), non-root, read-only root filesystem, all capabilities dropped, seccomp RuntimeDefault, NetworkPolicies (OTLP in from the exporting namespaces only; the UIs are reached by `kubectl port-forward`, which a NetworkPolicy does not see). Jaeger doubles as the content filter (below), so no separate collector is run. No Grafana.

Rejected: a full OpenTelemetry Collector in the bundle (Jaeger v2 is a collector build and contains the `attributes` processor, so it filters by itself and saves a pod); Prometheus Operator CRDs as a dependency (the monitors are rendered only when the API is there); Tempo or Loki (more pods than the node can spare).

## Correlation scheme

- **Conversation key:** the A2A `contextId` = kagent session id = the Substrate actor `session-<contextId>`. It is `gen_ai.conversation.id` on the runtime's spans, `ate.actor.name` in the egress proxy's log, and the `user_id` (actor subject `actor/kagent/session-<contextId>`) in the vMCP audit record.
- **Trace key:** a W3C `traceparent` started at the entry point (bridge per message, `a2a-call` per run) or by the platform if the caller sends none. Proven to survive the whole path (below).
- **Where to read it:** `hack/trace-turn.sh <contextId|traceId>` (Jaeger when present, else the egress log joined on the actor name).
- **Cost** is per AGENT (the gateway's virtual key), not per person: no end-user id is a metric label anywhere.

## Privacy default

Content capture is OFF everywhere and one value, `observability.capture.content` (default `false`), is the only way to turn it on: kagent `captureContent`, the ToolHive audit `includeRequestData` and `includeResponseData`, and the Jaeger content filter all follow it. Baggage propagation is off (`observability.baggage`, default false): with baggage on, agent and conversation ids would travel to MCP servers and model providers. The gateway's spans carry no prompt or completion.

**Found by experiment: two components write tool arguments and results into spans regardless of any capture setting.**

| Attribute | Written by | Content |
|---|---|---|
| `gcp.vertex.agent.tool_call_args`, `gcp.vertex.agent.tool_response` | the Go ADK (upstream issue google/adk-go#1634) | the full arguments and the full tool response (here: the OSV advisory text) |
| `mcp.tool.arguments`, `gen_ai.tool.call.arguments` | the ToolHive vMCP `tools/call` span | the arguments (`ecosystem=npm, package_name=lodash, version=4.17.20`) |

They cannot be turned off at the source, so they are dropped before storage: the bundled Jaeger deletes them (and the GenAI content keys, and `user.id`/`enduser.id`, which carries `telegram:<chat id>` for Telegram users) with an `attributes` processor, unless `capture.content` is true. Verified: with the processor removed the stored trace contained the keys above; with it, none of them (0 matching keys among 55 spans). **For your own endpoint the chart cannot filter**, so it exports only the gateway's spans (no content) and leaves kagent runtime and vMCP tracing OFF until you set `observability.otlp.contentFiltered: true`, meaning your collector deletes those attributes. Metrics and the audit log carry ids and outcomes only.

## What was proven (with evidence)

**A. Cost per agent: pass.** VictoriaMetrics scraped the gateway, the Substrate pods and the vMCP (9 targets up; Kourier after its NetworkPolicy got the scraper). After one A2A call, `sum by (user_id)(increase(agentgateway_gen_ai_client_cost_usd_total[1h]))` returned `assistant` > 0 through `/api/v1/query`, and `sum by (user_id, gen_ai_token_type)(increase(agentgateway_gen_ai_client_token_usage_sum[1h]))` returned tokens by `user_id` (assistant, kb, memory) and type. Cross-check with the access log over ten calls: 153 802 input and 2 757 output tokens, cost 0.0167587 USD; the log's per-request `agw.ai.usage.cost.total` values equal exactly 0.10 USD per million input and 0.50 USD per million output tokens (e.g. 6 831 in + 185 out = 0.0007756; 23 915 in + 523 out = 0.0026530), the catalog price of `claude-haiku-5-5`. The token series matched the log exactly. The **cost** `increase()` read 0.0785685, which is the log's 0.0167587 plus 0.0618098: VictoriaMetrics counts the value a counter had at its first scrape as an increase when the series is new inside the window. After the series is older than the window the figure is exact; the raw samples (0.0618098 then 0.0652384 after the first call: +0.0034286 = 30 746 x 1e-7 + 708 x 5e-7) agree.

**B. One trace of one turn: pass.** One A2A request sent with `traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-...` produced one trace with 59 spans from four services: `kagent-controller` (`POST /agents/`, `SendMessage`, task store, memory), `assistant` (the sandbox: `a2a.request` with `gen_ai.agent.name=assistant` and `gen_ai.conversation.id`, `invoke_agent assistant`, two `generate_content claude-haiku` with `gen_ai.usage.input_tokens=6831`/`23915`, `execute_tool osv_query_vulnerability`), `tools` (the vMCP: `tools/call osv_query_vulnerability`) and `model-gateway` (`POST /v1/chat/completions` with `gen_ai.request.model=claude-haiku-5-5`, tokens, cost; `POST /v1/embeddings` for memory recall). Answers to the open questions:
- *Does the sandbox reach the collector through `atenet-egress`?* Yes, over **http/protobuf** (the kagent file endpoint carries the `/v1/traces` path). No egress policy had to be written by hand. gRPC was not tried.
- *Does `traceparent` survive router -> sandbox -> egress -> gateway?* Yes. The egress proxy's log shows `trace.id=<the id we sent> ate.actor.name=session-<contextId>` on the sandbox's calls, and the gateway's spans are in the same trace.
- *What is in the spans by default?* Model, token counts, agent, conversation and task ids, tool names, cost; no prompt or completion (kagent `captureContent: false`). The tool-argument and tool-result leak above is real in both the runtime and the vMCP.
- *`a2a.request` is not always exported:* it was in the trace of step B and absent from the trace of step D (`invoke_agent` carries the same ids, and the script falls back to it).

**C. Tool hop and audit: pass.** `toolhive_mcp_tool_calls_total{server="tools",status="success",tool="osv_query_vulnerability"}` counted the calls. The audit record (pod log of the vMCP): `type=mcp_tool_call outcome=success target.name=osv_query_vulnerability subjects.user_id=actor/kagent/session-<contextId> duration_ms=2229`, no arguments and no response (`includeRequestData`/`includeResponseData` false). **The vMCP continues the incoming trace**: its `tools/call` span has the same trace id and is a descendant of the runtime's `execute_tool` span. The metric names differ from the research notes only in that both `toolhive_mcp_*` and `mcp_server_operation_duration_seconds_*` exist.

**D. Entry points: pass (unit tests plus a Job).** `tools/a2a-call` and the bridge send a generated W3C `traceparent` (standard library only), the bridge logs one `turn` line per message (trace id, `chat_hash` = 8 hex digits of SHA-256 over a per-process random salt, context id, outcome, durations, reply length; never text, never the raw chat id), `a2a-call` prints `traceId` and `contextId` in its summary. Go tests were written first for each behaviour and run under `-race`. A throwaway Job with the schedule pod spec (a prompt that does not deliver) printed `"traceId":"d642753c49539ef59a22b07d8b010f66"`, and that trace id was found in Jaeger with 55 spans from four services. The bridge's live Telegram path was not exercised (no Telegram message was sent): its behaviour is covered by the unit tests only.

**E. Correlation view: pass.** `hack/trace-turn.sh` (read-only, shellcheck-clean) for the trace of step D printed: `model: 2 calls 3.2s tokens in=30782 out=263 cost=$0.00321`, `memory: 2 embedding calls 2.9s`, `tools: osv_query_vulnerability 2.9s (vMCP: 1.3s)`, `notify: not called`, and the span count per service. Without Jaeger it falls back to the egress log (`model: 2 calls 4.6s`, `memory: 2 calls 5.3s`, `tools`, `kagent`).

**Footprint.** Node memory requests 4002 Mi -> 4146 Mi (+144 Mi: VictoriaMetrics 96 Mi, Jaeger 48 Mi; the first run, 64 + 64, gave 4130 Mi). Measured working sets (crictl): VictoriaMetrics 153 to 156 MB, Jaeger 27 to 34 MB, after about an hour and a dozen requests. VictoriaMetrics was OOM-killed once (exit 137) with a 192 Mi limit during the incident below, so the limit is now 256 Mi and its cache is capped at 40 percent.

## What does not work, or is not proven

- **Unexplained cluster stall.** The first roll of the kagent controller (new telemetry values) coincided with about 20 minutes of load average above 100 on the node: the API server and kubelet timed out, probes failed, `ate-idp-server` and the kagent controller crash-looped ("leader election lost", migrations "context canceled") until their pods were deleted by hand. Everything recovered without data loss and the demo worked afterwards. The cause is not established; the README already records a start-up race of the controller, which this reproduces. Rolling the controller on a node this small is risky.
- OTLP over **gRPC** from the sandbox, **baggage on**, and **Kourier traces** (the env and NetworkPolicy are applied; no webhook was sent, so no `agent-kourier` span was seen) were not exercised.
- The **PodMonitors** were rendered and unit-tested only: the cluster has no `monitoring.coreos.com` CRDs. They use `portNumber`, which needs a recent prometheus-operator.
- The bridge's Telegram path was not run live (rule: no Telegram messages).
- `user.id` is dropped from stored spans, so per-user attribution of traces is not possible with the bundled profile; for an own backend it is the operator's choice.
- Jaeger keeps traces in memory only (2 000 by default, lost on restart); VictoriaMetrics keeps 7 days on a 2 Gi volume. No authentication on either UI beyond the port-forward.
- kindnet does not enforce internet egress, so the NetworkPolicies of the bundle are only proven for in-cluster ingress.

## Deferred

Grafana and alert rules (round 2 adds a local page, not Grafana); per-user and per-team cost views; USD budget caps (the gateway has token budgets only); traces for kb and notify; a persistent trace store; sampling beyond a head rate; exemplars linking metrics to traces; log shipping; tail sampling in a real collector; the Kourier and Telegram live traces; an upstream fix for the tool-argument attributes.

## Round 2 (2026-11-23): kb and notify metrics, local dashboard

**Status: built and tested offline; not yet run on the cluster.** What was added, and why in this shape:

- **Metrics from our own services**, off unless `observability.enabled`: `KB_METRICS_ADDR` and `NOTIFY_METRICS_ADDR` start a second listener (port 9090) that serves only `GET /metrics`, so the API's authentication and NetworkPolicy are not widened and `/metrics` is not on the API port (tested both ways). No new dependency: a small text-exposition package, copied byte for byte into both modules and guarded by a test.
- **Scraping** reuses the round 1 mechanism: two entries in `observability.metrics.scrape.targets` gated by `when: knowledge` and `when: notify`, feeding the PodMonitors and the bundled VictoriaMetrics config from one list. Their namespace and port follow the service's own values. A NetworkPolicy per service admits only the bundled VictoriaMetrics pods on the metrics port (as for the vMCP); an external Prometheus behind a PodMonitor needs a policy of the operator's own. With observability off, the default render and every demo values file render byte-identically to the commit before (`cmp`).
- **Cardinality and privacy.** Every label value is validated against a closed set, and anything else is folded to a fixed value (`other`, `unknown`, `error`). HTTP routes are the mux pattern, never the path. kb graph gauges have one `collection="all"` series; collection names are opt-in (`knowledge.kb.metrics.collectionLabels`) and capped at 50 because operators choose them. notify uses the platform kind, not the destination name, because the name is operator-chosen, can identify a person, and is already in the service log; per-destination counts remain a log query. Each metric also stops at 500 series and counts what it dropped. Canary strings in documents, queries, messages, addresses and tokens never reach `/metrics` or the logs (tests).
- **Dashboard without a new pod.** The demo node is full, so there is no Grafana. A static page plus a small `python3` proxy that the script runs locally. Same-origin through the proxy removes any dependence on VictoriaMetrics CORS behaviour (not checked). `--check` runs the page's queries against any Prometheus-compatible API so the page can be validated on the cluster later.
- **Deviations from the brief.** kb search modes are `lexical` and `hybrid`: the code always runs the lexical search and fuses the vector result when the query was embedded, so there is no vector-only path. notify has two more outcomes than the brief listed, `busy` and `uncertain` (idempotency in doubt), and a platform timeout counts as `upstream_error`.
- **Not done or not proven:** a run on the cluster; the real VictoriaMetrics accepting every query of the page; label names of upstream series assumed from round 1 notes; alert rules; traces for kb and notify.

## Open core vs enterprise (input, not a decision)

Everything above ran on open-source releases without a licence. As input to the packaging discussion, these are the places where a team usually wants more than the bundle and where a commercial layer could sit; none is claimed to exist in any vendor's product, none was compared:

- durable, replicated metric and trace storage with retention policy and multi-tenancy;
- cost reporting by team or customer with budgets, alerts and chargeback (OSS here: per agent, token budgets, no USD cap);
- access control and audit over the observability data itself (who may see traces, redaction by role), and tamper-evident export of the tool audit log (here: pod stdout);
- a managed content filter with a maintained list of attributes, instead of the static list in the chart;
- SLO dashboards and alert packs for agents (latency, error rate, cost anomaly);
- per-user attribution with consent and retention rules.

## Consequences

- One value turns observability on; the default render and the default install are unchanged.
- Switching it on rolls the kagent controller and recompiles agent revisions (the controller restarts, see the stall above), the vMCP pod and the Kourier pod.
- The chart test suite covers: default off and byte-identical, expected kinds and fields, monitors only with the API, capture values false unless the single switch is set, no Secret values.
