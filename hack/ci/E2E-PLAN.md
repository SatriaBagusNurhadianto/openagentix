# Keyless end-to-end install in CI: what has to change

Status: **not built.** `.github/workflows/e2e.yml` is a scaffold whose first step fails on purpose. This page says exactly what
would have to change in `install.sh` and the chart before a CI run on a GitHub-hosted runner (4 vCPU, 16 GB, no secrets) can
honestly go green. Nothing here was run; the findings come from reading `install.sh`, `charts/openagentix/values.yaml` and
`components/`.

## What `install.sh` hard-requires today

1. **`ANTHROPIC_API_KEY`** (preflight, lines ~86-88): the script stops before changing anything unless the variable is set or
   the Secret `agentgateway-system/anthropic-api-key` exists. It is then written to that Secret (`put_secret`, ~218). The default
   chart model `claude-haiku` (`gateway.models[0]`, `provider: Anthropic`, `authSecret: anthropic-api-key`) and the default agent
   `assistant` (`model: claude-haiku`) depend on it.
2. **`TELEGRAM_BOT_TOKEN`** (preflight, lines ~89-91; Secret `openagentix-gateway/telegram-bridge`, ~220). The bridge is
   `bridge.enabled: true` by default, builds and loads its image, and is part of the wait loop. `TELEGRAM_ALLOWED_CHAT_IDS` only
   warns.
3. **Tools**: `docker kind kubectl helm jq yq openssl shasum` (preflight). `shasum` is on the ubuntu runner (perl); `yq` must be
   the Python wrapper (`hack/ci/install-tools.sh yq`).
4. Everything else is conditional on chart values (`knowledge`, `notify`, `governance.a2a`, `schedules`, `channels.kourier`,
   `observability`, memory), and each image build has a `SKIP_*` switch.

## A stub model path already exists in pieces

- `components/runtime/manifests/mock-llm.yaml` is an OpenAI-compatible stub server (a ConfigMap-hosted Python `http.server`, answers
  `MOCK-LLM reply ... 2+2 is 4.`, streaming and non-streaming) in the `kagent` namespace, Service `mock-llm:8000`. It is "spike only":
  `components/runtime/up.sh` applies `first-agent.yaml`, and the mock is referenced by `components/model-gateway/manifests/10-models.yaml`
  (model `stub`, `provider: OpenAI`, `baseURL: http://mock-llm.kagent.svc.cluster.local:8000/v1`) and
  `components/tools/manifests/30-mock-llm-tools.yaml`. Those are the spike manifests; the **chart does not render any of it**.
- The chart's `gateway.models[]` supports `provider: OpenAI` with a `baseURL`, so a `stub` model entry needs no chart template change.
- The bench harness (`components/bench`) has no stub of its own; it measures real backends.

## Proposed changes (minimal, in order)

### In `install.sh`

1. **A profile switch**, `E2E_PROFILE=ci` (or `KEYLESS=1`), read before the preflight. When set:
   - skip the `ANTHROPIC_API_KEY` and `TELEGRAM_BOT_TOKEN` requirements;
   - create `agentgateway-system/anthropic-api-key` with a dummy value only if the chosen values file still names it (better: the CI
     values file does not, see below), and create no `telegram-bridge` Secret;
   - set `SKIP_BRIDGE_IMAGE=1`, `SKIP_SKILL=1`, `SKIP_KB_INGEST=1`, `SKIP_NOTIFY_IMAGE=1`, `SKIP_A2A_GATE_IMAGE=1`, `SKIP_A2A_CALL_IMAGE=1`
     by default.
   Alternative with the same effect and less script logic: make both variables required only when the rendered chart actually
   uses them (derive "needs Anthropic key" from `gateway.models[].authSecret` and "needs bridge token" from `bridge.enabled`).
   This is the cleaner fix, because the preflight then states the real dependency and works for any keyless setup, not only CI.
