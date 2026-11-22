# OpenAgentix

OpenAgentix is a Kubernetes-native platform for running AI agents. It puts existing open-source pieces behind one install: an agent runtime (kagent on Agent Substrate, with gVisor sandboxes), a model gateway (agentgateway), an MCP tool layer (ToolHive with code mode), OCI-packaged skills, and a Telegram bridge that lets people talk to an agent from a phone.

This repository is an MVP. It runs on a single-node kind cluster. Read "Known limitations" before you point anything real at it.

## Architecture

```
Telegram user
     |
     v
telegram-bridge (Go, long-polls Telegram, speaks A2A)        ns openagentix-gateway
     |  A2A SendMessage
     v
kagent controller --> Agent "assistant" in a gVisor sandbox  ns kagent
                          |                       |
                          | OpenAI API            | MCP (streamable HTTP)
                          | + per-agent           v
                          |   virtual key     ToolHive vMCP (code mode)   ns mcp-tools
                          v                       |-- fetch MCP server
                     agentgateway                 `-- OSV MCP server
                     (model-gateway)              ns agentgateway-system
                          |
                          v
                     Anthropic (claude-haiku)
```

Agents never hold a provider key. Each agent holds one gateway virtual key, so the gateway can budget and log usage per agent. Skills (Agent Skills format) are mounted into the sandbox from digest-pinned OCI images.

## How it is packaged

Upstream constraints make a single Helm chart impossible today: kagent and Substrate need fixed release names, a strict install order, and key material minted by `kubectl ate` before the charts start. The Gateway API CRDs ship in neither agentgateway chart. So there are two layers:

| Layer | What | Where |
|---|---|---|
| Installer | Creates the kind cluster (`hack/kind-config.yaml`), installs the upstream releases at pinned versions, creates Secrets from your environment, builds images, runs Helm | `install.sh`, `uninstall.sh` |
| Chart | Our own resources only, all driven by values | `charts/openagentix/` |

The chart owns: the Telegram bridge Deployment, the model gateway (Gateway, one AgentgatewayModel per entry in `gateway.models`, one AgentgatewayPolicy), the MCP layer (MCPGroup, MCPServers, VirtualMCPServer), and the kagent wiring (RemoteMCPServer, plus a ModelConfig, Harness, AgentTemplate and Agent for every entry in `agents`). Each block has an `enabled` flag. The chart never contains a secret value. It only references Secrets by name.

Pinned upstream versions: kagent and Agent Substrate 1.0.1, Gateway API CRDs 1.6.3, agentgateway v1.7.0, ToolHive 0.67.1.

## Prerequisites

- Kubernetes 1.37 or newer. The installer creates a kind cluster with `kindest/node:v1.37.0`. The pod-certificate APIs that Substrate uses are only GA from 1.37.
- On your machine: `docker`, `kind` 0.33+, `kubectl` 1.37+, `helm` 3, `jq`, `yq` (the Python jq wrapper, not mikefarah's), `openssl`, `shasum`, and `curl` (the first install downloads the `kubectl-ate` plugin from the public Agent Substrate release and verifies its SHA-256; no GitHub login is needed).
- Docker needs about 7.5 GB of memory or more (tested with a 7.7 GiB VM). The full stack idles at roughly 2.3 GiB in the node; each agent sandbox adds more.
- An Anthropic API key, and a Telegram bot token from @BotFather.

## Quickstart

```sh
export ANTHROPIC_API_KEY=sk-ant-...
export TELEGRAM_BOT_TOKEN=123456:ABC...
export TELEGRAM_ALLOWED_CHAT_IDS=111111111       # required: the bridge refuses to start without it
./install.sh
```

A cold install from an empty cluster (images already in the local Docker cache) took about 10 minutes (604 s) on a 5 CPU / 7.7 GiB VM; allow longer when every image has to be pulled. If a required variable is missing the script says which one and changes nothing. Re-running is safe.

Useful switches: `CREATE_CLUSTER=0`, `SKIP_PREREQS=1`, `FORCE_PREREQS=1`, `SKIP_SKILL=1`, `SKIP_BRIDGE_IMAGE=1`, `VALUES_FILE="my-values.yaml"`. They are documented at the top of `install.sh`.

Check it works from inside the cluster:

```sh
kubectl run a2a-test --rm -i --restart=Never -n openagentix-gateway --image=curlimages/curl:8.10.1 \
  --pod-running-timeout=90s --command -- curl -s -m 170 \
  -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"Does lodash 4.17.20 on npm have known vulnerabilities? Give the count."}]}}}' \
  http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/assistant
```

Expect `TASK_STATE_COMPLETED` and an answer that says 4. Then message your bot on Telegram.

To remove OpenAgentix: `./uninstall.sh`. It removes only the `openagentix` Helm release. kind, kagent, Substrate, agentgateway, ToolHive, namespaces and all Secrets stay. `PURGE_SECRETS=1` also deletes the Secrets and the key ConfigMap that `install.sh` created.

## Add an agent

Create a values file and pass it to the installer:

```yaml
# my-values.yaml
agents:
  - name: assistant            # keep the existing one if you still want it
    # ...same fields as in charts/openagentix/values.yaml
  - name: researcher
    description: Looks things up
    model: claude-haiku        # a name from gateway.models
    systemPrompt: |
      You are a careful research assistant. Cite what you fetched.
    tools:
      - tools: [fetch_fetch, osv_query_vulnerability]   # server defaults to the vMCP RemoteMCPServer
    skills: [openagentix-canary]                         # names from the top-level `skills` map
