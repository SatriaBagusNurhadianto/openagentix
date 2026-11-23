# Changelog

All notable changes to OpenAgentix are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Notifications: the notify service (`services/notify`) deployed by the chart (block `notify`, **off by default**: the default render is byte-identical to before). Namespace `openagentix-notify`: one replica (Recreate), Service, destinations file mounted from an existing Secret (`notify.destinations.existingSecret`), token env vars from existing Secrets (`notify.tokens`, values-driven list), NetworkPolicies (port 8080 only from the vMCP pods; egress DNS and public 443), pod stamped with a hash of `services/notify`. Attached to the vMCP as an `MCPServerEntry` named `delivery` plus an `unauthenticated` `MCPExternalAuthConfig`; agent-visible tools `delivery_notify_send` and `delivery_notify_list_destinations`. Agents opt in with `agents[].notify: {enabled: true}` (tools plus a short prompt addition; others render unchanged). The chart renders no chat id, channel id, URL or token. `notify.requireReady=false` makes readiness `/healthz` so a pod with no destination does not block `helm --wait`.
- `install.sh`: builds and loads `openagentix/notify:dev` (`SKIP_NOTIFY_IMAGE=1`), creates the destinations and credentials Secrets only when absent from `NOTIFY_TELEGRAM_CHAT_ID` (or `NOTIFY_OWNER_TELEGRAM=1`, which takes the first `TELEGRAM_ALLOWED_CHAT_IDS` entry; never implicit) with one destination `owner-telegram`, copies the bridge bot token without printing it, and sets `notify.requireReady=false` while no destination exists. `uninstall.sh` with `PURGE_SECRETS=1` removes both Secrets.
- Demo wiring: `components/notify/demo-values.yaml` (notify on, `assistant` opted in); `demo-brief` (still suspended) ends by delivering to `owner-telegram`; a second demo webhook binding `demo-assistant-notify` (event `report.requested`, field `deliver_to` names the destination).
- README "Notifications"; chart tests for the notify block (default off and byte-identical, enabled kinds, no Secret values, opt-in, policies, validation).
- Schedules (chart blocks `schedules` and `scheduling`, default empty: the default render is byte-identical to before; the CronJob option of ADR 0002): each entry renders a prompt ConfigMap and a CronJob in the namespace `openagentix-schedules` that calls the agent over A2A with `X-User-Id: schedule:<name>` (one memory scope per schedule, memory-enabled agents work, unlike kagent's own scheduler). Render-time checks: unknown agent, name shape and uniqueness, 5-field cron with ranges and names, timeout, concurrencyPolicy, time zone shape. `backoffLimit: 0`, `activeDeadlineSeconds` = timeout + 60, `startingDeadlineSeconds: 300`, finished Jobs removed after a day, hardened pod (non-root, read-only root filesystem, no capabilities, seccomp, no service-account token). NetworkPolicies: the controller's A2A port admits those pods (namespace and pod label), an egress policy lets them reach only CoreDNS and the controller. 91 new chart assertions (302 to 393), written first.
- `tools/a2a-call`: Go (stdlib only) A2A 1.0 caller used by the schedules: unique message id, caller identity, timeout, size limit, no redirects, one-line JSON summary without the answer text (unless `PRINT_ANSWER=true`), exit 0 only for `TASK_STATE_COMPLETED`. Distroless non-root image.
- `install.sh` builds and loads `openagentix/a2a-call:dev` only when schedules exist (`SKIP_A2A_CALL_IMAGE=1` to skip) and stamps a source hash on the pod template; `VALUES_FILE` documented as several space-separated files. `components/schedules/demo-values.yaml`: one suspended demo schedule.
- README "Schedules".

### Changed
- ADR 0002 status: Accepted (schedules: option A implemented; result delivery: notify service implemented).

- Chart `networkPolicy` (default on): ingress NetworkPolicies for `mcp-tools` (backends from their own namespace and the ToolHive operator; the vMCP also from `atenet-egress` and the kagent controller) and for the kagent controller port 8083 (the bridge pod by label, the `kagent` namespace, `atenet-egress`; other ports untouched).
- Tool-layer identity: `mcp.virtual.incomingAuth.type: oidc` is now the default. The chart renders an `MCPOIDCConfig` for the Substrate IdP (explicit JWKS URL, CA from a ConfigMap) and the RemoteMCPServer requests an actor JWT (`actorJwt`, audience `openagentix-vmcp`). `install.sh` copies the Substrate service CA from its ClusterTrustBundle into that ConfigMap.
- `mcp.virtual.incomingAuth.toolAccess`: per-atespace tool allowlist rendered as Cedar rules (replaces the permit-all policy when set).
- README "Security" section stating what is and is not protected.
- `services/kb`: knowledge base service (MCP tools `kb_search`, `kb_get_document`, `kb_list_collections`; token-protected ingest API; in-memory and PostgreSQL stores with optional pgvector hybrid search; optional OpenAI-compatible embedder). Not deployed by `install.sh` yet.

- Knowledge base deployment (chart block `knowledge`, default on; `knowledge.enabled=false` renders byte-identical output to the chart before this change):
  - `openagentix-knowledge` namespace with the kb (`KB_STORE=postgres`, hybrid search on), PostgreSQL with pgvector (`pgvector/pgvector:pg17` pinned by tag and digest, StatefulSet and PVC) and a CPU embedding server (llama.cpp `server-b10088` pinned by digest, `bge-small-en-v1.5` GGUF Q8_0, 384 dimensions, model cache on a PVC, readiness on `/health`). Hardened: non-root, read-only root filesystem, all capabilities dropped. Hugging Face text-embeddings-inference was not used: its CPU images (`cpu-1.8`, `cpu-1.9`, `cpu-latest`) have no arm64 manifest.
  - The model gateway routes `/v1/embeddings` to that server as the model `embed-default` (an `AgentgatewayModel`, provider `OpenAI` with `baseURL`), behind the same virtual-key auth; the kb has its own key (`user_id="kb"`). `viaGateway=false` calls the Service directly.
  - The kb is one more backend of the vMCP group through an `MCPServerEntry` (no extra pods) plus an `unauthenticated` `MCPExternalAuthConfig` (without it the vMCP drops the entry). Agent-visible tool names: `knowledge_kb_search`, `knowledge_kb_get_document`, `knowledge_kb_list_collections`.
  - NetworkPolicies: the kb takes 8080 only from the vMCP pod, PostgreSQL only from the kb, the embedding server only from the gateway proxy.
  - Agents opt in with `knowledge: true`; the demo `assistant` searches the knowledge base for questions about OpenAgentix and cites document titles.
  - `hack/kb-ingest.sh` (idempotent markdown ingest through `kubectl port-forward`), sample corpus `components/knowledge/sample/` (includes the canary fact), ingested by `install.sh`.
  - `install.sh`: builds and loads `openagentix/kb:dev`, creates `kb-admin`, `kb-postgres`, `kb-database`, `kb-virtual-key` only when absent, rolls the kb pod when `services/kb` changes (pod annotation `openagentix.io/source-hash`), switches `SKIP_KB_IMAGE` and `SKIP_KB_INGEST`. `uninstall.sh`: `PURGE_DATA=1` for the knowledge volumes.
  - Verified on kind: semantic hits for queries that share no word with the chunk (PostgreSQL full-text match count 0): "Could a hijacked worker exfiltrate our language engine supplier passphrase" returned the "Security model" credentials chunk first, "Nobody outside an approved roster can talk to the messenger relay" returned the Telegram allowlist chunk first; a third paraphrase ranked a neighbouring chunk first. Score margins with bge-small are small (0.613 vs 0.612, 0.605 vs 0.592).

- Long-term memory for kagent agents (per-agent opt-in `agents[].memory: {enabled, ttlDays}`, default on for the demo `assistant`; agents without the block render byte-identical output to before):
  - Chart: the Harness gets `spec.kagent.memory.modelConfigRef`; one shared embedding ModelConfig `gateway-embed-memory` (own virtual key `memory-virtual-key`, user `memory`), gateway model `embed-memory`, an `ExternalName` alias Service `model-gateway-memory` (kagent allows one credential per origin, so the embedding ModelConfig needs its own origin), and an embedding server in `openagentix-memory` (llama.cpp `server-b10088` by digest, `bge-base-en-v1.5` GGUF Q8_0, 768 dimensions, NetworkPolicy: only the gateway proxy). The render fails when `memory.embeddings.model.dim` is not 768 or the gateway is disabled. Memory-enabled agents get a short system prompt addition about `save_memory`.
  - kagent: `config.database.vectorEnabled=true` in `spikes/kagent/values/kagent.yaml`; new `spikes/kagent/pgvector.sh` (creates the `extensions` schema and the `vector` extension, grants both kagent roles usage) and `spikes/kagent/kagent-helm.sh` (the single `helm upgrade --install kagent ... -f values/kagent.yaml`); `up.sh` uses both. `install.sh` step 3b runs them on an existing cluster when an agent opts in; the migration then creates `kagent.memory` (`extensions.vector(768)`, HNSW cosine index). Existing kagent data is kept (row counts of agents, sessions, tasks, tools unchanged).
  - Tests: 30 new assertions in `charts/openagentix/tests/run.sh` (written first, failing, then implemented).
  - Verified on kind: a fact told to the agent in one conversation is answered correctly in a new one; a second user id and the default user do not get it. See README "Memory" for how it works, its scope (per agent and per user), and a privacy note: the Telegram bridge sends no user id, so all chats share one memory space, and `ttlDays` is recorded but not enforced by kagent 1.0.1 (nothing prunes).

- Chart value `networkPolicy.extraA2ACallers` (default `[]`, default render unchanged): additional `{namespace, podLabels}` sources admitted to the kagent controller's A2A port. `install.sh` documents `VALUES_FILE` as the way to pass it.

- Channels and triggers with Agent Kourier 0.6.0 (opt-in, default off; with `channels.kourier.enabled=false` the render is byte-identical to before):
  - Chart: new top-level `channels` block. `channels.kourier` (namespace, broker namespace/ServiceAccount/pod labels, backend name and URL derived from the kagent values), `channels.connections[]` (name, platform `slack|webhook`, `existingSecret`, the platform block copied unchanged into the ChatConnection) and `channels.bindings[]` (name, connection, channel, `agent` which must be an `agents[].name` or the render fails, `userId` default `kourier:<binding name>`, `output`, `triggers[]` copied unchanged, optional `chat`/`interactions`/`feedback`/`session`). Renders an AgentBackend (dialect `kagent-v1`), ChatConnections, Bindings, a Role and RoleBinding that let Kourier's ServiceAccount `list`/`watch` exactly the connections' Secrets, a marker ConfigMap for the installer, and a controller NetworkPolicy peer for Kourier's pod (so `networkPolicy.extraA2ACallers` is no longer needed for it; it keeps working). Kourier custom resources are skipped when their CRDs are absent. No Secret values are ever rendered. 44 new assertions in `charts/openagentix/tests/run.sh` (written first, failing).
  - `install.sh`: when the chart renders the block it generates each webhook connection's Secret when absent (random, never printed, kept on re-runs), warns about missing Slack Secrets, installs the pinned `agent-kourier` release before the chart (`components/channels/kourier.sh`: cosign verification of chart and image digest when cosign is installed, a printed warning when not; idempotent: same chart version and values changes nothing), waits for the Bindings. `uninstall.sh` removes the Kourier release; `PURGE_SECRETS=1` also the generated webhook Secrets, `PURGE_DATA=1` also its PVC and CRDs.
  - Kourier's own NetworkPolicy is enabled (egress: DNS, API server, the kagent controller, Slack 443 outside private ranges; ingress: the chat port from its own namespace only); the chat listener stays a ClusterIP Service, no Ingress.
  - `components/channels/demo-values.yaml`: one demo webhook connection and binding to `assistant`. The spike's hand-applied `kourier-spike` namespace was replaced by it; `spikes/kourier/` stays as evidence.
  - README "Channels and triggers" (mapping patterns, local test, limits, Slack steps marked UNTESTED).
  - Verified on kind: a locally signed webhook through the chart-managed connection ran a turn (`outcome=completed`), called `osv_query_vulnerability` and answered 4; a bad signature got 401; a second `install.sh` run changed nothing (same pods, no new Kourier revision). Slack is untested.
- `docs/decisions/0002-schedules-and-trigger-delivery.md` (Proposed): how kagent 1.0.1 scheduled runs are reached (gRPC and gRPC-Web on the controller port), what was observed, and options for declarative schedules and result delivery. Nothing implemented.
- `spikes/kourier/`: Agent Kourier 0.6.0 spike, part A (webhook only): runs against kagent 1.0.1 (signed webhook to agent run with tool use, dedupe, threading, rate limits, two connections side by side); caveats in `spikes/kourier/NOTES.md` (recovered turns end `refused`, one memory scope per Binding, no reply path for webhooks). Left installed.

### Fixed
- Telegram bridge: privacy fix for long-term memory. Every A2A request now carries `X-User-Id: telegram:<chat id>` (env `A2A_USER_ID_PREFIX`, chart `bridge.userIdPrefix`), so kagent no longer puts all Telegram chats into one shared memory space; a group chat is one identity. Tests: `TestSend_UserIDHeaderFromContext`, `TestBridge_UserIDIsPerChat`, `TestBridge_UserIDPrefixIsConfigurable`, `TestBridge_UserIDNeverDerivedFromMessageText`, `TestLoad_UserIDPrefix*`. Verified live: a fact saved under one id was recalled for it in a new conversation and not for another id.
- `install.sh` now rolls the bridge pod when `bridges/telegram` changes (pod annotation `openagentix.io/source-hash`, chart value `bridge.sourceHash`), as it already did for the kb.
- `services/kb`: an MCP tool call no longer outlives `KB_REQUEST_TIMEOUT`. The MCP SDK does not pass the HTTP request deadline to tool handlers, so with a stalled PostgreSQL a call hung until the client gave up (observed: 100 s client timeout hit); each tool call now runs under the request timeout and a timeout is reported as a tool error. Test: `TestMCPToolCallHonoursRequestDeadline`.

### Changed
- README Memory section: the "all Telegram chats share one memory space" caveat is gone (fixed, see above).
- The vMCP no longer accepts anonymous callers by default (HTTP 401 without a valid actor JWT). Set `mcp.virtual.incomingAuth.type=anonymous` for the old behaviour. The RemoteMCPServer reports `DiscoveryDisabled` because tools are listed at run time inside the agent.
- `components/tools/NOTES.md`: corrected the claim that RemoteMCPServer has no `headersFrom` or `tokenPassthrough`.

### Tried, not adopted
- kagent `config.server.auth.serviceAccount.authority: kubernetes` for the A2A endpoint: with `mode: insecure` the controller still answered requests with no token or a garbage token (200), so it adds no protection. Reverted; the bridge keeps the NetworkPolicy as its only control (`A2A_TOKEN_FILE` was not added).

## [0.1.0] - 2026-11-22

### Added
- `charts/openagentix` (chart 0.1.0, appVersion 0.1.0): first-party Helm chart, values-driven, one `enabled` flag per block.
  - Telegram bridge Deployment with `existingSecret` references and an optional chat-id allowlist; the chart renders no Secret.
  - Model gateway: Gateway, AgentgatewayModels from `gateway.models` (provider, upstream model rewrite, health, failover), and a single AgentgatewayPolicy for virtual-key auth, per-key token budget, retry and per-key log/metric attribution.
  - MCP tool layer: MCPGroup, MCPServers from `mcp.servers`, VirtualMCPServer with a `codeMode` toggle.
  - kagent wiring: RemoteMCPServer for the vMCP, and for each entry in `agents` a Harness, AgentTemplate, Agent and a ModelConfig bound to that agent's own virtual key. Skills must be digest-pinned OCI references.
- `charts/openagentix/tests/run.sh`: 84 shell assertions over `helm template` output.
- `install.sh`: one-command idempotent install (kind cluster, pinned upstream prerequisites, Secrets from environment, per-agent gateway virtual keys, bridge image and example skill build, adoption of hand-applied objects, `helm upgrade --install`).
- `uninstall.sh`: removes only the `openagentix` release; `PURGE_SECRETS=1` also removes the Secrets it created.
- Component scripts gained opt-in switches used by the installer: `PREREQS_ONLY=1` (`components/model-gateway/install.sh`, `components/tools/install.sh`) and `SKIP_FIRST_AGENT=1` with an optional `GOOGLE_API_KEY` and OS/arch detection for `kubectl-ate` (`spikes/kagent/up.sh`).
- Spike results kept as reference: kagent + Agent Substrate on kind (`spikes/kagent`), agentgateway model gateway (`components/model-gateway`), ToolHive MCP layer with code mode and OCI skills (`components/tools`), Google AX evaluation (`spikes/ax`).
- Telegram to A2A bridge in Go (`bridges/telegram`).
- Demo `assistant` agent: Claude Haiku through the gateway, ToolHive tools (fetch, OSV) and the `openagentix-canary` skill.
- `README.md` with architecture, quickstart, how to add an agent, and known limitations.
- `docs/decisions/0001-agent-runtime-kagent-vs-ax.md`: decision record for kagent as the agent runtime, with the same agent built and measured on Google AX (`spikes/ax/compare/RESULTS.md`).
- `bridges/ax-adapter` (experimental, not installed): A2A front for Google AX that runs one AX Task per turn, used for the runtime comparison.

### Changed
- Default gateway token budget raised from 100000 to 1000000 tokens per hour per virtual key (one tool question costs about 27k tokens). The chart now renders the number as an integer (`| int64`) so large values do not turn into floats.
- The chart's default skill reference is an unpullable placeholder (`registry.invalid`, zero digest) instead of a hard-coded node IP; `install.sh` sets the real reference.
- `hack/kind-config.yaml` replaces `spikes/kagent/kind-config.yaml`.

### Fixed
- `install.sh` now runs the component scripts through `bash`, so a missing execute bit no longer aborts the first install ("Permission denied").
- The skill registry is a dedicated `openagentix-registry` container on the `kind` docker network. It no longer shares the node's network namespace (which broke after cluster recreation and left a stale container named `ax-registry` looking healthy), and its address is looked up on every run.
- `spikes/kagent/up.sh` downloads `kubectl-ate` with curl and verifies its SHA-256; no `gh` login is required.
- The missing-allowlist warning in `install.sh` and the README now match the bridge, which refuses to start without `TELEGRAM_ALLOWED_CHAT_IDS`.

### Known issues
- Cold install verified twice from a deleted cluster on one machine with a warm Docker image cache. See "Known limitations" in `README.md`.
