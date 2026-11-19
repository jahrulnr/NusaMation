# Operating the Telegram channel

The channel is a private Telegram bot driven over the Bot API's
`getUpdates` **long polling** — no webhook and no public URL are required.
There is no Telegram library in the binary; the adapter uses a thin stdlib
HTTP client.

## Token and connection

1. Open **@BotFather** in Telegram, run `/newbot`, and copy the token.
2. Web UI → Channels → Telegram, paste the token, and press **Save token**.
   The adapter calls `getMe` first; a token Telegram rejects is never stored.
   A valid token is sealed with the store key (`channel.telegram.token`) and
   is never returned by the settings API — only `tokenSet` is.
3. Press **Connect**. The adapter validates the token once, publishes the
   default `/` menu, then polls `getUpdates` until **Disconnect** or logout.
   The next update offset (`channel.telegram.offset`) is persisted per batch,
   so a restart does not replay old messages.
4. **Forget token** cancels polling, removes the sealed token and clears the
   per-chat menu fingerprints. The bot itself still exists in Telegram;
   create a new token with @BotFather whenever needed.

`allowed_updates` is pinned to `message` and `inline_query`; edited messages,
channel posts and callback queries are not delivered. On every connect the
adapter re-validates the token with `getMe` and republishes the 12-command
default menu. Replacing a token while connected restarts the poll flow
immediately; the old flow's cleanup cannot overwrite the new one.

The same operations are available over HTTP (all authenticated):

```sh
GET  /api/channels                      # channel status and stats
POST /api/channels/telegram/connect
POST /api/channels/telegram/disconnect
POST /api/channels/telegram/logout
GET  /api/channels/telegram/settings
PUT  /api/channels/telegram/settings
```

## Settings

`PUT /api/channels/telegram/settings` accepts a JSON object; omitted fields
keep their current value. The web panel is the normal editor.

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Accept inbound messages and send replies. |
| `autoStart` | `true` | Connect on server boot when a token is saved. |
| `allowedUserIds` | `[]` | Numeric Telegram user ids; a lone `*` allows every sender. |
| `allowedGroupIds` | `[]` | Numeric **negative** group/supergroup chat ids. |
| `groupPolicy` | `mention` | `disabled`, `mention` or `all`. |
| `token` | — | Write-only; a non-empty value is verified before it replaces the saved token. |

**An empty allowlist allows no one.** A message is answered only after an id
or the wildcard `*` is saved, so an unfilled form never becomes a public
agent. `*` must be the only entry across users and groups; combining it with
ids is refused. Group ids must start with `-`: a positive id in the group list
is refused (and dropped from stored legacy settings), because a private
chat's `chat_id` equals the user id — accepting it would silently open that
user's DM.

Group policy: `disabled` ignores groups; `mention` requires an `@bot` mention
entity or literal, a `text_mention` pointing at the bot, a reply to a bot
message, a message sent through this bot's inline mode, or a `/` command;
`all` accepts whatever Telegram delivers, which still depends on privacy mode
being off in @BotFather. Messages older than two hours are dropped before the
allowlist runs. Rejected senders land in a bounded blocked list with
**Allow** (numeric user id) and **Allow group** (negative chat id) actions;
usernames are display-only because they can change.

## Delivery

One message per finished turn, sent with `parse_mode: HTML` and capped at
4,096 **UTF-16 code units** — Telegram's unit, where an astral emoji counts
as two. The agent is told this in its channel rules. Thoughts and tool
calls stay on the web.

Rendered tags: `<b>`, `<i>`, `<u>`, `<s>`, `<code>`, `<pre>`, `<blockquote>`
and `<a href="http(s)://…">`. Markdown, headings and tables do not render.
Every outgoing text — turns, command replies, `/help`, approval pings — passes
one sanitizer: plain `&`, `<`, `>` become entities, unknown or mismatched tags
become text, tags inside `code`/`pre` stay literal, and a tag left open is
closed. If Telegram still rejects the entities, the original text is resent
unparsed and the first error is logged. A 429 `retry_after` hint is honoured
(polling waits without counting a failure; a small send hint retries once
inside the caller's budget).

While a turn runs, the adapter refreshes `sendChatAction` (`typing`) about
every four seconds. It stops on the final send, disconnect or shutdown, and
never creates a message.

## Media

Replies may attach files by writing `[name](path)` or `![name](path)`; the
chat layer collects the paths and the adapter uploads each file as multipart
`form-data` with `chat_id`, `message_thread_id` for forum topics, and a
sanitized caption. The chat layer's 25 MB ceiling is not raised.

- `image/gif` → `sendAnimation`
- other images up to 10 MB → `sendPhoto`; larger images → `sendDocument`
- `video/*` → `sendVideo`
- `audio/ogg` → `sendVoice`; other audio → `sendAudio`
- everything else → `sendDocument`

A caption Telegram cannot parse is retried without `parse_mode`; the file
still arrives. Upload file names are stripped of control characters so an
agent-supplied label cannot inject multipart headers.

Inbound `photo` (the largest rendition), `document`, `video`, `audio`,
`voice`, `video_note`, `animation` and `sticker` are downloaded through
`getFile` and stored as conversation attachments. Stickers arrive as
`image/webp` files, never echoed back as stickers. A file whose declared size
exceeds the Bot API's 20 MB `getFile` limit is not requested at all and the
prompt gets `[File is larger than 20 MB and was not downloaded]`; a failed
lookup or transfer appends a WhatsApp-style marker such as
`[Image could not be downloaded]`. The bot token is redacted from those
errors and the log.

Telegram sends an album as one update per item. Items sharing
`chat_id` + `message_thread_id` + `media_group_id` are buffered for 400 ms
after the last item and delivered as one inbound with at most six
attachments, the first item's id and the captions joined. A pending album is
flushed on disconnect.

## Menus and mentions

The `/` menu starts with `start help new agent model mode set status stop
retry approve deny`. When the linked conversation's ACP session advertises
commands, the chat gets its own scoped menu with those appended (names must
match `^[a-z0-9_]{1,32}$`, descriptions 3–256 characters, at most 100 total).
An empty session list — and `/new` — deletes the scoped menu so the default
one returns. A new token resets the per-chat fingerprints, so the new bot
publishes its own menus.

Typing `@botusername` (after `/setinline` in @BotFather) offers rooms and
projects. Tapping a result inserts `@handle`; Telegram returns the message
with `via_bot` set and the handle becomes a conversation or project
reference. A literal `@botname` only counts as a mention when it is a whole
username token — `@nusa_bot_extra` and `x@nusa_bot` do not. `#`, and file,
folder, skill and MCP search, stay on the web composer.

## Troubleshooting

- **No updates / silent bot**: check **Connected** in the web panel and the
  `state`/`detail` fields from `GET /api/channels`. A revoked token stops the
  poll loop after six consecutive failures; save a fresh token.
- **`getMe failed`**: the token is wrong or revoked. Nothing was saved.
- **No `inline_query`**: inline mode is off — run `/setinline` in @BotFather.
- **Group messages ignored**: privacy mode, group policy `mention`, or the
  group id missing from `allowedGroupIds`. The blocked list shows the chat id
  to allow.
- **Nothing sent**: `enabled` off, no token, or the bot was never connected.
  The web transcript records the channel-delivery error.
