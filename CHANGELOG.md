# Changelog

All notable changes to the Go core will be documented in this file. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.5] - 2026-11-10

### Changed

- MCP changes are detected automatically and refresh each room after its active turn or session preparation finishes, without stopping other rooms.
- Group internal MCP servers under `nusamation mcp run --browser-use` and `--conversation`, and show long CLI options with double dashes.

### Fixed

- Make fake-agent prompt dispatch traces follow stdin order so immediate cancellation tests are deterministic on Windows, and check export permission bits only on platforms that expose Unix modes.
- Preserve read-only control permissions when toggling spawn or progress pings, normalize nonpositive message page limits, retain MCP read cursors across hidden thoughts, and report additional wait pages. Serialize send deduplication before checking completed requests and treat punctuation as search term boundaries.
- The **MCP servers** and **Agent control** settings tabs now refetch from the server when opened, so changes made outside this browser (API calls, another tab, direct DB edits) no longer render a stale list.
- Keep steered messages in the running turn's timeline and show the history control only when earlier messages exist. Image and text attachments open in a preview, with a download option for files. The web send mode can be selected in Defaults; channel messages continue to steer.
- Recheck the after-tools boundary when requesting cancellation and wait for active ACP terminals to exit.

### Added

- **MCP management CLI.** `nusamation mcp` lists, inspects, adds, updates and removes shared MCP servers without an external SQLite installation, using the existing sealed environment and header storage.
- **Conversation control MCP.** A new `nusamation_conversation` stdio server (`nusamation mcp run --conversation`) lets an authorized conversation list, read, wait on, send to, stop, and spawn other conversations. `projects_list` and the `project` filter resolve a project by id, name, or folder basename so an agent can act on “project A”; `spawn(project)` creates the delegated chat inside it. `search` uses a new SQLite FTS5 index and returns ranked message snippets, while list rows carry a compact last-turn summary. `export` writes a policy-scoped JSONL file for deep `rg`/`jq` analysis. It is off by default: enable **Settings → Agent control**, then opt the calling conversation in from its conversation menu. The loopback bridge uses a per-conversation hashed token, a turn started by orchestration has no control access, and the tools never answer another conversation's approval. Cross-conversation `steer` is delivered as `after_tools` (running target) or `queue` (idle target); target finals are not delivered to the target's own channel; target approvals notify its first linked chat. `spawn` and `report` have separate per-chat opt-ins; writes are rate limited and an in-memory activity trail is available from the conversation menu. `nusamation conversations ...` is the same bridge for scripts. The MCP packaging moved under `internal/mcp` (`stdio`, `loopback`, `grants`, `browseruse`, `conversation`); the `browser_use` server name and tool names are unchanged.

## [0.2.4] - 2026-11-09

### Fixed

- **Skipping an agent question no longer fails the tool.** Closing the elicitation popup (X, Esc, backdrop) on a form whose fields are all optional now answers it with empty content, so the agent receives "Unanswered" and the turn keeps going. Forms with required fields and URL requests still cancel. Previously a dismissal sent `cancel`, which OpenCode's `question` tool turns into a failed call ("The user dismissed this question").

## [0.2.3] - 2026-11-09

### Changed

- **Tool calls and thoughts render as flat rows, not stacked cards.** Collapsed rows show only a kind glyph, the title, and a chevron — the icon box, `name · N locations` sub-line, per-call `DONE` pill, preview snippet, and per-message avatar are gone. Expanded bodies open a single box with the section labels and actions inside it, and shell inputs render as `$ command` under `Command in <cwd>` instead of the raw JSON. Status only appears when it matters: `Failed · Exit N` in red or a pulsing `Running`; metadata-only result envelopes (`{"metadata": …}`) are no longer dumped as their own section.

### Fixed

