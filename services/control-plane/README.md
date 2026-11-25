# control-plane

The controller of the agent builder (ADR 0007, `docs/decisions/0007-control-plane-and-agent-builder.md`). It watches
`AgentDefinition` resources (`openagentix.io/v1alpha1`, short name `oad`) and turns each one into what an agent needs: the
runtime objects (for kagent: ModelConfig, Harness, AgentTemplate, an optional EgressPolicy and the Agent), a gateway virtual
key, and the key's registration at the model gateway. It writes the result back as conditions and facts in `status`.

It is off by default (chart value `controlPlane.enabled`, added by a later slice). It has no dependency outside the Go
standard library and no client library: `internal/kube` is a small REST client.

## What it does, per definition

1. **Adds the finalizer** `openagentix.io/gateway-key`.
2. **Validates** the definition against the platform catalog (what exists) and the policy (what is allowed), see "Refusals".
   A refusal sets `Accepted=False` and puts one sentence per rule into `status.violations`. Nothing else is written.
3. **Checks names.** If an object with one of the target names exists and does not carry this definition's labels, the
   condition `Applied=False`, reason `NameConflict`, names it and **nothing is written** (no key, no object).
4. **Makes the key.** `vk-` plus 16 random bytes in hex, in memory only. `POST` the Secret `<name>-virtual-key` (data key
   `OPENAI_API_KEY`, `immutable: true`, labels, owner reference). 201: write the hash. 409 and the gateway has an entry for
   the user: nothing to do. 409 and no entry: delete its own Secret, create a new one, write the hash. The controller never
   GETs or LISTs a Secret.
5. **Applies** the runtime's objects with server-side apply (field manager `openagentix-control-plane`, force), each with the
   labels `app.kubernetes.io/managed-by: openagentix-control-plane` and `openagentix.io/agent-definition: <name>` and an
   owner reference (`controller: true`) to the definition.
6. **Prunes** owned objects (found by label) that are no longer wanted.
7. **Reports**: conditions `Accepted`, `KeyReady`, `Applied`, `Ready`; `status.agentRef` (one generic reference to the
   runtime object), `gatewayUserId`, `toolServer`, `effectiveTools`, `effectiveEgress`, `a2aUrl` (the gate's route; empty when
   the gate is off), `violations`.
8. **On delete**: removes the owned objects, removes the key hash from the gateway, then the finalizer. The key Secret goes
   by its owner reference.

The loop is level-triggered. It lists, reconciles every definition one at a time (oldest first), then watches from the
list's resourceVersion. A new definition, a changed spec (generation) or a requested deletion ends the watch and starts
the next pass; so does the resync timer (60 s), a failure, or `410 Gone` (it lists again). Its own status and finalizer
writes do not start a pass.

A definition that was accepted and later edited into something refused keeps its old objects (nothing is deleted on a
refusal); delete the definition to remove the agent.

## Structure

```
cmd/control-plane          env, wiring, /healthz and /readyz, graceful shutdown
internal/api               AgentDefinition types, status, conditions
internal/catalog           loads catalog.json and policy.json; the types of both
internal/policy            pure validation: definition + catalog + policy -> violations
internal/plan              definition + catalog -> Plan (final prompt, tools, skills, egress, memory), no upstream words
internal/core              the loop; declares Runtime, ModelGateway, Kube; imports no adapter (a test enforces it)
internal/adapters/kagent        Runtime: Plan -> the five kagent objects, readiness, call endpoint
internal/adapters/agentgateway  ModelGateway: entries in the ConfigMap llm-api-keys-control-plane
internal/adapters/toolhive      Tools: profile -> tool server and tool names, read from the catalog
internal/keys              mint, hash, and the create/409 logic
internal/kube              REST client (get, list, watch, apply, merge patch, create, update, delete)
internal/kubetest          fake API server for tests
crd/agentdefinition.yaml   the CustomResourceDefinition
testdata/                  example catalog.json and policy.json, golden Helm output, regen-golden.sh
```

The `Runtime` adapter produces the same objects as `charts/openagentix/templates/kagent.yaml`; the parity tests
(`internal/adapters/kagent`) compare them field by field with `helm template` output for two demo agents (`profile-demo`,
`assistant`). The ignored paths are the four Helm-only labels, listed in the test. `testdata/regen-golden.sh` regenerates
the golden files (needs helm, yq, jq).

## Settings (environment)