```

```sh
VALUES_FILE=my-values.yaml ./install.sh
```

Helm replaces lists, so repeat any agent you want to keep. For each agent the installer creates the Secret `<name>-virtual-key` in `kagent` and adds its SHA-256 hash to the gateway's `llm-api-keys` ConfigMap, so gateway logs and metrics carry `user_id="<name>"`. The chart then renders a ModelConfig that points at that Secret, a Harness, an AgentTemplate and an Agent. A new agent takes one to two minutes to become Ready while its golden snapshot is built.

To add a model, add an entry to `gateway.models` (see the commented Gemini and failover examples in `values.yaml`) and create its provider Secret in `agentgateway-system` with the key name `Authorization`. To add an MCP server, add an entry to `mcp.servers`. Tool names are prefixed with the server name (`fetch_fetch`), and an agent's tool allowlist has to list them explicitly. To turn code mode off, set `mcp.virtual.codeMode.enabled=false`.

Skill references must be digest-pinned (`...@sha256:<64 hex>`); the render fails otherwise.

## Tests

```sh
charts/openagentix/tests/run.sh      # shell assertions over `helm template`, needs helm, yq, jq
helm lint charts/openagentix
```

## Adopting an existing hand-applied install

If the cluster already holds hand-applied copies of the objects this chart renders, `install.sh` labels and annotates them for Helm (`app.kubernetes.io/managed-by=Helm`, `meta.helm.sh/release-*`), runs `helm upgrade --install`, then converges them with a server-side apply. Nothing is deleted or recreated. Helm alone would leave adopted custom resources unchanged. Objects the chart does not render (old stub models, test agents, a second policy) are left alone; remove them yourself.

## Known limitations

These are the gaps found while building the components. Most are acceptable for a laptop demo and not for anything else.

- **Single node, dev databases.** Substrate uses a bundled dev PostgreSQL with mTLS. kagent uses a plain pgvector PostgreSQL with password auth and `sslmode=disable`. RustFS credentials are `rustfsadmin/rustfsadmin`. The identity CA and JWT keys are minted once by `kubectl ate` and live in Secrets.
- **Tool layer has no authentication.** The vMCP accepts anonymous callers with a permit-all Cedar policy. Anyone who can reach `vmcp-tools:4483` can call any tool, including `fetch_fetch` against arbitrary URLs, which is a path into the cluster network. No NetworkPolicy restricts `mcp-tools`. The agent's identity is not forwarded to the tools. Production needs OIDC on the vMCP, per-tool Cedar rules, NetworkPolicies and egress limits for `fetch`.
- **Code mode** runs Starlark with no `json` module, so processing JSON tool output is awkward. Per-call authorization of inner tool calls is not verified.
- **Gateway budgets** are token-based and per proxy replica; there are no USD caps. Cross-replica limits need an external rate-limit service. Virtual keys are static, with no rotation or expiry. The gateway serves plain HTTP inside the cluster.
- **Provider key:** one Anthropic key is shared by every agent behind the gateway. Per-agent attribution is by virtual key only.
- **Telegram bridge** long-polls from a single replica (strategy Recreate). The bridge refuses to start without `TELEGRAM_ALLOWED_CHAT_IDS`; if you open it explicitly (`bridge.allowlist.enabled=false`), anyone who finds the bot can use your agent and your API budget.
- **Skills** need a registry the node can pull from over plain HTTP. On kind the installer runs its own `registry:2` container (`openagentix-registry`) on the `kind` docker network, pushes the skill there and passes the registry's current IP and the digest to Helm (the chart default is an unpullable placeholder). The registry survives cluster deletion, but its IP can change, so re-run `./install.sh` after recreating the cluster. Model-driven skill selection has only been exercised lightly.
- **Sandbox DNS** does not apply search domains, so every in-cluster URL an agent uses must be a full `*.svc.cluster.local` name.
- **Agent changes roll slowly.** Editing a ModelConfig re-snapshots the agent (15 to 20 seconds); calls in that window still use the old configuration. Agents share one Substrate worker pool, so a busy agent can starve another.
- **Gemini** has not produced a real completion in this project (the test key had no credits). The Gemini and failover model entries are examples only.
- **A kagent-controller start-up race** (two restarts while Substrate's API came up) was seen once and not root-caused.
- **AX is not part of the install.** `spikes/ax` added Substrate values for Google AX. `install.sh` skips prerequisites that already exist, but `FORCE_PREREQS=1` (or a first install followed by an AX install) re-applies `spikes/kagent/values/substrate.yaml` and drops those additions. AX also does not provide a way to message a running task, so it is not used here.
- The cold install was run twice from a deleted kind cluster on one machine (Docker image cache warm). A machine with nothing cached has not been tried.

## Repository layout

```
install.sh, uninstall.sh        installer and release-only uninstaller
charts/openagentix/             the Helm chart (values.yaml, templates/, tests/run.sh)
bridges/telegram/               Go Telegram -> A2A bridge, Dockerfile, deploy/ reference manifests
components/model-gateway/       agentgateway install script, original manifests and NOTES
components/tools/               ToolHive install script, original manifests, example skill, verify/ clients, NOTES
components/agents/              the original hand-applied assistant agent (now rendered by the chart)
hack/kind-config.yaml           kind cluster config (node image pin) used by install.sh
spikes/kagent/                  kagent + Substrate bring-up (up.sh, values) and NOTES
spikes/ax/                      Google AX spike; not installed
```

The `NOTES.md` files record versions, deviations from upstream docs, failures and evidence.