- **Browser pane no longer opens on the wrong room.** `browser.drive` events were gated on `S.current` — a detail object that could still hold another conversation (bootstrap seeded it with the top row, so the draft/welcome screen counted as that room — always on mobile, and after any WS reconnect). Drives for a room that is not on screen no longer force the pane open, a pane can only attach a session owned by the driving conversation, and the pane no longer races a second session attach when it opens. `S.current` is no longer pre-filled at bootstrap; it only ever holds the room actually loaded — which also fixes draft config previews silently no-op'ing on the welcome screen and a WS reconnect yanking the draft view into the top room.
- **A browser pane the user closed stays closed.** Previously every `browser_use` tool call reopened the pane, and the server rewrote `browser.open:true` on each drive so even a refresh resurrected it. The drive now respects the persisted `open:false` (and the in-session close flag), the server only records the driven URL, and a manual open lifts the suppression again.
- **`browser_use` actions verify the target.** Ref-based click/type/hover/fill/select now fail with `not_actionable` when the element is disabled, read-only, outside the viewport, or covered at its center, instead of silently clicking an overlay or changing a disabled field. Page dialogs (`alert`/`confirm`/`prompt`/`beforeunload`) are auto-dismissed, so they no longer stall the pane with `context deadline exceeded`.
- **Snapshots use real form labels and disclose truncation.** `<label>`/`aria-labelledby`/wrapping labels now name checkboxes, radios, inputs, and selects; the tree ends with a truncation marker when the 600-line/depth-24 cap hides elements.
- **Open shadow roots are part of the snapshot.** Snapshot and ref actions now enter open shadow roots — names, refs, clicks, typing, fills, and selects work for web components; closed roots remain opaque. Refs resolve through a per-snapshot registry, so detached or stale elements fail as `missing` instead of hitting a replacement.
- **Unserializable eval values are not null.** `browser_evaluate` returns BigInt/NaN/Infinity as strings (for example `"10n"`, `"NaN"`) and `-0` as `-0`; previously they collapsed to `null`.
- **Console error objects keep their stack.** Error and unhandled-rejection values are formatted with their message/stack instead of `{}`.
- **`select_option`/`fill_form` reject bad targets.** Missing options, disabled or read-only fields, and multiple values on a single select are errors instead of silent success.
- **Raw HTML in messages no longer renders as elements.** Agent text like `srcdoc="<button>Inner</button>"` used to produce a real (dead) `<button>` inside the bubble — DOMPurify's allowlist permits interactive tags. Marked `html` tokens are now escaped to text before sanitizing, so embedded markup always displays literally.

## [0.2.2] - 2026-11-08

### Changed

- **Annotation prompts are tagged.** The prompt block for page annotations is wrapped in `<annotation-context>…</annotation-context>` so agents can tell injected context apart from user text, and it now points at the `browser_use` tools for inspecting the live page behind the screenshots.
- **Static asset caching:** files under `/static/` are now served with a strong ETag (SHA-256 of the content) and `Cache-Control: no-cache`, so browsers revalidate every load and get a cheap `304 Not Modified` when nothing changed — fresh assets no longer depend on `?v=` cache-bust specifiers. The app shell and login page still use `no-store`.
- **Agent browser pointer:** the overlay marking where `browser_use` moves or clicks inside the embedded browser pane is now a real arrow cursor image (36px, LightsOut theme) instead of a small blue dot. The image's tip hotspot lands on the remote coordinates, and click events still shrink it briefly with a blue halo.
- **"New conversation" wording:** every user-facing "New chat" label — the sidebar button, the per-project menu item, the composer scope chip, and the automation session mode — now reads "New conversation". Internal ids and classes are unchanged.

### Added

- **`browser_use` now speaks Playwright's vocabulary.** The tool list uses the names agents are already trained on — `browser_navigate`, `browser_navigate_back`/`forward`, `browser_snapshot`, `browser_take_screenshot`, `browser_click`, `browser_type`, `browser_hover`, `browser_press_key`, `browser_scroll`, `browser_select_option`, `browser_fill_form`, `browser_wait_for`, `browser_evaluate`, `browser_console_messages`, `browser_resize`, `browser_emulate`, `browser_mouse_click_xy`/`move_xy`. `browser_snapshot` returns an indented accessibility tree with `ref` markers (`- button "Go" [ref=e4]`) that the element tools resolve back to page coordinates; refs are re-stamped per snapshot and stale refs return a clear "run browser_snapshot again" error. `browser_fill_form`/`browser_select_option` set values through native setters with `input`/`change` events so framework-controlled inputs register them; `browser_console_messages` returns console output and page errors captured since load. The earlier short names (`navigate`, `snapshot`, `click`, `type`, `press`, `scroll`, `move`, `emulate`, `eval`) still work as aliases.

- **`browser_use` gains an `eval` tool** — evaluates a JavaScript expression in the page via `Runtime.evaluate` (promises awaited, result returned as JSON in `result`). DOM queries, computed styles and counts that a screenshot cannot prove are now possible without leaving the pane.

