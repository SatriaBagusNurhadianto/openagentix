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
                     agentgateway                 |-- OSV MCP server
                     (model-gateway)              `-- knowledge base (MCPServerEntry)
                     ns agentgateway-system           |
                       |            |                 v
                       v            | /v1/embeddings  kb (Go)  --> PostgreSQL + pgvector
              Anthropic             |  (own virtual    ns openagentix-knowledge
              (claude-haiku)        |   key "kb")         |
                                    +--------------> kb-embeddings (llama.cpp, bge-small, CPU)
```

Agents never hold a provider key. Each agent holds one gateway virtual key, so the gateway can budget and log usage per agent. Skills (Agent Skills format) are mounted into the sandbox from digest-pinned OCI images.

## How it is packaged

Upstream constraints make a single Helm chart impossible today: kagent and Substrate need fixed release names, a strict install order, and key material minted by `kubectl ate` before the charts start. The Gateway API CRDs ship in neither agentgateway chart. So there are two layers:

| Layer | What | Where |
|---|---|---|
| Installer | Creates the kind cluster (`hack/kind-config.yaml`), installs the upstream releases at pinned versions, creates Secrets from your environment, builds images, runs Helm | `install.sh`, `uninstall.sh` |
| Chart | Our own resources only, all driven by values | `charts/openagentix/` |

The chart owns: the Telegram bridge Deployment, the model gateway (Gateway, one AgentgatewayModel per entry in `gateway.models`, one AgentgatewayPolicy), the MCP layer (MCPGroup, MCPServers, VirtualMCPServer), the knowledge base (`knowledge` block: kb, PostgreSQL with pgvector, a CPU embedding server, an MCPServerEntry that makes the kb a backend of the vMCP, an `embed-default` gateway model, NetworkPolicies), and the kagent wiring (RemoteMCPServer, plus a ModelConfig, Harness, AgentTemplate and Agent for every entry in `agents`). Each block has an `enabled` flag. The chart never contains a secret value. It only references Secrets by name.

Pinned upstream versions: kagent and Agent Substrate 1.0.1, Gateway API CRDs 1.6.3, agentgateway v1.7.0, ToolHive 0.67.1.

## Prerequisites

