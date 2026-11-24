# ADR 0005: Runtime speed, kagent versus AX, and whether to build on AX

- **Status:** Accepted. Owner decision, 2026-11-24: continue with kagent for now (option 1). Options 2 and 3 (AX as a second runtime, AX as the base) are kept on record here for a later review.
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

## Tuning results

Owner decision 2026-11-24: stay on kagent 1.0.1, tune it through our layer only (chart, installer, upstream Helm values, prompts). Done on 2026-11-24 against the three weak points above, with the existing harness (`components/bench/run.sh` tooling, one `assistant` agent, no AX), traces from the bundled Jaeger and the sources of kagent 1.0.1, Agent Substrate 1.0.1, agentgateway v1.7.0 and google/adk-go v2.6.0 (read only).

### A. Memory saving

Root cause. kagent has no automatic save in the Go runtime. The only way a memory is written is the model calling the `save_memory` tool (`go/adk/pkg/runner/adapter.go:45`, `go/adk/pkg/memory/save_memory_tool.go:16`); the session summariser `AddSessionToMemory` exists but nothing calls it and no model is passed to it (`go/adk/cmd/main.go:164`). Harness `spec.kagent.memory` has only `modelConfigRef` and `ttlDays`. So a save is a tool call, and after a tool call the ADK always sends the result back to the model: a second model round trip in the same turn. In the trace of the benchmark's first turn (`baa0fcd619e5469fa19c1cc89b6955be`): model call 1 took 1.5 s (184 output tokens, finish reason tool call), the tool `save_memory` 84 ms, call 2 took 0.67 s, 16,190 prompt tokens in total. The second call cannot be avoided by wording, and the save cannot be moved after the answer. Our own instruction made the model save any preference it was told.

Owner decision, 2026-11-24: the change below was measured and then reverted. The default stays automatic saving (the model saves what looks worth keeping), at the price of the second model call. The save-on-request wording is kept here as an option for a values file.

Change tested. `memory.agentPrompt` in `charts/openagentix/values.yaml`: save only when the user asks to remember something ("remember", "keep in mind", "note that", "from now on", "always"); do not save facts that are merely mentioned. A fact told without a request is answered in one model call and is not kept. This is a deliberate trade: predictable latency and cost over automatic capture.

Measured (runtime part of turn 1, wall time minus the wait before dispatch, `assistant`, calm node):

| | n | median | min | max | model calls |
|---|---|---|---|---|---|
| before, "My favourite colour is teal ... Just say OK." | 3 | 3.96 s | 3.87 | 4.69 | 2 (16,190 tokens) |
| after, same prompt | 6 | 2.7 s | 2.19 | 3.98 | 1 (8,037 tokens) |

Feature proof (user `bench:tune:alice`, then `bob`; each request a new conversation with no contextId): "Please remember that my project codename is BLUEHERON-42" saved it; a new conversation of the same user answered `BLUEHERON-42`; a new conversation of another user answered `Unknown`. A fact only mentioned ("My favourite colour is teal") was not saved and was not recalled later (`Unknown`), as designed. The test rows of these two users stay in `kagent.memory` for 30 days.

Not fixable here: a save will always cost one more model call when it happens. Upstream would need either a save that ends the turn without a second model call (the ADK has `SkipSummarization` on tool results, the kagent tool does not set it) or the periodic session save that the Python runtime has (EP-1256, "every 5 user turns", in the background).

### B. Prompt caching

Root cause of "no cache read". Nothing was marking the request. agentgateway only adds Anthropic cache marks when the policy asks for it: `AgentgatewayPolicy.spec.backend.ai.promptCaching` (CRD field verified on the cluster; `AgentgatewayModel.spec.policies` has no such field, and the controller maps only this policy field, `controller/pkg/agentgateway/plugins/backend_policies.go:950`; marks are added in `crates/llm/src/conversion/messages.rs:26` and `:547` unless the client sent its own). kagent's OpenAI-compatible path sends none. The usage translation does not drop anything: the gateway span carried `cache_read.input_tokens=0` and `cache_creation.input_tokens=0` before, so the fields are there (`crates/llm/src/conversion/messages.rs:993`, `crates/agentgateway/src/telemetry/log.rs:1898`). The prompt is far above any provider minimum (about 8,000 tokens, of which the tool list is the larger part).

