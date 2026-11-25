# control-plane

The controller of the agent builder (ADR 0007, `docs/decisions/0007-control-plane-and-agent-builder.md`; the platform-level changes of
ADR 0012 are in). It watches `AgentDefinition` resources (`openagentix.io/v1alpha1`, short name `oad`) and turns each one into what an
agent needs: the runtime objects (for kagent: ModelConfig, Harness, AgentTemplate, an optional EgressPolicy and the Agent), a gateway
virtual key, and the key's registration at the model gateway. It writes the result back as conditions and facts in `status`.

It is off by default (chart value `controlPlane.enabled`). It has no dependency outside the Go standard library and no client library:
`internal/kube` is a small REST client. The platform-wide API rules (one group, versions per kind, deprecation, reserved labels) are in
`docs/api-conventions.md`; the parts that belong to this kind are below.

## The spec

A definition is the whole builder surface. There is deliberately no field for a model config, image, tool server, egress policy,
namespace, caller, gateway user or budget, and no upstream product is named in a field or in a text of the spec.

| Field | Type | Meaning |
|---|---|---|
| `description` | string, at most 256 | one line; not part of the revision |
| `model` | string | a gateway model the policy allows (required) |
| `instructions` | string, 1 to 16 KiB | the system prompt |
| `instructionsFrom` | `{configMap: {name, key}}` | **reserved**: exactly one of `instructions` and `instructionsFrom` must be set (a CEL rule in the CRD and a check in the controller), and this release refuses `instructionsFrom` with "not available yet" |
| `toolProfile`, `tools` | string, [string] | a tool profile of the catalog and the tools of it the agent may use |
| `knowledge` | `{enabled: bool, collections: [string]}` | the knowledge tools and prompt; `collections` is **reserved**: a non-empty list is refused with "selecting collections is not available yet" |
| `notify` | `{enabled: bool, destinations: [string]}` | the delivery tools and prompt; `destinations` is **reserved** in the same way |
| `memory` | `{enabled: bool, ttlDays: int}` | long-term memory; `ttlDays` at least 1, at most the policy maximum |
| `skills` | [`{name, version?}`] | skills of the catalog. `version`, when set, must be one the catalog offers: today the digest `sha256:<64 hex>` of the catalog's reference (one digest per skill); absent takes the catalog's. An unknown version is refused with a sentence that says what the catalog offers |
| `egress.allow` | [origin] | origins the sandbox may reach, checked against the policy |
| `suspend` | bool | see "Suspend" |

A definition's rules are **per namespace**: reserved names, `maxAgents` and every object and key name are scoped to the definition's own
namespace, and nothing in the code assumes the namespace is called `kagent` (the name comes from the catalog, `namespaces.agents`). The
controller still lists and watches one namespace, the catalog's.

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
   **`<name>-virtual-key-<first 8 hex characters of the definition's UID, dashes removed>`** (the data key is the runtime adapter's,
   `OPENAI_API_KEY` for kagent; `immutable: true`, labels, owner reference, annotation `openagentix.io/key-fingerprint`). Because the
   name carries the UID, a Secret of that name was made for this very definition, so the controller may replace it without reading
   it; a foreign Secret called `<name>-virtual-key` is never touched. The **gateway user** of the key is **`<namespace>.<name>`**
   (`status.controller.gatewayUserId`): treat it as an opaque string. It is also the name of the entry in the key ConfigMap
   `llm-api-keys-control-plane`, so two definitions with one name in two namespaces never share a key or a cost history, and a chart
   agent's user (a plain name, no dot) can never equal it. A ConfigMap key may hold `.`, and the gateway never reads the key's text
   (it reads the entry's JSON: agentgateway `controller/pkg/agentgateway/plugins/traffic_plugin.go`, `processAPIKeyAuthenticationPolicy`,
   and `crates/agentgateway/src/http/apikey.rs`, where `metadata` is a free map), so `user_id` carries the same string. The controller
   reads the gateway's key ConfigMap once per pass and compares the hash registered for the user with the fingerprint it recorded in
   `status.controller.keyFingerprint` (the first 16 hex characters of the hash, which is public in that ConfigMap):
   - registered hash present and equal to the recorded fingerprint: nothing to do, no mint, no request (if only the `team` or `project`
     label changed, the entry is rewritten with the same hash);
   - registered hash present but not the recorded one (an older entry was restored): the Secret is deleted, a new key is
     created and its hash written (the fingerprint on the runtime objects changes with it);
   - nothing recorded or no entry: `POST` the Secret. 201: write the hash. 409 and an entry exists: adopt it. 409 and no
     entry: delete the Secret, create a new one, write the hash.
   The controller never GETs or LISTs a Secret. Consequence: a Secret deleted by hand is not noticed until the hash or the
   definition changes.
