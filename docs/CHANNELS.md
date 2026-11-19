# Channels and routing

## The contract

Every channel implements `internal/channels.Channel`:

```go
type Channel interface {
    Name() string
    Label() string
    Start(context.Context) error
    Stop() error
    Status() Status
    Begin(context.Context, Target, string) (Delivery, error)
    Notify(context.Context, Target, string) error
}
```

- `Begin` starts a streaming outbound message and returns a `Delivery`. Platforms without edits can return a delivery that only sends on `final=true`.
- `Notify` sends a one-off message (command replies, approval pings, mirrored digests).
- `Status` is safe to poll and is broadcast as `channel.status` by the manager.
- `Start` must not block application boot. Network connection work runs in goroutines.

Controllable channels additionally implement `Controller` (`Connect`, `Disconnect`, `Logout`, `QR`, `Settings`, `UpdateSettings`). The HTTP API type-asserts this interface.

Channels whose surface has its own message format implement `ResponseStyler` (`ResponseRules() string`). The chat service includes these rules in a compact `<nusamation-context>` text block at the end of each prompt (WhatsApp markup vs the web UI's full Markdown). This is client context in a user-role message, not an ACP system prompt. Channels without a style still receive input and delivery metadata, but no formatting rules. See [ACP prompt context](ACP_PROMPTS.md) for channel switching, steering, and automation behavior.

Channels with a native poll widget may implement `PollAsker` (`SendPoll(ctx, target, question, options, maxSelectable) → pollMessageID`). The chat service then delivers approvals and single-field choice questions as polls instead of text. Incoming votes on those polls must be delivered as `Inbound.PollVote{PollMessageID, Selected}` (option names, not platform hashes) so the orchestrator can resolve the pending question; votes on polls the user created stay regular `Text`. Without poll support approvals fall back to `/approve <id>`; questions point to the web UI.

Adapters receive a `channels.InboundHandler` (the chat orchestrator) and call:

```go
Handler.HandleInbound(ctx, channels.Inbound{
    Channel: "whatsapp", ExternalID: chatJID, ThreadID: "", SenderID: senderJID,
    SenderName: pushName, ChatName: chatName, MessageID: messageID, Text: text,
    Attachments: attachments, IsGroup: isGroup, MentionsMe: mentioned, Timestamp: ts,
})
```

The handler deduplicates by `(channel, MessageID, ThreadID)`, finds/creates a linked conversation, runs commands, and queues a prompt.

## Link resolution

`channel_links` is unique on `(channel, external_id, thread_id)`. A conversation can have several links (e.g. a WhatsApp DM and a Telegram DM).

- Existing link → use its conversation.
- No link → create a conversation with the first enabled agent and add the link.
- `/new` → create a new conversation, move the current chat link from the old conversation to the new one. The old transcript stays available on web.
- `/agent` → switch the linked conversation's agent and start a fresh ACP session with a transcript recap on the next message.

## Channel commands

Slash-prefixed messages are handled by the chat service, not the agent.

- `/new [slug]` — start a fresh conversation and move this chat's link to it.
- `/agent [slug]` — list enabled agents, or switch the linked conversation's agent.
- `/model [selector]` — list available model values, or apply a selector `value[k=v,...]`. The left side matches model values case-insensitively; `k=v` modifiers are applied against the catalog re-read after the model switch. Ambiguous matches list candidates. Examples: `/model claude-opus-4.5`, `/model claude-opus-4.5[effort=high]`.
- `/mode [value]` — list modes, or set the active mode. Agents exposing a `mode` config option get `session/set_config_option`; legacy agents get `session/set_mode`.
- `/set <key> <value>` — set any advertised config option; `<key>` resolves by option id, then category, then name.
- `/approve [id-prefix]` `/deny [id-prefix]` — without an argument, list pending approvals with id prefixes. A unique prefix resolves the approval.
- `/stop` — cancel the current turn.
- `/status` — show linked agent, model, mode and session state.
- `/help` — list the commands above plus commands the current ACP session advertises (shown only while the session still owns them).

Unknown commands close to a NusaMation command get a "did you mean" hint; everything else is forwarded to the agent as text.

## Routing

Each conversation stores:

```json
{
  "replyToOrigin": true,
  "mirrorFinal": false,
  "notifyApprovals": true
}
```

- **replyToOrigin** — the final answer is sent to the linked chat matching the turn's origin channel.
- **mirrorFinal** — the final digest is also sent to every other linked external chat.
- **notifyApprovals** — an approval request pings the origin chat with `/approve <id>` / `/deny <id>` instructions.

Web always receives every event. External channels never receive thoughts or raw tool input/output.

Default routing for new conversations is in **Control center → Routing**. Per-conversation overrides are in the top bar's link menu.

## WhatsApp

### Pairing

The adapter stores the whatsmeow device in `data/whatsapp.db`. It does not auto-connect unpaired devices; press **Connect** to get a QR channel. The QR is rendered server-side as PNG and refreshed automatically until pairing succeeds. The device announces itself as **Chrome (NusaMation)** in the phone's linked-devices list.

Channel settings live under the `channel.whatsapp.settings` key and can also be managed from the CLI:

```bash
nusamation whatsapp show
nusamation whatsapp set --enabled --group-policy mention --allow-add +628123456789
```

### Allowlist

`allowedNumbers` accepts phone numbers, one per line/entry:

```text
+62 812 3456 7890
```

Values are normalised to digits. **An empty list allows no one** — the agent stays silent until you add a number, so an unforgotten form never turns into a public agent. A single `*` entry allows everyone explicitly and must be the only entry; the CLI and the web control center refuse `*` combined with numbers or groups. The CLI and the web control center both refuse to save a list whose entries are all invalid — it would normalize to empty and deny everyone by accident; clearing the list explicitly (`[]` or an empty textarea) is the deliberate way to deny everyone. The success toast reports the normalized entry count and how many raw lines were dropped as invalid or duplicate.

### Group allowlist

Groups are allowlisted by **picking**, not typing: a group has no identity an operator can enter from memory like a phone number, so `allowedGroups` stores full `...@g.us` JIDs and is filled from the list of groups the linked account has joined. In the web panel the **Group allowlist** section lists every joined group by name with an **Add**/**Remove ✓** toggle — each tap saves immediately, there is no textarea step and no JID copying. The picker loads automatically once the account is linked; **Load groups** refreshes it. A group that was allowed but has since been left keeps a remove row so the list stays honest.

A group message is allowed when its group JID is in `allowedGroups` — or, for legacy installs, when the group's stripped digits or a sender still sits in `allowedNumbers`. The group policy still decides when the bot replies inside an allowed group. The CLI mirrors the picker:

```bash
nusamation whatsapp set --group-add 1203630...@g.us --group-remove 1203630old@g.us
nusamation whatsapp set --groups '1203630a@g.us,1203630b@g.us'   # replace; empty clears
```

Bare digits are refused as group entries — they are ambiguous with phone numbers and belong in `allowedNumbers`. `nusamation whatsapp groups` (or `GET /api/channels/whatsapp/groups`) lists every joined group with its JID, name and allow state.

**Blocked senders:** messages a rejected sender is logged at info level and recorded in a small per-channel list, shown in the web panel with an **Allow** button that appends the number to the allowlist; traffic from a group also gets **Allow group**, because allowing the member's number only opens their private chat, not the group they wrote in.

**Set an allowlist before sharing the linked number.** The agent has access to the configured workspace.

Messages timestamped more than two hours in the past are ignored without a reply — late delivery retries and stale history must not trigger fresh agent turns.

**Retention:** `--retention-days N` (CLI, or the `retentionDays` setting, default 0 = keep forever) purges indexed messages older than N days; the purge runs at startup and daily afterwards.

**Message index and search:** every message (live and history-synced) is indexed with its chat name; full-text search uses an FTS5 index (Unicode case-folding) with a substring fallback, and deleted-for-everyone messages are tombstoned out of the index. Outgoing messages carry a `receipt` field (`delivered`/`read`) from incoming receipts.

### Group policy

- `disabled` — ignore all group messages.
- `mention` (default) — only process messages that @mention the bot or reply to it. Poll traffic (creations, votes, added options) cannot carry @-mentions, so it is exempt: user/general polls reach the agent as `[Poll]`/`[Poll vote]`/`[Poll option added]` text markers, and votes on approval/question polls resolve their pending request.
- `all` — process every group message. Use with care; this can be noisy and expensive.

### Approvals and questions

When `notifyApprovals` is on, pending approvals and single-field choice agent questions (elicitation form fields with enum/oneOf/boolean/array options) are delivered as **native WhatsApp polls** — tap to vote, single-select resolves immediately, multi-select resolves after a ~15 s quiet window. Without poll support or for option lists over 12, approvals fall back to `/approve <id>` and questions point to the web UI. Votes on approval/question polls are routed to the pending request; votes on user-created and general-purpose polls arrive as `[Poll vote: …]` text. Approval-poll option names are persisted, so a vote still decodes — and a tap on a request that already ended answers "That request has already ended." instead of vanishing. In a group, a vote from anyone other than the request's author is logged, never answered into the room.

### Intake queue

Inbound messages occupy one of four intake slots; media downloads can hold a slot up to 90 s. Gating (from-me, chat filters, group policy, allowlist, staleness) runs before a slot is taken. If all slots are busy, an allowed sender receives "The assistant is still working on the previous message. Please try again in a moment." instead of silence.

### Media

Images, documents, videos, video notes, voice notes and stickers are downloaded up to 25 MB and stored as app uploads. Locations, live locations, contact cards (single or multi), events, group invites, Flow/form responses, orders and product cards arrive as `[Type: …]` text markers. Images (and stickers) are sent to models whose ACP prompt capabilities advertise image support; audio goes to models that advertise audio support and otherwise reaches the agent as a `file://` resource link, like videos and other binary documents. In the other direction, media emitted by the agent — image/audio content blocks, `file://` resource links, or `file://` URIs and markdown links (including `![alt](path)` images) to allowed local paths in its reply — is uploaded to WhatsApp as a real attachment. Reply paths are read verbatim: spaces, Windows backslashes and a `/C:/…` drive spelling are accepted, and angle brackets or quotes around a destination (`[shot](<C:\Users\Some Name\shot.png>)`) are unwrapped. Paths are confined to the workspace, upload dir and conversation/agent working directories.

### Sending and updating messages

Besides reply delivery, an agent can send explicitly with `nusamation whatsapp send` (or `POST /api/channels/whatsapp/send`): `type` is `image`, `video`, `audio` or `document`/`file` with a server-readable `path` or base64 `data`, plus optional `mime`, `caption` and `name`. Stickers are a separate `sticker` type and require WebP — a photo must not be converted to a sticker to deliver it. Two more types update history: `edit` (`messageId` + `text`) rewrites one of the bot's own messages, and `revoke` (`messageId`) deletes it for everyone; both update the index to match. Group replies @mention the author of the message being answered, so WhatsApp highlights them.

### Delivery receipts

Incoming delivered/read receipts for the bot's own outgoing messages are recorded per indexed message (`receipt: delivered|read`) and returned in search results.

### Presence and read receipts

When enabled, allowed messages are marked read after the orchestrator accepts them. `Begin` sends a composing presence, and final updates pause it.

### Outbound pacing

Outbound messages are sent with a human-like cadence, per chat: `sendPacing` (seconds, default 5, 0–60) is the minimum gap between two messages to the same chat, and `queueCap` (default 20, 1–200) bounds the per-chat send queue. Messages that arrive faster than the gap wait in the queue — they are never dropped for pacing; the only rejections are the queue cap and a caller that is already gone. Order per chat is FIFO, chats never wait for each other, only a successful send starts the gap, and the queue lives in memory (a restart discards pending sends — the callers' failure paths run). Both values are editable in the web panel's Behaviour section and via `whatsapp set --send-pacing / --queue-cap`.

### One message per turn

WhatsApp receives a single message when the turn finishes. Interim assistant blocks, tool calls and new agent message ids never reach the channel while the turn runs. Partial replies are not sent and existing messages are not edited. While the turn runs, typing presence is refreshed if that setting is on. If the final send fails, the web conversation shows an error notice — the reply text is retried once to the origin chat and otherwise only exists on the web.

### Outbound digest

The chat layer converts markdown to WhatsApp-friendly text (bold, italic, headings, links) and strips ANSI. The final digest is the assistant's final text; if the agent only used tools, a short tool summary is sent. Replies are truncated by characters — WhatsApp accepts up to 30,000, matching the rule the agent is told; other channels use a 3,500-character default unless they declare their own limit. Explicit `whatsapp send` paths are read verbatim from anywhere the server can read, unlike reply attachments, which stay confined to the workspace.

## Telegram

The Telegram adapter is a private bot driven over `getUpdates` **long polling** — no webhook, no public URL needed. It speaks to `https://api.telegram.org/bot<token>/METHOD` through a thin stdlib HTTP client; there is no Telegram library in `go.mod`.

### Token and polling

A bot token from `@BotFather` is stored sealed with the store sealer under `channel.telegram.token`; `Settings()` only ever reports `tokenSet`, never the token itself. A new token is verified with `getMe` before it is saved — a typo cannot overwrite a working token. **Connect** validates the token once, publishes the command menu once, then runs the single poll loop. Saving a different token while connected cancels the old flow and starts a new one immediately; a per-flow generation prevents the old flow's cleanup from clearing the new flow's client or status. **Forget token** also clears the per-chat menu fingerprints. The poll offset (`last update_id + 1`) is persisted under `channel.telegram.offset`, so a restart resumes where it left off instead of replaying updates. `allowed_updates` is pinned to `message` and `inline_query`; edited messages, channel posts and callback queries are never delivered, and channel-type chats are skipped outright.

A `429` with `parameters.retry_after` is honored: the poll loop sleeps (capped at one minute) without counting a consecutive failure, and a send waits out a hint of ten seconds or less once when the caller's deadline leaves room.

### The `/` command menu

On connect the adapter calls `setMyCommands` with the fixed 12-entry menu — `start help new agent model mode set status stop retry approve deny` — in the **default** scope. Names satisfy `^[a-z0-9_]{1,32}$` and descriptions stay within 256 characters. Telegram clients offer that menu while a user types `/`. The actual command handling is unchanged: `internal/chat` parses `/cmd@botname` suffixes, and `/start` is answered alongside `/help`. `/cancel` still works but is not advertised.

When the linked conversation's ACP session advertises slash commands, that chat gets its own menu (`scope` `chat`): the same 12 commands first, then session commands whose names pass the grammar and do not collide, capped at 100. Names outside the grammar are skipped, as are entries whose description is shorter than Telegram's 3-character minimum (an empty description defaults to the command name), so one bad entry cannot fail the whole `setMyCommands` call. An empty session list deletes the chat scope so the default menu shows again, and `/new` does the same because the chat now belongs to a conversation that has not advertised commands yet. Forum topics share a chat id, so they share one menu. A failed publish is logged and does not fail the turn.

### Mentions

A bare `@` in a Telegram client is the username list; the bot cannot fill it with room labels. `#` has no registry in the Bot API and stays parked. Room and project mentions go through **inline mode**: the user types `@botusername`, the bot answers with rooms (label prefix or title) and projects (name or slug), and tapping one inserts `@handle` as the user's own message. That message comes back with `via_bot` set to this bot, and those tokens become conversation or project references — room labels win when a slug collides. An ordinary `@cat` that was typed without the inline picker stays plain text. Inline mode has to be enabled in BotFather (`/setinline`); until then Telegram never sends `inline_query` updates. File, folder, skill and MCP search stay on the web composer.

An inline query from a sender the allowlist rejects gets an empty answer, so their client stops waiting. In a group whose policy is `mention`, a message sent through this bot's inline mode counts as addressing the bot.

### Allowlist and group policy

The allowlist model mirrors WhatsApp: **an empty list allows no one**. `allowedUserIds` takes decimal numeric user ids; `allowedGroupIds` takes **negative** numeric chat ids for groups/supergroups. A positive id in the group list is refused by the settings API and dropped from stored legacy settings — a private chat's `chat_id` equals the user id, so accepting it would silently open that DM. A lone `*` in the user list allows every sender — it must be the only entry, and group policy still applies on top. Rejected senders land in a bounded blocked ring shown in the panel with an **Allow** button that adds the numeric id; group traffic also gets **Allow group** for the chat id. Usernames are never allowed — they can change. Messages older than two hours are dropped before the allowlist runs, so stale stranger messages never enter that ring.

Group policy: `disabled` ignores groups, `mention` (default) requires an `@bot` mention entity or literal, a `text_mention` of the bot, a reply to a bot message, a message sent through this bot's inline mode, or a `/` command, and `all` accepts everything Telegram delivers — which still requires privacy mode off in BotFather, since privacy mode keeps ordinary group messages from reaching the bot at all. A literal mention must be a whole username token: `@nusa_bot_extra` and `x@nusa_bot` do not count. Forum topics stay separated: `message_thread_id` is carried inbound and back out on every reply.

### Delivery

One `sendMessage` per completed turn, sent with `parse_mode: HTML` and capped at 4,096 **UTF-16 code units** — Telegram's own unit, where an astral emoji counts as two. Cutting happens at the send boundary without splitting a rune. Thoughts and tool output stay on the web. `ResponseRules` tells the agent which tags render — `<b>`, `<i>`, `<u>`, `<s>`, `<code>`, `<pre>`, `<blockquote>` and `<a href="https://…">` — that Markdown, headings and tables do not, and that an emoji can consume two units.

While a turn runs, the adapter refreshes `sendChatAction` (`typing`) about every four seconds; it stops on the final send, disconnect or shutdown and never creates a message.

Every outgoing text — turn final, `/help`, command replies, approval pings — passes one sanitizer before send: `&`, `<` and `>` in plain text become entities, unknown or mismatched tags are escaped as text, tags inside `<code>`/`<pre>` stay literal, and a tag still open at the end is closed. If Telegram still rejects the entities, the original text is resent without `parse_mode` (the first error is logged), so a formatting mistake never swallows a message. Media captions use the same sanitizer and are cut to 1,024 UTF-16 units.

### Media

Replies may attach files with `[name](path)` or `![name](path)`; the chat layer collects and reads them, and the adapter uploads each as multipart `form-data` with `chat_id`, `message_thread_id` when the chat is a forum topic, and the sanitized caption. Upload file names have control characters stripped and are length-bounded so an agent-supplied label cannot inject multipart headers. Images up to 10 MB go through `sendPhoto`, larger images become documents (`sendPhoto` refuses them), GIFs go through `sendAnimation`, video through `sendVideo`, OGG audio through `sendVoice`, other audio through `sendAudio`, and everything else through `sendDocument`. The 25 MB chat-layer ceiling is not raised. A caption Telegram cannot parse is retried unparsed while the file still arrives.

Inbound, `photo` (largest rendition), `document`, `video`, `audio`, `voice`, `video_note`, `animation` and `sticker` are downloaded through `getFile` and stored as conversation attachments; stickers arrive as `image/webp` files, never echoed back as stickers. A file whose declared size exceeds the Bot API's 20 MB `getFile` limit is not requested at all and the prompt text gets `[File is larger than 20 MB and was not downloaded]`; a failed lookup or transfer appends the WhatsApp-style marker for that kind (`[Image could not be downloaded]`, …). The bot token never appears in those errors or the log. Telegram sends albums as one update per item: items sharing `chat_id`, `message_thread_id` and `media_group_id` are buffered for 400 ms after the last one and delivered as a single inbound with at most six attachments, the first item's id, and the captions joined. A pending album is flushed on disconnect. There is no Telegram-specific send route; control goes through the generic `/api/channels/telegram/*` endpoints.

## Adding a channel

1. Create `internal/channels/<name>`.
2. Implement the `Channel` interface. Use `Manager.PublishStatus` for state changes.
3. Convert inbound platform messages to `channels.Inbound`; deduplicate platform retries before the handler.
4. Enforce allowlists and group policy inside the adapter, not in the orchestrator.
5. Register it in `internal/app`:
   ```go
   ch := mychannel.New(...)
   ch.SetHandler(chatSvc)
   chanMgr.Register(ch)
   ```
6. Implement `ResponseStyler` when the platform has its own message format — the rules are appended to each prompt so the agent writes for that surface.
7. Implement `PollAsker` when the platform has a native poll widget — approvals and choice questions become tappable; report votes back via `Inbound.PollVote`.
8. Add a settings card to `web/static/components/settings.js` (optional; plain channels still show status).
9. Add tests for allowlist, dedupe and target parsing.

The orchestrator and web UI do not need channel-specific code for text delivery.

### Explicit WhatsApp outbound messages

The connected WhatsApp adapter can also send native general-purpose polls,
quoted text replies, reactions, WebP stickers, locations, vCard contacts, and
events. Use `nusamation whatsapp send` with a JSON object on stdin or the
authenticated `POST /api/channels/whatsapp/send` endpoint. See the synced
[WhatsApp operations guide](../assets/docs/whatsapp.md#sending-native-whatsapp-content)
for fields and examples. General-poll votes are agent text context; polls for
pending approvals and questions retain their existing resolution behavior.
