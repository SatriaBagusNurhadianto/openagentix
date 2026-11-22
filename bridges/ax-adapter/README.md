# ax-adapter

A2A 1.0 JSON-RPC `SendMessage` front for Google AX. Same request/response shapes kagent returns, so the Telegram bridge client (or the comparison harness) can talk to either. Each turn is one AX Task: `CreateTask` (user text as `spec.goal.text`, bounded history in `spec.config`) -> `ResumeTask` -> poll `GetTask` -> answer from `status.outcome.output` -> `DeleteTask`.

## Task contract with the agent image

`spec.config = {"contextId": "...", "history": [{"role":"user"|"assistant","text":"..."}, ...]}`, `spec.sessionId = contextId`, no `spec.profile` (it comes from the Workspace; `AX_IMAGE` overrides). The agent writes its answer to `$AX_OUTPUT_FILE`. If that is Claude Code's `--output-format json` object, the adapter uses `result` as the answer, fails the turn on `is_error`, and exposes usage: `inputTokens=input_tokens`, `cachedInputTokens=cache_read_input_tokens`, `outputTokens=output_tokens`, `totalTokens=input+cache_creation+cache_read+output`, under task metadata `https://kagent.dev/extensions/usage/v1`; `total_cost_usd`, `num_turns`, `cache_creation_input_tokens` go under `openagentix.dev/ax/claude`. Any other output is the plain-text answer.

Failures (task failed/cancelled, non-zero exit, `is_error`, create/resume error, timeout, oversized message) are HTTP 200 with `TASK_STATE_FAILED` and the text in `status.message`.

## Behaviour

- History is in memory per `contextId`: last `MAX_HISTORY_TURNS` pairs, oldest pairs dropped until `spec.config` JSON fits `CONFIG_BUDGET_BYTES` (AX hard limit 16 KiB). Only successful turns are recorded. Lost on restart; an unknown supplied `contextId` starts a new conversation. Messages over `MAX_MESSAGE_BYTES` are rejected, not truncated.
- Turns on one context are serialized; at most `MAX_CONCURRENT_TASKS` (default 2) AX tasks run at once.
- Tasks are always deleted (fresh context, so also after timeout/cancel) unless `KEEP_TASKS=true`.

## Config (env, flags override)

| Env | Default |
|---|---|
| LISTEN_ADDR | `:8080` |
| AX_SERVER | `ax-server.ax-system.svc.cluster.local:8080` (plaintext gRPC) |
| ATESPACE | `default` |
| AX_WORKSPACE | `assistant-ws` (selects the agent variant: the Workspace pins the agent image; `deploy/` uses `assistant-ws-v1` = Claude Code launcher and `assistant-ws-api` = Messages-API launcher) |
| AX_GATEWAY | `anthropic-gw` (empty = none) |
| AX_IMAGE | unset |
| TURN_TIMEOUT / POLL_INTERVAL | `170s` / `500ms` |
| KEEP_TASKS | `false` |
| MAX_HISTORY_TURNS / CONFIG_BUDGET_BYTES / MAX_MESSAGE_BYTES | `8` / `12288` / `8192` |
| MAX_CONCURRENT_TASKS / MAX_CONTEXTS | `2` / `1000` |

## Build

`go.mod` has `replace github.com/google/ax => /Users/gshah/work/agentix/ax` (checkout at commit 0b44e971, needs Go 1.27.1; the toolchain auto-downloads). Because of that replace the image is built from a prebuilt binary: `make test`, `make build` (CGO off, linux/arm64), `make image`. Manifests: `deploy/deployment.yaml` (Service `ax-adapter:8080`, variant A') and `deploy/deployment-b.yaml` (Service `ax-adapter-b:8080`, variant B) in the existing namespace `openagentix-gateway`; `deploy/namespace.yaml` is only for a fresh cluster (the namespace is owned by the Helm release here, do not apply it over it). Both variants write the same Claude-Code-style JSON object to `$AX_OUTPUT_FILE`, so the adapter code is identical. See `spikes/ax/compare/` for the measured comparison with kagent.