5. **Applies** the runtime's objects with server-side apply (field manager `openagentix-control-plane`, force), each with the
   two labels and the owner reference above (and the reserved labels `team` and `project`, see "Reserved labels"). The kagent adapter
   also puts the annotation `openagentix.io/key-fingerprint` on the Harness and the Agent so that replacing the key changes them;
   whether kagent recreates the agent's pod on that annotation is **not verified on a live cluster**.
6. **Prunes** owned objects (labels and owner reference) that are no longer wanted. A kind the cluster does not know (404 on
   the list, for example a CRD that is not installed) has nothing to prune; it never blocks a pass or a deletion.
7. **Reports** the status contract (below). `observedGeneration` is the newest generation the controller has looked at; each condition's
   `observedGeneration` is the generation that condition describes. Reasons are cut to 64 and messages to 1024 characters (the CRD
   limits), on a character boundary. A status the API server refuses as invalid (422) is logged and not retried.
8. **On delete**: removes the owned objects, removes the key hash from the gateway, then the finalizer. The key Secret goes
   by its owner reference.
9. **Sweeps** every pass: an entry in the controller's key ConfigMap whose user is not `<namespace>.<name>` of a listed definition is
   removed (a definition created in the same pass is in the list, so it is never swept; a failed or partial list sweeps nothing). An
   entry of the previous format (the plain name) is nobody's user and goes too.

The loop is level-triggered. It lists (in pages of 100, at most 10 000 items), reconciles every definition one at a time,
then watches from the list's resourceVersion. A new definition, a changed spec (generation) or a requested deletion ends
the watch and starts the next pass; so does the resync timer (60 s), a retry that is due, or `410 Gone` (it lists again).
Its own status and finalizer writes do not start a pass. Passes start at least `CONTROL_PLANE_MIN_INTERVAL` (1 s) apart. The
watch has an open deadline (30 s) and the resync timer runs while it is being opened, so a hung API connection ends in a new
pass. One object that cannot be decoded, or that panics, is skipped and logged; the others are reconciled.

Order and limit: definitions that are accepted now come first, then the oldest (creation time, namespace, name). `maxAgents`
counts accepted definitions **per namespace**, so a refused definition holds no place, and a definition that is fixed later never
displaces one that already holds a place. When the limit is lowered below the number accepted, the last ones in that order
are refused.

Retries are per definition: a failure retries after `CONTROL_PLANE_BACKOFF` and the delay doubles per failure up to 30 times
that value; a runtime that is not ready yet is retried at the base interval; a new generation resets it. The other
definitions are not held back.

