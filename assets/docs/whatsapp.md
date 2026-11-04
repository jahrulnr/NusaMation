# Operating the WhatsApp channel

The channel uses whatsmeow (WhatsApp Web protocol). Session state lives in
`whatsapp.db`; the linked device appears as **Chrome (NusaMation)** on the
phone.

## Pairing (needs a human with the phone)

Pairing requires scanning a QR code — it cannot be done headlessly:

1. Web UI → Channels → WhatsApp → show QR, scan it with the phone
   (or fetch `GET /api/channels/whatsapp/qr` — it renders the QR as PNG).
2. `GET /api/channels` → `{"channels":[…]}` — find the entry with
   `"name":"whatsapp"`; it reports `enabled`, `connected`, `loggedIn`,
   `state`, `selfId` and `stats.paired`/`stats.groupPolicy`.

If the session is logged out or corrupted: `POST /api/channels/whatsapp/logout`
(or delete `whatsapp.db` while the server is stopped), then re-pair.

## Settings

```bash
nusamation whatsapp show                       # current effective settings
nusamation whatsapp set -enabled               # master switch
nusamation whatsapp set -auto-start            # connect on server start
nusamation whatsapp set -group-policy mention  # disabled | mention | all
nusamation whatsapp set -allow '+628123456789,+15550100'   # replace allowlist
nusamation whatsapp set -allow-add +628… -allow-remove 999… # edit allowlist
nusamation whatsapp set -read-receipts=false -send-typing=false
nusamation whatsapp set -stream-edits          # progressive reply edits
```

- **AllowedNumbers**: who may talk to the bot in private chats. Numbers are
  normalized to digits only (`+`, spaces and `-` are stripped). **An empty
  allowlist allows everyone** — the CLI refuses to save one that contains no
  valid numbers.
- **GroupPolicy**: `disabled` ignores all groups; `mention` replies only when
  @-mentioned or replied to; `all` replies to every group message (use
  carefully). Poll creations, votes and added options are exempt from the
  `mention` requirement — they cannot carry @-mentions. User and general
  polls reach the agent as `[Poll]`, `[Poll vote: …]` and `[Poll option added: …]`
  text markers; votes on approval/question polls resolve the pending request.
- **ReadReceipts** blue-ticks incoming chats; **SendTyping** shows "typing…"
  while the agent works; **StreamEdits** progressively edits the ack message
  into the final answer instead of sending a new message.

Pending approvals and single/multi-choice agent questions arrive as tappable
WhatsApp polls when they fit (≤12 options); otherwise approvals use the
`/approve <id>` text flow and questions point to the web UI. Multi-select answers settle ~15 s after the last vote. Only the
user who started the turn may vote — other group members' taps are ignored.

## Multiple ACP agents

In a WhatsApp chat, send `/agent` to list enabled agents and their slugs, then
`/agent <slug>` to switch the linked conversation. Switching starts a fresh ACP
session and supplies a transcript recap with the next message; it is rejected
while a turn is active. `/new <slug>` creates a new conversation for that agent
and moves this WhatsApp chat's link to it, requesting cancellation of the
current turn if there is one; the old transcript stays on the web.
`/new` without a slug keeps the currently linked agent. A previously unlinked
chat uses the configured default ACP, or the first enabled agent if no valid
default is set. Different WhatsApp chats can use different agents, but each
chat links to one active NusaMation conversation at a time. Group messages must
also satisfy the configured group policy.


## Applying changes

A running server caches settings. Either restart the server, or reconnect just
the channel without a restart:

```
POST /api/channels/whatsapp/disconnect
POST /api/channels/whatsapp/connect
```

## Sending native WhatsApp content

The running server must have WhatsApp connected. `nusamation whatsapp send`
reads one JSON object from stdin and sends it through the existing connection.
It returns `{"messageId":"…"}`. It uses the configured data directory and
`NUSAMATION_ADDR`; use `-data-dir` or `-addr` if the server uses other values.
The same JSON is accepted by authenticated `POST /api/channels/whatsapp/send`
(with the usual session cookie and CSRF header).

For a WhatsApp-origin turn, the prompt includes `target`, `messageId`,
`senderId`, and `quotedText` for the incoming message. Use its `target` for
the current chat and its message and sender IDs for a reply or reaction. For
a different chat or message, use its actual WhatsApp JIDs and message ID.
Do not substitute the NusaMation conversation/message ID for a WhatsApp ID.

```bash
# Native single-choice poll; maxSelectable 0 means unlimited choices.
printf '%s' '{"target":"123@s.whatsapp.net","type":"poll","text":"Choose","options":["Yes","No"],"maxSelectable":1}' | nusamation whatsapp send

# Quoted reply and reaction. An empty reaction text removes the reaction.
printf '%s' '{"target":"123@s.whatsapp.net","type":"reply","messageId":"WA_MESSAGE_ID","senderId":"123@s.whatsapp.net","quotedText":"original message","text":"My reply"}' | nusamation whatsapp send
printf '%s' '{"target":"123@s.whatsapp.net","type":"reaction","messageId":"WA_MESSAGE_ID","senderId":"123@s.whatsapp.net","text":"👍"}' | nusamation whatsapp send

# Location, contact and event. Event times are Unix seconds.
printf '%s' '{"target":"123@s.whatsapp.net","type":"location","latitude":-6.2,"longitude":106.8,"name":"Jakarta","address":"Jakarta"}' | nusamation whatsapp send
printf '%s' '{"target":"123@s.whatsapp.net","type":"contact","name":"Alice","vcard":"BEGIN:VCARD\nVERSION:3.0\nFN:Alice\nTEL:+62123456789\nEND:VCARD"}' | nusamation whatsapp send
printf '%s' '{"target":"123@s.whatsapp.net","type":"event","name":"Meeting","description":"Planning","startTime":1800000000,"endTime":1800003600}' | nusamation whatsapp send

# Sticker data must be a WebP file encoded as base64; optionally set animated=true.
python3 -c 'import base64,json,sys; print(json.dumps({"target":sys.argv[1],"type":"sticker","data":base64.b64encode(open(sys.argv[2],"rb").read()).decode()}))' '123@s.whatsapp.net' sticker.webp | nusamation whatsapp send
```

Polls support 2–12 distinct options. Votes on a poll sent with this command
are delivered to the agent as `[Poll vote: …]` and `[Poll ID: …]` context,
where the poll ID matches the command's `messageId`, while polls issued for
an approval or agent question continue to resolve that pending request.
If the server reports `send_partial`, the poll was already sent but its vote
metadata could not be stored; the response includes its `messageId`, so do
not retry blindly.
Contact `vcard` must contain the desired vCard. For event locations, add both
`latitude` and `longitude`; `joinLink` is optional. Sending these operations
is separate from the normal streamed final answer and does not itself start a
turn.
