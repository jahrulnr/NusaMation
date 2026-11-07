# NusaMation

**ACP-native personal agent mesh.** Your agent lives in the web control center with full thoughts, tools and approvals — and travels to WhatsApp as a concise, human-sized presence.

NusaMation is not another LLM wrapper. The brain is any **Agent Client Protocol (ACP)** CLI: Devin, Claude Code ACP, Codex ACP, Gemini ACP, or your own agent. NusaMation owns the conversation, the workspace, approvals, routing, sessions and the control center.

![NusaMation conversation](docs/screenshots/conversation.png)

## Why

- **ACP first.** Speak the open Agent Client Protocol over stdio. No provider API keys, no bespoke model plumbing, no lock-in.
- **Web sees everything.** Streaming markdown, collapsible thoughts, tool cards with raw input/output, plans, usage and inline approvals.
- **Chats get the digest.** WhatsApp shows a typing indicator while the agent works, then receives one final answer. Reasoning and raw tool output stay private in the control center.
- **One mesh, many surfaces.** The same conversation can span web and WhatsApp. Per-conversation routing decides where the final answer goes.
- **Local-first.** Single binary, embedded web UI, SQLite, encrypted secrets, no build step and no cloud dependency.
- **Built for real use.** Queueing, cancel, retry, session recovery, transcript handoff, MCP management, allowlists, approval rules and reconnect handling.

## Quick start

Install the latest release with one command — no Go toolchain or source checkout required:

- **Linux** (binary + systemd user service, autostart at boot):

  ```bash
  curl -fsSL https://github.com/jahrulnr/NusaMation/releases/latest/download/install.sh | bash
  ```

- **Windows** (PowerShell: binary under `%LOCALAPPDATA%\NusaMation\bin`, user PATH, hidden logon task):

  ```powershell
  irm https://github.com/jahrulnr/NusaMation/releases/latest/download/install.ps1 | iex
  ```

The installer downloads the release binary, verifies its SHA-256 checksum and starts NusaMation immediately. Open <http://127.0.0.1:8787>.

On first run NusaMation generates an admin password and prints it once. Find it in the service log:

```bash
# Linux (systemd user service)
journalctl --user -u nusamation.service -n 60 --no-pager

# Windows (PowerShell)
Get-Content (Join-Path $env:USERPROFILE '.nusamation\logs\nusamation.log')
```

Forgot it? Stop NusaMation and reset it:

```bash
nusamation reset-password -data-dir ~/.nusamation
```

On first run NusaMation also scans `PATH` for popular ACP CLIs (Devin, Claude Agent, Codex, Gemini, Copilot, OpenCode, Auggie, goose, Kimi, Cursor, Kilo, Qwen, Mistral Vibe, Kiro) and seeds every one it finds as an agent. Add more manually in **Control center → Agents** or with `nusamation agent add`.