**Transient read errors.** When a read fails (the name check, the key registrations, the runtime's readiness) for a
definition that runs, `Applied`, `Ready` and the facts keep their last known state and a separate condition `Synced=False`
(reason `TransientError`, message the error) says the pass could not finish. It turns `True` when a pass succeeds.

## The status contract

The public fields are what products may read; they follow the deprecation rules of `docs/api-conventions.md`. Everything the
controller keeps for itself is under `status.controller` and may change without notice.

| Field | Meaning |
|---|---|
| `conditions` | `Accepted`, `KeyReady`, `Applied`, `Ready` (and `Synced`), see "Conditions" |
| `observedGeneration` | the newest generation the controller has looked at |
| `endpoint` | `{protocol: "a2a", url, protocolVersion}`: where the agent is callable; absent while suspended or refused, and when the platform advertises no route (the A2A gate is off). A second protocol or version needs no new field |
| `revision` | a stable hash (12 hex characters) of what was applied: the final prompt, the model, the tool profile and the effective tools, the skills with their digests, the knowledge, notify and memory settings, and the egress. Lists are sets (sorted before hashing). A change of `description`, labels, `suspend`, name or namespace does not change it. The runtime adapter's own settings (image, worker pool) are not in it |
| `effectiveTools`, `effectiveEgress` | the tools and egress origins as applied |
| `violations` | one sentence per refused rule |
| `capabilities` | what the runtime can do, see "Runtime capabilities" |
| `agentRef` | one generic reference to the runtime's object, for people who debug |
| `controller` | `keySecret`, `keyFingerprint`, `appliedGeneration`, `appliedSpec` (that spec as JSON text), `gatewayUserId`, `toolServer` (the backend the profile resolved to). Internal |

`kubectl get oad` shows Model, Profile, Accepted, Ready, Revision, Suspended and Age.

## Conditions

`Ready=True` means: **the agent is callable at `status.endpoint`, at `status.revision`.** A runtime that reports the agent ready while the
platform advertises no endpoint (the A2A gate is off) gives `Ready=False`, reason `NoEndpoint`: the promise cannot be made. The other
conditions (`Accepted`, `KeyReady`, `Applied`, `Synced`) are informational: say what happened, never gate on them. Every reason the controller
can set is in this table, and nowhere else; a test (`internal/core/contract_test.go`) fails when the code sets a reason that is not here,
and another (`internal/api`) fails when this table and the code differ. A reason that comes from the runtime adapter and is not in the table is
mapped to `RuntimeNotReady` with its own word at the start of the message.

| Condition | Reason | Status | Meaning |
|---|---|---|---|
| `Accepted` | `Accepted` | True | the definition passes the catalog and the policy |
| `Accepted` | `PolicyViolation` | False | one or more rules refused it; each is one sentence in status.violations |
| `KeyReady` | `KeyReady` | True | the agent's gateway key exists and its hash is registered |
| `KeyReady` | `KeyFailed` | False | the key could not be created or registered; the message says why |
| `KeyReady` | `NotAccepted` | Unknown | the definition is refused, so no key is made |
| `KeyReady` | `NotApplied` | Unknown | an earlier step (the name check) stopped the pass before the key |
| `Applied` | `Applied` | True | the runtime objects of this generation were written |
| `Applied` | `PreviousGenerationRunning` | True | this generation is refused; the previously applied one keeps running |
| `Applied` | `NotAccepted` | False | the definition is refused and nothing is running |
| `Applied` | `NotApplied` | False | a step before the write failed or is waiting (the key) |
| `Applied` | `NameConflict` | False | an object with a target name exists and is not owned by this definition; nothing was written |
| `Applied` | `CheckFailed` | False | the name check could not read the cluster |
| `Applied` | `RenderFailed` | False | the runtime adapter could not turn the plan into objects |
| `Applied` | `ApplyFailed` | False | writing an object failed |
| `Applied` | `PruneFailed` | False | removing an object that is no longer wanted failed |
| `Applied` | `RemoveFailed` | False | removing the objects or the key of a definition that is no longer allowed failed |
| `Applied` | `PolicyRemoved` | False | the catalog or the policy changed and what ran is no longer allowed: objects and key were removed |
| `Ready` | `Ready` | True | the agent is callable at status.endpoint at status.revision |
| `Ready` | `Suspended` | False | suspended by spec.suspend or by the operator (policy suspendAll): the agent takes no new work; nothing is wrong |
| `Ready` | `NotAccepted` | False | the definition is refused (or its objects were removed) |
| `Ready` | `NotApplied` | False | the objects of this generation are not written yet or could not be |
| `Ready` | `ReadinessUnknown` | False | the runtime's state could not be read |
| `Ready` | `NoEndpoint` | False | the runtime reports the agent ready but the platform advertises no endpoint for it (the A2A gate is off), so it is not callable at status.endpoint |
| `Ready` | `RuntimeNotCreated` | False | the runtime has not created the agent yet |
| `Ready` | `RuntimeStale` | False | the runtime has not caught up with the latest spec yet |
| `Ready` | `RuntimePending` | False | the runtime reports no verdict yet |
| `Ready` | `RuntimeNotReady` | False | the runtime reports the agent not ready; its own words are in the message |
| `Ready` | `RuntimeRejected` | False | the runtime refused the agent; its own words are in the message |
| `Synced` | `Synced` | True | the last pass could read everything it needed |
| `Synced` | `TransientError` | False | a read failed; the other conditions are the last known state, not a new verdict |

## Suspend

`spec.suspend: true` keeps the definition accepted and its objects in place, but the agent takes no new work: `Ready=False`, reason
`Suspended`, `endpoint` and `agentRef` cleared, `revision` and the effective lists unchanged. The policy file's `suspendAll: true` is the
operator's stop switch and has the same effect for every definition (also for one whose edit is refused while its previous generation runs).

**How it is done:** kagent 1.0.1's `Agent` and `Harness` have no suspend, pause, replicas or desired-state field (read in the kagent API
types, `go/api/v1alpha3/agent_types.go` and `harness_types.go`, and `docs/architecture/runtime-and-lifecycle.md`, which says session
suspension is platform-managed with no public controls). So the runtime adapter, asked for a suspended plan, produces every object
**except the `Agent`**; the core prunes the `Agent` like any object no longer wanted. The key Secret, its registration, the
ModelConfig, Harness, AgentTemplate and EgressPolicy stay, and so does the status. Resuming applies the `Agent` again. **What this costs, from the kagent documents (not tried on a cluster):** deleting an Agent "retires its
definition; sessions and checkpoints retain their pinned revisions", and "recreating the same name creates a new identity"
(`docs/architecture/configuration-and-compilation.md`); the identity lookup of an Actor JWT requires the Agent to have the same UID
(`docs/architecture/agent-identity-lookup.md`). So a resumed agent is a new Agent for new sessions, and an old session's tool calls that resolve
the agent by identity may fail. Suspend is therefore a stop with a fresh start, not a pause of running conversations. If a runtime offers a real
pause, only its adapter changes.

