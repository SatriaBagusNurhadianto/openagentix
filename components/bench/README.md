# Runtime speed benchmark: kagent versus Google AX

Measures how long one turn takes on each runtime, on the single-node kind cluster, with the same model (`claude-haiku-5-5`), the same
prompts and the same OSV tool. It extends the earlier comparison (`spikes/ax/compare/RESULTS.md`). Nothing here is installed by
`install.sh`; the AX side is temporary and must be removed again (see Remove AX).

## What is measured

Every request is one A2A `SendMessage` from the client pod `cmp`; the number is client wall-clock seconds. Prompt groups (the checks are
string checks, not quality judgements):

| group | prompt | correct when |
|---|---|---|
| a | capital of Kenya, one word | answer has "nairobi" |
| b | lodash 4.17.20 vulnerabilities (the OSV tool) | says 4 and gives an advisory id |
| c | the openagentix canary phrase (the skill) | contains `PELICAN-7731-OPENAGENTIX-SKILL-LOADED` |
| d | three turns on one conversation (colour and number, 6x7, recall both) | turn 3 has 7319 and teal; the cell is the sum of the 3 turns |
| e | group a's question, then a second plain question (Uganda) in the same conversation | turn 2 has "kampala"; the cell is turn 2 only (warm-conversation latency) |

Backends:

| name | what it is |
|---|---|
| `kagent` | the `assistant` agent as deployed: memory ON, knowledge, notify, skill, 5 vMCP tools |
| `kagent-nomem` | `bench-nomem`: identical to `assistant`, memory OFF |
| `kagent-lean` | `bench-lean`: memory, knowledge and notify OFF, one OSV tool (`osv_query_vulnerability`), the canary skill, base system prompt only |
| `ax-claude` | AX, variant A': Claude Code (`claude --print`) under AX's default runner, behind the `ax-adapter` |
| `ax-api` | AX, variant B: the 200-line Messages-API loop under the default runner, behind `ax-adapter-b` |

Each CSV row (`out/<timestamp>/results-<phase>.csv`) has: backend, group, run, turn, `wall_s`, `http_status`, task `state`, `throttled`, `correct`,
tokens as the backend reports them, `model_calls`, the A2A `context_id`, the W3C `trace_id` the harness sent, `load1` (node load average at the start of the
run), `gap_s` and a UTC timestamp (the end of the request, ms). HTTP 429 and the gateway's "429 Too Many Requests" answer are recorded as throttled
and never retried. kagent requests carry `X-User-Id: bench:<backend>:<run id>-<group><run>`, distinct per run, so no memory or session carries over.

## Like for like, and where it is not

Same: model, prompts, system prompt text (the AX Workspaces carry the kagent `assistant` base prompt verbatim), the canary skill content,
the OSV MCP server (one `osv` ToolHive server behind both vMCPs), the client, the node, the sequential interleaved order (backends alternate, the
order rotates per run).

Not the same, and it matters when reading the numbers:

- **Model path.** kagent calls the model through OUR gateway: OpenAI-compatible request with the agent's virtual key, translated to Anthropic by
  agentgateway, with a token budget (1,000,000 tokens per hour per key by default) and retry. AX does NOT use our gateway: the sandbox calls
  `api.anthropic.com` directly (Messages API) and the AX egress Gateway `anthropic-gw` replaces `x-api-key` outside the sandbox from the Secret
  `ax-system/anthropic-creds`. No budget, no translation, no gateway hop. Claude Code (`ax-claude`) is a different agent loop again.
- **Tool path.** kagent: sandbox, egress proxy, the real vMCP `tools` (validates the actor JWT, Cedar permit-all, four backends, tools filtered per
  agent), the osv ToolHive proxy, osv. AX: sandbox, egress proxy, a bench-only second vMCP `bench-osv` (no JWT check, one backend), the SAME osv proxy.
  The AX agents therefore see all three OSV tools (kagent-lean sees one) and no `execute_tool_script`, `fetch` or knowledge tools: smaller prompts than the
  kagent assistant's. Token counts mix this with runtime effects.
- **Memory.** `kagent` runs a memory search (embeddings through the gateway, then a search call) before every model call and may save at the end of a session;
  `kagent-nomem` and `kagent-lean` and both AX variants do none. Conversation history for AX is replayed by the adapter (last 8 turns in memory), kagent
  keeps it behind the `contextId`.
