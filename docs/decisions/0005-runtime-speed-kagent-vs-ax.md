# ADR 0005: Runtime speed, kagent versus AX, and whether to build on AX

- **Status:** Accepted for the measurement. The recommendation (stay on kagent, tune it, keep AX as an optional second runtime) is proposed and waits for the owner's decision.
- **Date:** 2026-11-23
- **Supersedes:** the speed figures in [ADR 0001](0001-agent-runtime-kagent-vs-ax.md). Its other findings stand.
- **Evidence:** [`components/bench/RESULTS.md`](https://github.com/openagentix/openagentix/blob/main/components/bench/RESULTS.md), method in [`components/bench/README.md`](https://github.com/openagentix/openagentix/blob/main/components/bench/README.md).

## Context

ADR 0001 measured kagent at 12.2 s for a plain question and AX at 4.1 to 7.1 s. That made kagent look slow. Speed matters to the owner, who also asked whether building on AX would be manageable.

That first measurement had problems. It ran on an overloaded 5-CPU node, it had no traces, and kagent ran into its own token budget. So it was repeated.

## What was measured

Same cluster (one kind node, 6 CPU, 8 GB), same model, same prompts, 5 runs each, sequential, a new conversation per run, each phase started only when the node was quiet. Every request carried a trace id.

Backends:

- `kagent`: the `assistant` agent as deployed (memory, knowledge base, graph, notify, OSV tools).
- `kagent-nomem`: the same with memory off.
- `kagent-lean`: memory off, one tool server. This is the like-for-like partner of AX.
- `ax-claude`: AX running Claude Code.
- `ax-api`: AX running a minimal model loop.

## Results

Median seconds. All 125 sequential runs answered correctly. None was throttled.

| Test | kagent | kagent-nomem | kagent-lean | ax-claude | ax-api |
|---|---|---|---|---|---|
| Plain question | 2.8 | 2.5 | 2.9 | 7.6 | 3.4 |
| Tool question (2 model calls) | 6.9 | 6.5 | 6.0 | 11.1 | 7.4 |
| Skill | 4.5 | 3.7 | 8.9 | 7.7 | 5.7 |
| 3-turn conversation | 18.0 | 9.9 | 8.4 | 17.8 | 10.4 |
| Second turn of a conversation | 3.3 | 2.9 | 3.0 | 5.5 | 4.0 |

Worst single run, plain question: kagent 14.5 s, kagent-lean 13.7 s, ax-api 5.2 s, ax-claude 9.7 s.

Median prompt tokens per run, plain question: kagent 8,005, kagent-lean 3,397, ax-claude 8,919, ax-api 1,214.

Four identical questions at once, two rounds:

| Backend | Answered | Median request | Batch wall time |
|---|---|---|---|
| kagent-lean | 7 of 8 | 56.5 s | 62.6 s |
| ax-api | 8 of 8 | 19.0 s | 35.8 s |

The node load average reached 89 during this phase.

Where kagent's time goes (from traces, plain question, 2.5 s in total): the model call 0.9 s, the time before the agent starts working 1.0 s, everything else 0.6 s. Memory recall costs 0.1 to 0.2 s per turn.

Cold start, kagent-lean: first turn of a new conversation took 2.1, 5.1, 6.6 and 14.4 s. A second turn right after took 2.4 and 2.6 s. A second turn after 120 s idle took 2.8 and 4.9 s.

## What this means

1. **kagent is not slow.** In sequential use it is as fast as the minimal AX loop or faster, and about twice as fast as AX with Claude Code. The 12 s in ADR 0001 came from the overloaded node, not from kagent. We cannot say more exactly what changed.
2. **Memory recall is cheap, memory saving is not.** With memory on, the first turn of the 3-turn conversation made a second model call to save a memory. That doubled the conversation time (18.0 s against 9.9 s).
3. **kagent has a tail.** About one new conversation in five waits 5 to 14 s before the agent starts. AX was steadier in this test.
4. **kagent sends more tokens.** About 8,000 prompt tokens per call as deployed, 3,400 lean. No call reported a cache read, so prompt caching is not active. This is cost, and some latency.
5. **Neither runtime handles four parallel conversations well on this node.** kagent was worse: one request failed and the median was 56 s. AX queued instead (its adapter admits two tasks at a time, its pool has one worker). On a single 6-CPU node this is mostly a capacity limit, and we have not measured either runtime on a larger cluster.
6. **AX with Claude Code pays 4 to 6 s of start-up per turn.** That is the harness, not AX.

## Building on AX: what it would take

Unchanged either way: the model gateway, the tool layer, the knowledge base and graph, notify, schedules, observability backend, the chart and the installer. The runtime is one layer.

| Gap in AX today | Work for us | Size |
|---|---|---|
| No invocation API | Harden `bridges/ax-adapter` (authentication, streaming, error paths) | Medium |
| No conversation state | A session store. The adapter keeps eight turns in memory and loses them on restart | Medium |
| No memory | Our own memory service and recall tool, reusing the knowledge base store | Medium |
| No declarative agent | Our own Agent resource and controller, or chart-rendered AX Workspaces | Medium to large |
| Cannot authenticate to our tool layer | AX refuses to inject a token toward a plain-HTTP service. The tool layer needs TLS, or AX needs a change. The benchmark used a separate anonymous endpoint | Small to medium, unproven |
| No traces | The adapter and the harness must emit spans | Small to medium |
| Channels | Agent Kourier speaks the kagent dialect. A good A2A adapter covers it | Small |
| Concurrency | One worker per running task. Pool sizing and tests | Configuration and testing |
| Maturity | No releases. We pin a commit. Its own Claude Code runner did not start against its server | Ongoing risk |

AX's real advantages: the provider key never enters the sandbox, egress is denied by default, full process checkpoint and restore, any harness, lean prompts, steadier latency.

## Options

1. **Stay on kagent and tune it.** Least work. Fix what the numbers show: memory saving, prompt size, caching, the wake-up tail, concurrency.
2. **Two runtimes.** kagent stays the default. AX runs stateless jobs (schedules, webhooks) where lock-down and steady latency matter and no conversation is needed. Work: adapter hardening and TLS for the tool layer.
3. **AX as the base.** We own the agent resource, sessions, memory and tracing. Several build rounds, and a dependency on a project with no releases.

## Recommendation

Option 1 now. Speed was the reason to consider AX, and the measurement removes it.

Keep option 2 open. It is worth doing when a customer needs the stronger isolation, and the adapter work is also the first step of option 3. Do not start option 3 until AX has releases and an invocation API of its own.

Next work on kagent, in order of expected gain:

1. Make memory saving cheaper: save in the background or at the end of a conversation, not as an extra model call in the first turn.
2. Turn on prompt caching at the gateway and check that cache reads appear.
3. Find the cause of the 5 to 14 s wake-up on some new conversations.
4. Repeat the concurrency test with a second worker, and on a larger node when one is available.
5. Give each agent only the tools it needs. The lean agent used less than half the tokens.

## Limits of this measurement

- Five runs per cell on one small node. Medians are indicative. There is no p90.
- AX used a tool endpoint with three tools. In ADR 0001 it had more, so its prompts were larger then.
- AX reaches the provider directly with the key injected at egress. kagent goes through our gateway. The model time was the same in both (0.9 s), so this did not change the result.
- For AX there are no spans. Its split comes from the egress log and is coarser.
- The AX cold-start numbers are not usable. One run followed the concurrency phase on a loaded node, and in the repeat the harness failed for the minimal loop.
- The concurrency result says as much about the node as about the runtimes.
- AX was removed from the cluster after the run. The steps to install it again are in the bench README.
