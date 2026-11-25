# ADR 0012: platform primitives for products built on OpenAgentix

Status: Accepted (2026-11-25). The changes in "Change now" are made to `AgentDefinition` before anyone depends on it. The kinds in "Reserved" have a decided shape and are built in the releases named. External facts were read in public documents on 2026-11-25; items marked *unverified* must be checked when used.

## Context

The project owner intends to build products on top of OpenAgentix, as separate or integrated projects and not inside this repository: a Kubernetes-native software factory, including the "dark factory" case where work runs unattended and people only set goals and approve at gates; an agentic software development life cycle with role agents handing work to each other; and agentic CI/CD, with agents as pipeline steps and pipelines that agents drive. The instruction is that OpenAgentix must be designed now to support everything such products need.

These three are one thing at three levels of autonomy: the person writes the code and agents help in the pipeline; the person assigns a work item and reviews the result; the person sets a goal and approves at gates. The engine underneath is the same, so its general parts belong in the platform.

## What such products need, from how existing systems work

A survey of current systems (GitHub agentic workflows and the Copilot coding agent, Factory.ai, OpenHands, goose, Devin, Kelos, StrongDM's software factory, `kubernetes-sigs/agent-sandbox`, Tekton, Argo, Dagger, Temporal, Dapr Agents, the A2A and MCP task models) gives this list. Each line is a platform capability, not a product feature.

| # | Capability | Evidence in short |
|---|---|---|
| 1 | A **run**: a long-running unit of work that can wait for a person, be cancelled, and be resumed | A2A task states, MCP tasks, Kelos `Task`, durable execution engines |
| 2 | Handoff between agents, with parent and child runs | Devin coordinator and child sessions, Kelos pipelines |
| 3 | A workspace per run with a repository checkout, kept across waits | Kelos, Copilot, OpenHands pause |
| 4 | A sandbox with build tools and controlled network | allowlist proxies in GitHub agentic workflows, agent-sandbox |
| 5 | Identity per agent and per run; scoped, short-lived credentials; acting for a user | GitHub's split of a read-only agent job and separate write jobs; MCP forbids passing tokens through |
| 6 | Triggers from Git, CI and trackers; status reported outward | Kelos spawners, A2A push notifications |
| 7 | Approval gates with expiry and escalation | Copilot "approve and run", A2A `input-required` |
| 8 | Policy on actions | mostly enforced by credential scope and by the forge, not by the agent runtime |
| 9 | Budget and cost per run and per work item | Devin's limit per child session |
| 10 | Quality gates that block, with test data the agent cannot read | StrongDM holdout scenarios; reward hacking has been observed |
| 11 | Provenance: which agent revision, model, prompt and skills made a change | signed commits and session links in Copilot; no standard yet |
| 12 | Audit | present everywhere; deterministic replay is not realistic |
| 13 | Concurrency limits and queueing | goose runs ten in parallel, MCP advises a cap; no system shows fairness |
| 14 | Delegation with narrowed authority | budget narrowing in Devin; credential narrowing is still drafts |
| 15 | Versioned agents, so a run records the exact revision | products keep versions in Git |
| 16 | A stop switch | cancel and caps exist; no fleet-wide switch found |
| 17 | Reaching test dependencies inside the cluster | StrongDM runs clones of the services its software depends on |

The one thing the platform lacks entirely is the first: the run. Almost everything else hangs off it.

## Boundary between platform and product

A primitive belongs in OpenAgentix when it makes sense without knowing what a repository, an issue, a pipeline or a release is, and when at least two unrelated agent applications would use it (a support desk and a factory, say), or when it governs, meters or secures agents. Security controls are always platform.

| Platform, in `openagentix.io` | Product, in its own API group |
|---|---|
| `AgentDefinition`, `AgentRun`, `Workspace`, `CredentialGrant`, `ApprovalRequest`, `AgentAccess`, `PlatformPolicy`, `Budget`, evaluation suites and runs, `Trigger`, run events, the provenance record, console plugins, runtime conformance | repository, work item, pipeline, stage, review gate, release, deploy target, merge and deploy rules, code-owner mapping, test twins, commit and pull request conventions |

Borderline cases, decided:

- **Workspace with a Git checkout: platform.** Fetching a source is generic. Branches, pull requests and pushes are product, done with a credential whose scope the product chooses.
- **Approval: the object is platform, the routing is product.** "Two code owners must approve a release" is a review gate in the product that creates approval requests.
- **Quality gate: the verdict is platform, the decision is product.** An evaluation run reports passed or not; the product combines it with CI checks and approvals.
- **Pipeline step: product or the CI system**, plus one small platform piece: a step container that creates a run, waits and exits by its result, so Tekton, Argo or GitHub Actions can call agents. Pipelines that agents drive are a tool server for the CI system in the tool catalog.
- **Workflow engine: not platform.** The product's controller plus run phases is the durable workflow. An engine is adopted behind an adapter only when two products need one.

A product plugs in with its own API group and controller, creates definitions and runs with an owner reference to its own object, watches conditions and events, supplies policy that can only tighten, and adds console pages through the plugin mechanism. The platform commits to: a status contract, reserved label keys, deprecation rules per kind, Roles per kind, and conformance for runtimes.

## Change now

Adding an optional field later does not break anyone. Changing a field's type, its meaning, naming, or status that others already read does. So these change before the first release that contains `AgentDefinition`.

| # | Was | Becomes | Reason |
|---|---|---|---|
| 1 | gateway user and cost label = the agent's name | `<namespace>.<name>`, documented as opaque. Chart agents keep theirs | two agents with one name in two namespaces would collide in keys and in cost history |
| 2 | "the `kagent` namespace" written into the rules | every rule is per namespace: reserved names, `maxAgents`, object names. The controller still serves one namespace for now | products map a project or team to a namespace |
| 3 | `knowledge: true`, `notify: true` | `knowledge: {enabled, collections: []}`, `notify: {enabled, destinations: []}` | collections and destinations are planned kinds; a boolean cannot grow |
| 4 | `skills: [name]` | `skills: [{name, version}]` | ADR 0007 already promises an optional version |
| 5 | status mixes public facts with the controller's own bookkeeping | a **status contract**: `conditions` (`Accepted`, `Ready`), `observedGeneration`, `endpoint`, `revision`, `effectiveTools`, `effectiveEgress`, `violations`, `capabilities`, `agentRef`. Everything else moves under `status.controller`, which may change without notice. `toolServer` leaves the contract (it is an upstream name) | products will read whatever is there |
| 6 | `a2aUrl` | `status.endpoint: {protocol, url, protocolVersion}` | a second protocol or version must not need a new field |
| 7 | no revision | `status.revision`, a hash of what was applied (instructions, model, tools, skill digests, egress) | provenance and pinning need it from the first run |
| 8 | no pause | `spec.suspend`: the agent stays, new work is refused. Policy can force it | the stop switch |
| 9 | conditions without a promise | `Ready=True` means: callable at `status.endpoint`, at `status.revision`. Reasons for `Ready=False` are documented. The other conditions are informational | the one signal products wait on |
| 10 | every in-cluster destination refused | still refused, except those an operator lists in `policy.egress.inClusterDestinations` (exact host and port, default none) | test dependencies and registries inside the cluster |
| 11 | instructions inline only | the name `instructionsFrom` is reserved (a reference to a ConfigMap, a skill or Git); exactly one of the two is set | products keep prompts in Git |
| 12 | no label convention | reserved label keys `openagentix.io/team`, `openagentix.io/project`, `openagentix.io/work-item`. Informational: anyone who can write the object can set them, so they never decide access or billing on their own | stable keys for cost and traces |
| 13 | - | one API group, `openagentix.io`; each kind has its own version and follows the Kubernetes alpha, beta, stable deprecation rules; products use their own group; no platform kind names a product concept | splitting a group later is a migration |

## Reserved: shape decided, built later

### `AgentRun` (release 0.4)

The unit a product creates, watches, cancels, approves against, bills and audits. Runs are for governed or unattended work; chat stays plain A2A. The conversation stays in the runtime's task store, not in the resource, and finished runs are removed after a time to live.

```yaml
apiVersion: openagentix.io/v1alpha1
kind: AgentRun
metadata:
  name: wi-123-code
  labels: {openagentix.io/work-item: wi-123}
  ownerReferences: []           # the product's own object
spec:
  agentRef: {name: coder}       # same namespace
  input: {text: "..."}          # bounded; never a secret
  contextRef: {run: wi-123-plan}   # continue a conversation and its workspace
  parentRef: {name: wi-123}        # the run that delegated this one
  workspaceRef: {name: wi-123}
  onBehalfOf: {userId: ""}         # honoured only under an access rule
  limits: {deadline: 2h, maxTokens: 2000000, maxToolCalls: 500}
  budgetRef: {name: project-a}
  priority: 0
  suspend: false                # queued, not started
  cancel: false                 # set to true to cancel; a run is never deleted to cancel it
  ttlSecondsAfterFinished: 604800
status:
  phase: Queued                 # Queued, then the A2A task states
  conditions: []                # Admitted, Started, Finished
  task: {contextId: "", taskId: ""}
  agentRevision: ""
  usage: {inputTokens: 0, outputTokens: 0, costUSD: "0", toolCalls: 0}
  pendingInput: {kind: approval, requestRef: {name: ""}}
  provenance: {models: [], skills: [], toolProfile: "", traceId: ""}
  result: {summary: "", artifacts: []}
```

### `ApprovalRequest` (0.4)

Created by the controller when a run reaches a tool that needs approval, or by a product for its own gate. It names what is to be approved, who may approve, when it expires and what happens then (deny, or escalate). The approver sets the decision and a reason; the controller records who decided. It is its own kind so that Kubernetes access rules can keep the person who starts a run apart from the person who approves.

### `AgentAccess` (0.3)

The A2A gate's caller rules as a resource: callers, agents, the user id rule, the allowed methods. `Schedule` needs it anyway, and a product's controller becomes a caller with the task methods (get, cancel, subscribe).

### Events (0.4)

Status and conditions are the contract. Kubernetes Events are informational only. For the outside world the platform sends CloudEvents through an event sink adapter, with these types reserved: `io.openagentix.run.v1alpha1.queued`, `.started`, `.input_required`, `.succeeded`, `.failed`, `.canceled`, and `io.openagentix.approval.v1alpha1.requested`, `.decided`, `.expired`. They carry the run's namespace and name and a trace reference, never content.

### Delegation (0.5)

`AgentDefinition.spec.delegates` lists the agents this one may call. The authority of a delegated run is the intersection of the caller's limits and the callee's definition, the user id passes through unchanged, and the chain is recorded in `parentRef`. This needs an identity per agent, which the runtime gives per session today. Whether agent-to-agent calls currently pass around the gate's caller rules through the egress proxy is *unverified* and is checked first.

### `Workspace`, `CredentialGrant` (0.5, 0.4)

A workspace has sources (Git, an OCI artifact, object storage), a size and a retention time; with the kagent adapter it is the session's durable directory. A credential grant names a subject (an agent or a run), a source (a Secret, a vault path, a Git provider application), an audience, scopes, a lifetime and where it is injected (at the egress proxy, for one host). No value ever appears in a spec or a status.

### Scoped policy and budgets (0.5)

`PlatformPolicy` gets a scope (a namespace or a label selector) and tool rules (allow, require approval, deny); the most restrictive rule wins. `Budget` can select runs by label, which gives cost per work item.

### Runtime contract and conformance (0.4, proven with a second adapter in 0.6)

The `Runtime` adapter gains a run side (submit, get, cancel, respond, subscribe) and reports its capabilities: whether cancel really stops the loop, whether waits survive a restart, how the workspace persists, the longest single step, approvals, push notifications, identity per run. They are shown in `status.capabilities`, and a definition or run that needs a missing one is refused with the reason. A conformance suite states what a second runtime must do for products to be portable.

### Console plugins (with the first product)

First step: a generic read-only page for any allowlisted resource kind, showing its columns and conditions, so a product's kinds are visible with no plugin code. Later a `ConsolePlugin` kind that names navigation entries and one backend Service; the console forwards only to that declared Service, strips identity headers and passes its own signed assertion. That is a narrow, explicit exception to the console's "no relay" rule and gets its own review.

## Roadmap change

The run primitive moves to the front.

| Release | Theme | Main content |
|---|---|---|
| 0.3 | Control plane and console | `AgentDefinition` with the changes above, controller, console, `AgentAccess`, `Schedule` |
| 0.4 | Runs and trust | `AgentRun`, `ApprovalRequest`, identity per agent, `CredentialGrant` (one source), event sink, `Trigger` creating runs, console login with OpenID Connect |
| 0.5 | Work | `Workspace`, approved execution environments with build tools, a long-step tool backed by a Job, `Budget` by run label, concurrency limits and queueing, delegation |
| 0.6 | Lifecycle and quality | agent revisions, evaluation as a blocking gate, the provenance record, a second runtime adapter passing conformance |
| 0.7 | Teams | several namespaces, scoped policies, quotas |
| 0.8 | Do more | the remaining configuration kinds, browser, OpenAPI as tools, an optional workflow adapter |
| 0.9 | Fleet and developers | unchanged |

The model, tool, knowledge and observability configuration kinds of ADR 0007 move from 0.3 to parallel slices or 0.8: they matter for the console, but no product is blocked by them.

A factory product can start when 0.5 is done.

## Limits and risks, stated plainly

- **Unattended agents with rights to merge or deploy.** The pattern to copy is the one GitHub uses: agents get credentials to read and to propose; writes happen in a separate step with narrower rights; the forge enforces branch protection. A merge token is never placed in an agent's sandbox.
- **Scope.** The platform builds the run, approval, credential and workspace primitives, and adopts Tekton, Argo or a durable execution engine behind adapters. It does not become a CI system or a workflow engine. Foundation reviewers decline projects with too much in one repository (ADR 0011).
- **What the runtime can do today.** kagent keeps a task while it waits for input and keeps the workspace across suspend and resume, but a turn that is running when the runtime restarts is lost and reported as failed. A single shell command is limited to ten minutes, so full builds need a Job-backed tool. One task runs per session; many parallel runs share one worker pool and can starve each other. No setting for a maximum number of turns was found, so cost limits come from deadlines, cancel and gateway limits. A product must make its steps safe to repeat.
- **A hard token limit per run** is not possible while one gateway key serves all runs of an agent. The limit is enforced by cancelling at a threshold, which lags by one metrics interval.
- **Evaluation honesty.** Test data that gates a release must stay out of the agent's reach.
- **One maintainer.** The minimal slice of each primitive is the floor; the rest waits for more people.