- **Cold versus warm.** A kagent request without a `contextId` is a new session: a new Substrate actor is created from the agent's golden snapshot and
  resumed (the trace shows it as the time before the agent's `a2a.request`, 9.4 s in the one smoke trace). A second turn on the same `contextId` reuses that actor if it
  is still running. An AX turn is always a new Task (CreateTask, ResumeTask, run, delete) restored from the Workspace snapshot; the snapshot is built by the
  first Task of a Workspace (one-off, not in the timed groups: the `--warmup` request and the earlier use pay it).
- **Adapter.** AX has no request/response path; `ax-adapter` is 880 lines of Go that turns A2A into Task create/poll/delete (poll interval 500 ms, so up to
  0.5 s is quantisation) and caps concurrent Tasks at 2.
- **Capacity.** One Substrate worker per pool (`kagent-default`, `ax-pool`), and AX tasks can be scheduled on kagent's worker (NOTES.md failure 2).
- Token numbers are what each backend reports; they are not one quantity (see RESULTS.md "Not comparable").

## Install (done once; the AX part is temporary)

Prerequisites: kind cluster `openagentix` running, `kubectl`, `helm`, `docker`, Go (the toolchain downloads 1.27.1), `yq` (the python one: `yq -y`), `jq`.
The ax-registry volume `ax-registry-data` must still hold the images (`ax-server`, `ax-runner`, `ax-assistant`, `ax-assistant-api`); if not, run
`spikes/ax/up.sh` without `SKIP_BUILD=1` and rebuild the agent images (`spikes/ax/agent/Dockerfile`, `spikes/ax/agent/api/Dockerfile`, push to `localhost:5001`).

```sh
S=<scratch dir>; export PATH=$HOME/.local/bin:$PATH      # a real kubectl first on PATH
# 1. AX source copy (never the checkout) and the ax CLI
git clone /Users/gshah/work/agentix/ax $S/ax-src && git -C $S/ax-src checkout --detach 0b44e971e802554e000bb7ca047bc9cef87b6691
( cd $S/ax-src && GOWORK=off go build -trimpath -o bin/ax ./cmd/ax )
# 2. AX server, Postgres, worker pool, registry, Substrate authz overlay (reuses the images in the volume)
SKIP_BUILD=1 SKIP_TASKS=1 AX_SRC=$S/ax-src OUT_DIR=$S/out RENDER_DIR=$S/render spikes/ax/up.sh
# 3. the Anthropic key as an AX credential Secret (read from your shell profile, never printed)
umask 077; zsh -ic "printf %s \"\$ANTHROPIC_API_KEY\" > $S/.k" >/dev/null
kubectl -n ax-system create secret generic anthropic-creds --from-file=api-key=$S/.k && rm -f $S/.k
kubectl -n ax-system label secret anthropic-creds ax.io/credential=true
# 4. the bench-only vMCP (see "The vMCP token problem")
kubectl apply -f components/bench/ax-bench-vmcp.yaml
# 5. Gateway + the two Workspaces (digests of the images in the registry; the node IP is derived, never typed)
NODE_IP=$(docker inspect openagentix-control-plane -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}')
dig() { docker exec openagentix-control-plane curl -sI -H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json' localhost:5000/v2/$1/manifests/dev | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}' | tr -d '\r'; }
export VMCP_HOST=vmcp-bench-osv.mcp-tools.svc.cluster.local AX=$S/ax-src/bin/ax CTX=kind-openagentix
spikes/ax/agent/apply.sh assistant-ws-v1  $NODE_IP:5000/ax-assistant@$(dig ax-assistant)
spikes/ax/agent/apply.sh assistant-ws-api $NODE_IP:5000/ax-assistant-api@$(dig ax-assistant-api)
# 6. the two adapters (the image is built from the prebuilt binary; the module replaces github.com/google/ax with the checkout, read only)
make -C bridges/ax-adapter build && docker build --platform linux/arm64 -t openagentix/ax-adapter:dev bridges/ax-adapter && kind load docker-image openagentix/ax-adapter:dev --name openagentix
kubectl apply -f bridges/ax-adapter/deploy/deployment.yaml -f bridges/ax-adapter/deploy/deployment-b.yaml
# 7. the client pod (see "How the client reaches kagent")
kubectl apply -f components/bench/cmp.yaml
```

For `kagent-nomem` and `kagent-lean`, install the bench agents later with the file `components/bench/bench-values.yaml` (its header lists the whole `VALUES_FILE`
order). `components/bench/check-values.sh` proves the rendered `assistant` does not change. Wait for both new agents to be Ready (1 to 2 minutes each) before running.

### How the client reaches kagent

The chart's NetworkPolicy `openagentix-kagent-controller` admits port 8083 only from the kagent, Substrate egress, the Telegram bridge (pods labelled `app: telegram-bridge`
in `openagentix-gateway`), Kourier, schedules and `networkPolicy.extraA2ACallers` (empty). Without editing the chart, the client pod `cmp` runs in `openagentix-gateway` and
carries the label `app: telegram-bridge`. This does not make the Deployment adopt it (its ReplicaSet selector also has a `pod-template-hash`) and no Service selects that label. It is
temporary and has `app.kubernetes.io/part-of: bench`. (The previous comparison ran `cmp` in `ax-compare`; that namespace is not admitted any more.)

