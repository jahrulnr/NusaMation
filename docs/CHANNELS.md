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

Channels whose surface has its own message format implement `ResponseStyler` (`ResponseRules() string`). The chat service includes these rules in a versioned `<nusamation-context>` JSON block at the end of each prompt (WhatsApp markup vs the web UI's full Markdown). This is client context in a user-role message, not an ACP system prompt. Channels without a style still receive input and delivery metadata, but no formatting rules. See [ACP prompt context](ACP_PROMPTS.md) for channel switching, steering, and automation behavior.

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

Values are normalised to digits. An empty list allows any sender who can message the account. A group is allowed if either the group JID or a sender is allowlisted.

**Set an allowlist before sharing the linked number.** The agent has access to the configured workspace.

### Group policy

- `disabled` — ignore all group messages.
- `mention` (default) — only process messages that @mention the bot or reply to it. Poll traffic (creations, votes, added options) cannot carry @-mentions, so it is exempt: user/general polls reach the agent as `[Poll]`/`[Poll vote]`/`[Poll option added]` text markers, and votes on approval/question polls resolve their pending request.
- `all` — process every group message. Use with care; this can be noisy and expensive.

### Approvals and questions

When `notifyApprovals` is on, pending approvals and single-field choice agent questions (elicitation form fields with enum/oneOf/boolean/array options) are delivered as **native WhatsApp polls** — tap to vote, single-select resolves immediately, multi-select resolves after a ~15 s quiet window. Without poll support or for option lists over 12, approvals fall back to `/approve <id>` and questions point to the web UI. Votes on approval/question polls are routed to the pending request; votes on user-created and general-purpose polls arrive as `[Poll vote: …]` text.

### Media

Images, documents, videos, video notes, voice notes and stickers are downloaded up to 25 MB and stored as app uploads. Locations, live locations, contact cards (single or multi), events, group invites, Flow/form responses, orders and product cards arrive as `[Type: …]` text markers. Images (and stickers) are sent to models whose ACP prompt capabilities advertise image support; audio goes to models that advertise audio support and otherwise reaches the agent as a `file://` resource link, like videos and other binary documents. In the other direction, media emitted by the agent — image/audio content blocks, `file://` resource links, or `file://` URIs and markdown links to allowed local paths in its reply — is uploaded to WhatsApp as a real attachment. Paths are confined to the workspace, upload dir and conversation/agent working directories.

### Presence and read receipts

When enabled, allowed messages are marked read after the orchestrator accepts them. `Begin` sends a composing presence, and final updates pause it.

### One message per turn

WhatsApp receives a single message when the turn finishes. Partial replies are not sent and existing messages are not edited. While the turn runs, typing presence is refreshed if that setting is on.

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