## Runtime capabilities

The `Runtime` adapter reports a small struct; the core copies it to `status.capabilities`. A value the adapter could not verify is
`unknown` (`maxStepSeconds: 0`). The kagent adapter's values, each from the kagent documents or API types (cited in
`internal/adapters/kagent/kagent.go`):

| Field | kagent | Source (in the kagent repository) |
|---|---|---|
| `approvals` | `yes` | `docs/architecture/human-in-the-loop.md`; `go/api/v1alpha3/agenttemplate_types.go` (`RequireApproval`) |
| `durableWaits` | `yes` | `docs/architecture/persistence-checkpoints-and-forks.md` ("Input-required and auth-required tasks are durably suspended") |
| `workspacePersistence` | `snapshot` | `docs/architecture/runtime-and-lifecycle.md` (snapshot fidelity; suspending an Actor preserves files, not running commands) |
| `maxStepSeconds` | `600` | `docs/architecture/runtime-and-lifecycle.md` (bash: 60 s default, `timeout_seconds` up to 600) |
| `cancelStopsLoop` | `unknown` | the docs speak of "the interrupted execution" but nothing states that a cancel ends the loop in every harness; not tried on a cluster |
| `perRunIdentity` | `session` | `docs/architecture/agent-identity-lookup.md` (one Actor JWT per Session; runs are not a runtime concept yet) |

No run-side methods exist yet.

## Reserved labels

`openagentix.io/team`, `openagentix.io/project` and `openagentix.io/work-item` are reserved. They are **informational**: anyone who can write
the object can set them, so **no access, policy or billing decision ever reads them** (a test asserts that the policy's verdict is the same with
and without them). The controller validates `team` and `project` as Kubernetes label values (at most 63 characters, not secret-shaped) and copies
them from the definition onto every object it creates and into the gateway key entry's metadata (`{"user_id": ..., "team": ..., "project": ...}`),
so that cost metrics can carry them later. `work-item` is for products and is not copied.

