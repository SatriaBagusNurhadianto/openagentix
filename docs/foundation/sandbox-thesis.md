# OpenAgentix: Sandbox thesis (draft)

**Status: a draft for a possible future proposal. Nothing has been submitted to the Agentic AI Foundation (AAIF) or to any other foundation, and nobody there has been contacted.** Facts about the foundation come from its public project lifecycle policy, read on 2026-11-25. See [ADR 0011](../decisions/0011-foundation-readiness.md). Statements about other projects are my reading of their public pages and must be checked before submission.

Repository: https://github.com/openagentix/openagentix. Licence: Apache-2.0. Version: 0.2.0. One maintainer ([MAINTAINERS.md](../../MAINTAINERS.md)).

## What it is

OpenAgentix is a Kubernetes-native platform for running AI agents under policy. An operator describes agents, tools, models, memory and channels as Kubernetes resources in the API group `openagentix.io`. A controller turns them into running agents, and a platform policy decides what they may use. It is for the team that wants to run agents on its own clusters, with its own data, and without a per-call fee.

## Who would use it

- **Platform and DevOps teams** that must offer agents to other teams, and need to say which models, tools, networks and budgets each agent gets.
- **Teams in regulated or data-sensitive settings** that want memory, knowledge, traces and keys to stay in their own cluster.
- **Builders of agent products** who want a neutral, GitOps-friendly control plane and do not want to be tied to one runtime or one model vendor.

Early interest from outside the project: none documented yet. See "Honest state".

## The problem, and what the hosted projects do not cover

The foundation already hosts the parts: MCP for tools, A2A for agent-to-agent calls, agentgateway as the data plane for models and tools. CNCF hosts kagent (an agent runtime) and Agent Substrate (sandboxed, resumable execution). Each solves one layer. A team that wants an agent platform has to join them, and the result is wiring: a fixed install order, key material, one set of policies per tool, and no single description of "this agent, with these tools, this budget, this egress".

OpenAgentix is that missing layer. It composes MCP, A2A and agentgateway with kagent and Substrate into one governed platform, and adds:

- **A neutral resource model.** `AgentDefinition` first, then the other kinds in [ADR 0007](../decisions/0007-control-plane-and-agent-builder.md): schedules, channels, model routes, tool servers and profiles, knowledge, skills, budgets.
- **A controller and a platform policy.** A definition outside the policy is refused before it costs anything.
- **Governance already built:** an A2A gate with per-caller rules, tool profiles, per-agent egress, memory expiry, and an audit trail ([ADR 0006](../decisions/0006-governance.md)).
- **A web console** for building and watching agents (in progress).
- **Adapters.** The upstream tools sit behind small interfaces so that one can be exchanged for another. Today there is one implementation of each. A second implementation of the runtime is recorded as an alternative in ADR 0005, not built in the platform.

## How it relates to other projects

| Project | Relation |
|---|---|
| MCP | OpenAgentix uses it for tools. It does not extend the protocol. Tool access is governed above it. |
| A2A | Used between callers and agents, through a gate that adds caller rules. No change to the protocol. |
| agentgateway (AAIF) | The model gateway today. OpenAgentix configures it from resources, and it is behind an adapter. It is a dependency, not a competitor. |
| goose | A different thing: an agent you run yourself. A goose agent can be a client of, or later run on, this platform. Not built. |
| Agent Skills | Skills are mounted into sandboxes from digest-pinned OCI images. A skill registry with approval is planned (ADR 0007). |
| OpenSandbox | A candidate for the sandbox adapter. Today the sandbox is gVisor through Agent Substrate. |
| AGENTS.md | A format for guidance to coding agents. Not used by the platform; may inform how agent definitions are described. |
| kagent, Agent Substrate (CNCF) | The runtime today. The project depends on them and wants to be able to work without them (see below). |

## Why this is not only a reference architecture