- Kubernetes 1.37 or newer. The installer creates a kind cluster with `kindest/node:v1.37.0`. The pod-certificate APIs that Substrate uses are only GA from 1.37.
- On your machine: `docker`, `kind` 0.33+, `kubectl` 1.37+, `helm` 3, `jq`, `yq` (the Python jq wrapper, not mikefarah's), `openssl`, `shasum`, and `curl` (the first install downloads the `kubectl-ate` plugin from the public Agent Substrate release and verifies its SHA-256; no GitHub login is needed).
- Docker needs about 7.5 GB of memory or more (tested with a 7.7 GiB VM). The full stack idles at roughly 2.3 GiB in the node; each agent sandbox adds more. The knowledge base adds about 0.2 GiB working set when idle (see "Knowledge base"); `knowledge.enabled=false` leaves it out.
- An Anthropic API key, and a Telegram bot token from @BotFather.

## Quickstart

```sh
export ANTHROPIC_API_KEY=sk-ant-...
export TELEGRAM_BOT_TOKEN=123456:ABC...
export TELEGRAM_ALLOWED_CHAT_IDS=111111111       # required: the bridge refuses to start without it
./install.sh
```

A cold install from an empty cluster (images already in the local Docker cache) took about 10 minutes (604 s) on a 5 CPU / 7.7 GiB VM; allow longer when every image has to be pulled. If a required variable is missing the script says which one and changes nothing. Re-running is safe.

Useful switches: `CREATE_CLUSTER=0`, `SKIP_PREREQS=1`, `FORCE_PREREQS=1`, `SKIP_SKILL=1`, `SKIP_BRIDGE_IMAGE=1`, `SKIP_KB_IMAGE=1`, `SKIP_KB_INGEST=1`, `VALUES_FILE="my-values.yaml"`. They are documented at the top of `install.sh`.

Check it works from inside the cluster:

```sh
kubectl run a2a-test --rm -i --restart=Never -n openagentix-gateway --image=curlimages/curl:8.10.1 \
  --pod-running-timeout=90s --command -- curl -s -m 170 \
  -H 'Content-Type: application/json' -H 'A2A-Version: 1.0' \
  -d '{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"Does lodash 4.17.20 on npm have known vulnerabilities? Give the count."}]}}}' \
  http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent/assistant
```

Expect `TASK_STATE_COMPLETED` and an answer that says 4. Then message your bot on Telegram.

To remove OpenAgentix: `./uninstall.sh`. It removes only the `openagentix` Helm release. kind, kagent, Substrate, agentgateway, ToolHive, namespaces and all Secrets stay. `PURGE_SECRETS=1` also deletes the Secrets and the key ConfigMap that `install.sh` created (the knowledge base's database credentials stay); `PURGE_DATA=1` deletes the knowledge base volumes and database credentials.

## Knowledge base

The `knowledge` block (default on) deploys a small document store with hybrid search into the namespace `openagentix-knowledge`, and gives agents three read-only MCP tools for it. Nothing in it needs an external account: the embedding model runs in the cluster.

| Piece | What | Notes |
|---|---|---|
| `kb` | Go service from `services/kb` (image `openagentix/kb:dev`, built and loaded by `install.sh`) | MCP at `/mcp`, admin ingest API under `/v1` (bearer token) |
| `kb-postgres` | `pgvector/pgvector:pg17` (tag and digest pinned), StatefulSet, 2 Gi PVC | chunks, a `tsvector` index and a 384-dim HNSW vector index |
| `kb-embeddings` | llama.cpp server b10088 (digest pinned), model `BAAI/bge-small-en-v1.5` as GGUF (Q8_0, 384 dimensions) | OpenAI-compatible `/v1/embeddings`, CPU only, weights cached on a 1 Gi PVC |
| `embed-default` | an `AgentgatewayModel` on the model gateway that routes to `kb-embeddings` | the kb calls the gateway with its own virtual key (`user_id="kb"` in the gateway log); later consumers can share the endpoint |
| `knowledge` | an `MCPServerEntry` in the vMCP's group | no extra pods: the vMCP itself connects to the kb |

Agents that set `knowledge: true` (the demo `assistant` does) get the tools `knowledge_kb_search`, `knowledge_kb_get_document` and `knowledge_kb_list_collections` and a short addition to their system prompt: search the knowledge base for questions about OpenAgentix itself and cite document titles. The name is the entry name plus the vMCP prefix (`mcp.virtual.prefixFormat` with `{workload}` = `knowledge.mcp.entryName`) plus the kb's own tool name.

`install.sh` creates four Secrets in `openagentix-knowledge` only when they are absent, so re-runs never rotate them: `kb-admin` (ingest token), `kb-postgres` (database password), `kb-database` (connection URL built from it) and `kb-virtual-key` (the kb's gateway key; its hash is added to `llm-api-keys`). It then ingests `components/knowledge/sample/` into the collection `openagentix`. Try it with the A2A command from the quickstart and the question "What is the OpenAgentix knowledge base canary code?". Expect `HERON-4412-KB-RETRIEVED` and a `knowledge_kb_search` call in the task's tool-call records.

**Ingest your own documents.** Put markdown files in a directory and run:

```sh
hack/kb-ingest.sh ./my-docs my-collection        # collection defaults to "openagentix"
```

Each file becomes one document: id from the path (`adr/0001-x.md` becomes `adr:0001-x`), title from the first `# ` heading, one idempotent `PUT` per file, so re-running replaces rather than duplicates. The script reads the token from the `kb-admin` Secret into a variable (never printed, not in argv) and talks to the kb through `kubectl port-forward`, which needs no NetworkPolicy entry. For an in-cluster job set `KB_URL` and `KB_ADMIN_TOKEN` and list the job's pods in `knowledge.kb.ingestFrom`. Agents see every collection; to restrict a search the agent passes `collection`.

**Swap the embedding model.**

- *Another local model:* set `knowledge.embeddings.model.hf` (a `<HuggingFace GGUF repo>:<quantisation>`), `.name`, `.dim` (at most 2000, the pgvector HNSW limit) and `.pooling` (`cls` for bge, often `mean` for others). Leave the image alone unless the model needs a newer llama.cpp.
- *A hosted endpoint:* set `knowledge.embeddings.enabled=false`, `knowledge.kb.embeddings.url` (an OpenAI-compatible base URL), `.model`, `.dim`, and `.apiKeySecret` (`{name, key}` of a Secret you create in `openagentix-knowledge`). The local server and the `embed-default` route are then not rendered.
- *No embeddings:* `knowledge.embeddings.enabled=false` alone gives lexical-only search.
- *Direct instead of through the gateway:* `knowledge.embeddings.viaGateway=false` (the kb calls the Service; NetworkPolicy then allows only the kb pod).

**Changing the model or its dimension means re-ingesting.** Vectors from different models are not comparable and the stored column has a fixed dimension. The kb refuses to start on a dimension mismatch; use a new `knowledge.kb.schema` (or drop the old one) and run `hack/kb-ingest.sh` again for every collection. Turning embeddings on for an existing database does not back-fill old chunks either. Changing `chunkMaxChars` needs a re-ingest too.

**Measured footprint** (kind, 1 arm64 node, 5 CPU / 7.7 GiB VM; working sets from the container runtime): idle, `kb` 6 MiB, `kb-embeddings` 110 MiB, `kb-postgres` 64 MiB. While ingesting 17 markdown files (160 KB, 25 s) `kb-embeddings` peaked at 155 MiB and used about 1.1 cores on average (2 threads, limit 2 CPU), `kb` 9 MiB, PostgreSQL 33 to 64 MiB. Requests: 100 m CPU, 304 MiB in total; limits: 384 MiB for the embedding server and for PostgreSQL, 192 MiB for the kb. The node's memory requests went from 3410 Mi to 3714 Mi.

## Memory

An agent with `memory.enabled: true` remembers facts across conversations. The demo `assistant` has it on. This is kagent's own long-term memory (Harness `spec.kagent.memory`); the chart wires it up and keeps its pieces behind the same gateway, virtual keys and NetworkPolicies as everything else.

**What is remembered.** Only what the model decides to save. kagent has no automatic "remember the whole conversation": it adds a `save_memory` tool, and a memory exists only after the model calls it with a short piece of text (a preference, a codename, a standing instruction). The chart appends a short paragraph to the system prompt of memory-enabled agents that tells the model to do that when the user shares something worth keeping or asks it to remember, and never to store secrets. Each saved text is embedded and stored as one row.

**When it is recalled.** Automatically, on every turn: kagent embeds the user's message, takes up to 5 stored memories of the same user and agent with cosine similarity of at least 0.3, and puts them into the model's context before it answers. The model also gets a `load_memory` tool for searching on its own. So a fact saved in conversation A shows up in a brand new conversation B (no `contextId`) when B's question is similar enough.

**For whom.** One memory space per agent AND per user: rows are keyed by the agent (`kagent/assistant`) and the session's user id, and the agent and user are taken from the stored session, never from the request. Another user of the same agent, or the same user on another agent, does not see them. Which user id a request has depends on kagent's auth mode. In the `insecure` mode this installation uses, kagent takes it from `?user_id=` or the `X-User-Id` header on the A2A request and otherwise uses the default admin user `admin@kagent.dev`. The Telegram bridge sends `X-User-Id: telegram:<chat id>` (prefix configurable with `bridge.userIdPrefix`), so **each Telegram chat has its own memory space**, and a group chat is one space shared by its members; the allowlist (`TELEGRAM_ALLOWED_CHAT_IDS`) still decides who may talk to the agent at all. Memory saved before this change was stored under the shared default user and is not visible to per-chat identities. Anything that can reach the controller's port 8083 can also claim any user id (the NetworkPolicy limits that to the bridge pod, `kagent` namespace and the egress proxy).

**Turn it on for an agent.**

```yaml
agents:
  - name: researcher
    # ...
    memory:
      enabled: true
      ttlDays: 30      # optional; kagent's default is 15
```

`install.sh` then (1) installs pgvector into kagent's PostgreSQL (`spikes/kagent/pgvector.sh`, idempotent, no data touched), (2) sets `config.database.vectorEnabled=true` on the kagent release with the one script that always applies `spikes/kagent/values/kagent.yaml` (`spikes/kagent/kagent-helm.sh`; fresh installs get both from `spikes/kagent/up.sh`), (3) creates the gateway key `memory-virtual-key` for the embedding ModelConfig, and (4) renders the shared pieces once: ModelConfig `gateway-embed-memory`, gateway model `embed-memory`, and the embedding server in namespace `openagentix-memory`. An agent without the `memory` block renders exactly as before, and if no agent opts in none of the shared pieces exist. `ttlDays` only sets `expires_at` on each row. In kagent 1.0.1 nothing calls the prune function (`PruneExpiredMemories`) and search ignores `expires_at`, so **memories do not actually expire**; delete them yourself (see "Clear it").

**Why a second embedding model.** kagent stores memory as `vector(768)` and refuses any other size (a 384-dimension model fails with "embedding dimension 384 is less than required 768"; a larger vector is cut to 768 and re-normalised). The knowledge base's `bge-small-en-v1.5` has 384, so memory runs its own llama.cpp server with `bge-base-en-v1.5` (768, same publisher and pooling, 118 MB Q8_0). The chart fails the render if `memory.embeddings.model.dim` is not 768. The knowledge base is untouched, so nothing needs re-ingesting.

**One origin per credential.** kagent injects the gateway key for an agent's model at the sandbox's egress proxy and allows one key per destination origin. The chat ModelConfig already uses `model-gateway.agentgateway-system.svc.cluster.local` with the agent's key, so the embedding ModelConfig reaches the same gateway under a second DNS name (an `ExternalName` Service `model-gateway-memory`). Gateway logs show the embeddings as `user_id="memory"`.

**Look at it.** Memories are in `kagent.memory` of kagent's PostgreSQL (the admin password is in Secret `kagent/kagent-pg-admin`; `kubectl exec` into `kagent-pg-0` uses the local socket and needs none):

```sh
kubectl exec -n kagent kagent-pg-0 -- psql -U postgres -d kagent -c \
  "select agent_name, user_id, left(content,80) as content, extensions.vector_dims(embedding) as dims, expires_at, access_count from kagent.memory"
```

**Clear it.** kagent offers list and delete only over its internal gRPC API, so use SQL. One user on one agent: `delete from kagent.memory where agent_name='kagent/assistant' and user_id='<user>'`. Everything: `truncate kagent.memory`. Turning memory off for an agent (`enabled: false`) stops saving and recall but does not delete rows.

**Privacy.** Memory holds what users said, as plain text, in the database and as vectors, until someone deletes it (`ttlDays` is recorded but not enforced in kagent 1.0.1). The text of every saved memory and of every user message (for recall) is sent to the embedding server in-cluster only; nothing leaves the cluster for embeddings. The model decides what to save, so it can store things you would rather it did not; the prompt tells it not to store secrets, which is advice, not a filter. The database is the development PostgreSQL (password auth, no TLS, no encryption at rest). A turn fails if the memory backend errors (the preload step has no fallback for a failing database), so keep pgvector enabled for as long as an agent has memory on.

**Footprint** (kind, 1 arm64 node): the embedding server idles at 74 MiB and was 133 MiB after a few calls (requests 192 MiB, limit 512 MiB, 1 GiB PVC for the model); kagent's PostgreSQL stayed at 60 to 61 MiB with pgvector on and a few rows; the `memory` table with two rows is 96 kB. The node's memory requests went from 3714 Mi to 3906 Mi.

## Channels and triggers

[Agent Kourier](https://github.com/agentkourier/agent-kourier) (0.6.0, pinned) connects chat platforms and signed webhooks to kagent agents. It is a small broker (one pod, SQLite on a 1Gi PVC, about 58 MB resident) that watches three custom resources: a **ChatConnection** (one Slack app or one webhook source, with its Secret), a **Binding** (connection channel -> agent, with triggers, limits and the user id the agent sees) and an **AgentBackend** (how to reach kagent, dialect `kagent-v1`). OpenAgentix renders all three from values; Kourier itself is installed by `install.sh`. It is **off by default**: with `channels.kourier.enabled: false` the chart renders nothing for it (byte-identical output to before).

**Enable it.** The one switch is the chart value `channels.kourier.enabled`, set in a values file you pass every run (Helm replaces release values on each run):

```sh
VALUES_FILE=components/channels/demo-values.yaml ./install.sh     # Kourier on, one demo webhook connection and binding to `assistant`
```

When the chart renders the block, `install.sh` (before the chart, so the CRDs exist): creates the Secret of each webhook connection if it is absent (random, never printed, kept on re-runs), checks that Slack Secrets exist, installs the `agent-kourier` Helm release in namespace `agent-kourier` (`components/channels/kourier.sh`; verifies the chart and the image digest with `cosign` when it is installed and says so when it skips that), then installs the chart. A re-run changes nothing (same chart version and values: no new revision, no pod restart). The chart also admits Kourier's pod to the kagent controller's port 8083 (a NetworkPolicy peer derived from the same switch, so a later install cannot drop it; `networkPolicy.extraA2ACallers` still works next to it). Kourier's own NetworkPolicy is on (`components/channels/kourier-values.yaml`): egress only to DNS, the API server, the kagent controller and port 443 outside the private ranges (Slack); ingress only the chat port from pods in its own namespace. Without a values file that enables it, a run that finds Kourier installed prints a note and removes the connections and bindings from the release; `./uninstall.sh` removes the Kourier release (see below).

**Values.** Connections and bindings live in the top-level `channels` block (see `charts/openagentix/values.yaml` for all fields and the three patterns as comments). A webhook connection and a binding to `assistant`:

```yaml
channels:
  kourier: {enabled: true}
  connections:
    - name: orders-hook
      platform: webhook
      existingSecret: orders-hook-webhook          # install.sh generates it (key webhookSecret) when absent
      webhook:                                      # copied unchanged into the ChatConnection spec
        identity: orders-system
        verify: {scheme: hmac-sha256, header: X-Signature, prefix: "sha256="}
        channel: {from: [/channel]}
        actor: {from: [/user/login]}
        thread: {field: conversation, prefix: conv-}
        delivery: {header: X-Delivery-Id}
        fields:
          - {name: event, from: [/event], kind: id}
          - {name: conversation, from: [/conversation], kind: id}
          - {name: detail, from: [/detail]}
  bindings:
    - name: orders-assistant
      connection: orders-hook
      channel: orders
      agent: assistant                              # must be one of agents[].name, or the render fails
      triggers:                                     # copied unchanged
        - type: webhookEvent
          match: {event: '^order\.failed$'}
          promptTemplate: |
            Order {{ .Fields.conversation }} failed. Find out why and say what to do. The detail is in the untrusted data block.
          limits: {rateLimit: {maxRuns: 20, per: 1h, maxConcurrent: 2}, maxRunsPerDay: 100}
```

The Secret is only referenced: the chart renders no Secret values. The chart also renders a Role that lets Kourier's ServiceAccount `list` and `watch` exactly the Secrets your connections name. The `userId` of a binding is what kagent sees as the caller (default `kourier:<binding name>`).

**Three ways to map agents.**

- **A trigger per agent.** One webhook connection, one binding per agent. Each binding has its own `channel` value and its own `match`, so each event goes to the agent that owns it (`orders` to `assistant`, `deploys` to `ops`).
- **One bot, an agent per channel.** One Slack connection, one binding per Slack channel id, each naming another agent: `{name: eng, connection: team-slack, channel: C0ENG000001, agent: assistant}`, `{name: sec, connection: team-slack, channel: C0SEC000001, agent: security}`.
- **One bot per agent.** One Slack app, so one connection and one Secret, per agent, each with a binding to its agent (the bots can sit in the same channel).

**Define a webhook trigger for an agent.** Add a connection (how to verify the signature and where the channel, actor, conversation and fields are in the JSON body) and a binding with a `webhookEvent` trigger whose `match` is a regular expression over a field you declared, plus a `promptTemplate` (fields of kind `id` may be used in the template and thread key; `text` fields reach the agent only as untrusted data). Always set `limits`: excess events are dropped, not queued, and the sender is still told `200`.

**Test it locally.** The chat listener Service is ClusterIP only; nothing is exposed. Port-forward and send a signed event (the demo connection `demo-webhook`, header `X-Demo-Signature`; the secret is read from the cluster at signing time and never printed):

```sh
kubectl port-forward -n agent-kourier svc/agent-kourier-chat 8081:8081 &
BODY='{"event":"question.asked","channel":"demo","user":{"login":"alice"},"conversation":"c1","question":"Does lodash 4.17.20 have known vulnerabilities? How many?"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$(kubectl get secret -n openagentix-channels demo-webhook -o jsonpath='{.data.webhookSecret}' | base64 -d)" -hex | awk '{print $NF}')
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8081/chat/webhook/openagentix-channels/demo-webhook \
  -H 'Content-Type: application/json' -H "X-Demo-Signature: sha256=$SIG" -H 'X-Demo-Delivery: d1' --data-binary "$BODY"
```

After a fresh install the binding can stay `Ready=False` (reason `AgentNotReady`) for a few minutes after the agent is Ready: Kourier re-checks the agent only periodically. A `200` means accepted; a wrong signature is `401`. The agent's answer is not returned (see limits): `kubectl logs -n agent-kourier deploy/agent-kourier` shows `session turn started` and `session turn ended outcome=completed`, and the text is in kagent's task store (`kagent.session_task` in the kagent PostgreSQL).

**Exposure.** The chat listener stays a ClusterIP Service on port 8081 and the chart creates no Ingress and no public endpoint. To receive real webhooks you would add an Ingress (or Gateway API route) in front of `agent-kourier-chat:8081` for the path `/chat/webhook/<namespace>/<connection>` only, terminate TLS there, and put the ingress controller's pods in `networkPolicy.chatFrom` (a values file passed with `KOURIER_VALUES_FILE`, replacing the default in `components/channels/kourier-values.yaml`) so the policy admits it; Kourier's chart has `chatListener.ingress` for the Ingress object. Webhook authenticity rests on the HMAC (or token) of the connection, so use a long random Secret.

**Limits (read before relying on it).**

- Upstream is experimental and 0.x with one maintainer; the webhook platform is the newest part. Pin and re-run the spike checks (`spikes/kourier/NOTES.md`) when you upgrade.
- A webhook has **no reply route**: the binding's `output` must be `none`, and the answer is visible only in kagent's store. The agent has to act through its own tools (see `docs/decisions/0002-schedules-and-trigger-delivery.md`).
- **No direct messages**: Kourier serves channels and mentions, so Telegram (which is chat-by-DM) stays on our own bridge.
- **One memory scope per Binding.** Every conversation of a binding runs as the binding's fixed `userId`; kagent's long-term memory is keyed by agent and user id, so all of a binding's conversations share it. Give different teams different bindings.
- **Anyone in a bound Slack channel can use the agent** (and spend its budget); the channel is the access control.
- The kagent controller does not authenticate callers (insecure mode), so Kourier's `userId` is an unauthenticated claim and the NetworkPolicy peer is the only fence.
- A Kourier restart in the middle of a turn ends the turn recorded as `refused` on kagent 1.0.1 (it re-attaches with a call kagent answers `UNSUPPORTED_OPERATION`), even though the task completes. With a Slack reply route the user would see a failure notice instead of the answer.
- Rate limits drop the excess; the dedupe key is the body hash and lives in memory.

**Slack (UNTESTED).** The Slack path of this integration has not been run: the project had no Slack tokens. The values and RBAC follow Kourier's documentation and CRD schema only. When you have them:

1. Create a Slack app with Socket Mode on, a bot token (`xoxb-...`) and an app-level token (`xapp-...`, scope `connections:write`); subscribe the bot to `app_mention` and `message.channels`, and invite it to the channel. See Kourier's `docs/reference/platforms.md` for scopes.
2. Create the Secret yourself, in `channels.kourier.namespace` (default `openagentix-channels`), with exactly the keys `botToken` and `appToken` (HTTP mode needs `botToken` and `signingSecret` instead). Do not put the values in a file or in shell history you keep:

   ```sh
   kubectl create namespace openagentix-channels --dry-run=client -o yaml | kubectl apply -f -
   kubectl create secret generic team-slack-tokens -n openagentix-channels --from-literal=botToken="$SLACK_BOT_TOKEN" --from-literal=appToken="$SLACK_APP_TOKEN"
   ```
3. Add to your values file: a connection `{name: team-slack, platform: slack, existingSecret: team-slack-tokens, slack: {mode: socket}}` and a binding `{name: eng, connection: team-slack, channel: <Slack channel id>, agent: assistant, output: live, triggers: [{type: mention}]}`; run the installer with that file. `kubectl get bindings.agentkourier.dev -n openagentix-channels` shows `READY`.
4. Check what the spike could not: streaming cards, the restart caveat with a real reply path, thread-to-session mapping and the memory scope per channel.

**Uninstall.** `./uninstall.sh` removes the openagentix release and then the `agent-kourier` release. It leaves the namespaces `agent-kourier` and `openagentix-channels`, Kourier's PVC and CRDs and the generated webhook Secret. `PURGE_SECRETS=1` also deletes the webhook Secrets install.sh generated (never your Slack Secrets); `PURGE_DATA=1` also deletes Kourier's PVC and its three CRDs (the chart marks them `keep`; deleting them deletes any ChatConnection, Binding or AgentBackend still present).

## Schedules

Run an agent on a timetable, declared in values like everything else. Each entry of `schedules` renders a **CronJob** in the namespace `openagentix-schedules` (its own namespace, so the NetworkPolicies can name exactly these pods) whose pod runs `tools/a2a-call`: one A2A `SendMessage` to the agent with a caller identity, then exit `0` only if the task completed. Empty by default: with no schedules the chart renders nothing for this and its output is byte-identical to before. [ADR 0002](docs/decisions/0002-schedules-and-trigger-delivery.md) explains why kagent's own scheduler is not used (it sends no caller identity, so memory-enabled agents fail, and its runs hung on a cold agent).

```yaml
schedules:
  - name: daily-brief                 # DNS label, at most 40 characters, unique
    agent: assistant                  # must be an agents[].name, else the render fails
    schedule: "0 8 * * 1-5"           # 5-field cron, checked at render time (field count, ranges, names)
    timeZone: Europe/Berlin           # optional, IANA name
    prompt: |                         # configuration, NOT a secret (see below)
      Check the vulnerability database for lodash 4.17.20 and answer in two sentences.
    # optional: userId (default schedule:daily-brief), timeout (300s), suspend (false), concurrencyPolicy (Forbid),
    # historyLimits {successful: 3, failed: 3}, backoffLimit (0), startingDeadlineSeconds (300), ttlSecondsAfterFinished
    # (86400), resources. The defaults live in `scheduling.defaults` in values.yaml.
```

Pass the file to the installer like any values file; several can be combined (space-separated, applied in order; maps merge, **lists are replaced**, so keep all schedules in one file): `VALUES_FILE="components/channels/demo-values.yaml components/schedules/demo-values.yaml" ./install.sh`. `install.sh` builds and loads the `openagentix/a2a-call:dev` image only when the chart renders a CronJob (switch `SKIP_A2A_CALL_IMAGE=1`), and stamps a hash of `tools/a2a-call` on the pod template.

**User id and memory.** The pod sends `X-User-Id: <userId>`, default `schedule:<name>`. kagent keys sessions and long-term memory by it, so **one schedule is one memory scope**: every run of `daily-brief` sees what earlier runs saved with `save_memory`, and no other schedule, chat or binding does. Give two schedules the same `userId` to share a scope. Each run is a new conversation (a new kagent session owned by that user id); runs do not continue each other's session.

**Where results go today: nowhere but kagent's store.** The job log has a one-line summary (state, task id, duration, length of the answer), never the answer text: answers can contain private data and job logs are widely readable. The answer is in kagent (the session of that user id, `kagent.session_task`, or the UI). Delivery to Telegram or Slack is the planned notify tool (ADR 0002); until it lands a schedule is useful for agents that act through their own tools, or whose memory you read later.

**Do not put secrets in prompts.** The prompt is stored in a ConfigMap (`schedule-<name>`) that anyone who can read ConfigMaps in `openagentix-schedules` can read, and it is sent to the model provider. The chart renders no Secret values for schedules.

**Run one now, and see failures.**

```sh
kubectl create job -n openagentix-schedules --from=cronjob/demo-brief demo-brief-manual-1   # one real model call
kubectl logs -n openagentix-schedules job/demo-brief-manual-1       # {"state":"TASK_STATE_COMPLETED","taskId":...,"durationMs":9279,"answerChars":308,...}
kubectl get cronjob,job -n openagentix-schedules                    # LAST SCHEDULE, SUSPEND, Failed jobs
```

A failed run (failed task, input required, HTTP or JSON-RPC error, timeout) makes the pod exit `1` with `a2a-call: FAILED: <reason>` on stderr and the Job `Failed`. **`backoffLimit` is 0 on purpose**: by the time a call fails the agent may already have used tools or written memory, and a blind retry repeats the cost and the side effects; the next scheduled firing is the retry. `activeDeadlineSeconds` is the timeout plus 60 seconds, so a hung pod cannot outlive its schedule; `startingDeadlineSeconds` (300) skips a firing that could not start within five minutes instead of running it late; finished Jobs and their logs are removed after a day (`ttlSecondsAfterFinished`) and the last 3 successful and 3 failed are kept. Nothing alerts you: look at `kubectl get jobs -n openagentix-schedules` or scrape kube-state-metrics' job metrics.

**Cost.** Every firing is at least one model call and often several (tool calls, memory preload); a `* * * * *` schedule spends tokens every minute. Use `suspend: true` while testing (`kubectl patch cronjob <name> -n openagentix-schedules --type=merge -p '{"spec":{"suspend":true}}'` for a quick stop; the next install restores the values). The gateway's token budget per virtual key still applies. The demo schedule is suspended.

**Security.** The pods are non-root with a read-only root filesystem, no capabilities, seccomp `RuntimeDefault` and no service-account token. Two NetworkPolicies: the kagent controller's A2A port admits the namespace `openagentix-schedules` **and** the pod label `openagentix.io/component=schedule` (a pod in that namespace without the label times out), and an egress policy lets those pods reach only CoreDNS and the controller on 8083 (the knowledge base and the model gateway time out). Anyone who can create pods in `openagentix-schedules` with that label can call any agent, so keep that namespace's RBAC tight. `X-User-Id` is an unauthenticated claim on port 8083 like for every caller (see Security). Verified on kind: **kindnet enforces the egress policy for cluster destinations but not for destinations outside the cluster** (a labelled pod still reached example.com); on a CNI that enforces egress fully (Calico, Cilium) the pods also have no internet.

## Add an agent

Create a values file and pass it to the installer (`knowledge: true` on an agent adds the knowledge base tools and prompt; `memory: {enabled: true}` turns on long-term memory, see "Memory"):

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
(cd tools/a2a-call && make test)     # the schedule caller: go vet, go test -race
```

## Adopting an existing hand-applied install

If the cluster already holds hand-applied copies of the objects this chart renders, `install.sh` labels and annotates them for Helm (`app.kubernetes.io/managed-by=Helm`, `meta.helm.sh/release-*`), runs `helm upgrade --install`, then converges them with a server-side apply. Nothing is deleted or recreated. Helm alone would leave adopted custom resources unchanged. Objects the chart does not render (old stub models, test agents, a second policy) are left alone; remove them yourself.

## Security

What is protected, what is not. Checked on kind (kindnet enforces NetworkPolicy) with kagent 1.0.1, Substrate 1.0.1 and ToolHive 0.67.1.

**Protected**

- **Network, `mcp-tools` (chart, `networkPolicy.enabled`, default on).** MCP backends and their proxies accept connections only from their own namespace and from the ToolHive operator. The vMCP (port 4483) additionally accepts the Substrate egress proxy (`atenet-egress` in `ate-system`) and the kagent controller pod. A pod in `default` or in another namespace times out.
- **Network, kagent controller port 8083.** A2A, the REST API and the sandboxes' task-store gRPC share this port. It accepts the Telegram bridge pod (selected by pod label `app=telegram-bridge` in `openagentix-gateway`, not the whole namespace), every pod in the `kagent` namespace (UI, tools, worker pool), `atenet-egress` and, when `channels.kourier.enabled`, the Agent Kourier pod (namespace and pod labels). Other pods time out. Ports other than 8083 are left open, as before (the controller listens on no other).
- **Tool identity (chart, `mcp.virtual.incomingAuth.type: oidc`, default).** The vMCP rejects requests without a valid Substrate actor JWT (HTTP 401, also for a malformed token). The RemoteMCPServer asks Substrate for a token with the dedicated audience `openagentix-vmcp`, and the egress proxy adds it to every tool call. The vMCP verifies issuer, signature (explicit JWKS URL, TLS checked against the Substrate service CA, which `install.sh` copies into a ConfigMap), audience and expiry. No `insecure*` flag is set; `jwksAllowPrivateIP` is on because the IdP's ClusterIP is a private address.
- **Network, `openagentix-knowledge` (chart, same switch).** The kb pod accepts port 8080 only from the vMCP pod (selected by namespace and pod labels); the kb port carries both `/mcp` (no authentication, by design) and the admin API (bearer token), so the policy and the token together are the control. PostgreSQL accepts only the kb pod. The embedding server accepts only the model gateway proxy pod (the kb pod in direct mode). Checked with a pod in `default` (all three time out), another pod in the knowledge namespace (all three time out) and a non-vMCP pod in `mcp-tools` (kb times out). A request to `/v1/embeddings` on the gateway without a key or with a wrong key gets 401.
- **Tool authorization (optional).** `mcp.virtual.incomingAuth.toolAccess` turns into Cedar `permit` rules keyed on the token's `atespace` claim and the tool name; with it set, a tool not listed is refused (and hidden from the agent's tool list). Verified: with `osv_query_vulnerability` left out, Cedar logged deny for it and allow for the others, and the agent answered through `osv_query_vulnerabilities_batch` instead.

**Not protected**

- **The kagent controller does not authenticate callers.** `server.auth.mode` is `insecure`. Enabling `config.server.auth.serviceAccount.authority: kubernetes` was tried and does not help: requests with no token and with a garbage token were still answered (HTTP 200); only a valid Kubernetes token with the wrong audience was refused (401). Any pod the NetworkPolicy lets through can call any agent. The policy is the only control, and it allows all of the `kagent` namespace and all sandboxes (their traffic leaves through `atenet-egress`, which therefore cannot be told apart from one agent to the next).
- **No per-agent tool authorization.** kagent creates one Substrate actor per session (`actor/kagent/session-<uuid>`) in the atespace `kagent`, for every agent. Cedar can tell sessions and atespaces apart, not agents. The default Cedar policy is permit-all for any authenticated caller. Only the agent's tool allowlist in its AgentTemplate limits which tools an agent offers to its model. Whether code running in a sandbox (for example through the `bash` tool) could call the vMCP directly, with the token the egress proxy injects for that destination, was not tested; assume it can.
- Anything with an actor JWT can present it to the vMCP until it expires (up to `tokenExpirationSeconds`, 3600). There is no revocation.
- **Global tool discovery is off.** With `actorJwt` the RemoteMCPServer shows `Accepted` with reason `DiscoveryDisabled`; agents list the tools at run time inside their sandbox. Set `incomingAuth.type=anonymous` to get the controller-side discovery back.
- The `fetch` tool can still reach any URL the pod network allows (no egress policy on `mcp-tools`). The Telegram bridge pod and the `kagent`/`ate-system` namespaces are not restricted at all. The Substrate CA copied into `mcp-tools` is read once at install time; re-run `./install.sh` if the CA is rotated.
- The Telegram allowlist and the model gateway virtual keys are separate controls described above.

## Known limitations

These are the gaps found while building the components. Most are acceptable for a laptop demo and not for anything else.

- **Single node, dev databases.** Substrate uses a bundled dev PostgreSQL with mTLS. kagent uses a plain pgvector PostgreSQL with password auth and `sslmode=disable`. RustFS credentials are `rustfsadmin/rustfsadmin`. The identity CA and JWT keys are minted once by `kubectl ate` and live in Secrets.
- **Tool layer authorization is coarse.** The vMCP authenticates callers (see Security) but its Cedar policy is permit-all by default, and the identity Substrate gives an agent is per session, not per agent. See Security for the exact grain, and for the NetworkPolicy and `fetch` egress caveats.
- **Code mode** runs Starlark with no `json` module, so processing JSON tool output is awkward. Per-call authorization of inner tool calls is not verified.
- **Gateway budgets** are token-based and per proxy replica; there are no USD caps. Cross-replica limits need an external rate-limit service. Virtual keys are static, with no rotation or expiry. The gateway serves plain HTTP inside the cluster.
- **Provider key:** one Anthropic key is shared by every agent behind the gateway. Per-agent attribution is by virtual key only.
- **Telegram bridge** long-polls from a single replica (strategy Recreate). The bridge refuses to start without `TELEGRAM_ALLOWED_CHAT_IDS`; if you open it explicitly (`bridge.allowlist.enabled=false`), anyone who finds the bot can use your agent and your API budget.
- **Skills** need a registry the node can pull from over plain HTTP. On kind the installer runs its own `registry:2` container (`openagentix-registry`) on the `kind` docker network, pushes the skill there and passes the registry's current IP and the digest to Helm (the chart default is an unpullable placeholder). The registry survives cluster deletion, but its IP can change, so re-run `./install.sh` after recreating the cluster. Model-driven skill selection has only been exercised lightly.
- **Knowledge base.** One replica of each piece, no database backups, and the embedding model is downloaded from HuggingFace on first start (the node needs internet once; later starts use the PVC). The kb and its database have no egress policy. Ingest is by token; there is no per-collection authorization and the vMCP's permit-all Cedar policy applies to the kb tools like any other. The kb's lexical half ORs every query word, so common words add noise on a tiny corpus; the vector half still ranks well (checked with queries that share no word with the target chunk, see CHANGELOG). Tool names carry the entry name twice (`knowledge_kb_search`).
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
services/kb/                    Go knowledge base service (tests, Dockerfile, reference manifests in deploy/)
components/knowledge/sample/    sample corpus install.sh ingests
hack/kb-ingest.sh               ingest a directory of markdown into a kb collection
components/model-gateway/       agentgateway install script, original manifests and NOTES
components/tools/               ToolHive install script, original manifests, example skill, verify/ clients, NOTES
components/agents/              the original hand-applied assistant agent (now rendered by the chart)
hack/kind-config.yaml           kind cluster config (node image pin) used by install.sh
components/channels/            Agent Kourier install script, its values, a demo webhook values file
components/schedules/           a demo values file with one suspended schedule
tools/a2a-call/                 Go A2A caller run by the schedule CronJobs (tests, Dockerfile)
docs/decisions/                 decision records (0001 runtime, 0002 schedules and trigger delivery: accepted, schedules implemented)
spikes/kourier/                 Agent Kourier spike (evidence; superseded by the integration)
spikes/kagent/                  kagent + Substrate bring-up (up.sh, values) and NOTES
spikes/ax/                      Google AX spike; not installed
```

The `NOTES.md` files record versions, deviations from upstream docs, failures and evidence.
