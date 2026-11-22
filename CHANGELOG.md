# Changelog

All notable changes to OpenAgentix are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
