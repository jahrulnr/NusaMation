# Troubleshooting NusaMation

Start at `logs/nusamation.log` inside the data dir — warnings and errors land
there (or `journalctl --user -u nusamation` under systemd).

| Symptom | Likely cause / fix |
|---|---|
| Agent never replies | Probe it: `POST /api/agents/{id}/probe`, then `GET /api/agents/{id}/logs`. Usually a missing binary, bad cwd, or expired auth — fix with `agent set`, or re-auth via `authenticate`. |
| Agent stuck waiting | A pending permission request — approve it (`POST /api/approvals/{id}`), or lower the friction with `-policy auto_safe`. |
| WhatsApp offline | `GET /api/channels` → find `name:"whatsapp"`; if `stats.paired:false`, re-scan the QR (see whatsapp.md). Try `connect`/`disconnect` before deleting `whatsapp.db`. |
| WhatsApp ignores someone | Check the allowlist and group policy (`whatsapp show`). Empty allowlist = everyone is allowed. |
| Port already in use | Another instance is running on `-addr`, or the port is taken by something else. |
| Database locked | Two processes writing the same `nusamation.db`. Run one server per data dir; the CLI is safe alongside the server. |
| Forgotten admin password | `nusamation reset-password -data-dir <dir>` prints a new one. |
| "restart to apply" after a CLI change | Expected — settings/agent rows are read at startup. Restart the server or reconnect the affected channel. |
| Agent env vars unreadable | `secret.key` was lost or replaced — sealed values cannot be recovered; re-add them with `agent set -env`. |
| Docs look stale | `docs/` is re-synced from the binary on every server start — restart updates it. |
| Need more detail | Start with `-log-level debug` (or `NUSAMATION_LOG_LEVEL=debug`). |
