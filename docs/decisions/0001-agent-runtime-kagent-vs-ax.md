# ADR 0001: Agent runtime for the MVP — kagent over Google AX

> **Update, 2026-11-23:** the speed figures below were taken on an overloaded node. A repeat with traces found kagent as fast as AX or faster in sequential use. See [ADR 0005](0005-runtime-speed-kagent-vs-ax.md).

- **Status:** Accepted for the MVP, confirmed after running the same agent on both runtimes. Revisit under the conditions in "When to revisit".
- **Date:** 2026-11-22 (revised the same day; see "Revision history")
- **Evidence:** `components/runtime/NOTES.md`, `spikes/ax/NOTES.md`, a source check of AX at commit `0b44e971`, and the same-flow measurements in `spikes/ax/compare/RESULTS.md`

## Decision

OpenAgentix uses **kagent 1.0 on Agent Substrate** as the runtime for chat and event-triggered agents.

**Google AX (Agent Executor)** is not the MVP runtime. We built the same agent on it and it worked: same model, same tools, same skill, same prompts, every answer correct, and turns were not slower than kagent's. But to get there we had to write the parts kagent supplies: the agent loop, the conversation memory, the A2A front, the images and the install. AX has no declarative agent and no Kubernetes-native packaging. It is a proven candidate for a second backend aimed at sandboxed batch and coding-agent work, and the adapter for that now exists as an experiment in `bridges/ax-adapter/`.

The contract between our gateway layer and the runtime is the A2A protocol, not kagent's own API. The Telegram bridge calls the agent through a small interface, so a different backend can be added without touching the channel code.

## Why this needed deciding

The original plan leaned towards AX. The spec asked for the choice to be well researched. Rather than decide from documentation, we built both on one machine, compared them, and then checked every AX claim against the AX source.

## How the comparison was done

Both runtimes were installed on the same kind cluster: Kubernetes 1.37, one arm64 node, Rancher Desktop VM with 5 CPUs and about 7.7 GiB of memory. Both ran on the same Agent Substrate 1.0.1 install, so the sandbox layer (gVisor) is identical and the comparison is about what each project adds on top.

| | kagent | AX |
|---|---|---|
| Version tested | 1.0.1, published Helm charts | `main` at commit `0b44e971`, built from source |
| Run on the cluster | Agent answering over A2A across several turns, then wired to Telegram, a model gateway, MCP tools and skills with a real model | Server running; a Task executing in a sandbox; suspend and resume; then the full agent flow: a real model with the key injected at the egress gateway over HTTPS, MCP tools, a skill, and three-turn conversations through an A2A adapter |
| Read in docs or source, not run | Long-term memory (pgvector) | Router ingress to a running Task. AX's own Claude Code runner was tried and does not start against this AX server (see "Same flow on both runtimes") |
| Not tested on either | Load, concurrency, a cold install from an empty cluster | |

The first pass did not test the two to equal depth; kagent received far more time. The second pass closed most of that gap by building the MVP's agent on AX and sending both the same prompts.

## What the MVP needs from a runtime

The MVP demo is an agent on Kubernetes, connected to tools, skills and a model, triggered from a messaging platform. That needs:

1. A way to declare an agent: instructions, model, tools.
2. A way for another service to send it a message and get a reply, across several turns.
3. MCP tools and Agent Skills.
4. Kubernetes-native packaging, because the product promise is a Helm install and GitOps-friendly configuration.
5. A permissive license and neutral governance, because we plan an open core.

## Summary