- **Device emulation for the embedded browser.** `browser_use` gains an `emulate` tool — preset `desktop`, `mobile`, or `tablet`, or explicit `width/height/dpr/mobile/ua/touch` — applied through CDP device metrics, user-agent and touch overrides. On a mobile profile, clicks become real touch taps. `move`/`click`/`scroll` coordinates now accept viewport fractions `0`–`1` (values above `1` stay CSS pixels, so existing calls are unchanged), and stream `meta` reports the active `mobile`/`dpr` profile. The pane gains a device row under the address bar — **Relative** follows the pane size with the plain profile (no letterboxing), the presets pin a fixed device, and a readout shows the live viewport — so a human can see or take back the device an agent set.

### Fixed

- **Empty user bubble on annotation-only sends.** Sending only annotations (no text) no longer renders an empty dark card above the attachment chips — the bubble renders only when it has a quote, reply sender, or text.
- **`browser_use` MCP server:** the stdio transport now mirrors the client's framing. The spec's stdio transport is newline-delimited JSON; real MCP clients (Claude Code SDK, Devin) speak NDJSON while the server only replied with LSP-style `Content-Length` headers, so clients timed out waiting for a line they could parse — `mcp_list_tools` failed while spawning the server by hand worked. Replies now come back in the request's framing, and the reader still tolerates blank lines between messages.
- **Send menu:** the composer's delivery menu now has just **Steer** and **Add to queue**. "Steer" is one concept again: the agent gets the input mid-turn when it supports steering, otherwise NusaMation waits for the current tools, cancels, and resends — the path previously shown as a separate "Send after tools" item.
- **`browser_use` tokens survive restarts:** callback tokens are persisted to `<data>/browser-use-tokens.json`, so an agent session loaded before a NusaMation restart no longer gets 401 "Unknown browser_use token" on every tool call. Callbacks for deleted conversations are still rejected (the row check lives next to the token lookup now).
- **Web UI live stream:** three modules kept `?v=43` specifiers while the rest moved to `?v=44`, which forked the module graph — two copies of `state.js`, two `S` objects. The WebSocket connected and `onopen` marked one copy online while renders read the other copy, so the "Reconnecting to the live stream…" banner never cleared. All JS specifiers share one generation again, and a test now asserts it.
- **Steer dropped at turn end:** an `_session/steering` "injected" input has no turn of its own — it rides the running turn. When that turn finished with no assistant output after the accept, the input sat in history as a final message and was never answered. Accepted steers are now tracked against the turn; on completion, one that saw no following output is requeued as a normal queued turn (cancellable and editable like any other), and a failed mid-turn inject is requeued immediately.
- **Empty agent-side sessions:** probe, catalog-preview, and dropped sessions that never ran a prompt now use `session/delete` when the agent advertises it (falling back to `session/close`), instead of always closing. Agents like OpenCode keep `session/close`d entries in their session history, so every probe, preview respawn, or agent switch on an unsent conversation used to leave a ghost "Session" row; those are now removed entirely. Sessions that saw a prompt still close (resumable via `session/load`).
- **Sidebar empty state:** "No conversations yet" no longer shows when the only conversations live inside projects — it now appears only when there are no unarchived conversations at all.
- **New project scope:** creating a project now selects it as the scope for the next conversation and expands it in the sidebar, so the composer's "New conversation in …" chip appears immediately. Editing a project leaves the current scope alone.
- **Wide markdown tables:** tables in messages scroll horizontally instead of compressing every column; cells keep a 7rem minimum width.

## [0.2.1] - 2026-11-08

### Fixed

- **Windows installer:** the `NusaMation` logon task no longer shows a console window. The task still launches the binary itself (now with `-hide-console`, which makes the server hide its own console window right after startup), so the registration works without elevation and `schtasks /End /TN NusaMation` still stops the real process. Previously a visible console window appeared at logon and closing it killed the server.
- **Windows installer:** re-installing while the server is running no longer fails at `Copy-Item` — the installer stops the scheduled task and any running copy of the binary before replacing it, since Windows locks a running exe.
- **Browser pane on Windows:** the embedded browser now finds Chrome-family executables installed in the standard locations (`Program Files` / `LOCALAPPDATA` — Chrome, Chromium, Edge) instead of only searching PATH, where Windows installers never put them. Previously the pane reported "no Chromium executable found" on a stock Windows install.

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
