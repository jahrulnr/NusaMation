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
  @-mentioned; `all` replies to every group message (use carefully).
- **ReadReceipts** blue-ticks incoming chats; **SendTyping** shows "typing…"
  while the agent works; **StreamEdits** progressively edits the ack message
  into the final answer instead of sending a new message.

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
