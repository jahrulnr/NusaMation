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

Channels whose surface has its own message format implement `ResponseStyler` (`ResponseRules() string`). The chat service appends the returned text to every prompt for that surface inside a `<system-reminder>` block, so the agent writes in the format its reply will be delivered on (WhatsApp markup vs the web UI's full Markdown). Channels that skip it contribute no block.

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
nusamation whatsapp set -enabled -group-policy mention -allow-add +628123456789
```

### Allowlist

`allowedNumbers` accepts phone numbers or JIDs, one per line/entry:

```text
+62 812 3456 7890
1203630...@g.us
```

Values are normalised to digits. An empty list allows any sender who can message the account. A group is allowed if either the group JID or a sender is allowlisted.

**Set an allowlist before sharing the linked number.** The agent has access to the configured workspace.

### Group policy

- `disabled` — ignore all group messages.
- `mention` (default) — only process messages that @mention the bot or reply to it.
- `all` — process every group message. Use with care; this can be noisy and expensive.

### Media

Images, documents, videos, voice notes and stickers are downloaded up to 25 MB and stored as app uploads. Images (and stickers) are sent to models whose ACP prompt capabilities advertise image support; audio goes to models that advertise audio support and otherwise reaches the agent as a `file://` resource link, like videos and other binary documents. In the other direction, media emitted by the agent — image/audio content blocks, `file://` resource links, or `file://` URIs and markdown links to allowed local paths in its reply — is uploaded to WhatsApp as a real attachment. Paths are confined to the workspace, upload dir and conversation/agent working directories.

### Presence and read receipts

When enabled, allowed messages are marked read after the orchestrator accepts them. `Begin` sends a composing presence, and final updates pause it.

### Streaming edits

WhatsApp supports message edits. With `streamEdits=true`, NusaMation sends an “On it…” message and progressively edits it with the final answer. If editing fails (old client, message age), it sends a fresh final message. Disable this for strict clients.

### Outbound digest

The chat layer converts markdown to WhatsApp-friendly text (bold, italic, headings, links), truncates safely by UTF-8 bytes and strips ANSI. The final digest is the assistant's final text; if the agent only used tools, a short tool summary is sent.

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
7. Add a settings card to `web/static/app.js` (optional; plain channels still show status).
8. Add tests for allowlist, dedupe and target parsing.

The orchestrator and web UI do not need channel-specific code for text delivery.