| Variable | Default | Meaning |
|---|---|---|
| `CONTROL_PLANE_CATALOG_FILE` | required | path of the catalog JSON |
| `CONTROL_PLANE_POLICY_FILE` | required | path of the policy JSON |
| `CONTROL_PLANE_HEALTH_ADDR` | `:8081` | listen address of `/healthz` (always 200) and `/readyz` (200 after the first full pass) |
| `CONTROL_PLANE_RESYNC` | `60s` | longest time between full passes (at least 1 s) |
| `CONTROL_PLANE_BACKOFF` | `10s` | retry interval while a definition is not finished (key or apply failed, runtime not ready) |
| `CONTROL_PLANE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

It uses the pod's ServiceAccount token (read again on every request) and CA. The catalog and the policy are read once at
start; the chart rolls the pod when either changes (checksum annotation). Only the namespace `namespaces.agents` of the
catalog is reconciled.

Logs are JSON on stderr: agent name, object names, outcome. A log line, a condition message or an error never contains a
key, and never an API error body: errors carry the HTTP status and the API's reason word only.

## The catalog (`CONTROL_PLANE_CATALOG_FILE`)

What exists on the platform. The chart renders it from the same values that render chart agents. No secret in it. Unknown
fields are refused. `testdata/catalog.json` is a full example for the demo values.

| Field | Type | Meaning |
|---|---|---|
| `adapters.runtime` | string | active runtime adapter; this build supports `kagent` |
| `adapters.modelGateway` | string | active gateway adapter; `agentgateway` |
| `adapters.tools` | string | active tools adapter; `toolhive` |
| `namespaces.agents` | string | namespace of definitions and of every object written (`kagent.namespace`) |
| `namespaces.gateway` | string | namespace of the gateway, where `llm-api-keys-control-plane` lives |
| `namespaces.tools` | string | the tool servers' namespace (`mcp.namespace`); an egress origin inside it is refused |
| `gatewayBaseUrl` | string | the gateway's OpenAI base URL an agent's model config points at (as `oa.gatewayBaseUrl`) |
| `models` | [string] | gateway model names (`gateway.models[].name`) |
| `toolServer` | string | name of the shared tool server (RemoteMCPServer `kagent.remoteMCPServer.name`), used only when an agent has tools without a profile (the policy refuses that; kept for the chart-identical output) |
| `profiles[]` | list | tool profiles (`mcp.profiles`) |
| `profiles[].name` | string | profile name |
| `profiles[].toolServer` | string | name of the profile's server object (`<remoteMCPServer.name>-<profile>`) |
| `profiles[].codeMode` | bool | the profile has `execute_tool_script` |
| `profiles[].tools` | [string] | agent-side tool names the profile offers, as the chart's `oa.profileToolNames` (workload prefix applied, `execute_tool_script` last when `codeMode`) |
| `knowledge.available` | bool | `knowledge.enabled` and `mcp.enabled` |
| `knowledge.tools` | [string] | the tools `knowledge: true` adds (`oa.knowledgeTools`: three, six with the graph) |
| `knowledge.prompt` | string | the addition to the system prompt, tool names already substituted (`oa.knowledgeAgentPrompt`) |
| `notify.available` | bool | `notify.enabled` and `mcp.enabled` |
| `notify.tools` / `notify.prompt` | | as above (`oa.notifyTools`, `oa.notifyAgentPrompt`) |
| `memory.available` | bool | the memory stack is installed |
| `memory.expiryEnabled` | bool | `memory.expiry.enabled`: the job that enforces the time limit |
| `memory.defaultTtlDays` | int | the runtime's default when a definition sets no `ttlDays` (kagent: 15) |
| `memory.modelConfig` | string | name of the shared embedding model config (`memory.modelConfig.name`) |
| `memory.prompt` | string | addition to the system prompt (`memory.agentPrompt`) |
| `skills` | map name -> ref | skill name to OCI ref; the ref must end in `@sha256:<64 hex>` (the file is refused otherwise) |
| `harnessImage`, `workerPool`, `snapshotLocation` | string | `kagent.harnessImage`, `kagent.workerPool`, `kagent.snapshotLocation` |
| `harnessEnv[]` | list of `{name, value}` | environment of the harness: `OTEL_PROPAGATORS` when observability and baggage are on, else `[]` |
| `gateUrl` | string | base URL of the A2A gate (`oa.a2aGateUrl`); empty when the gate is off |
| `reservedNames` | [string] | names a definition may not use: every chart agent plus `kb`, `kb-extract`, `memory` |

Derived names (not in the file): ModelConfig `gateway-<name>`, Harness `<name>-harness`, AgentTemplate `<name>-template`,
EgressPolicy `<name>-egress`, Agent `<name>`, Secret `<name>-virtual-key`, gateway user id `<name>`.

## The policy (`CONTROL_PLANE_POLICY_FILE`)

What the operator allows (chart values `controlPlane.policy`). An allow-list that is empty allows nothing; the single entry
`"*"` allows everything the catalog has. `testdata/policy.json` is an example.

| Field | Type | Meaning |
|---|---|---|
| `maxAgents` | int >= 0 | number of definitions; the oldest keep their place, newer ones are refused |
| `allowedModels` | [string] | models a definition may name |
| `requireToolProfile` | bool | every definition must name a `toolProfile` |
| `allowedProfiles` | [string] | tool profiles a definition may name |
| `allowKnowledge` | bool | `knowledge: true` is allowed |
| `allowNotify` | bool | `notify: true` is allowed |
| `allowedSkills` | [string] | skills a definition may name |
| `memory.allowed` | bool | `memory.enabled` is allowed |
| `memory.maxTtlDays` | int >= 1 when memory is allowed | largest `ttlDays` (the catalog default counts when none is set) |
| `memory.requireExpiry` | bool | refuse memory while `catalog.memory.expiryEnabled` is false |
| `egress.allowedOrigins` | [string] | origins (`https://host[:port]`); a host may start with one `*.`, which stands for exactly one label. A definition's origin must equal an entry or match a wildcard entry (scheme and port must be equal). A wildcard origin in a definition matches only an identical entry. Empty: no egress |
| `reservedNames` | [string] | more reserved names, besides the catalog's |

