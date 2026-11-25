# ADR 0011: readiness for a foundation (Agentic AI Foundation)

Status: Accepted as direction (2026-11-25). Nothing has been submitted to any foundation. The facts below were read in the foundation's public documents on 2026-11-25 and must be checked again before a proposal. This is not legal advice.

## Context

The project owner wants OpenAgentix to be proposed to the Linux Foundation's Agentic AI Foundation (AAIF), which hosts MCP, A2A, agentgateway, goose, Agent Skills and other agent projects.

## What the foundation asks

Sandbox, the entry stage (from the AAIF project lifecycle policy):

- an OSI-approved permissive licence;
- a working implementation;
- at least one actively committing maintainer, with a documented intent to grow the contributor base;
- a written thesis of one to two pages: who would use it, what would justify the next stage, and early outside interest or a credible argument;
- transfer of the project's trademarks and other assets (name, domain, GitHub organisation, accounts) to the Linux Foundation, and adoption of its technical charter;
- a majority vote of the Technical Committee and approval of the Governing Board.

Membership of the foundation is not required and no fee for a project is documented. A sandbox project must apply for the next stage within twelve months.

Growth, the next stage: production use by at least two unaffiliated organisations, commits from at least two organisations in six months, a documented process for accepting committers.

What reviewers have asked for in practice, from their public comments on declined proposals:

- about three months of steady public history;
- an outside signal: a merged contribution from someone else, or a named organisation using it;
- `CONTRIBUTING.md`, `SECURITY.md`, and a `GOVERNANCE.md` that says how maintainers are added;
- a scope that one project can govern (a proposal with "a lot in one repository" was declined);
- not being tied to one runtime (a proposal read as a framework on one runtime was declined);
- no vendor name in the project name.

## Where we stand

| Requirement | State | Action |
|---|---|---|
| Apache-2.0 | met | add `NOTICE` and a third-party licence table |
| Working implementation | met (release 0.2.0, one kind node) | - |
| One active maintainer | met | document how maintainers are added |
| Contributing, conduct, security, governance, maintainers files | being added now | - |
| Developer Certificate of Origin | not yet | sign-off on every commit from now; a check in CI |
| CI, release process, signed artefacts, SBOM | CI being added now | release workflow with signing and SBOM in 0.3 |
| Thesis | draft being written (`docs/foundation/sandbox-thesis.md`) | - |
| Public history of about three months | no: the public repository dates from 2026-11-24 | time |
| Outside contributor or named adopter | no | people; see below |
| Scope and neutrality | at risk today: mostly wiring of other projects, and tied to kagent | ADR 0007 answers both: own resource kinds, a controller, a console, and adapters |
| Trademark | open | search the registers; settle the commercial name first |

The earliest sensible moment for a proposal is therefore about late February 2027, after release 0.3, and only with at least one outside signal. That date is our reading of reviewers' comments, not a rule.

## Decisions

1. **Target: AAIF at Sandbox.** The fallback is the CNCF Sandbox, where kagent and Agent Substrate are hosted; it rejects pure reference architectures, so the control plane matters there too. One foundation only, never two proposals at once.
2. **The project is `OpenAgentix`, and that name will belong to the foundation.** Its API group stays `openagentix.io`, its domain is `openagentix.org`. Nothing in this repository depends on a commercial product or checks a licence key.
3. **The commercial product does not use the project name as its own.** No "OpenAgentix Enterprise". The accepted form under the Linux Foundation trademark policy is "`<company or product name>` for OpenAgentix". Its code lives in a separate organisation of the company, its resource kinds in an API group under the company's own domain. ADR 0010 is changed accordingly.
4. **"Agentix" as the commercial brand is a risk and is not chosen yet.** It is the distinctive part of the name that will be transferred, so the foundation may see a product called "Agentix ..." as confusingly similar, and many unrelated products already use "Agentix". Before any proposal: a trademark register search, and the question put to the foundation's staff in writing. A company brand that does not share the stem avoids the problem.
5. **Roadmap ownership.** After acceptance the project's steering committee decides the roadmap in the open, not the company. `GOVERNANCE.md` says so from now on.
6. **Every commit carries a sign-off** (Developer Certificate of Origin) from 2026-11-25.

## What needs people, not code

- One or two design partners who run it and agree to be named.
- A first outside contributor with merged work; the adapters, connectors and tests are the places where that is possible without knowing the whole system.
- A second maintainer from another organisation, needed for the Growth stage.
- Public channels: GitHub Discussions now, a regular community call later.
- Legal review of the contribution agreement and the trademark transfer.

## Not checked

The foundation's contribution agreement is not public. Whether it requires a DCO, whether it would cover the shorter "Agentix" mark, and the length of its review are unknown. No trademark register was searched.
