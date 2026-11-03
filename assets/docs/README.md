# NusaMation operations guide

Operational documentation for agents and humans running or reconfiguring a live
NusaMation instance. These are not development docs — they cover operating the
installed app: agents, WhatsApp, settings, the HTTP API, and debugging.

NusaMation is a self-hosted control center: it spawns ACP-compatible agent CLIs
(like you, if you are an ACP agent) and connects them to a web control center
and chat channels (currently WhatsApp).

## Data layout

All state lives in the data directory — default `~/.nusamation`, or whatever
`-data-dir` the server was started with. The workspace `AGENTS.md` lists the
resolved paths for this install.

| Path | Purpose |
|---|---|
| `nusamation.db` | SQLite: agents, conversations, approvals, settings |
| `whatsapp.db` | whatsmeow device/session store |
| `secret.key` | AES-GCM key protecting agent/MCP env secrets |
| `workspace/` | Shared agent working directory (default session cwd) |
| `uploads/` | Web and WhatsApp attachments |
| `docs/` | This documentation (synced from the binary on startup) |
| `logs/nusamation.log` | Application log |

## Where to look

| If you need to… | Read |
|---|---|
| Add/configure/remove agent CLIs, auth, policies, models | [agents.md](agents.md) |
| Pair WhatsApp, set allowlist/group policy/behavior | [whatsapp.md](whatsapp.md) |
| Read or change any stored setting key | [settings.md](settings.md) |
| Drive NusaMation over HTTP instead of the CLI | [api.md](api.md) |
| Something is broken — logs, common failures | [troubleshooting.md](troubleshooting.md) |

## Rules

1. Change state through `nusamation` CLI commands or the API — never write to
   `nusamation.db` directly (secrets are encrypted; the CLI handles it).
2. Settings/agent mutations apply on the next server start; a running instance
   caches them. Restart the server — or, for WhatsApp, reconnect the channel —
   after changing them.
3. Never print, copy, or commit `secret.key` or sealed env var values.
4. Files under `docs/` are owned by the app: they are rewritten on startup when
   the binary ships a newer version. Do not edit them.
