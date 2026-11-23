# OpenAgentix notify service (`notify`)

A small **MCP server** that lets an agent deliver a result to a chat. Agents started by a webhook or a schedule have no
reply route (see `docs/decisions/0002-schedules-and-trigger-delivery.md`); `notify` gives them one without giving them
the keys. An operator lists the **named destinations** that are allowed (a Telegram chat, a Slack channel, a webhook).
The agent can only say "send this text to `ops-telegram`". It never sees, and can never choose, a chat id, channel id,
URL or token.

```
 agent (kagent) ──► ToolHive vMCP ──► POST /mcp   notify_send / notify_list_destinations
                      (authenticates          │
                       the agent)             ├─ checks: known name, size, rate, concurrency, idempotency
                                              ├─ cleans text, adds the "automated message" header
                                              └─► Telegram Bot API | Slack chat.postMessage | your webhook
```

One binary (`cmd/notify`), one port (default `:8080`):

| Path | Purpose |
| --- | --- |
| `/mcp` | MCP, streamable HTTP transport (official Go SDK), stateless by default |
| `/healthz` | process is up |
| `/readyz` | at least one destination is configured (otherwise `503`, see "Fail closed") |

## Security: read this first

**The MCP endpoint has no authentication.** It trusts whoever can reach the port. The ToolHive gateway in front of it
authenticates agents, and `deploy/networkpolicy.yaml` admits only the `mcp-tools` namespace. Do not expose the Service
through an Ingress or LoadBalancer. NetworkPolicy only works if your CNI enforces it (Calico, Cilium, ...); on plain
kindnet or flannel it is silently ignored and the port is open to the whole cluster.

### Threat model in plain words

| Who or what | What could go wrong | What stops it |
| --- | --- | --- |
| A prompt-injected agent | Messages a stranger, a customer, or a public channel | Tool arguments only contain a destination **name**. Names map to ids in a config file the agent cannot read or write. There is no argument for a chat id, channel or URL, the schema rejects unknown arguments, and a test checks that nothing an agent sends reaches the upstream request except as message text. |
| A prompt-injected agent | Spams a chat | Per-destination rate limit (default 6 per minute, 60 per hour), a text length limit, and a global in-flight cap. |
| A prompt-injected agent | Pings everyone (`@channel`) or fakes a link or sender | Slack: `&`, `<`, `>` are escaped, so `<!channel>`, `<@U...>` and `<url\|label>` show as literal text; Slack markup, link names and unfurling are switched off. Telegram: plain text, no `parse_mode`. Every message starts with a fixed line `[Automated message from <label>]`, and the same line inside agent text is rewritten so it cannot be forged. |
| A prompt-injected agent | Hides content with control or bidi characters | Control characters and bidirectional overrides are stripped; text is data and is never interpreted. |
| Anyone who can reach the port | Sends as the bot | There is no auth here by design: network policy plus the gateway are the control. |
| A log reader | Learns tokens, chat ids, URLs or message content | Logs carry destination name, platform, outcome, sizes and durations only. Error text returned to agents carries no ids, URLs or tokens, and Go's transport errors (which embed the Telegram token in the URL) are replaced by fixed phrases. Tests assert this on captured log output. |
| A bad config | Silently sends somewhere wrong | The file is validated at startup (unknown fields, bad names, bad ids and non-https URLs are rejected, and typos like `chatID` fail). Nothing secret may be written inline: tokens come from an env var or a file only. |
| A webhook receiver that redirects | Leaks the body or signature to another host | Redirects are never followed. |

Out of scope: preventing a legitimate agent from sending a misleading message to an allowed destination. The header
tells recipients it was automated; the content is the agent's.

## Run locally

```sh
cd services/notify
cat > /tmp/destinations.yaml <<'EOF'   # use a scratch dir; this holds personal data
destinations:
  - name: demo-hook
    description: Local test receiver
    platform: webhook
    url: http://127.0.0.1:9000/hook
    allowInsecureHTTP: true
EOF
NOTIFY_CONFIG=/tmp/destinations.yaml go run ./cmd/notify
```

`make test` needs nothing but Go (everything runs against `httptest` fakes; no real Telegram or Slack call is ever
made). `make docker` builds the image (distroless static, non-root).

## Fail closed

