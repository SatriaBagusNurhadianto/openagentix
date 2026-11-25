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
3. **Checks names.** An object is *owned* by a definition only when it carries both labels
   (`app.kubernetes.io/managed-by: openagentix-control-plane`, `openagentix.io/agent-definition: <name>`) **and** an owner
   reference to this very definition (kind `AgentDefinition`, its UID, `controller: true`); labels alone prove nothing. If an
   object with one of the target names exists and is not owned, the condition `Applied=False`, reason `NameConflict`, names
   it and **nothing is written** (no key, no object). A conflict is terminal until the definition's generation changes (the
   resync pass looks again, but it is not retried in between). A 404 on the check (the kind is not installed) is "does not exist".
4. **Makes the key.** `vk-` plus 16 random bytes in hex, in memory only, and only when needed. The Secret is named
   **`<name>-virtual-key-<first 8 hex characters of the definition's UID, dashes removed>`** (data key `OPENAI_API_KEY`,
   `immutable: true`, labels, owner reference, annotation `openagentix.io/key-fingerprint`). Because the name carries the UID,
   a Secret of that name was made for this very definition, so the controller may replace it without reading it; a foreign
   Secret called `<name>-virtual-key` is never touched. The controller reads the gateway's key ConfigMap once per pass and
   compares the hash registered for the user with the fingerprint it recorded in `status.keyFingerprint` (the first 16 hex
   characters of the hash, which is public in that ConfigMap):
   - registered hash present and equal to the recorded fingerprint: nothing to do, no mint, no request;
   - registered hash present but not the recorded one (an older entry was restored): the Secret is deleted, a new key is
     created and its hash written (the fingerprint on the runtime objects changes with it);
   - nothing recorded or no entry: `POST` the Secret. 201: write the hash. 409 and an entry exists: adopt it. 409 and no
     entry: delete the Secret, create a new one, write the hash.
   The controller never GETs or LISTs a Secret. Consequence: a Secret deleted by hand is not noticed until the hash or the
   definition changes.
5. **Applies** the runtime's objects with server-side apply (field manager `openagentix-control-plane`, force), each with the
   two labels and the owner reference above. The kagent adapter also puts the annotation `openagentix.io/key-fingerprint` on
   the Harness and the Agent so that replacing the key changes them; whether kagent recreates the agent's pod on that
   annotation is **not verified on a live cluster**.
6. **Prunes** owned objects (labels and owner reference) that are no longer wanted. A kind the cluster does not know (404 on
   the list, for example a CRD that is not installed) has nothing to prune; it never blocks a pass or a deletion.
7. **Reports**: conditions `Accepted`, `KeyReady`, `Applied`, `Ready` (and `Synced`, see below); `status.agentRef` (one generic
   reference to the runtime object), `gatewayUserId`, `toolServer`, `effectiveTools`, `effectiveEgress`, `a2aUrl` (the gate's
   route; empty when the gate is off), `violations`, and what the controller remembers: `keySecret`, `keyFingerprint`,
   `appliedGeneration` (generation of the spec whose objects exist, 0 when none), `appliedSpec` (that spec as JSON text).
   `observedGeneration` is the newest generation the controller has looked at; each condition's `observedGeneration` is the
   generation that condition describes. Reasons are cut to 64 and messages to 1024 characters (the CRD limits), on a
   character boundary. A status the API server refuses as invalid (422) is logged and not retried.
8. **On delete**: removes the owned objects, removes the key hash from the gateway, then the finalizer. The key Secret goes
   by its owner reference.
9. **Sweeps** every pass: an entry in the controller's key ConfigMap whose user is not a listed definition is removed (a
   definition created in the same pass is in the list, so it is never swept; a failed or partial list sweeps nothing).

The loop is level-triggered. It lists (in pages of 100, at most 10 000 items), reconciles every definition one at a time,
then watches from the list's resourceVersion. A new definition, a changed spec (generation) or a requested deletion ends
the watch and starts the next pass; so does the resync timer (60 s), a retry that is due, or `410 Gone` (it lists again).
Its own status and finalizer writes do not start a pass. Passes start at least `CONTROL_PLANE_MIN_INTERVAL` (1 s) apart. The
watch has an open deadline (30 s) and the resync timer runs while it is being opened, so a hung API connection ends in a new
pass. One object that cannot be decoded, or that panics, is skipped and logged; the others are reconciled.

Order and limit: definitions that are accepted now come first, then the oldest (creation time, then name). `maxAgents`
counts accepted definitions only, so a refused definition holds no place, and a definition that is fixed later never
displaces one that already holds a place. When the limit is lowered below the number accepted, the last ones in that order
are refused.

Retries are per definition: a failure retries after `CONTROL_PLANE_BACKOFF` and the delay doubles per failure up to 30 times
that value; a runtime that is not ready yet is retried at the base interval; a new generation resets it. The other
definitions are not held back.