## When a definition that runs is refused

Status records what is applied (`status.controller.appliedGeneration` and `appliedSpec`). When a definition that was accepted is refused later:

- **The author edited it** into something refused, and the previously applied spec still passes the current catalog and
  policy: that version keeps running. `Accepted=False` (reason `PolicyViolation`, violations listed), `Applied=True` with
  reason `PreviousGenerationRunning`, `Ready` read from the runtime for the running version; `endpoint`, `effectiveTools` and
  the rest keep describing what runs; the `Applied` and `Ready` conditions carry the running generation in their
  `observedGeneration`.
- **The catalog or the policy changed** (the spec is the one that was applied), **or** the author edited it and the
  previously applied spec no longer passes, **or** what was applied is unknown (a status written by an older controller):
  the agent's objects are removed (all owned kinds, as on delete) and its key hash is unregistered; the definition and its
  status stay. `Applied=False` reason `PolicyRemoved`, `Ready=False` reason `NotAccepted`, and `agentRef`, `endpoint`,
  `revision`, `effectiveTools`, `effectiveEgress` and everything under `status.controller` are cleared. The
  key Secret stays until the definition goes (its owner reference); when the definition is accepted again a new key replaces it.
- Nothing ran before: only the status changes.

## Structure

```
cmd/control-plane          env, wiring, /healthz and /readyz, graceful shutdown
internal/api               AgentDefinition types, status contract, conditions and their reason table
internal/catalog           loads catalog.json and policy.json; the types of both, adapter-owned sections included
internal/policy            pure validation: definition + catalog + policy -> violations
internal/plan              definition + catalog -> Plan (final prompt, tools, skills, egress, memory, revision), no upstream words
internal/core              the loop; declares Runtime, ModelGateway, Kube; imports no adapter (a test enforces it)
internal/adapters/kagent        Runtime: Plan -> the kagent objects, readiness, endpoint, capabilities; derives its own names
internal/adapters/agentgateway  ModelGateway: entries in the ConfigMap llm-api-keys-control-plane
internal/adapters/toolhive      Tools: profile -> backend and tool names, read from the catalog; explains a missing tool
internal/keys              mint, hash, and the create/409 logic
internal/kube              REST client (get, list, watch, apply, merge patch, create, update, delete)
internal/kubetest          fake API server for tests
crd/agentdefinition.yaml   the CustomResourceDefinition
testdata/                  example catalog.json and policy.json, golden Helm output, regen-golden.sh
```

