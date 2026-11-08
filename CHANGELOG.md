# Changelog

All notable changes to the Go core will be documented in this file. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-11-08

### Added


- **Projects:** local projects group chats under ordered folders. `rootPaths[0]` is the primary folder and becomes each member's cwd; the rest are `additionalDirectories`. A chat joins only when created with `projectId` or restored by undo, which also restores the pin. Removing a project deletes no directory and no chat. A folder change drops an idle ACP session, or waits until the running turn finishes, without clearing `acpSessionId`. Terminal cwd is accepted inside any session root. New routes cover projects and `GET /api/fs/list`. The composer shows which project new chats join.
- **browser_use:** each ACP session is given a stdio MCP server named `browser_use` (`nusamation browser-use`). The agent drives that conversation's embedded browser with `navigate`, `snapshot`, `move`, `click`, `type`, `press`, and `scroll`. `navigate` and `snapshot` return the page URL, title, viewport size, and a JPEG. Clicks and pointer moves draw a blue cursor on the live pane; the first action opens the pane. Coordinates are CSS pixels of the viewport size in the tool result. While the agent is driving, the pane letterboxes that viewport instead of resizing the remote page, so a later click still matches the screenshot. These calls are MCP tool calls inside the agent and do not create NusaMation approval cards. A user-configured MCP server already named `browser_use` is replaced by this one. Queued turns resume only after the server is registered, so a restart does not open an ACP session that lacks it.
- **Web styles:** `web/static/app.css` is an import list. Component styles live in `web/static/components/*.css`, in cascade order.

## [0.1.0] - 2026-11-07

Initial release of the ACP-native personal agent mesh: a single local binary that owns the conversation, workspace, approvals, routing and sessions around any Agent Client Protocol (ACP) CLI.

### Added

