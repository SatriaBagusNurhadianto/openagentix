# Security policy

## Supported versions

Only the latest release gets security fixes. OpenAgentix is at version 0.x and the interfaces are still changing, so older releases are not patched. Fixes land on `main` and in a new patch release. See [CHANGELOG.md](CHANGELOG.md) for releases.

| Version | Supported |
|---|---|
| Latest release | yes |
| Older releases | no |
| `main` | best effort |

## Reporting a vulnerability

Do not report a vulnerability in a public issue, a pull request or a Discussion.

Use GitHub private vulnerability reporting:

1. Open the repository on GitHub: https://github.com/openagentix/openagentix
2. Go to the **Security** tab.
3. Choose **Report a vulnerability**.

Only the maintainers can read the report. If you are not sure whether something is a vulnerability, report it privately anyway. A private report can be made public later. A public issue cannot be made private.

This project does not publish an email address for security reports.

### What to include

- What you found, and which part of the repository it is in (a chart template, a Go service, an installer script, a document).
- The version or commit.
- Steps to reproduce, with the smallest example you can make. A values file or a manifest is ideal.
- What an attacker gains, and what they need first (network position, a Kubernetes role, a Telegram chat on the allowlist).
- Any fix or workaround you have in mind.
- Whether you plan to publish, and when.

Do not include real keys, tokens or personal data. Use made-up values.

## What to expect

| Step | Target |
|---|---|
| We acknowledge your report | within 3 working days |
| First assessment (is it a vulnerability, how serious, who is affected) | within 14 days |
| Fix or a written plan for one | depends on severity; we will tell you the date we aim for |

The project has one maintainer today, so these are targets, not guarantees. If you hear nothing after the acknowledgement time, write a comment on the private report to nudge us.

## Disclosure process

1. We confirm the problem and work out the affected versions in the private report.
2. We prepare a fix in a private fork or a draft security advisory, and test it.
3. We agree a disclosure date with you. The default is up to 90 days after the report, and sooner when a fix is ready. Actively exploited problems are handled faster.
4. We publish a GitHub security advisory, with a CVE where it is warranted, and a patched release. The `CHANGELOG.md` entry sits under "Security". We credit you if you want to be credited.

We do not take legal action against people who report in good faith and keep to this process.

## Scope

In scope: the code and configuration in this repository.

- The Helm chart in `charts/openagentix` (default values, templates, NetworkPolicies, RBAC).
- The Go services and tools: `services/a2a-gate`, `services/control-plane`, `services/kb`, `services/notify`, `tools/a2a-call`, `bridges/telegram`.
- The installer and uninstaller scripts and the scripts under `components/` and `hack/`.
- Documentation that tells people to do something unsafe.

Out of scope here, report it to the upstream project:

| Component | Where to report |
|---|---|
| kagent | https://github.com/kagent-dev/kagent/security |
| Agent Substrate (and its gVisor worker) | https://github.com/kagent-dev/substrate/security |
| agentgateway | https://github.com/agentgateway/agentgateway/security |
| ToolHive | https://github.com/stacklok/toolhive/security |
| Agent Kourier | https://github.com/agentkourier/agent-kourier/security |
| gVisor | https://github.com/google/gvisor/security |
| Gateway API | https://github.com/kubernetes-sigs/gateway-api |
| VictoriaMetrics | https://github.com/VictoriaMetrics/VictoriaMetrics/security |
| Jaeger | https://github.com/jaegertracing/jaeger/security |
| PostgreSQL and pgvector | https://www.postgresql.org/support/security/ and https://github.com/pgvector/pgvector/security |
| llama.cpp | https://github.com/ggml-org/llama.cpp/security |

The full list of what the platform installs is in [THIRD-PARTY.md](THIRD-PARTY.md). If you are not sure which side a problem is on, report it to us privately and we will route it.

Also out of scope, because they are documented limits and not vulnerabilities: the "Known limitations" and "Security" sections of the README list things that are not protected yet (for example, kagent's insecure auth mode, and kindnet not enforcing egress rules). A bypass of a protection that the project says it provides is in scope.

## Security principles in this repository

These are the rules the design follows. A break of one of them is a good report.

- **Decisions are written down.** The governance and threat decisions are in [ADR 0006](docs/decisions/0006-governance.md). The diary page `docs/diary/features/security.md` lists what is protected and what is not.
- **Off by default, deny by default.** New features are off until an operator turns them on. With them off, the default render is unchanged. Network access is closed by NetworkPolicy and opened per peer.
- **No secrets in the chart.** The chart only references Secrets by name. The installer creates Secrets only when absent, never prints them, and keeps them on re-runs.
- **No secrets or content in logs.** Services log counts, names and outcomes, not message text, document text, tokens, chat ids or addresses.
- **Agents never hold provider keys.** Each agent has one gateway virtual key, so use can be budgeted and logged per agent.
- **Untrusted text stays untrusted.** Text from users, webhooks and documents reaches a model as data. Output to chats is cleaned.
- **Pinned and verified inputs.** Upstream releases are pinned. Images are pinned by tag, and most of them also by digest (the two MCP server images are pinned by tag only). Downloads are checked by SHA-256, and Agent Kourier's chart and image by `cosign` when it is installed.
- **Isolation.** Agents run in gVisor sandboxes through Agent Substrate.

The platform is an MVP that has run on one kind node. Do not treat it as hardened for a hostile environment.