A reference architecture is a document and some manifests. OpenAgentix has new code that does not exist upstream: the `openagentix.io` kinds, a controller with admission policy, the A2A gate, the knowledge base and graph service, the notify service, the Telegram bridge, and the console. These are tested Go modules and a chart with several hundred render assertions (`make test`). An honest limit: most of the repository is still wiring, and the control plane is at its first slice ([CHANGELOG](../../CHANGELOG.md)).

## Why it is not tied to one runtime

Every upstream tool is behind an adapter interface, and the controller core never imports an adapter's types (ADR 0007). The decision to stay on kagent today is recorded with its alternative ([ADR 0001](../decisions/0001-agent-runtime-kagent-vs-ax.md), ADR 0005). The test of this claim is a second runtime adapter, which does not exist yet. It is a Growth-stage task, and a reviewer should treat the claim as a design intent until then.

## Neutrality and what a foundation would change

- No feature depends on a commercial product, and nothing checks a licence key ([ADR 0010](../decisions/0010-community-and-enterprise-editions.md)).
- The project would transfer its name and assets to the foundation. The original author plans a separate commercial product that uses the project, under the foundation's trademark rules and under a different name ([ADR 0011](../decisions/0011-foundation-readiness.md)). Reviewers should know this and may weigh it.
- The roadmap is decided in the open by the maintainers ([GOVERNANCE.md](../../GOVERNANCE.md)).

## What would justify Growth

From the lifecycle policy, and where we are:

| Growth criterion | Today |
|---|---|
| Production use by two unaffiliated organisations | none; the platform has run only on one kind node |
| Commits from at least two organisations in six months | none; one maintainer |
| A documented committer process | written in GOVERNANCE.md, not yet used by anyone but the author |
| A growth plan with diverse maintainership | the plan is in GOVERNANCE.md, "Growing the maintainer group" |

## Honest state and risks

- One maintainer, and a public history that starts on 2026-11-24. No outside contributors and no named users.
- Proven on a single kind node (6 CPU, 8 GiB). Not run on a managed cluster, not load tested, not audited. Slack is untested. The console is not built.
- Security: kagent runs in an insecure auth mode in this setup, and kindnet does not enforce egress rules; the README lists the limits.
- **Scope risk:** reviewers have turned down proposals that were "a lot in one repository" or a framework tied to one runtime. The answer is the control plane and adapters, and it has to be shown in code in release 0.3.
- **Name risk:** the commercial brand is not chosen and no trademark search has been made.
- **Bus factor:** one person. This is the main risk, and the main goal is to reduce it.

## Plan for the next 6 to 12 months

From [docs/roadmap.md](../roadmap.md). Sizes are relative. Dates depend on people, not only on code.

| When | Milestone | Outcome |
|---|---|---|
| Months 0 to 3 | **0.3 Control plane and console.** `AgentDefinition`, controller, platform policy, console with builder and playground, then the other kinds. DCO check, release signing and an SBOM. | The scope claim is shown in code. |
| Months 0 to 6 | **Find people.** One or two design partners who agree to be named. A first outside merged contribution. A second maintainer from another organisation. | The Growth criteria become reachable. |
| Months 3 to 6 | **0.4 Trust.** An identity per agent, credential store, guardrails, human approval, console login with OpenID Connect. | Security that does not rely on network position. |
| Months 6 to 9 | **0.5 Lifecycle.** Immutable agent versions, environments, canary releases, registries. | Teams can promote agents safely. |
| Months 9 to 12 | **0.6 Quality.** Datasets, evaluation, red-team runs, a release gate, and the model router with measured savings. A second runtime adapter is the aim, to prove the adapter claim. | Evidence that the neutrality is real. |

At 6 months: the checkpoint. At 12 months: apply for Growth or accept archive. Both are normal outcomes under the policy.

## What I ask of the foundation

Neutral governance early, while there is still one maintainer, a sponsor from the Technical Committee, and a review of the trademark and contribution terms. I do not ask for funding or marketing. The project's README would say that Sandbox status is not an endorsement.