### The vMCP token problem for AX

The vMCP `tools` requires a Substrate actor JWT with audience `openagentix-vmcp`; kagent gets it because the egress proxy injects it (`RemoteMCPServer.spec.actorJwt`).
AX sandboxes are Substrate actors too, and AX's Gateway rules can carry an `actorJwt` credential (the schema is in AX's `types_test.go`), so in principle the same injection works.
It is refused: `ax apply` answers `spec.egress[1].http.effects.replaceHeaders: not allowed on an http rule: the credential would be sent in cleartext`
(`internal/apivalidation/gateway.go`), and the vMCP serves plain HTTP on 4483 (an `https` rule needs a TLS upstream). Substrate itself accepts the rule on `http` (AX uses it
for its own runner calls), but only AX's validator is in the way, and it cannot be bypassed without patching AX. Whether a sandbox can read a token itself was not checked (AX documents `/run/ate/actor-uid` as the identity file; no JWT file is documented), so the egress rule is the only route found.

Bench-only arrangement (documented, removable): `ax-bench-vmcp.yaml` adds a second vMCP `bench-osv` in `mcp-tools` that accepts anonymous callers and exposes only the OSV
backend (an `MCPServerEntry` pointing to the existing osv proxy), plus a NetworkPolicy that admits only the Substrate egress proxy. The real vMCP's auth is untouched.
The AX Gateway rule and the Workspace MCP endpoint point to `vmcp-bench-osv` (`VMCP_HOST` in `apply.sh`). Not verified: an AX sandbox calling the real vMCP and being refused (the validator
stops the rule before that), and whether an `https` front for the vMCP would work.

## Run

```sh
PHASE=plan components/bench/run.sh        # what will run, with the expected model calls and rough tokens; touches nothing
PHASE=a    components/bench/run.sh        # then b, c, d1, d2, e, concurrency, cold-kagent, cold-ax, one at a time (each at most about 9 minutes)
PHASE=report components/bench/run.sh      # summary.md and breakdown.md from all CSVs; no model calls
```

Between phases look at `kubectl get pods -A` (crash loops) and let the node settle: the script itself waits for the node load (`docker exec openagentix-control-plane cat /proc/loadavg`,
first number) to fall below `LOAD_MAX` (3.0) for up to `LOAD_TIMEOUT` (180 s), then starts and records `TIMED_OUT` in `load-waits.log`. Output is in `components/bench/out/<timestamp>/`
(git-ignored): the CSVs, `answers-*.jsonl`, `env.txt`, `harness-*.log`, `summary.md`, `breakdown.md`. Every phase can be repeated; a repeat of a phase overwrites that phase's files.

Cold start, defined per runtime:
- kagent: *cold* is turn 1 of a new session (new actor from the golden snapshot, then resume); *warm* is turn 2 on the same `contextId` right away; *resumed after idle* is turn 2 after
  `IDLE_GAP` seconds (120). The trace's `pre-dispatch` column shows where the time goes. It cannot be forced safely to the true "agent golden snapshot not built" state: that needs a ModelConfig edit
  (re-snapshot) or a controller restart, which disturbs the platform. What it measures is new-session cost and idle-resume cost, nothing about a first-ever build.
- AX: *cold* is the first Task of a brand-new Workspace (a nonce file in the Workspace setup changes its snapshot tag, so Substrate builds the snapshot in a setup actor); *warm* is the second.
  `cold-ax` creates the Workspace and a throw-away adapter per cycle (2 cycles per variant) and deletes both afterwards. It needs `AX_BIN=$S/ax-src/bin/ax`.