- **Browser annotations:** element picks inject a numbered badge + highlight ring into the page and capture the marked full-viewport screenshot — badge `[N]` in the image matches the `[N]` label in the agent prompt. Descriptors carry `viewport{w,h,dpr}` (the explicit breakpoint), `computed` style fields (font, weight, letter-spacing, colors, display), the element's opening `html` tag, `framePath`, click point, scroll offsets and page dimensions — rendered as `Layout:` / `Style:` / `HTML:` / `Frame:` lines in the prompt block. Per-pin "Include screenshot" checkbox keeps the image out of the prompt when unchecked (`omitScreenshot` in message metadata). Pin numbers show on composer and history chips; `POST /browser/sessions/{id}/annotate` accepts `marker` and the messages endpoint accepts `screenshot:false` per annotation. Annotate mode uses a custom chat-bubble cursor with the hotspot at its tail.
- **Conversation meta files:** ephemeral UI state lives outside SQLite as `<data>/conversations/{id}/meta.json` — title mirror, `browser.lastUrl` (recorded after each successful navigation) and the composer draft (text, attachments incl. annotation chips, send mode, reply toggle). Drafts save on input (debounced) and on blur, clear on successful send, and restore when a conversation is opened — chips and text survive page refresh and backend restarts. New `GET`/`PUT /api/conversations/{id}/meta` endpoints; deleting a conversation removes its meta.
- **Desktop notifications:** OS-native notifications for approval requests, finished turns and agent errors — `notify-send` with severity-mapped urgency on Linux, `osascript` on macOS, a hidden PowerShell balloon on Windows. Toggle plus test button in Settings → Account; `notify.enabled` setting. Notifications are muted while the server shuts down so intentionally stopped agents stay quiet.
- **Self-update:** periodic GitHub release check (after boot, then every 6h) with an OS notification once per new version. Settings → Account shows installed/latest versions, a **Check for updates** button, and an install flow that downloads the release asset, verifies it against `SHA256SUMS`, swaps the binary and restarts — progress streams over `update.status` WebSocket events and the web UI reloads itself when the new server version answers. `go run` temp builds refuse self-update and link to the installer instead.
- **API:** `GET /api/update`, `POST /api/update/check`, `POST /api/update/apply`, `POST /api/notifications/test`; `notifications` and `update` in `GET /api/settings`; `{notifications:{enabled}}` accepted by `PUT /api/settings`.
- **Queue management:** per-message **Edit** and **Cancel** buttons on queued inputs (`POST /api/conversations/{id}/turns/{turnId}/cancel`, `dropMessage` removes the queued message for the edit flow). Cancelling a queued input no longer requires stopping the running turn.
- **Agent mesh (ACP core):** ACP v1 client over stdio (newline-delimited JSON-RPC) with `initialize`, sessions, prompts, streaming updates, agent requests and permission flows.
- **Agent mesh (ACP core):** per-conversation turn queue with one active agent session at a time; cooperative `session/cancel`, queued-turn cancellation, cancel-and-resend, and hard process cleanup.
- **Agent mesh (ACP core):** session recovery chain (`session/load` → `session/resume` → fresh session with a bounded transcript recap injected into the next prompt) and transcript handoff when switching agents mid-conversation.
- **Agent mesh (ACP core):** mid-turn steering for agents that advertise `_session/steering`, plus queue and send-after-tools delivery modes.
- **Agent mesh (ACP core):** Unix process-group start/kill so shutdown reaches the whole agent tree; cross-platform process handling through `internal/procsys` for Windows.
- **Agent mesh (ACP core):** bounded stderr ring buffer per agent process (last lines survive a dead agent, never flood the chat).
- **Web control center:** embedded vanilla-JS SPA (no Node build) with a dark responsive UI from desktop to phone.
- **Web control center:** streaming markdown with collapsible thought cards, tool cards with raw input/output and diffs, plan checklists, usage meter and live session configuration (model, mode, thought level, speed).
- **Web control center:** composer with file/image attachments, drag & drop, paste, keyboard shortcuts, stop button, draft mode with config preview, and a send menu (steer / queue / send-after-tools).
- **Web control center:** inline approval cards, a global Approvals inbox, and mid-turn mode switching with `session/set_mode` / `session/set_config_option`.
- **Web control center:** conversation search, pin, rename, archive, export, delete and per-conversation routing; command palette (`Ctrl/Cmd+K`).
- **Web control center:** live event stream over WebSocket with reconnect/backoff, state resync, offline indicator and slow-client dropping.
- **Web control center:** vendored rendering stack (`marked`, `DOMPurify`, `highlight.js`, KaTeX and markdown extensions).
- **Agents:** auto-detection of popular ACP CLIs on first run (Devin, Claude Agent, Codex, Gemini, Copilot, OpenCode, Auggie, goose, Kimi, Cursor, Kilo, Qwen, Mistral Vibe, Kiro).
- **Agents:** agent probe for capabilities, models, config options and auth methods without disturbing live conversations.
- **Agents:** per-agent approval policies `ask` / `auto_safe` / `auto_all` with conversation-scoped remembered allow rules.
- **Agents:** encrypted per-agent environment variables (AES-GCM), runtime logs, stderr view, advertised authentication methods with in-dialog terminal login, and logout.
- **Agents:** idle mode that keeps opted-in agents warm from boot for instant composer responses.
- **Agents:** Codex adapter resolution (`codex-acp` + `CODEX_PATH`) and Cursor's `parameterizedModelPicker` capability so model parameters surface in the model menu.
- **MCP servers:** shared stdio, HTTP and SSE servers with encrypted secrets at rest.
- **MCP servers:** capability-based filtering of servers per agent on `session/new` / `session/load` / `session/resume`; editing a server invalidates running agent runtimes.
- **Channels:** modular channel manager (`Channel` / `Controller` interfaces) with the web as the native surface.
- **Channels:** WhatsApp via [whatsmeow](https://github.com/tulir/whatsmeow) — QR pairing, auto-reconnect, read receipts, typing indicator, one concise digest message per finished turn, allowlist, group policy (`disabled` / `mention` / `all`), media download, outbound media, message search and approval answers from chat (`/approve` / `/deny`).
- **Channels:** per-conversation routing (`replyToOrigin`, `mirrorFinal`, `notifyApprovals`); web always gets the full stream while external channels get a markdown-aware final digest.
- **Channels:** channel-specific response formatting rules and WhatsApp-style markdown rendering.
- **Automations:** scheduled ACP prompts — one-off (`-at`) or five-field cron expressions with a schedule builder.
- **Automations:** per-job agent, timezone, optional expiry, dedicated or per-run chat, and run history linked to the resulting conversation.
- **Automations:** catch-up execution of overdue jobs after downtime, expiry enforcement, and queueing when the dedicated chat is busy.
- **Browser pane:** lazy headless Chromium via chromedp/CDP with one isolated browser context per session.
- **Browser pane:** JPEG screencast over a dedicated WebSocket, CDP input forwarding, and element picking for composer annotations.
- **Reliability:** startup recovery (interrupted turns marked, stale approvals cancelled, queued turns resumed, statuses normalised); failed side-effecting turns are never replayed automatically.
- **Reliability:** SQLite in WAL mode with a single connection and busy timeout, pure Go (`modernc.org/sqlite`, no CGO).
- **Security:** bcrypt password hashing, SHA-256 session-token hashes, per-session CSRF tokens, Origin checks on mutations and WebSocket upgrades, login rate limiting, CSP and security headers.
- **Security:** filesystem and terminal requests confined to workspace roots with sensitive paths denied; uploads served only through authenticated endpoints; secrets encrypted at rest and redacted from API responses.
- **CLI:** management commands alongside the server — `agent`, `automation`, `config`, `whatsapp`, `reset-password` and `version`.
- **Distribution:** single static binary (`CGO_ENABLED=0`) with the web UI and operational docs embedded.
- **Distribution:** `make install` user-level install with a systemd user service, env file, boot autostart (linger) and safe re-installs; `uninstall` / `purge` keep or wipe data.
- **Distribution:** GitHub Actions pipeline on push to `master` — tests and vet on Linux and Windows, cross-compiled binaries (linux/windows × amd64/arm64), versioned tags from `VERSION`, releases with checksums and installers, and release notes from this file.
- **Distribution:** one-line installers — `curl …/install.sh | bash` (Linux, mirrors `make install`) and `irm …/install.ps1 | iex` (Windows, `%LOCALAPPDATA%\NusaMation` + hidden logon task), both checksum-verified.
- **Distribution:** deterministic in-repo fake ACP agent (`cmd/fake-acp-agent`) for testing and demos.

## Older releases