| Need | kagent 1.0.1 | AX at `0b44e971` |
|---|---|---|
| Declare an agent | `Agent` resource: prompt, model, tools, skills | No agent kind. A `Task` runs an image's command; an `Agent` spec is on the roadmap |
| Single request and reply | Yes, A2A `SendMessage` | Yes: goal in, output in task status once the command exits |
| Multi-turn conversation | Yes, `contextId` | No. No message to a running Task, no stored history |
| Trigger from webhook or chat | Through an A2A call | Through a gRPC `CreateTask` call |
| Chat or webhook connectors built in | No (we wrote the Telegram bridge) | No |
| MCP tools | Attached by reference, used by the supplied agent loop | Declared on the Workspace, handed to your program as a config file |
| Skills | OCI artifacts, loaded by the agent | Skill registries on the Workspace, consumed by your program |
| Kubernetes-native | CRDs, Helm, GitOps | Rows in the AX server's database, applied with the `ax` CLI; no Helm chart |
| Maturity | 1.0, published charts and images | Pre-release, no published images |

## Findings

### 1. Agent abstraction

kagent has `Agent`, `AgentTemplate`, `Harness` and `ModelConfig` resources. An agent is a prompt, a model reference, a list of tools and a list of skills. The runtime that executes it is supplied.

AX has `Task`, `Workspace`, `Model` and `Gateway`. A Task means "run this image's command in a sandbox, with this workspace". The agent is whatever program you put in the image. An `Agent` spec is on the AX roadmap and does not exist yet.

The original instinct was right that a Task with a Workspace can run an agent. The spike confirmed it: a Python program in an AX sandbox called an LLM and wrote its answer to the workspace. AX also ships a Claude Code runner that builds a headless `claude --print` run from `spec.config` and the Workspace's MCP servers. What AX lacks is a conversation around that program, and a way to describe the agent without building an image.

### 2. Invocation

kagent serves A2A 1.0 on a stable in-cluster endpoint. We sent `SendMessage`, got a completed task with the answer, sent a second message with the returned `contextId`, and the agent remembered the first turn. Streaming works as well. The Telegram bridge was written against this in a few hundred lines.

AX has a request and response path, but it is one-shot.

What AX has:

- **Goal in, output out.** `CreateTask` takes `spec.goal.text` and `spec.config`. The runner hands them to the task's command as files (`AX_GOAL_FILE`, `AX_CONFIG_FILE`, `AX_SESSION_FILE`). When the command exits, the runner reports the last 64 KiB of its output, or the contents of `AX_OUTPUT_FILE`, and the server stores it as `status.outcome.output`. `GetTask` and `ax describe task` return it. We verified this on the cluster: a task with a goal went from created to `Completed`, with its output in the status, in about five seconds.
- **A programmatic API.** The `AX` gRPC service (create, get, list, delete, suspend, resume) and an alpha Go `client` package. Anything that can call gRPC can start a Task, so a webhook or a chat message can trigger one through a small adapter.
- **Exec into a running Task.** With `spec.debug: true`, port 80 serves Substrate's guest process and file services over gRPC. This is what `ax ssh` uses. It works programmatically, but it grants arbitrary process execution and is off by default.

What AX does not have, confirmed in the source:

- **No way to send a message to a running Task.** There is no update or message RPC; a Task's goal is fixed at creation. The roadmap lists "Task Communication and Steering Protocols" as future work.
- **No conversation state across Tasks.** `spec.sessionId` is an identifier passed to the command. Nothing stores history. Sessions, memory and workspaces shared between tasks are roadmap items.
- **No chat or webhook connectors, and no A2A.** The only mention of a webhook in the repository is a code comment in the runner docs.
- **No inbound policy.** The `Gateway` kind covers egress only; the proto says ingress is intentionally omitted while Substrate settles its model.
- **No application port.** Port 80 in the sandbox serves the runner's health probes and, in debug mode, the guest services. The task's own command is not exposed there. Whether a task can be reached on another port through the Substrate router was not tested.
- **No authentication on the AX API** that we could find; the client dials plaintext gRPC.

There are two ways to put chat on AX. Neither ships with AX. We built the first.