Harness alone, for a one-off: `kubectl -n openagentix-gateway exec -i cmp -- python3 - --backends 'a=URL,b=URL' --groups a,e --runs 3 --warmup --concurrency 4 ... < spikes/ax/compare/a2a_compare.py`
(`--header 'kagent:X-User-Id:bench:{backend}:{rid}'`, `--turn-gap`, `--load`, `--load-cmd`, `--no-header`, `--run-id`; see its docstring). Tests: `cd spikes/ax/compare && python3 -m unittest test_a2a_compare test_summarize test_bench`
and `cd components/bench && python3 -m unittest test_trace_breakdown`.

## Reading the breakdown

`trace-breakdown.py` (run by `PHASE=report`) splits each turn:
- kagent, from Jaeger spans of the row's `trace_id`: total (the controller's `POST /agents/` span), pre-dispatch (controller work and the actor resume, before the agent's `a2a.request`),
  model (gateway `POST /v1/chat/completions`), embeddings (`POST /v1/embeddings`, the memory recall), tools (vMCP `tools/call`; if a trace has none, the agent's `execute_tool` spans),
  remainder, prompt tokens per model call, and whether any cache-read tokens were reported. The tool split was not exercised on a real tool trace yet (only the plain question was).
- AX, from the egress proxy log (one line per outbound request of a sandbox, joined by the actor name `turn-<id>` and the request's time window) and the ax-server log: request start to
  "task successfully resumed", model time (sum of the `POST api.anthropic.com /v1/messages` durations and their count), tool time (POSTs to the vMCP, including the MCP handshake, so it is not zero on a plain
  question), other egress (Claude Code's own settings calls), remainder. It cannot split sandbox start-up, the agent process's own time and the gaps between calls (all remainder), nor show prompt tokens or
  cache use per call (no content). Concurrent requests cannot be joined by time and are skipped.

## Footprint of the AX side (measured 2026-11-23)

7 pods (46 to 53 in the cluster), memory requests 1,104 Mi, measured working set about 135 MB idle: `ax-pool` worker 512 Mi request (19.6 MB), `ax-postgres` 256 Mi (37.6 MB, plus a 10 GiB PVC),
`ax-server` 128 Mi (9.6 MB), `bench-osv` vMCP 128 Mi (about 35-57 MB), two adapters 24 Mi each (4.8 and 11.4 MB), `cmp` 32 Mi (5.5 MB), plus the `ax-registry` container (10.5 MiB) outside Kubernetes. The
node container went from 3.70 to about 4.06 GiB (includes page cache for the images). Running tasks add sandbox memory (a Workspace limit of 768 Mi each); the kagent bench agents add one golden snapshot each.

## Remove AX again (it must not stay on the full node)

```sh
S=<the scratch dir used at install>; export PATH=$HOME/.local/bin:$PATH
kubectl delete -f components/bench/cmp.yaml
kubectl delete -f bridges/ax-adapter/deploy/deployment.yaml -f bridges/ax-adapter/deploy/deployment-b.yaml
kubectl -n openagentix-gateway delete deploy,svc ax-adapter-cold --ignore-not-found
for w in assistant-ws-v1 assistant-ws-api; do $S/ax-src/bin/ax --context kind-openagentix delete workspace $w; done
$S/ax-src/bin/ax --context kind-openagentix delete gateway anthropic-gw
kubectl delete -f components/bench/ax-bench-vmcp.yaml
kubectl delete namespace ax-system                       # server, Postgres and its PVC, worker pool, the anthropic-creds Secret
helm rollback substrate 1 -n ate-system --wait --timeout 8m    # drops the three additive grants (revision 1 = the original values); rolls ate-api-server and the credential provider
docker rm -f ax-registry                                 # the volume ax-registry-data stays (275 MB); `docker volume rm ax-registry-data` removes it
docker exec openagentix-control-plane crictl rmi openagentix/ax-adapter:dev 2>/dev/null || true
docker rmi openagentix/ax-adapter:dev 2>/dev/null || true
```
Then check: `kubectl get pods -A` (all Running), `kubectl -n kagent get agents` (Ready), `helm get values substrate -n ate-system` (only the original keys). The bench kagent agents are removed
by installing without `bench-values.yaml`. Memory entries the `assistant` saved for the `bench:*` user ids stay in the memory store (30-day TTL).
