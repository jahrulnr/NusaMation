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

`allowedNumbers` accepts phone numbers or JIDs, one per line/entry:

```text
+62 812 3456 7890
1203630...@g.us
```

Values are normalised to digits. **An empty list allows no one** — the agent stays silent until you add a number, so an unforgotten form never turns into a public agent. A single `*` entry allows everyone explicitly and must be the only entry; the CLI and the web control center refuse `*` combined with numbers. A group is allowed if either the group JID or a sender is allowlisted. The CLI and the web control center both refuse to save a list whose entries are all invalid — it would normalize to empty and deny everyone by accident; clearing the list explicitly (`[]` or an empty textarea) is the deliberate way to deny everyone. The success toast reports the normalized entry count and how many raw lines were dropped as invalid or duplicate.

**Finding the JIDs:** `nusamation whatsapp groups` (or `GET /api/channels/whatsapp/groups`) lists every joined group with its JID and name; the web panel has a **Load groups** button beside the allowlist that fills a list with a **Use** action per group.

**Blocked senders:** messages a rejected sender is logged at info level and recorded in a small per-channel list, shown in the web panel with an **Allow** button that appends the number to the allowlist.

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
