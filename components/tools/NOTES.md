# components/tools: ToolHive MCP layer + code mode + kagent wiring

Date 2026-11-22, cluster `kind-openagentix` (arm64, k8s v1.37.0), kagent 1.0.1 / Substrate 1.0.1.

## Versions
| Item | Version |
|---|---|
| ToolHive charts `toolhive-operator-crds`, `toolhive-operator` | 0.67.1 (latest GitHub release; matches research) |
| Operator / vMCP / proxyrunner images | `ghcr.io/stacklok/toolhive/{operator,vmcp,proxyrunner}:v0.67.1` (multi-arch, ran on arm64) |
| fetch backend | `ghcr.io/stackloklabs/gofetch/server:2.0.1` (amd64+arm64 verified with `docker manifest inspect`) |
| OSV backend | `ghcr.io/stackloklabs/osv-mcp/server:0.2.1` (amd64+arm64). The repo example uses 0.0.7; tags have no `v` prefix |
| Namespaces | `toolhive-system` (operator), `mcp-tools` (group, servers, vMCP) |

## Layout
- `install.sh` idempotent; `WITH_SKILL=1` also builds/pushes/attaches the skill. Second run verified: all `unchanged`.
- `manifests/10-mcp-layer.yaml` MCPGroup, 2 MCPServers, VirtualMCPServer (code mode). `20-remote-mcp.yaml` RemoteMCPServer. `30-mock-llm-tools.yaml` stub + ModelConfig `tools-stub`. `40-tools-agent.yaml` Harness `tools-harness` + AgentTemplate + Agent `tools-agent`. `50-skill-agent.yaml` skill copy.
- `stub/mock_llm_tools.py` the deterministic LLM stub. `verify/` in-cluster MCP and A2A clients (`run-mcp-check.sh`, `run-a2a.sh`), `verify/out/` raw captured A2A responses. `skills/` example skill + Dockerfile.

## Deviations from docs / research
1. No deviation in the ToolHive install. MCPServer Service is `mcp-<name>-proxy:8080/mcp`; vMCP is `vmcp-tools:4483/mcp` as predicted. The operator also creates a StatefulSet per backend (`fetch-0`, `osv-0`) besides the proxy Deployment.
2. RemoteMCPServer is namespaced to `kagent` (same ns as the Agent; cross-ns needs `allowedNamespaces`). Agent tool binding: `apiGroup` must be OMITTED for `kind: RemoteMCPServer` (admission rejects `kagent.dev`). `mcp.tools` is an explicit allowlist; I listed all 5 names.
3. Tool names after aggregation are prefixed by workload (`fetch_fetch`, `osv_query_vulnerability`) because `conflictResolution: prefix`, `prefixFormat: "{workload}_"`.
4. OSV tool args are `package_name`, `ecosystem`, `version` (not `package`).
5. A first `install.sh` run hit a transient API-server TLS timeout (cluster busy with the other agent's work); rerun was clean.
6. Substrate/network (at the time, before the chart added NetworkPolicies to `mcp-tools`): NO allowance was needed. The only NetworkPolicy on the worker pool is ingress-only; the sandbox reached `vmcp-tools.mcp-tools.svc.cluster.local:4483` with the default egress path, and discovery (controller side) worked too. Not tested: a restrictive EgressPolicy on the Agent.

## What code mode really does (observed)
- With `codeMode.enabled`, `tools/list` returns the 4 backend tools PLUS `execute_tool_script` (5 total). Backend tools stay directly callable (code mode adds, it does not replace).
- Script is Starlark; tools are callable as functions with kwargs (`osv_query_vulnerability(package_name=..)`); `call_tool("orig_name", ...)`, `parallel([lambda: ...])` are built in; `data` param injects top-level variables; `return` yields the result.
- Tool results come back as a dict `{"text": "<raw JSON string>"}`. There is NO `json` module (`undefined: json`), so scripts must use string ops (`split`, `count`) to extract fields. This is the major practical limit: structured post-processing of JSON tool output is awkward. Chain used (`verify/chain.star`) does osv query, extracts the first id by string split, calls `osv_get_vulnerability`, returns a small dict. One agent turn instead of two LLM round trips.
- Limits: `stepLimit` 100000, `parallelMaxConcurrency` 10, `toolCallTimeout` 30s (all configurable). Per upstream docs inner calls are individually authorized (not verified here: the Cedar policy is permit-all).
- Context cost: the `execute_tool_script` description embeds all tool names+summaries, so the schema grows with the tool count.

## Evidence summary
- vMCP Ready, 2 backends healthy; RemoteMCPServer `Accepted: Discovered 5 MCP tools`.
- Agent path: A2A `SendMessage` to `tools-agent` -> stub returns `tool_calls` -> sandbox calls vMCP -> real OSV data -> stub quotes it. Both for `osv_query_vulnerability` and `execute_tool_script` (`verify/out/a2a-plain-tool.json`, `a2a-code-mode.json`). The agent's tool list seen by the LLM = 7 harness built-ins (read_file, write_file, edit_file, list_files, grep_file, bash, ask_user) + the 5 vMCP tools.
- Skill: `verify/out/a2a-skill-bash.json` (bash `ls -la /skills` shows `/skills/openagentix-canary/SKILL.md`) and `a2a-skill-load.json` (`load_skill` returns the instructions with `PELICAN-7731-...`). Stub advertised tools for that agent include `list_skills, load_skill, load_skill_resource`.

## Skills (step 5)
- First attempt worked, no blocker. Skill image: `FROM scratch / COPY openagentix-canary/ /`, built `--platform linux/arm64` (docs show amd64; use the node arch). `docker save` + `crane push --insecure` run inside the node's netns (`--network container:openagentix-control-plane`) into `ax-registry` (host port is not published, host `localhost:5001` was not listening). Reference used: `172.18.0.2:5000/skills/openagentix-canary@sha256:d9ce280e...`. Plain HTTP to the RFC1918 node IP worked for the Substrate pull exactly as in spikes/ax; docs say a `localhost` registry fails and HTTPS is required for general registries.
- Digest is pinned in `50-skill-agent.yaml`; it is environment specific (digest changes if the image/arch/build changes; `install.sh WITH_SKILL=1` rewrites it).
- Only `oci` sources tested (docs say git/bucket leave the agent not-Ready).
- The skill is only proven mounted and loadable by tool call; the stub does not "decide" to use a skill, so model-driven skill selection is not tested.

## Auth (updated 2026-11-22; the original MVP had none)
- Now done in the chart: NetworkPolicies on `mcp-tools` (sources found from the atenet-egress and controller logs), vMCP `incomingAuth: oidc` against the Substrate IdP, and RemoteMCPServer `actorJwt` with audience `openagentix-vmcp`. See README "Security" for the proof and the limits. `verify/run-mcp-check.sh` and `mcp_check.py` call the vMCP without a token, so they now get 401 (and, from outside `mcp-tools`, a timeout); use them with `mcp.virtual.incomingAuth.type=anonymous` or exercise tools through an agent.
- Correction: RemoteMCPServer v1alpha3 in kagent 1.0.1 DOES have `headersFrom`, `tokenPassthrough` and `actorJwt {audiences, expirationSeconds}`. The older note here that it had none was wrong. With `actorJwt`, the controller no longer discovers tools globally (status reason `DiscoveryDisabled`); the agent lists tools at run time.
- Findings: the sandbox's calls reach the vMCP through `atenet-egress` (the vMCP and the controller see that pod's IP, not the agent). The token's `sub` is `actor/kagent/session-<uuid>` and the atespace is `kagent` for every agent, so Cedar cannot tell agents apart. ToolHive's Cedar context exposes the claims as `context["claim_..."]` and the principal as `Client::"<sub>"`; tools are `Tool::"<name>"`, action `Action::"call_tool"`. Tool listing is filtered by the same checks. `execute_tool_script` did not appear in the Cedar log; inner calls from code mode were checked as normal tool calls (4 checks for `osv_query_vulnerability` in one code-mode turn).
- The first JWKS fetch failed with "context deadline exceeded" until `jwksAllowPrivateIP: true` was set (the IdP Service has a private ClusterIP).
- Still true: OSV/fetch call the public internet; no secrets involved. Backends with credentials need MCPExternalAuthConfig (untested). `mock-llm-tools` has a fake API key Secret and no auth.