1. **One Task per message.** An adapter creates a Task with the message as the goal, polls until it completes, and returns `status.outcome.output`. It uses only supported API. Ours came to about 880 lines of Go plus tests, more than the few hundred first estimated. The adapter has to carry conversation history itself and pass it in on every turn, within the 16 KiB limit on `spec.config`. Each turn is a fresh sandbox with no memory of the last.
2. **One long-lived Task per conversation.** A custom runner serves the agent's HTTP interface, and an invoker routes messages to it through the Substrate router. This keeps state in the sandbox, which suits AX's checkpointing, but it means building a runner, an invoker and per-conversation authorization, because the router lets any allowlisted identity reach any actor.

Option 1 makes AX a workable backend for stateless or short-history agents. It does not make AX an agent platform: the agent loop, tool use, skills and memory are still whatever program we put in the image.

### 3. Kubernetes-native packaging

kagent's resources are CRDs. They work with `kubectl`, Helm and GitOps, and report status on the resource. Our chart renders agents from a values list.

AX's four kinds are not CRDs. They are rows in the AX server's PostgreSQL database, applied with the `ax` CLI or the gRPC API. GitOps would need a pipeline that runs `ax apply`. There is no Helm chart; the repository ships two plain manifests, for the server and its database.

### 4. Install and maturity

| | kagent 1.0.1 | AX |
|---|---|---|
| Artifacts | Published charts and images | No published images; the default example image is private |
| Install | Six Helm releases plus a key bootstrap step | Build two images from source, run a registry for digest pinning, edit Substrate authorization, deploy Postgres and the server by hand |
| Automated installer | `kubectl ate install` exists | `ax install` fails on Substrate 1.0.1: it patches a container named `atenet-router`, which that Substrate version no longer has |
| Stability | 1.0 with compatibility commitments | Pre-release; the README says "heavy development" with breaking changes expected |
| Governance | CNCF Sandbox, Apache-2.0 | Google, Apache-2.0 |

kagent is not a simple install either. The six releases have fixed names and a strict order, and one required value is missing from the docs. That cost is shared, though: AX needs the same Substrate underneath.

### 5. Tools, skills, memory

kagent: MCP servers attach by reference, skills load from OCI artifacts, and both were verified with a real model. The agent called an OSV vulnerability tool and loaded a skill unprompted. Long-term memory exists (pgvector) but was not exercised.

AX: a Workspace declares MCP servers and skill registries. The runner writes the MCP servers to a config file in the common `mcpServers` format and points the command at it, so a harness such as Claude Code picks them up directly. Any other agent program has to read that file and do the tool calling itself. Sessions and memory are roadmap items. In the same-flow experiment both routes worked on the first try once the images existed: Claude Code picked the tools up from the file, and our own 200-line loop called them itself. The skill was delivered as a Workspace file.

### 6. Multi-tenancy

Neither is ready for hostile tenants. kagent's open-source build authorizes every API caller, and A2A authentication is off by default. AX sets no worker selector: in the spike an AX Task ran on kagent's worker pool even though AX had its own, so a second pool does not isolate workloads. The AX API itself appears to be unauthenticated.

## Same flow on both runtimes

After the first two passes we built the MVP's `assistant` agent on AX and measured it against the kagent one. Full method, raw rows and caveats are in `spikes/ax/compare/RESULTS.md`.

Three backends, all on `claude-haiku-5-5` with the same system prompt, the same ToolHive MCP endpoint and the same skill:

- **kagent:** the live `assistant` agent, unchanged.
- **AX with Claude Code:** the default `ax-runner` starting Claude Code for each Task.
- **AX with a minimal loop:** the default `ax-runner` starting a 200-line Python agent loop that we wrote.

Both AX backends sit behind `bridges/ax-adapter/`, which speaks the same A2A `SendMessage` as kagent, creates one Task per turn and replays up to eight earlier turns from memory.

Four prompts, five runs each, sent one at a time from inside the cluster. Latency is the client's wall-clock time.

