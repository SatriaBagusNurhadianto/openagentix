# ADR 0010: community and enterprise editions

Status: Accepted as direction (2026-11-25). The licence text of the enterprise edition and the pricing are business decisions that need legal review; this record fixes the technical split and the rules, so that code can be written on the right side from now on.

## Context

The product brief asks for an open-source core with an enterprise version and a revenue model around it. The roadmap (`docs/roadmap.md`) now lists far more than one edition can sensibly gate. The project owner asked for the split to be decided and is ready to build both editions at the same time.

## Rule

> What one team needs to build, run and govern agents safely on one cluster is community. What an organisation needs because it has many teams, auditors, several clusters and a support contract is enterprise.

Three consequences, which are commitments:

1. **Security is never an enterprise feature.** Isolation, egress control, the A2A gate, tool policy, guardrails, secret handling, login to the console, and every security fix are community.
2. **The community edition is complete, not a trial.** No feature is limited by count (agents, users, nodes) and nothing in the community code checks a licence.
3. **A feature that is community stays community.** Nothing moves from community to enterprise later.

## The split

| Area | Community (Apache-2.0, this repository) | Enterprise |
|---|---|---|
| Resource kinds | every `openagentix.io` kind of ADR 0007, with its controller and adapters | additional kinds only (see below); never extra fields that change a community kind |
| Runtime, gateways, tools, memory, knowledge, graph, channels, schedules, notify | all of it | connectors to business systems that need vendor agreements or heavy upkeep (for example SharePoint, Salesforce, ServiceNow) |
| Console | the whole console: builder, playground, every configuration page, costs, traces | per-user identity and writes, console roles per team, an audit view |
| Login | one operator token; OpenID Connect login (one identity provider, everyone who logs in is an operator or a viewer) | single sign-on with groups mapped to roles, SCIM provisioning, fine-grained roles |
| Tenancy | one namespace, one platform policy | tenants and teams (`Tenant`, `TeamPolicy`, `TeamQuota`), several namespaces, delegated administration |
| Governance | platform policy, gate, tool profiles, egress allowlists, memory expiry, skill approval by one approver | approval workflows with routing and escalation, policy packs mapped to regulations, separation of duties |
| Audit | audit lines on pod output, exportable through the observability adapter | tamper-evident audit history with retention, search and export for auditors |
| Cost | cost and tokens per agent and model, budgets per agent, alerts | chargeback and showback per team and cost centre, budgets in money across teams, forecasts and reports |
| Model routing | static, rules, the router service | routing policies per team, an optimisation report with recommendations |
| Quality and safety | guardrail kinds and engines, evaluation and red-team runners | scheduled evaluation with release gates across environments, compliance reports |
| Lifecycle | versions, environments, canary | promotion across clusters with approvals |
| Skills and registries | the organisation skill registry, agent and tool registries | signed skills with enforced provenance, a private marketplace across tenants |
| Credentials | Kubernetes Secrets, write-only in the console | external vault integrations, key rotation, on-behalf-of-user brokers for many providers |
| Fleet | one cluster | several clusters (`Cluster`, `Fleet`), a central console, air-gap bundles with support |
| Operations | one-command install, upgrades | high-availability profiles, backup and restore tooling, long-term support releases, support with response times |

## How the two editions are built

- **Two repositories.** The community edition is this repository. The enterprise edition is a private repository that depends on community releases. It contains no fork of community code.
- **Enterprise adds, never patches.** Enterprise features are extra kinds in the API group `enterprise.openagentix.io`, extra controllers, extra adapters and extra console pages. If an enterprise feature needs a change in community code, that change is a general extension point and lands in community first.
- **Extension points in community**, built when the first enterprise feature needs each one:

| Extension point | Used by |
|---|---|
| `Authenticator` in the console API (who is this request) | single sign-on, per-user identity |
| `Authorizer` in the console API (may this identity do this) | roles, tenancy |
| Policy sources (the controller merges policies from more than one place, most restrictive wins) | team policies |
| Admission hooks before a definition is accepted | approvals, separation of duties |
| Audit sink interface | audit history |
| Console plugin registry (routes, navigation entries, panels) | every enterprise page |
| Adapter registry (adapters register at start) | vault, fleet, connectors |

- **Packaging.** One Helm chart for community. The enterprise chart depends on it and adds its own controllers and a console image that includes the enterprise plugins. An installation becomes enterprise by installing the second chart, and goes back by removing it. Community resources keep working either way.
- **Licence key.** Checked only inside enterprise components. When a key expires, enterprise controllers stop accepting changes to enterprise kinds and say so in status; nothing that already runs is stopped, and community behaviour is unaffected.
- **Licence of the code.** Community: Apache-2.0, unchanged. Enterprise: a commercial licence in a private repository. Source-available is an option to decide later with legal advice.

## Building both at once

The community roadmap does not wait for enterprise. Enterprise work starts where it does not need missing community pieces:

| Community release | Enterprise work that can run next to it |
|---|---|
| 0.3 control plane and console | the console's `Authenticator`, `Authorizer` and plugin registry are designed in from the start (community ships the token and basic OpenID Connect implementations); enterprise starts single sign-on with groups and console roles |
| 0.4 trust | audit history, approval routing, vault integrations |
| 0.5 lifecycle | promotion across clusters |
| 0.7 teams (the multi-namespace mechanics are community) | tenants, team policies, quotas, chargeback |
| 0.9 fleet mechanics | central console, fleet management |

The first enterprise slice is therefore single sign-on and console roles, and it can begin when the console API exists (ADR 0007 slice 3). The private repository is created then.

## What this rules out

- A "community" build that is the enterprise build with features switched off.
- Limits on the number of agents, users or nodes in community.
- Putting a security control behind the enterprise licence.
- Enterprise code in this repository, including in its history.