Prefix stability. kagent puts the memory preload into the system instruction, after the static prompt, on every model request (`google.golang.org/adk/v2/tool/preloadmemorytool/tool.go`, `AppendInstructions`), so the system prompt differs per user message when memories are found. The tool list is first in Anthropic's order and is stable. Evidence: across 7 new conversations of 7 different users the gateway reported `cache_read` 6,635 tokens each time (the stable prefix); inside a conversation the second call read 8,036. A system mark alone would have missed whenever a memory is recalled, so the chart also marks the end of the tool list.

Change. `gateway.policy.promptCaching` (`enabled: true`, `cacheTools`, `cacheSystem`, `cacheMessages`, `minTokens`), rendered into the one existing policy as `spec.backend.ai.promptCaching` (`charts/openagentix/templates/gateway.yaml`). **On by default** (the default render changes; tests updated deliberately). A cache entry lives 5 minutes; the call that writes it costs 25% more than a plain input token.

Measured (group a, a new conversation per run, `assistant`, calm node; model time is the gateway's `POST /v1/chat/completions` span; cost is the gateway's `agw.ai.usage.cost.total` from its price catalog):

| | n | model call median | prompt tokens | cache read | cost per call |
|---|---|---|---|---|---|
| before | 8 | 0.715 s (0.662 to 0.781) | 7,998 | 0 | 0.000803 USD |
| after | 7 | 0.753 s (0.707 to 0.970) | 8,029 | 6,635 | 0.000209 USD (74% less) |