| Prompt | kagent | AX, Claude Code | AX, minimal loop |
|---|---|---|---|
| Plain question | 5/5 correct, median 12.2 s | 5/5, 7.1 s | 5/5, 4.1 s |
| Tool call (OSV lookup) | 3/3, 16.0 s (2 more runs refused, see below) | 5/5, 10.7 s | 5/5, 8.3 s |
| Skill (canary phrase) | 5/5, 6.5 s | 5/5, 7.4 s | 5/5, 5.7 s |
| Three-turn memory | 5/5, 31.3 s | 5/5, 18.1 s | 5/5, 11.5 s |

What this shows:

- **AX can run this agent.** All 40 AX runs completed and were correct, including the three-turn conversations that AX has no native support for.
- **A fresh sandbox per turn is not slow.** The fastest AX turn took 3.1 s end to end. The earlier worry that one Task per message would be too slow for chat did not hold.
- **kagent was not faster on any prompt except, narrowly, the skill.** Most of the gap is not the runtime. The three backends run different agent loops with different prompt sizes (about 5,000 tokens per call for kagent's, 10,000 for Claude Code, 2,400 for ours), and kagent's calls go through the model gateway while AX called the provider directly. kagent's first two runs were also unusually slow (28.5 s and 22.9 s) and we did not find out why.
- **Every kagent failure was our own rate limit.** Seven kagent attempts were refused with HTTP 429 by the model gateway's token budget of 100,000 tokens per hour for the agent's key. One tool question costs about 27,000 tokens, so the demo's default budget allows only three or four of them an hour. That is a setting to raise, not a runtime defect. AX had no such failures because it has no budget control at all.

What it cost to get AX there, against twelve lines of chart values for the kagent agent:

- An agent program: either Claude Code in an image with a launcher script, or a loop we wrote and now own.
- The adapter, to supply A2A, conversation history, failure mapping, concurrency limits and task cleanup. It does not stream, does not return tool-call records, has no authentication, and loses history on restart.
- Two images built from AX source, a registry for digest pinning, edits to Substrate's authorization, and a Gateway and Workspace manifest.
- A dead end: AX's own Claude Code runner never started. The AX server at this commit hands work to runners that ask for it, and the shipped Claude Code runner only supports the older mode where work is pushed to it, so its Task stayed `Queued` until deleted. We did not patch AX. The "AX with Claude Code" column is therefore Claude Code under the default runner, not AX's Claude Code runner.

## Where AX is better

These are real strengths, and reasons to keep watching it:

- **Egress control with credential injection.** A Task has no network access unless a Gateway rule allows a host. The control test confirmed this: without the rule the same program could not resolve the host. Credentials can be injected at the egress proxy so the sandbox never holds the key. In the same-flow experiment this ran over HTTPS against the real provider: the key lived in one Secret outside the sandbox, and the Task, the Workspace and the adapter never held it.
- **Full process checkpointing.** After suspend and resume, the workspace files survived and the running Python process came back restored, not restarted.
- **Fast one-shot runs.** A complete turn in a fresh sandbox, including one model call, took as little as 3.1 seconds. The measured medians are in the table above; they compare different agent loops as well as different runtimes.
- **Any harness.** A Task runs any image, which suits Claude Code or similar coding-agent CLIs working on a cloned repository. kagent's bring-your-own-agent path is narrower.
- **A simple job model.** Goal in, exit code and output out, with retries planned. For batch work this is a better fit than a conversation protocol.

## Why kagent, given all that

The experiment removed two arguments we might have used. AX can hold a conversation if an adapter carries the history, and it is not slower. The decision does not rest on either.

It rests on what we would have to build and maintain to make AX a product, all of which kagent supplies:

1. **A declarative agent.** With kagent a customer configures an agent in a dozen lines of YAML. With AX they, or we, build and publish an image containing an agent program. The agent builder and control plane planned for v1 need something to declare; on AX we would have to invent it.
2. **The agent loop and the conversation.** On AX the loop, tool calling, skill loading and history are ours. The minimal loop was fast because it does little. Keeping it correct as tools, skills and memory grow is the work kagent's harness already does.
3. **Kubernetes-native resources.** CRDs and Helm are central to the product promise of a single-command install and GitOps configuration. AX's resources live in its own database behind a CLI.
4. **Something we can ship.** kagent has published 1.0 artifacts. AX has no published images, an installer that fails against current Substrate, and a shipped Claude Code runner that does not start against its own server at the commit we tested. We would be distributing our own builds of a project that says to expect breaking changes.
5. **Governance through one gateway.** kagent's model calls go through the model gateway, which gives per-agent keys, budgets and cost attribution. The budget is what throttled the test, which shows it works. The AX path in this experiment had none of that. Routing AX through the same gateway is probably possible and was not tried.

What AX does better, egress lockdown and one-shot job semantics, matters most for batch and coding-agent work, not for the chat agent the MVP demonstrates.

## Consequences

- We accept kagent's requirements: Kubernetes 1.37 or later, nodes that can run gVisor, PostgreSQL, and a multi-step install that our `install.sh` wraps.
- We depend on Agent Substrate either way. Both candidates run on it.
- Slower turns than AX showed: medians of 6.5 to 16 s per turn against 4 to 11 s. Acceptable for chat. The unexplained slow first runs and the size of the harness prompt are worth investigating before any latency-sensitive use.
- The default token budget of 100,000 tokens per hour per agent is too low for tool-using agents: three or four tool questions exhaust it. The chart default should be raised. (Done: the default is now 1,000,000 tokens per hour.)
- kagent's weak authorization becomes our problem to close before any shared deployment. It is also a natural place for the commercial layer.
- Agents stay behind the model gateway, which gives us the "sandbox never holds the provider key" property that AX offers natively.
- Because the bridge talks A2A, an AX backend can be added without changing the channel code. `bridges/ax-adapter/` proves this in principle; it is an experiment and not part of the install. The Telegram bridge was not pointed at it.

## When to revisit

The experiment suggested in the previous revision has been run; see "Same flow on both runtimes".

**Add AX as a second backend** when a customer need appears for sandboxed batch or coding-agent jobs with locked-down egress. The adapter is the starting point. It would need authentication, durable history and a route through the model gateway first.

**Reconsider the primary runtime** if AX ships any of the following:

1. Task communication: a supported way to send a message to a running Task.
2. The `Agent` spec and sessions.
3. Published images and an installer that works against current Substrate.

The likely shape then is AX as a second backend for long-running, sandboxed coding tasks, called through the same interface, with kagent remaining the runtime for conversational agents.

## Limits of this comparison

- One laptop, one node, one day. Nothing was load tested, and no figures here describe behaviour under concurrency.
- The same-flow timings are medians of five sequential runs (three for kagent's tool prompt). Differences of a few seconds are inside the spread.
- The three backends run different agent loops and take different paths to the model, so the timings and token counts do not isolate the runtime.
- kagent's skill and three-turn runs were repeated alone after its token budget refilled, not interleaved with the AX runs.
- AX's own Claude Code runner was not measured, because it did not start.
- Correctness was a string check, not a judgement of answer quality.
- AX moves quickly. Findings apply to commit `0b44e971`; the install failure in particular may already be fixed upstream.
- A cold install of the full kagent stack from an empty cluster has not been repeated end to end.

## Revision history

- **2026-11-22, first version.** Stated that AX returned no task output and had no way for an application to get an answer from a Task.
- **2026-11-22, revised.** That was wrong. AX reports a task's output into `status.outcome.output` when its command exits. The spike missed it because its test command never exited. Every other AX claim was checked against the source and held. Finding 2, the summary table, "Where AX is better", the rationale and "When to revisit" were rewritten. The decision is unchanged, on narrower grounds.
- **2026-11-22, second revision.** Built the MVP's agent on AX behind an A2A adapter and measured it against the kagent agent with the same prompts. AX answered every prompt correctly and was not slower. Added "Same flow on both runtimes"; rewrote the rationale, which no longer leans on multi-turn or speed; recorded the token-budget finding. The decision is unchanged.
