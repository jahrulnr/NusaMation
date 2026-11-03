# Stored settings

Arbitrary key→value config in `nusamation.db` — the same table the web UI
writes to:

```bash
nusamation config list [-json]
nusamation config get <key>
nusamation config set <key> <value>                 # upsert
nusamation config add <key> <value>                 # fails if key exists
nusamation config remove <key>
nusamation config set <key> '<json>' -validate-json # reject non-JSON values
```

## Known keys

| Key | Shape | Meaning |
|---|---|---|
| `channel.whatsapp.settings` | JSON | WhatsApp channel settings — prefer `nusamation whatsapp` commands, which validate and normalize |
| `routing.defaults` | JSON | `{"replyToOrigin":bool,"mirrorFinal":bool,"notifyApprovals":bool}` — per-conversation routing defaults |
| `app.installed_at` | Unix ms (string) | written once at install |

Prefer dedicated commands (`whatsapp set`, `agent …`) over raw `config set` —
the dedicated commands normalize and validate; `config` writes values verbatim.
`GET /api/settings` and `PUT /api/settings` expose the same keys on a live
server.

Settings are cached by a running server; restart to apply.