## Refusals

Each is one sentence in `status.violations`, with `Accepted=False` (reason `PolicyViolation`):

- name not a DNS label, longer than 40, reserved (catalog or policy), or more definitions than `maxAgents`;
- description over 256 characters; instructions empty, over 16 KiB, or shaped like a secret (the ten patterns of
  `charts/openagentix/files/memory-expiry.sh`, same names; the sentence names the pattern, never the text);
- a model not in the catalog or not allowed; a profile not in the catalog or not allowed; a skill not in the catalog or not allowed;
- tools without a `toolProfile` (the shared tool server filters on the client side only), no profile when the policy requires
  one, a tool the profile does not offer (and the chart's special message for `execute_tool_script` with `codeMode` off), a tool listed twice;
- `knowledge` or `notify` when the policy does not allow it, when the service is off, **without a profile**, or when the profile does not offer their tools;
- memory when the policy does not allow it, the stack is not installed, the expiry job is off (with `requireExpiry`), or `ttlDays` is above the maximum or below 1;
- an egress origin that is malformed, listed twice, inside the tools namespace, not matched by the policy, or more than 64 origins.

## RBAC it needs

Namespaced Roles only, no ClusterRole, no wildcard (the chart renders them in a later slice):

| Namespace | Resource | Verbs |
|---|---|---|
| agents (`kagent`) | `agentdefinitions` | get, list, watch, update |
| agents | `agentdefinitions/status` | patch |
| agents | `modelconfigs`, `harnesses`, `agenttemplates`, `egresspolicies`, `agents` (`api.kagent.dev`) | get, list, watch, create, patch, delete |
| agents | `secrets` | **create, delete** only |
| gateway | `configmaps`, resourceName `llm-api-keys-control-plane` | get, patch, update |

`update` on definitions is the finalizer (a full PUT); `patch` on the kagent kinds is server-side apply. It creates no
Events (it has no `events` right): the conditions are the record.

## What it never does

- Read, list or watch a Secret; print, log, put into status or return a key (the key is in the body of one `POST`).
- Touch the installer's ConfigMap `llm-api-keys`, a chart agent's objects, or any object it does not own (it stops on a name conflict).
- Write outside the agents namespace and the one gateway ConfigMap; use a ClusterRole.
- Accept a field that names a model config, image, tool server, egress policy, namespace, caller, gateway user or budget (the CRD has none).
- Write gate rules: nobody may call a built agent until an operator adds a caller rule.
- Copy an API error message into a log line or a condition.
- Delete an agent because a definition became refused or the policy changed.

## Tests

`make test` (`go test ./... -race -count=1`). They need nothing but Go (the CRD test also uses `yq` and skips without it):
the Kubernetes API is `internal/kubetest`, a fake that records every request so tests can assert what was sent and what was not.

- policy: one case per refusal, and accepted cases;
- kube and core: list and watch resume, 410 Gone (HTTP and event form) and re-list, server-side apply bodies, status patch, finalizer, pruning, name conflicts, serialized reconciles;
- keys: 201, 409 with the hash present, 409 without, never a GET on Secrets;
- canary: a planted key value appears in no log line, status, error string or request body except the one Secret create;
- parity with `helm template` (golden files); the core imports no adapter package.

`make docker` builds the image (distroless, non-root, port 8081 for the probes).