**Transient read errors.** When a read fails (the name check, the key registrations, the runtime's readiness) for a
definition that runs, `Applied`, `Ready` and the facts keep their last known state and a separate condition `Synced=False`
(reason `TransientError`, message the error) says the pass could not finish. It turns `True` when a pass succeeds.

## When a definition that runs is refused

Status records what is applied (`appliedGeneration`, `appliedSpec`). When a definition that was accepted is refused later:

- **The author edited it** into something refused, and the previously applied spec still passes the current catalog and
  policy: that version keeps running. `Accepted=False` (reason `PolicyViolation`, violations listed), `Applied=True` with
  reason `PreviousGenerationRunning`, `Ready` read from the runtime for the running version; `a2aUrl`, `effectiveTools` and
  the rest keep describing what runs; the `Applied` and `Ready` conditions carry the running generation in their
  `observedGeneration`.
- **The catalog or the policy changed** (the spec is the one that was applied), **or** the author edited it and the
  previously applied spec no longer passes, **or** what was applied is unknown (a status written by an older controller):
  the agent's objects are removed (all owned kinds, as on delete) and its key hash is unregistered; the definition and its
  status stay. `Applied=False` reason `PolicyRemoved`, `Ready=False` reason `NotAccepted`, and `agentRef`, `a2aUrl`,
  `toolServer`, `effectiveTools`, `effectiveEgress`, `appliedSpec`, `appliedGeneration` and the key fields are cleared. The
  key Secret stays until the definition goes (its owner reference); when the definition is accepted again a new key replaces it.
- Nothing ran before: only the status changes.

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
| `CONTROL_PLANE_BACKOFF` | `10s` | per-definition retry delay while it is not finished (key or apply failed, runtime not ready); doubles on failures up to 30 times |
| `CONTROL_PLANE_MIN_INTERVAL` | `1s` | shortest time between the starts of two passes, however many events arrive (new, optional; at least 1 ms) |
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
EgressPolicy `<name>-egress`, Agent `<name>`, Secret `<name>-virtual-key-<8 hex of the UID>` (at most 40 + 13 + 8 characters;
the UID is a UUID, dashes removed, first 8 characters), gateway user id `<name>`.

## The policy (`CONTROL_PLANE_POLICY_FILE`)

What the operator allows (chart values `controlPlane.policy`). An allow-list that is empty allows nothing; the single entry
`"*"` allows everything the catalog has. `testdata/policy.json` is an example.

| Field | Type | Meaning |
|---|---|---|
| `maxAgents` | int >= 0 | number of accepted definitions; those already accepted keep their place, then the oldest; the rest are refused |
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
- description over 256 characters; instructions empty or over 16 KiB; instructions, description, model, toolProfile, tools,
  skills or egress entries shaped like a secret (the ten patterns of `charts/openagentix/files/memory-expiry.sh`, same names,
  plus `platform_virtual_key`, `vk-` and 32 hex characters; the sentence names the pattern, never the text, and any other
  sentence that quotes a value has secret-shaped parts replaced by `[redacted]`);
- a model not in the catalog or not allowed; a profile not in the catalog or not allowed; a skill not in the catalog or not allowed;
- tools without a `toolProfile` (the shared tool server filters on the client side only), no profile when the policy requires
  one, a tool the profile does not offer (and the chart's special message for `execute_tool_script` with `codeMode` off), a tool listed twice;
- `knowledge` or `notify` when the policy does not allow it, when the service is off, **without a profile**, or when the profile does not offer their tools;
- memory when the policy does not allow it, the stack is not installed, the expiry job is off (with `requireExpiry`), or `ttlDays` is above the maximum or below 1;
- an egress origin that is malformed, listed twice, inside the tools namespace, not matched by the policy, or more than 64
  origins; and, whatever the policy patterns say, one that names a cluster-internal or local host: `.svc`, `.cluster.local`,
  a name without a dot, an IP address, or a name inside the agents or gateway namespace (the tools namespace has its own rule).

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
- Delete an agent just because an edit was refused: it removes one only when the previously applied spec itself no longer passes (see above).

## Tests

`make test` (`go test ./... -race -count=1`). They need nothing but Go (the CRD test also uses `yq` and skips without it):
the Kubernetes API is `internal/kubetest`, a fake that records every request so tests can assert what was sent and what was not. It paginates (`limit`/`continue`) and models server-side apply for the fields a client sends (a field no longer sent is removed); it does not model several field managers, conflicts, list merge keys or defaulting.

- policy: one case per refusal, and accepted cases;
- kube and core: list and watch resume, 410 Gone (HTTP and event form) and re-list, server-side apply bodies, status patch, finalizer, pruning, name conflicts, serialized reconciles;
- keys: 201, 409 with the hash present, 409 without, a restored older hash, no request when the key is in order, never a GET on Secrets;
- canary: a planted key value appears in no log line, status, error string or request body except the one Secret create, over several passes, also with the real adapters (`cmd/control-plane`);
- parity with `helm template` (golden files); the core imports no adapter package.

`make docker` builds the image (distroless, non-root, port 8081 for the probes).