Latency: no gain on the model call itself (the small model's time does not depend on the prompt size; the difference is inside the noise). The wall time of the 8 turns went from median 2.67 s to 2.04 s, but the baseline had one 4.1 s outlier and I do not claim that. The gain is cost. A tool-loop call inside a turn read 8,036 of 8,070 prompt tokens.

Not fixable here: kagent drops the cached count. Its own span and the A2A usage show `cache_read 0` while the gateway shows thousands, because the chat-completions adapter reads only `PromptTokens` and `CompletionTokens` (`go/adk/pkg/models/openai_adk.go:535`, `:477`), whereas its Responses adapter maps `cached_tokens` (`openai_responses.go:445`). Use the gateway span or the gateway metrics for cache numbers. Also: the memory preload in the system prompt limits the cached part to the tools and the static head of the prompt when a memory is recalled; moving the preload into the user message would be an upstream change.

Side effect: the per-key token budget counts the full prompt tokens. A benchmark with 8,000-token calls reached the 1,000,000 tokens per hour budget of the `assistant` key after about 125 calls and the next call got `429 Too Many Requests`.

### C. Slow start of some new conversations

What the traces show (series of 30, 24 and 14 sequential first turns on `assistant`, node load 1.5 to 2.8, one request every 8 or 6 s, plus 19 back-to-back runs). Of the new conversations started 8 s after the previous one ended, 16 of 54 waited 2 to 14 s before the controller sent the message to the agent; with a 6 s pause 0 of 14, back to back 0 of 19. (Which pause matters is an observation, not an explanation.) The wait is not in the agent and not at Substrate:

- The controller span `POST /agents/` starts at 0. The controller's `SendMessage` span (the call to the Substrate router) starts at 10.09 s in the slow trace `78b63d35284d46f3b8fd55fd90830e02`, at 0.1 to 0.4 s in fast ones. After it, the resume (router to the agent) takes 0.6 to 1.0 s in both cases, so restoring the golden snapshot is not slow (atelet log: all `filecache: hit`, restore 0.69 s).
- ateapi (both replicas) logs `CreateActor` at 07:25:35.862 and `CreateActorEgressPolicy` at .875 and then nothing for 10.1 s; the next ateapi event is the router's `ResumeActor` at 07:25:45.99 (restore 0.7 s). So Substrate is idle during the wait. Scheduling is not the cause either: one worker, 1000 actor slots, `Picked worker` immediately after the resume starts.
- Not DNS (400 lookups of the router name from a pod in 60 s, none above 0.5 s), not node load (load 1.5 to 2.8 during the slow ones), not the database log (kagent-pg logs show no slow statement), not a worker or pool limit.
- The 16 waits cluster at 9.2 to 10.2 s (8), 14.1 to 14.2 s (2), 5.1 to 6.2 s (2) and 2.2 to 4.2 s (4). A 10 s timer exists in exactly one place on this path, `reserveDispatch` (`go/core/internal/service/session/interactions.go:218`), which polls every 100 ms while `ReserveSessionDispatch` reports busy (`go/core/internal/database/session_tasks.go:32`, `requireSettledRuntime` at `:823`). Turn 2 and 3 of a conversation show the same waits (for example turn 2 of `d8f93680bb6e43238243d678dbcbe990`, 7.6 s), where the previous task's idle suspend (`go/core/internal/service/session/quiescence.go:35`, synchronous suspend through `go/core/internal/substrate/client.go:215`) is the documented reason for the busy state. For a brand new session no busy state should exist, so this is a hypothesis, not a proof.

Root cause: not established. The wait is inside the kagent controller between creating the actor's egress policy and sending to the router, and I could not see inside it without a debug build of the controller or database lock logging (not allowed here). What would settle it: spans or debug logs around `FinishSessionOperation`, `reserveDispatch` and the pool in the controller; `log_lock_waits` and `log_min_duration_statement` in kagent's PostgreSQL. This needs an upstream change or a debug build; there is no kagent or Substrate setting for it (the only related settings are `config.sessions.quiescenceRecoveryInterval`, which paces a recovery scan, and the router's `--parked-request-budget` of 5 s, which governs capacity errors that did not occur).

Warm pools and workers. Substrate 1.0.1 has no warm pool of pre-resumed actors: an actor is created from the template snapshot and resumed on demand (`cmd/ateapi/internal/controlapi/workflow_resume.go`). Pool size is the number of worker pods (`WorkerPool.spec.replicas`, one here) times `--max-actors` (1000 per worker); the scheduler only books against worker capacity (`cmd/ateapi/internal/scheduling/scheduling.go`). Nothing bounds concurrent actor starts; the atelet's only concurrency knobs are for background snapshot upload and detach (`--snapshot-upload-workers`, `--snapshot-detach-workers`; delay 5 and 30 minutes by default). A second worker would not help a CPU-bound node and costs memory the node does not have, so I did not test it. The concurrency result of the benchmark (7 of 8, median 56 s, load 89) shares no cause with the wait above that I can show; it is node CPU.

What I did find, and it matters for the concurrency result: the platform falls into a restart loop under load, and it was already in it when I started (load 46 to 58 for 20 minutes with 46 pods on 6 CPUs, and a database container of another project on the same Docker host using 120% CPU). Evidence: `ate-idp-server` restarted 123 times, `kagent-controller` 134, `agentgateway` 110, Substrate `postgres-0` 74 times in 26 to 40 hours; the events say `Liveness probe failed ... timed out after 1s` (postgres is probed with `pg_isready`, a Perl script started through the container runtime) and the controller exits with `fetch Actor JWT discovery ... connection refused` while the IdP is down. The probe timeouts are Kubernetes defaults (1 s) and the Substrate chart hard-codes the probes of the IdP, atelet and router (`charts/substrate/templates/ate-idp-server.yaml:57`), `ateIdpServer.resources` is empty (BestEffort, first to be starved). From our layer I changed the one probe we own: the liveness probe of Substrate's PostgreSQL manifest (`components/runtime/manifests/substrate-postgres/postgres.yaml`, `timeoutSeconds: 5`, `failureThreshold: 6`). **Not rolled out and not measured**: `install.sh` does not re-apply that manifest on an existing cluster and I was only allowed to run the installer. Giving `ateIdpServer` and the worker pool CPU requests through `components/runtime/values/substrate.yaml` is the next candidate (installer does not upgrade Substrate on an existing cluster either; it needs `FORCE_PREREQS=1`).

Summary of what is fixed and what is not:

| Weak point | Result | Fixed from our layer |
|---|---|---|
| A. save costs a second model call | 1 call instead of 2 for a mentioned fact; saving on request still costs one more call | measured, not adopted (owner decision: keep automatic saving) |
| B. no cache reads | cache reads on every call, 74% lower cost per call, no latency change | yes (kagent still reports 0) |
| C. slow start | located in the controller before dispatch, cause not established | no |