**The adapter boundary.** `plan`, `policy` and `api` name no upstream product (a test, `internal/core/vocabulary_test.go`, refuses the words
kagent, toolhive, vmcp, harness and ModelConfig in them outside comments that cite an ADR). The runtime adapter derives its own object names
from the definition's name (kagent: ModelConfig `gateway-<name>`, Harness `<name>-harness`, AgentTemplate `<name>-template`, EgressPolicy
`<name>-egress`, Agent `<name>`) and the data key of the key Secret; the gateway adapter derives the user id's use; the tools adapter says
what a profile offers and why a tool is missing (the code-mode tool's name is catalog data). What only one adapter needs is in that
adapter's section of the catalog (`runtime.kagent`, `tools.toolhive`). One thing is still kagent-shaped on purpose: the key lives in a
Kubernetes Secret named with the UID, which is a core rule.

The `Runtime` adapter produces the same objects as `charts/openagentix/templates/kagent.yaml`; the parity tests
(`internal/adapters/kagent`) compare them field by field with `helm template` output for two demo agents (`profile-demo`,
`assistant`). The ignored paths are the four Helm-only labels, listed in the test; the one name that differs is the chart's hand-set
ModelConfig name of `assistant`, replaced in the golden before the comparison. `testdata/regen-golden.sh` regenerates the golden files
(needs helm, yq, jq).

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
catalog is listed and watched; every rule and name is still per namespace.

Logs are JSON on stderr: agent name, object names, outcome. A log line, a condition message or an error never contains a
key, and never an API error body: errors carry the HTTP status and the API's reason word only.

## The catalog (`CONTROL_PLANE_CATALOG_FILE`)

What exists on the platform. The chart renders it from the same values that render chart agents. No secret in it. Unknown
fields are refused. `testdata/catalog.json` is a full example for the demo values. The top level is platform-level; what only one
adapter needs is in that adapter's own section, `runtime.<adapter>` and `tools.<adapter>`, and the section of the adapter named in
`adapters` must be there.

| Field | Type | Meaning |
|---|---|---|
| `adapters.runtime` | string | active runtime adapter; this build supports `kagent` |
| `adapters.modelGateway` | string | active gateway adapter; `agentgateway` |
| `adapters.tools` | string | active tools adapter; `toolhive` |
| `namespaces.agents` | string | the namespace listed and watched, and where the chart's agents live (`kagent.namespace`); the memory model config is read there |
| `namespaces.gateway` | string | namespace of the gateway, where `llm-api-keys-control-plane` lives |
| `namespaces.tools` | string | the tool servers' namespace (`mcp.namespace`); an egress origin inside it is refused, and it can never be listed in `inClusterDestinations` |
| `gatewayBaseUrl` | string | the gateway's OpenAI base URL an agent's model config points at (as `oa.gatewayBaseUrl`) |
| `models` | [string] | gateway model names (`gateway.models[].name`) |
| `knowledge.available` | bool | `knowledge.enabled` and `mcp.enabled` |
| `knowledge.tools` | [string] | the tools `knowledge.enabled` adds (`oa.knowledgeTools`: three, six with the graph) |
| `knowledge.prompt` | string | the addition to the system prompt, tool names already substituted (`oa.knowledgeAgentPrompt`) |
| `notify.available` | bool | `notify.enabled` and `mcp.enabled` |
| `notify.tools` / `notify.prompt` | | as above (`oa.notifyTools`, `oa.notifyAgentPrompt`) |
| `memory.available` | bool | the memory stack is installed |
| `memory.expiryEnabled` | bool | `memory.expiry.enabled`: the job that enforces the time limit |
| `memory.defaultTtlDays` | int | the runtime's default when a definition sets no `ttlDays` (kagent: 15) |
| `memory.prompt` | string | addition to the system prompt (`memory.agentPrompt`) |
| `skills` | map name -> ref | skill name to OCI ref; the ref must end in `@sha256:<64 hex>` (the file is refused otherwise); the digest is the one version a definition may name |
| `gateUrl` | string | base URL of the A2A gate (`oa.a2aGateUrl`); empty when the gate is off (then no definition has an `endpoint`) |
| `reservedNames` | [string] | names taken in the agents namespace by every chart agent plus `kb`, `kb-extract`, `memory` |
| `runtime.kagent.harnessImage`, `.workerPool`, `.snapshotLocation` | string | `kagent.harnessImage`, `kagent.workerPool`, `kagent.snapshotLocation` |
| `runtime.kagent.harnessEnv[]` | list of `{name, value}` | environment of the harness: `OTEL_PROPAGATORS` when observability and baggage are on, else `[]` |
| `runtime.kagent.memoryModelConfig` | string | name of the shared embedding model config (`memory.modelConfig.name`) |
| `tools.toolhive.codeModeTool` | string | the name of the code-mode tool (`execute_tool_script`) |
| `tools.toolhive.profiles[]` | list | tool profiles (`mcp.profiles`): `name`, `toolServer` (name of the profile's server object, `<remoteMCPServer.name>-<profile>`), `codeMode`, `tools` (agent-side names, as the chart's `oa.profileToolNames`; the code-mode tool last when `codeMode`) |

Derived names (not in the file; the kagent adapter's): ModelConfig `gateway-<name>`, Harness `<name>-harness`, AgentTemplate `<name>-template`,
EgressPolicy `<name>-egress`, Agent `<name>`, all in the definition's namespace; Secret `<name>-virtual-key-<8 hex of the UID>` (at most 40 + 13
+ 8 characters; the UID is a UUID, dashes removed, first 8 characters); gateway user `<namespace>.<name>`.

## The policy (`CONTROL_PLANE_POLICY_FILE`)

What the operator allows (chart values `controlPlane.policy`). An allow-list that is empty allows nothing; the single entry
`"*"` allows everything the catalog has. `testdata/policy.json` is an example.

| Field | Type | Meaning |
|---|---|---|
| `maxAgents` | int >= 0 | number of accepted definitions **per namespace**; those already accepted keep their place, then the oldest; the rest are refused |
| `allowedModels` | [string] | models a definition may name |
| `requireToolProfile` | bool | every definition must name a `toolProfile` |
| `allowedProfiles` | [string] | tool profiles a definition may name |
| `allowKnowledge` | bool | `knowledge.enabled: true` is allowed |
| `allowNotify` | bool | `notify.enabled: true` is allowed |
| `allowedSkills` | [string] | skills a definition may name |
| `memory.allowed` | bool | `memory.enabled` is allowed |
| `memory.maxTtlDays` | int >= 1 when memory is allowed | largest `ttlDays` (the catalog default counts when none is set) |
| `memory.requireExpiry` | bool | refuse memory while `catalog.memory.expiryEnabled` is false |
| `egress.allowedOrigins` | [string] | origins (`https://host[:port]`); a host may start with one `*.`, which stands for exactly one label. A definition's origin must equal an entry or match a wildcard entry (scheme and port must be equal). A wildcard origin in a definition matches only an identical entry. Empty: no egress |
| `egress.inClusterDestinations` | [string] | default empty. Exact `host:port` entries: the one exception to the rule that refuses every in-cluster destination. Each is a namespace-qualified service (`registry.ci.svc:5000`, `db.data.svc.cluster.local:5432`, `svc.ns:80`) and a numeric port: no scheme, no wildcard, no address, no entry twice, never inside the tools namespace. An origin is allowed when its host and port (the scheme's default port when it names none) are exactly an entry, whatever scheme; the platform admission guard's EgressPolicy rule allows exactly these and nothing else in the cluster. kagent needs full DNS names in its egress policies (its docs), so use the `.svc` forms |
| `reservedNames` | [string] | more reserved names: `name` in every namespace, `namespace/name` in one (the catalog's names are reserved in the agents namespace) |
| `suspendAll` | bool | the operator's stop switch: every definition is treated as suspended |

## Refusals

Each is one sentence in `status.violations`, with `Accepted=False` (reason `PolicyViolation`):

- name not a DNS label, longer than 40, reserved in its namespace (catalog or policy), or more definitions than `maxAgents` in its namespace;
- `team` or `project` label that is not a valid label value, or shaped like a secret;
- description over 256 characters; neither or both of `instructions` and `instructionsFrom`; `instructionsFrom` at all ("not available yet");
  instructions over 16 KiB; instructions, description, model, toolProfile, tools, skills (name and version), knowledge collections, notify
  destinations or egress entries shaped like a secret (the ten patterns of `charts/openagentix/files/memory-expiry.sh`, same names,
  plus `platform_virtual_key`, `vk-` and 32 hex characters; the sentence names the pattern, never the text, and any other
  sentence that quotes a value has secret-shaped parts replaced by `[redacted]`);
- a model not in the catalog or not allowed; a profile not in the catalog or not allowed; a skill not in the catalog or not allowed, or a skill
  version the catalog does not offer;
- tools without a `toolProfile` (the shared tool server filters on the client side only), no profile when the policy requires
  one, a tool the profile does not offer (the tools adapter words it, including its code-mode case), a tool listed twice;
- `knowledge.enabled` or `notify.enabled` when the policy does not allow it, when the service is off, **without a profile**, or when the profile
  does not offer their tools; `knowledge.collections` or `notify.destinations` non-empty ("selecting ... is not available yet");
- memory when the policy does not allow it, the stack is not installed, the expiry job is off (with `requireExpiry`), or `ttlDays` is above the maximum or below 1;
- an egress origin that is malformed, listed twice, inside the tools namespace, not matched by the policy, or more than 64
  origins; and, whatever the policy patterns say, one that names a cluster-internal or local host (`.svc`, `.cluster.local`,
  a name without a dot, an IP address, or a name inside the agents or gateway namespace) **unless** its host and port are exactly one of the
  operator's `egress.inClusterDestinations` (the tools namespace has its own rule and no exception).

## RBAC it needs

Namespaced Roles only, no ClusterRole, no wildcard (rendered by the chart):

| Namespace | Resource | Verbs |
|---|---|---|
| agents (the catalog's `namespaces.agents`) | `agentdefinitions` | get, list, watch, update |
| agents | `agentdefinitions/status` | patch |
| agents | `modelconfigs`, `harnesses`, `agenttemplates`, `egresspolicies`, `agents` (`api.kagent.dev`) | get, list, watch, create, patch, delete |
| agents | `secrets` | **create, delete** only |
| gateway | `configmaps`, resourceName `llm-api-keys-control-plane` | get, patch |

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

## Upgrading from the first shape (before ADR 0012)

Nothing was released with the first shape, so the CRD changed in place (still `v1alpha1`, no conversion). On a cluster that runs the previous controller:

1. Build and load the new image and apply the new chart (`./install.sh` does both); the CRD is part of the release and is replaced by `helm upgrade`
   (a CRD is applied by the chart's template, not by Helm's `crds/` rules, so it is updated).
2. A stored definition keeps its old `status` fields until the controller writes them again; the API server prunes fields the new schema does not
   know. `spec` that only uses fields whose shape did not change (no `knowledge`, `notify`, `skills`) is read as it is. A stored `knowledge: true`,
   `notify: true` or `skills: [name]` no longer decodes: that definition is skipped and logged until it is edited to the new shape.
3. The new controller does not know the previous one's bookkeeping (it was at the top level of `status`) and finds the key's hash under the old
   entry name. With no entry under the new name it tries to create the key Secret: if the Secret exists (`409`, the usual case: its name is the same),
   the old hash is moved to the entry `<namespace>.<name>` and **the key is not replaced**; if it does not exist, a new key is made. Either way the old
   entry `researcher` is swept at the end of the pass and `status.controller` is filled again. No rollout is expected unless the Secret was missing.

## Tests

`make test` (`go test ./... -race -count=1`). They need nothing but Go (the CRD test also uses `yq` and skips without it):
the Kubernetes API is `internal/kubetest`, a fake that records every request so tests can assert what was sent and what was not. It paginates (`limit`/`continue`) and models server-side apply for the fields a client sends (a field no longer sent is removed); it does not model several field managers, conflicts, list merge keys or defaulting.

- policy: one case per refusal, and accepted cases; labels never change a verdict; the example definition in `components/control-plane/` is accepted and strictly decodable;
- plan: the revision is stable across list orders and changes with what was applied; the user id is `<namespace>.<name>`;
- kube and core: list and watch resume, 410 Gone (HTTP and event form) and re-list, server-side apply bodies, status patch, finalizer, pruning, name conflicts, serialized reconciles, per-namespace rules (a second namespace), suspend and the stop switch, the status contract, and a scan of `core.go` that fails when the code sets a condition reason that is not in the table;
- keys: 201, 409 with the hash present, 409 without, a restored older hash, no request when the key is in order, label-only changes, never a GET on Secrets;
- canary: a planted key value appears in no log line, status, error string or request body except the one Secret create, over several passes, also with the real adapters (`cmd/control-plane`);
- parity with `helm template` (golden files); the core imports no adapter package; `plan`, `policy` and `api` hold no upstream vocabulary; the README's reason table matches the code.

`make docker` builds the image (distroless, non-root, port 8081 for the probes).

## Known limits

- Suspend removes the kagent Agent (kagent has no pause field), so a resume is a new Agent identity for new sessions (see "Suspend").
- `cancelStopsLoop` is `unknown`; there are no run-side methods yet (`AgentRun` is reserved by ADR 0012).
- The admission guard (ValidatingAdmissionPolicy) pins the kagent objects' shapes; its EgressPolicy rule was checked with the Kubernetes admission CEL
  library, not on a live API server.
