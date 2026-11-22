# Telegram bridge

Relays Telegram chats to an A2A 1.0 agent (JSON-RPC `SendMessage`) and sends the
agent's reply back. It long-polls Telegram's `getUpdates`, so it needs no inbound
connection from the internet. Go, standard library only.

## Behaviour

- One A2A conversation (`contextId`) per Telegram chat, kept in memory.
- **One user identity per chat.** Every A2A request carries the header `X-User-Id: <prefix><chat id>` (default
  `telegram:<chat id>`, for example `telegram:42`). kagent keys its sessions and its long-term memory by (agent, user
  id), so one chat never sees what another chat asked the agent to remember. The id comes only from the chat id Telegram
  puts on the update (a number), never from message text. A **group chat is one identity**, keyed by the group's chat id
  (a negative number such as `telegram:-1001234567890`): everyone in the group shares the group's sessions and memory,
  and what one member asks the agent to remember is recalled for all members of that group (and for nobody outside it).
  `/new` starts a fresh conversation but keeps the identity, so saved memories survive it. Verified against kagent
  1.0.1 in `insecure` auth mode: the header is honoured and `:` and `-` are accepted. In other auth modes kagent takes
  the user from the authenticated principal and ignores this header.
- `/new` (or `/new@<this bot's username>`) starts a fresh conversation. The bot
  resolves its own username once at startup with `getMe`; commands addressed to
  other bots are not treated as `/new`.
- Chats are served concurrently (up to `MAX_CONCURRENT_TURNS`); messages within one
  chat are handled strictly in order.
- Only text messages are handled. Replies longer than Telegram's 4096 UTF-16 code
  unit limit are split at a newline, else a space, else mid-text (never inside a
  character).
- Only a completed task, an input-required or auth-required question, or a direct
  agent message is relayed. Failed, canceled, rejected, submitted, working and
  unknown task states produce a generic apology; details go to the log only.
- Telegram 429 responses are honoured (`retry_after`, up to 2 retries, at most 30 s
  each). A 401 from Telegram (bad token) stops the process with exit code 1.
- Failed polls back off exponentially from 3 s up to 1 min.
- On SIGTERM/SIGINT the bridge stops polling and waits up to `SHUTDOWN_GRACE` for
  in-flight turns before cancelling them.

## Configuration (environment)

| Variable | Default | Meaning |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | required | Bot token. Never logged. |
| `A2A_URL` | required | A2A JSON-RPC endpoint. |
| `A2A_TOKEN` | empty | Bearer token for the agent, if it needs one. |
| `TELEGRAM_ALLOWED_CHAT_IDS` | empty | Comma-separated chat ids allowed to talk to the agent. |
| `TELEGRAM_ALLOW_ALL` | `false` | Set `true` to accept every chat. Startup fails unless this or the allowlist is set (fail closed). |
| `A2A_USER_ID_PREFIX` | `telegram:` | Prefix of the per-chat user id. Give each bot or bridge its own prefix when several share one agent. Printable ASCII, no spaces, at most 64 characters. |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | Override for tests. |
| `AGENT_TIMEOUT` | `120s` | Per-turn deadline for the agent call. |
| `MAX_CONCURRENT_TURNS` | `8` | Agent turns in flight across all chats. |
| `SHUTDOWN_GRACE` | `25s` | Wait for in-flight turns on shutdown. Keep below the pod's `terminationGracePeriodSeconds`. |
| `HEALTH_ADDR` | `:8080` | Listen address for `/healthz`; empty disables it. |

## Health

`GET /healthz` returns `200` while the last successful `getUpdates` is at most 3x
the poll timeout (90 s) old, and `503` otherwise (also before startup completes).
Use it as a Kubernetes liveness probe.

## Known limits

- **At-least-once delivery.** The polling offset is held in memory only. Telegram
  confirms updates when the next `getUpdates` carries a higher offset, so after a
  restart the last unconfirmed batch can be delivered again and answered twice.
  Updates still queued in memory at shutdown (their offset already advanced) can be
  dropped.
- Conversation contexts are in memory: a restart forgets them, and every chat
  starts a new agent conversation.
- Rejected chats are logged at Debug only, so they do not show in default logs.
- Per-chat queues are unbounded; a flooded chat can queue messages while its turn
  runs.
- Telegram and agent response bodies are capped at 4 MiB.