2. **Apply the stub** when the profile is on: `kubectl apply -f components/runtime/manifests/mock-llm.yaml` after the kagent
   prerequisites (the stub Deployment lives in the `kagent` namespace, which `components/runtime/up.sh` creates). It must be
   promoted from "spike only" to a supported test fixture (move to `components/testing/` or keep and document), and pin its
   image (it runs a stock Python image; check the digest).
3. **`kubectl-ate` download** (`components/runtime/up.sh`, release asset with SHA-256 check) and the Helm charts for kagent,
   Substrate, agentgateway, ToolHive come from `ghcr.io`/`oci://`/GitHub. These are anonymous pulls; the risk is rate limits and
   flakiness, not credentials.
4. Optional: `AGENT_WAIT` default 900 s is right for a cold runner; keep it.

### In the chart (a values file, no template change expected)

`components/ci/values.yaml` (new), passed with `VALUES_FILE`:

```yaml
bridge: {enabled: false}
knowledge: {enabled: false}          # kb + pgvector + local embedding model are the heaviest pieces
observability: {enabled: false}
governance: {a2a: {enabled: false}}
notify: {enabled: false}
schedules: []
channels: {kourier: {enabled: false}}
gateway:
  models:
    - {name: stub, provider: OpenAI, baseURL: "http://mock-llm.kagent.svc.cluster.local:8000/v1"}
agents:
  - name: assistant
    model: stub
    modelConfigName: gateway-stub
    tools: []                        # no vMCP/ToolHive in the minimal profile
```

Open questions to settle by running it once: whether `mcp.enabled: false` (no ToolHive, no vMCP) is accepted by the chart and
the agent template; whether the agent needs a `skills` entry (the default `openagentix-canary` skill needs the in-cluster
registry push, hence `SKIP_SKILL=1` and no skill in the CI values); whether the agent's virtual key (`assistant-virtual-key`)
is created for a stub model the same way.

## Minimal profile and what it costs

| Part | In the minimal profile | Why |
|---|---|---|
| kind (1 node, `kindest/node:v1.37.0`) | yes | `hack/kind-config.yaml` |
| kagent + Substrate (+ its PostgreSQL) | yes | the runtime under test |
| agentgateway + Gateway API CRDs | yes | the stub is routed through it, the same path as production |
| ToolHive + vMCP | no | tools are not needed for a message round trip |
| kb, pgvector, embedding model | no | biggest image pulls and memory; covered by the integration job |
| bridge, notify, a2a-gate, Kourier, observability | no | each has its own offline tests |

Cold install of the full profile took about 23 minutes locally. The minimal profile removes ToolHive, the knowledge stack
(PostgreSQL, embedding model), and the image builds, which were the longest waits; an estimate of **8-12 minutes** on a 4 vCPU runner
is a guess and has to be measured. Memory: kagent controller, Substrate, a PostgreSQL, agentgateway and a gVisor-backed sandbox
for the agent should fit in 16 GB, but the gVisor/proxy_arp sysctls (`install.sh` step 1) and Substrate's sandbox runtime on a
GitHub runner (nested kernel features) are **unverified** and the most likely blocker.

## Smoke test (to write)

After `install.sh`: port-forward the kagent controller (`:8083`) and send one A2A `message/send` to
`/api/a2a/kagent/assistant/` (same call `tools/a2a-call` makes), assert the reply contains `MOCK-LLM reply`. That proves
kagent, Substrate sandbox, gateway routing, virtual-key auth and the A2A path with no model call and no secret. A second check:
`helm upgrade` with the same values is a no-op (idempotency, which `install.sh` promises).

## CI wiring when ready

- Runner `ubuntu-24.04`, `timeout-minutes: 45`, nightly plus manual; not on every pull request until the run is stable and under
  about 15 minutes. Then add it as a path-filtered job (`install.sh`, `charts/**`, `components/**`) to `ci.yml`.
- Cache: docker images are not cached between runs on hosted runners; pre-pulling in parallel (`docker pull` of the images
  listed by `helm template`) before `install.sh` saves a few minutes.
- On failure upload `kubectl get pods -A`, events and `kind export logs` as an artifact (the scaffold prints pods and events).