* `NOTIFY_CONFIG` unset, the file missing, or the file defining zero destinations: the service **starts** (so a missing
  optional Secret does not crash-loop), logs a warning, every `notify_send` returns a tool error ("no destinations are
  configured"), `notify_list_destinations` returns an empty list, and `/readyz` answers `503` with the reason.
* A file that exists but is invalid (unknown field, bad value, missing token, unreadable token file): the process
  **exits with an error** that names the destination and field but never echoes values. A half-working config is worse
  than a loud one.
* Tokens and files are read once at startup. Rotating a token means restarting the pod.

## Destinations file (`NOTIFY_CONFIG`)

YAML or JSON. Mount it from a Kubernetes **Secret**, because chat ids are personal data. It never holds a token: tokens
come from an environment variable (`tokenEnv`) or a file (`tokenFile`).

```yaml
destinations:
  - name: ops-telegram            # required; ^[a-z0-9][a-z0-9_-]{0,62}$ ; what agents use
    description: On-call chat for the operations team   # required, one line, max 200 chars; agents see it
    platform: telegram            # telegram | slack | webhook
    chatId: "-1001234567890"      # numeric chat id or @channelname (quote it)
    tokenEnv: NOTIFY_TELEGRAM_TOKEN      # exactly one of tokenEnv / tokenFile
    maxTextChars: 3000            # optional, default NOTIFY_MAX_TEXT_CHARS, max 20000
    rate: {perMinute: 3, perHour: 30}    # optional, defaults NOTIFY_RATE_PER_MINUTE / _HOUR

  - name: eng-slack
    description: "#engineering updates"
    platform: slack
    channel: C0123456789          # a Slack channel id (C..., G..., D...), not a name
    tokenFile: /etc/notify-secrets/slack-token

  - name: audit-hook
    description: Audit trail receiver
    platform: webhook
    url: https://hooks.example.com/notify/abc123   # https only
    # allowInsecureHTTP: true     # opt in to http:// for an in-cluster receiver
    secretEnv: NOTIFY_AUDIT_SECRET    # optional: enables the HMAC signature header (or secretFile)
```

All ids, URLs and secrets above are fake. Fields that do not belong to a platform (for example `channel` on a Telegram
destination) are rejected. Unknown fields are rejected. At most 100 destinations.

### Platform behaviour

| Platform | Request | Notes |
| --- | --- | --- |
| telegram | `POST /bot<token>/sendMessage` with `chat_id` and `text` | Plain text, no `parse_mode`, link previews off. Text is split at 4096 UTF-16 code units (emoji count as two), preferring a newline or space in the second half of a part, never inside a character. On `429` the service waits `retry_after` once per part if it is at most `NOTIFY_MAX_RETRY_AFTER`. If a later part fails the error says how many parts were delivered. Telegram's error description is not relayed. |
| slack | `POST /api/chat.postMessage` with `Authorization: Bearer` | `mrkdwn`, `link_names`, `unfurl_links` and `unfurl_media` are all `false`; `&`, `<`, `>` escaped. `429` handled like Telegram using `Retry-After`. Only Slack's `error` code (for example `channel_not_found`) is relayed, and only if it looks like a code. |
| webhook | `POST` JSON `{"title","text","destination","sent_at"}` | `text` starts with the automated-message header. 2xx is success; `429` is reported as rate limited; no retries, no redirects, response capped at 64 KiB (a larger response is an error). With a secret, header `X-Notify-Signature: sha256=<hex HMAC-SHA256 of the body>`; `sent_at` is inside the signed body so receivers can reject stale requests. |

Message layout (Telegram and Slack): header line, optional title line, blank line, text. For webhooks the header is the
first line of `text` and `title` is its own field.

## Environment variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `NOTIFY_ADDR` | `:8080` | listen address |
| `NOTIFY_CONFIG` | (unset) | path of the destinations file; unset means no destinations |
| `NOTIFY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (JSON logs on stderr) |
| `NOTIFY_REQUEST_TIMEOUT` | `30s` | deadline for one request, **also applied to each MCP tool call** |
| `NOTIFY_SHUTDOWN_GRACE` | `25s` | time to finish in-flight requests on SIGTERM |
| `NOTIFY_OUTBOUND_TIMEOUT` | `10s` | timeout of each call to Telegram, Slack or a webhook |
| `NOTIFY_MAX_RETRY_AFTER` | `10s` | longest upstream `429` wait honoured (once); `0` disables the retry |
| `NOTIFY_MAX_INFLIGHT` | `8` | concurrent sends across all destinations; extra calls get a "busy" error |
| `NOTIFY_MAX_TEXT_CHARS` | `4000` | default text limit in characters (max 20000) |
| `NOTIFY_RATE_PER_MINUTE` | `6` | default per-destination burst and per-minute rate |
| `NOTIFY_RATE_PER_HOUR` | `60` | default per-destination hourly budget |
| `NOTIFY_SENDER_LABEL` | `OpenAgentix agent` | text in the header line (1 to 60 characters) |
| `NOTIFY_IDEMPOTENCY_WINDOW` | `10m` | how long a key remembers its result |
| `NOTIFY_IDEMPOTENCY_MAX_ENTRIES` | `1000` | bound on remembered keys |
| `NOTIFY_MCP_STATELESS` | `true` | no MCP sessions; any replica can answer any request |

Plus whatever variables your destinations name in `tokenEnv` / `secretEnv`.

## MCP tools

### `notify_send`

| Argument | Required | Meaning |
| --- | --- | --- |
| `destination` | yes | a configured name |
| `text` | yes | plain-text body; trimmed, control characters removed, at most the destination's limit in characters |
| `title` | no | one-line title, at most 100 characters |
| `idempotency_key` | no | `A-Za-z0-9._:-`, up to 128 characters |

Result (text and structured): `destination`, `platform`, `delivery_id` (`ntf_...`, generated here), `sent_at` (RFC 3339),
`deduplicated`.

Tool errors (`isError: true`), all safe to show an agent:

| Situation | Message says |
| --- | --- |
| unknown destination | `unknown destination "x": valid destinations are: a, b, c` |
| text empty after cleaning | text must not be empty |
| text or title too long | the length, and the limit for that destination |
| rate limited here | which limits, and `retry in 20s` |
| platform rate limited us | `retry in Ns` (after one honoured retry) |
| busy | in-flight cap reached, or the same `idempotency_key` is still being sent |
| `idempotency_key` reused with different content | use a new key |
| upstream failure | `could not deliver to destination "x" (telegram): telegram refused the message (HTTP 400)`; never ids, URLs or tokens |
| timeout | delivery may or may not have happened; retry later with an `idempotency_key` |
| no destinations configured | notifications are disabled |

### `notify_list_destinations`

No arguments. Returns each destination's `name`, `platform`, `description` and `max_text_chars`. Never ids, channels,
URLs or tokens.

## Limits and caveats

* **Per replica.** The rate limit, the in-flight cap and idempotency memory live in the process. With `replicas: 2`
  each destination can receive twice the configured rate, and a repeated `idempotency_key` is only de-duplicated by
  the replica that saw the first one. The manifest keeps one replica. Telegram and Slack enforce their own limits
  regardless.
* **Rate limit = token bucket**: a burst of `perMinute` is allowed, refilling evenly; the hourly bucket is checked
  too. A refused call consumes nothing; a failed upstream send does consume a token (it still hit the platform). A
  replayed idempotency key consumes nothing.
* **Idempotency** only covers successful sends: a failed send frees the key so a retry actually sends. Same key plus
  different content is refused rather than silently dropped. Keys are scoped per destination.
* **At-most-once is not guaranteed.** If the platform accepted a message but the answer was lost (timeout), the agent
  cannot know; use a key and retry. A multi-part Telegram message can be delivered partially (the error says how many
  parts).
* The sent text is not stored anywhere and not logged.
* No real Telegram or Slack call is made in tests, so platform request shapes follow their public API documentation,
  not an observed exchange. Do a manual send to a test chat after deploying.

## Deploy

Manifests in `deploy/` are not applied by anything in this repo. Create the namespace, then the Secrets imperatively
(nothing secret is in git; commands are at the top of `deploy/deployment.yaml`):

```sh
kubectl apply -f deploy/namespace.yaml
kubectl -n openagentix-notify create secret generic notify-destinations --from-file=destinations.yaml=./destinations.yaml
kubectl -n openagentix-notify create secret generic notify-credentials \
  --from-literal=NOTIFY_TELEGRAM_TOKEN="$TELEGRAM_BOT_TOKEN" --from-literal=NOTIFY_SLACK_TOKEN="$SLACK_BOT_TOKEN"
kubectl apply -f deploy/deployment.yaml -f deploy/networkpolicy.yaml
```

`networkpolicy.yaml` admits ingress only from namespace `mcp-tools` on the MCP port, and allows egress to DNS and to
TCP 443 on public addresses (private ranges and link-local excluded). NetworkPolicy cannot match hostnames, so it cannot
say "only api.telegram.org and slack.com"; use an FQDN-aware policy or an egress proxy for that. Agents then reach the
tool through the gateway entry for `http://notify.openagentix-notify.svc.cluster.local:8080/mcp`.

## Layout

| Package | Role |
| --- | --- |
| `cmd/notify` | wiring, config load, graceful shutdown |
| `internal/config` | environment settings |
| `internal/destinations` | parse and validate the destinations file, resolve secrets, build senders |
| `internal/notify` | policy: lookup, size, rate, concurrency, idempotency, logging, error mapping |
| `internal/sanitize` | control-character stripping, Slack escaping, the header line |
| `internal/ratelimit`, `internal/idempotency` | in-process token bucket and bounded result cache |
| `internal/delivery` | `Message`, `Sender` and the safe `Error` shared by the platform clients |
| `internal/httpx` | no-redirect client, size-capped reads, transport errors without URLs |
| `internal/telegram`, `internal/slack`, `internal/webhook` | platform clients |
| `internal/mcpserver` | the two MCP tools; applies the request timeout to every tool call |
| `internal/server` | `/mcp`, probes, access log, panic recovery, request deadline, shutdown |