## Umbrella Helm chart must handle
- Two ToolHive charts (CRDs first, then operator), pinned together; CRDs must be a separate release or `crds/` dir since the CRs depend on them.
- Operator-created Deployments/StatefulSets/Services are named from CR names; the `tools` VirtualMCPServer yields `vmcp-tools` which the RemoteMCPServer URL hard-codes. Template that URL from the same value.
- Ordering: RemoteMCPServer discovery needs vMCP Ready (it retries? not tested; kagent status would show failed discovery). Agent templates reference tool names explicitly, so they must change when backends are added/removed or prefix config changes.
- RemoteMCPServer/Agent live in the `kagent` namespace; MCP layer in its own namespace (FQDN required: sandboxes resolve only `*.svc.cluster.local`).
- Stub (dev-only) and skill registry address (node IP) must be optional values; skill OCI digest must be a build output.
- Images are multi-arch only if you choose them so: verify backend images with `docker manifest inspect` per arch.

## Footprint (idle, crictl working set)
Tool layer: operator 47 MiB, vMCP 21 MiB, fetch proxy 15 + fetch 6 MiB, osv proxy 16 + osv 7 MiB = ~112 MiB across 6 pods (1 + 5). Stub 14 MiB. Node container `docker stats` was 3.29 GiB / 7.74 GiB at measurement, with other agents' work (agentgateway, ax, assistant agent) running concurrently, so it is not attributable to this component. Each added Harness/Agent adds a golden snapshot + sandbox on the shared worker pool (not itemised).

## Unverified
- Cold install on a fresh cluster (only re-run on the existing cluster).
- Anything with a real LLM; model-chosen tool/skill selection; the stub only proves the plumbing.
- Per-inner-call authorization in code mode, the optimizer (`find_tool`/`call_tool`) mode, OIDC auth, MCPToolConfig filtering, HPA/replicas.
- Restrictive EgressPolicy on the agent and whether it would block vMCP.
- RemoteMCPServer behaviour if vMCP is down at creation (retry/backoff).
- Skill from a private/HTTPS registry; git/bucket sources; plugins.
- Memory of sandboxes while executing tool calls.