Pin a version, customize install paths or uninstall: see [Installation](#installation).

## Installation

### From a release

The one-liners above install the latest release. Pin a version with `NUSAMATION_VERSION=0.1.0` (Linux) or `$env:NUSAMATION_VERSION = '0.1.0'` (Windows). Both installers verify the binary against the published SHA-256 checksums, are safe to re-run (data is kept) and support uninstall. See `scripts/install.sh` / `scripts/install.ps1` for all options. On Linux the installer mirrors `make install` and merges the `PATH` of the shell that ran it into the service unit, so agent CLIs outside the standard directories are detected by the service.

### Install as a user service (from source)

On Linux with a systemd user manager, install the binary and autostart it without root:

```bash
make install
```

This installs:

| Path | Purpose |
|---|---|
| `~/.local/bin/nusamation` | Executable |
| `~/.nusamation/` | Data directory (database, workspace, uploads, secrets) |
| `~/.config/systemd/user/nusamation.service` | systemd user unit |
| `~/.config/nusamation/env` | Optional environment overrides, created only when missing |

The service unit merges the `PATH` of the shell that ran `make install` (install dir first), so agent CLIs in non-standard locations (`~/go/bin`, `~/.cargo/bin`, …) are detected. Add more entries later with a `PATH=...` line in `~/.config/nusamation/env`.

The service is enabled for login and started immediately:

```bash
make service-status
make service-logs      # follow the journal, or the data log without journalctl
make service-restart
make service-stop
make service-start
```

`make install` also tries to enable systemd lingering so the service starts at boot without a login. If `loginctl` is unavailable or not permitted, it prints the command to run manually:

```bash
sudo loginctl enable-linger "$USER"
```

On first run, read the generated admin password from the journal:

```bash
journalctl --user -u nusamation.service -n 60 --no-pager
```

If port `8787` is already used by a manually started instance, stop that instance before `make install`. Re-running `make install` is safe: the environment file and data are not overwritten. `make uninstall` removes the binary and service but keeps data; `make purge` also removes the data and environment file.

## Screens

<div style="text-align: center;">

| Mobile |
|---|
| ![mobile](docs/screenshots/mobile.png) |

| Conversation | Tools & raw output |
|---|---|
| ![conversation](docs/screenshots/conversation.png) | ![tools](docs/screenshots/tools.png) |

| Approval | WhatsApp pairing |
|---|---|
| ![approval](docs/screenshots/approval.png) | ![whatsapp](docs/screenshots/whatsapp.png) |

</div>

## Features

### Web control center

- ChatGPT/Claude-like dark UI, responsive from desktop to phone.
- Streaming assistant text and **thinking**, with collapsible thought cards.
- Tool cards for terminal, read, edit, search, fetch and other kinds; raw input/output, locations and diffs.
- Plan checklist, usage/token meter, session configuration (model, mode, thought level, speed) as the agent publishes them.
- Inline approval cards and a global **Approvals** inbox.
- Composer with file/image attachments, drag & drop, paste, keyboard shortcuts and a stop button.
- Conversation search, pin, rename, archive, export, delete, per-conversation routing.
- Command palette (`Ctrl/Cmd + K`), live event stream over WebSocket, offline/reconnect state.

### Agents

- Add any command that speaks ACP over stdio.
- Probe an agent before using it: capabilities, models, config options, auth methods.
- Per-agent approval policy: `ask`, `auto_safe` (read/search/fetch/think) or `auto_all`.
- Remembered allow rules scoped to a conversation.
- Encrypted environment variables (AES-GCM key in the data directory).
- Runtime logs and stderr ring buffer, authentication methods, logout.
- Shared MCP servers for all agents: stdio, HTTP and SSE, with encrypted env/headers.

### Channels

- **Web** is native and always available.
- **WhatsApp** via [whatsmeow](https://github.com/tulir/whatsmeow): QR pairing, reconnect, read receipts, typing indicator, one message per finished turn, allowlist, group policy (`disabled` / `mention` / `all`), media download and approval notifications.
- Channel manager is modular. Future Telegram, Discord or Slack adapters implement the same `Channel` / `Controller` interfaces.
- Routing is per conversation:
  - `replyToOrigin` — answer the chat where the prompt came from.
  - `mirrorFinal` — copy the final digest to every linked chat.
  - `notifyApprovals` — ping the origin chat when a decision is needed.
- Web always gets the complete stream; external channels get a markdown-aware final digest.

### Automations

In **Control center → Automations**, schedule an ACP prompt once or with a five-field cron expression. The schedule builder can generate common minute, hour, daily, weekly and monthly expressions; the expression remains editable. Choose an agent, timezone, optional expiry, and whether each run gets a new chat or all runs reuse a dedicated chat. Run history links back to the corresponding conversation and shows the turn status.

The server must be running to execute jobs. After downtime, each overdue job runs once when the server returns, then resumes its normal schedule; jobs past their expiry do not run. A repeated job queues turns when its dedicated chat is still busy, and a queued automation turn is cancelled if it reaches the head of the queue after expiry. An execution interrupted between its scheduler claim and linking the turn is recorded as interrupted and is not automatically retried; inspect its linked chat before retrying.

### Notifications & updates

- **Desktop notifications** on the host OS for approval requests, finished turns and agent errors — `notify-send` on Linux, `osascript` on macOS, a hidden PowerShell balloon on Windows. Toggle and test button in **Control center → Account**.
- **Self-update** from GitHub releases: periodic checks surface a notification once per new version; **Account → Check for updates** runs one on demand. Installing downloads the release asset, verifies it against `SHA256SUMS`, swaps the binary and restarts — progress streams live and the web UI reloads into the new version automatically.

### Reliability

- Per-conversation turn queue; one active agent session at a time.
- `session/cancel` for cooperative cancellation, queued-turn cancellation and hard process cleanup.
- `session/load` when supported, then `session/resume`, then a fresh session with a transcript recap injected into the next prompt.
- Startup recovery: interrupted turns are marked, stale approvals cancelled, queued turns resumed, statuses normalised.
- Slow WebSocket clients are dropped instead of blocking the agent stream.
- CSRF protection, HttpOnly session cookies, login rate limiting, origin checks, CSP and path confinement for files/terminals.
- SQLite in WAL mode with a single connection and busy timeout; SQLite is pure Go (`modernc.org/sqlite`), no CGO.

## Architecture

```mermaid
flowchart TD
    subgraph NM["NusaMation"]
        UI["Web UI (embedded)"]
        API["HTTP API + Event Hub"]
        WA["WhatsApp"]
        CM["Channel Manager"]
        CO["Chat Orchestrator"]
        AM["Agent Manager"]
        CLI["Agent CLI (Devin, etc.)"]
        APPDB[("SQLite (app)")]
        WADB[("SQLite (WhatsApp)")]
        WS["workspace/"]
        UP["uploads/"]
    end

    UI -- "HTTP/WS" --> API
    WA <-->|"whatsmeow"| CM
    API --> CO
    CM -- "Inbound" --> CO
    CO --> AM
    AM -- "ACP over stdio" --> CLI
```

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the turn lifecycle, approval flow, session recovery and event model. Deployment notes (systemd, Caddy/Nginx, backups) are in [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Configuration

Environment variables:

| Variable | Default | Description |
|---|---|---|
| `NUSAMATION_ADDR` | `127.0.0.1:8787` | HTTP listen address |
| `NUSAMATION_DATA_DIR` | `~/.nusamation` | Database, uploads, workspace, logs and secrets |
| `NUSAMATION_PUBLIC_URL` | — | Public base URL for links and reverse proxies |
| `NUSAMATION_PASSWORD` | generated | First-run admin password |
| `NUSAMATION_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NUSAMATION_NO_WHATSAPP` | `false` | Disable the WhatsApp channel |
| `NUSAMATION_OPEN` | `false` | Open the browser after startup |
| `NUSAMATION_DEV` | `false` | Development mode (asset/cache behaviour) |

Flags mirror the variables: `-addr`, `-data-dir`, `-public-url`, `-password`, `-log-level`, `-no-whatsapp`, `-open`, `-dev`.

### CLI

The binary doubles as a management CLI, so agents and scripts can reconfigure NusaMation without the web UI. Every command accepts `-data-dir` (default `~/.nusamation`); run `nusamation <command> -h` for full usage.

```bash
nusamation --help                       # global help
nusamation --version                    # print version

# ACP agents
nusamation agent list                   # configured agents
nusamation agent detect [-add]          # popular ACP CLIs found on PATH (-add registers them)
nusamation agent get <slug|id> [-json] [-show-secrets]
nusamation agent add -name X -command codex-acp -cwd /work -policy auto_safe -env KEY=val
nusamation agent set <slug|id> -policy ask -env KEY=val -env-rm OLD -arg acp
nusamation agent remove <slug|id>

# Scheduled ACP jobs
nusamation automation add -name 'Daily review' -prompt 'Review the workspace' \
    -agent codex -cron '0 9 * * *' -tz Asia/Jakarta -session same
nusamation automation add -name 'One-off' -prompt 'Inspect the logs' \
    -agent codex -at '2026-12-01T09:00' -tz Asia/Jakarta -session isolated
nusamation automation list [-json]
nusamation automation get <id> [-json]
nusamation automation set <id> -prompt 'Updated prompt' -expires '2026-12-31T18:00'
nusamation automation pause <id>        # resume, remove, history also supported
nusamation automation history <id> [-json]

# Stored settings (key/value)
nusamation config list [-json]
nusamation config get channel.whatsapp.settings
nusamation config set my.key 'value'        # upsert
nusamation config add my.key 'value'        # fails if it exists
nusamation config remove my.key
# structured settings take JSON; -validate-json rejects non-JSON values
nusamation config set routing.defaults '{"replyToOrigin":true,"notifyApprovals":true}' -validate-json

# WhatsApp channel
nusamation whatsapp show
nusamation whatsapp set -enabled -group-policy mention \
    -allow-add +628123456789 -allow-remove 999 -send-typing=false
```

Agent and stored-setting mutations may require a restart. Automation changes are picked up by the running scheduler without a restart.

Data directory:

```text
~/.nusamation/
├── nusamation.db      # app state, conversations, approvals, settings
├── whatsapp.db        # whatsmeow device/session store
├── secret.key         # AES-GCM key for agent/MCP secrets
├── uploads/           # web and WhatsApp attachments
├── workspace/         # shared agent working directory (README/AGENTS seeded)
├── docs/              # ops docs for agents (agents.md, whatsapp.md, …; synced from binary)
└── logs/nusamation.log
```

Back up the whole directory to preserve agents, conversations and the WhatsApp link.

## WhatsApp

1. Start NusaMation with WhatsApp enabled.
2. Open **Control center → Channels → WhatsApp → Connect**.
3. Scan the QR from **WhatsApp → Linked devices → Link a device**. The device shows up as **Chrome (NusaMation)**.
4. Set an allowlist and group policy once paired.

Security notes:

- An empty allowlist allows **anyone who can message the linked account**. Set it before sharing the number.
- Group policy defaults to **mention only**.
- The agent's raw thoughts, tool inputs and outputs never leave the web control center.
- Approvals can be answered in web or from the origin chat with `/approve <id>` / `/deny <id>`.

See [docs/CHANNELS.md](docs/CHANNELS.md).

## Agent setup

Any ACP CLI works. Example commands (the exact adapter package varies):

| Agent | Command | Notes |
|---|---|---|
| Devin | `devin acp` | Auto-detected; log in first. |
| Claude Agent | `claude-agent-acp` / `claude-code-acp` | Auto-detected; ACP adapter for Claude Code. |
| Codex | `codex-acp` | Auto-detected. The Codex CLI has no ACP mode; `codex acp` is not used. |
| Gemini CLI | `gemini --acp` | Auto-detected. |
| GitHub Copilot | `copilot --acp` | Auto-detected. |
| OpenCode | `opencode acp` | Auto-detected. |
| Auggie | `auggie --acp` | Auto-detected. |
| goose | `goose acp` | Auto-detected. |
| Kimi CLI | `kimi acp` | Auto-detected. |
| Cursor | `cursor-agent acp` | Auto-detected. |
| Kilo | `kilo acp` | Auto-detected. |
| Qwen Code | `qwen --acp` | Auto-detected. |
| Mistral Vibe | `vibe-acp` | Auto-detected. |
| Kiro CLI | `kiro-cli acp --agent-engine=v3` | Auto-detected. |
| Fake demo | `fake-acp` | In-repo deterministic agent for testing. |

Any command that speaks ACP over stdio works — the catalog is just what gets auto-detected. Use `nusamation agent detect` to see which known CLIs are on your `PATH`.

Use **Probe** to discover models and configuration options. Devin publishes model, mode, thought level and speed. The model picker in the top bar is populated from the live session.

Read [docs/AGENTS.md](docs/AGENTS.md) for approval policies, MCP, auth and workspace guidance.

## Development

### Build and run from source

Requirements: Go **1.27+**, and an ACP agent CLI on `PATH` (popular ones are auto-detected on first run).

```bash
git clone https://github.com/jahrulnr/NusaMation.git
cd NusaMation
go build -o nusamation ./cmd/nusamation

# Linux/macOS
./nusamation
```

Open <http://127.0.0.1:8787>. On first run the binary prints a generated admin password once; save it. Set your own password instead with `NUSAMATION_PASSWORD='a-long-password' ./nusamation`.

### Run the demo agent

The repository includes a deterministic fake ACP agent for UI and integration testing:

```bash
go build -o fake-acp ./cmd/fake-acp-agent

# Add it in Control center → Agents:
#   Name:    Fake ACP
#   Command: /absolute/path/to/fake-acp
#   Cwd:     /absolute/path/to/a/workspace
#   Policy:  ask
```

It streams a thought, a plan, a tool call, a **permission request**, a tool result and a final message. Use it to see the approval flow end to end.

### Make targets

```bash
make check      # fmt + vet (linux & windows) + tests + windows compile check
make test       # go test ./... (+ GOOS=windows go build ./... as compile check)
make vet        # go vet ./... (+ GOOS=windows go vet ./...)
make fmt        # go fmt ./...
make build      # bin/nusamation
make fake       # bin/fake-acp-agent
make run        # go run ./cmd/nusamation
```

The `make check` targets also vet and compile the Windows target, so platform-specific code (see `internal/procsys` for the Unix/Windows process-group split) is caught before CI. Windows unit tests themselves run on the Windows CI runners in [`.github/workflows/release.yml`](.github/workflows/release.yml); a Linux host cannot execute Windows test binaries.

## Security model

NusaMation is designed to be exposed through a reverse proxy or a private network, not the public internet without TLS and additional controls. The default bind address is loopback.

- Password hashes use bcrypt; sessions store only SHA-256 token hashes.
- CSRF token per session; Origin checks on mutations and WebSocket upgrades.
- Login rate limiting.
- Agent/MCP secrets encrypted at rest; API responses redact values.
- Filesystem and terminal requests are constrained to the agent workspace roots; sensitive paths (`.env*`, `.git/hooks`, agent config directories) are denied by default.
- Uploaded files are served only through authenticated endpoints.
- Approvals are local policy: `ask` is the safe default. `auto_all` is deliberately powerful.

## License

MIT — see [LICENSE](LICENSE).
