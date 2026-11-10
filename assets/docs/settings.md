# Stored settings

Arbitrary key→value config in `nusamation.db` — the same table the web UI
writes to:

```bash
nusamation config list [--json]
nusamation config get <key>
nusamation config set <key> <value>                 # upsert
nusamation config add <key> <value>                 # fails if key exists
nusamation config remove <key>
nusamation config set <key> '<json>' --validate-json # reject non-JSON values
```

## Known keys

| Key | Shape | Meaning |
|---|---|---|
| `channel.whatsapp.settings` | JSON | WhatsApp channel settings — prefer `nusamation whatsapp` commands, which validate and normalize |
| `routing.defaults` | JSON | `{"replyToOrigin":bool,"mirrorFinal":bool,"notifyApprovals":bool}` — per-conversation routing defaults |
| `agent.defaults` | JSON | `{"agentId":"…","selections":[{"id":"…","value":"…"}]}` — default ACP and ordered session choices |
| `app.installed_at` | Unix ms (string) | written once at install |
| `notify.enabled` | `"true"`/`"false"` | desktop notifications; absent means enabled — prefer the Account tab toggle |
| `update.status` | JSON | last release check (`latest`, `available`, `checkedAt`, `releaseUrl`) — managed by the updater |
| `update.notified` | tag string | last release version already pushed to the OS — managed by the updater |

Prefer dedicated commands (`whatsapp set`, `agent …`) over raw `config set` —
the dedicated commands normalize and validate; `config` writes values verbatim.
`GET /api/settings` and `PUT /api/settings` expose routing defaults and the
notification switch, not every key in this table. The current default ACP is returned as `agentDefaults` by
`GET /api/bootstrap`. Set it through **Control center → Defaults** or the
dedicated preview/save endpoints in [api.md](api.md): those endpoints validate
the ordered choices against a temporary ACP session. Raw `config set` does not
perform that validation.

Some settings and agent changes may require a restart or channel reconnect.
Automation jobs are stored separately from settings; see [automations.md](automations.md).
