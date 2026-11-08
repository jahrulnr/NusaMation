# HTTP API

Base URL: `http://127.0.0.1:8787/api`

The API is private to the single admin account. All routes except `/health`, `/auth/status`, `/auth/login`, and `POST /browser-use` require the `nm_session` cookie. Mutating requests additionally require:

```http
X-CSRF-Token: <csrf from login or /auth/status>
Content-Type: application/json
```

Errors look like:

```json
{ "error": "human readable message", "code": "machine_code" }
```

## Auth

| Method | Path | Body / notes |
|---|---|---|
| `GET` | `/health` | public |
| `GET` | `/auth/status` | `{initialized, authenticated, csrf?}` |
| `POST` | `/auth/login` | `{password, remember}` → `{ok, csrf, expiresAt}` + cookie |
| `POST` | `/auth/logout` | clears session |
| `GET` | `/auth/me` | current session |
| `POST` | `/auth/password` | `{current, new}`; rotates sessions and returns `{ok, csrf}` |

Login is rate limited per IP. Cookies are `HttpOnly`, `SameSite=Lax`, `Secure` when served over HTTPS.

## Bootstrap

`GET /bootstrap` returns everything the SPA needs:

```json
{
  "app": {"name": "NusaMation", "version": "0.2.0", "publicUrl": "...", "dataDir": "...", "dev": false},
  "agents": [], "mcp": {}, "channels": [], "conversations": [], "projects": [], "approvals": [],
  "routingDefaults": {"replyToOrigin": true, "mirrorFinal": false, "notifyApprovals": true},
  "whatsapp": {"enabled": true, "groupPolicy": "mention", "...": "..."},
  "stats": {"conversations": 0, "active": 0, "pendingApprovals": 0}
}
```

## Conversations

| Method | Path | Notes |
|---|---|---|
| `GET` | `/conversations?q=&agentId=&channel=&archived=&limit=&offset=` | list |
| `POST` | `/conversations` | `{agentId?, title?, cwd?, projectId?}`. When `projectId` is set, cwd and `additionalDirectories` come from that project (unknown project → 404 `not_found`). When it is omitted, cwd falls back to the agent cwd, then the service workspace. |
| `GET` | `/conversations/{id}` | conversation + latest messages + their turn timing + approvals + configs + active turn + agent |
| `PATCH` | `/conversations/{id}` | `{title?, pinned?, archived?, routing?, agentId?}` |
| `DELETE` | `/conversations/{id}` | cancels a live turn, then deletes |
| `DELETE` | `/conversations` | `{confirm:"DELETE"}` erases all conversations |
| `GET` | `/conversations/{id}/messages?after=&before=&limit=` | pagination by `seq` |
| `POST` | `/conversations/{id}/messages` | `{text, attachments:[{uploadId}], annotations?:[{pickId, comment}], replyToChannel?, replyChannel?, delivery?: "queue" \| "after_tools" \| "steer"}` → `{message, turn}`; `delivery` defaults to `queue`, unknown values return 400 before anything is written |
| `POST` | `/conversations/{id}/cancel` | cancels running or first queued turn |
| `POST` | `/conversations/{id}/turns/{turnId}/cancel` | cancels that queued turn without touching the running one → `{turn}`; 404 unknown turn, 409 when it is no longer queued. Optional `{dropMessage:true}` also deletes the user message that queued it (used by "edit queued message"). |
| `POST` | `/conversations/{id}/retry` | re-queues the last user message |
| `POST` | `/conversations/{id}/prepare` | starts or resumes the ACP session and returns live configuration options |
| `POST` | `/conversations/{id}/config` | `{configId, value}` or `{model}` |
| `POST` | `/conversations/{id}/elicitations/{requestId}` | `{action:"accept"|"decline"|"cancel", content?}` answers a pending agent input request |
| `POST` | `/conversations/{id}/agent` | `{agentId}`; starts a handoff recap |
| `GET` | `/conversations/{id}/meta` | file-backed UI meta: `{title?, browser?:{lastUrl, open?}, draft?:{text, attachments, sendDelivery, replyToChannel, updatedAt}}`; missing file → `{meta:{}}` |
| `PUT` | `/conversations/{id}/meta` | merge patch `{title?, browser?:{lastUrl, open?}, draft?}`; `lastUrl` and `open` merge independently; `about:blank` does not replace `lastUrl`; `draft:null` clears the draft (sent after a successful message send) |
| `POST` | `/conversations/{id}/routing` | full `Routing` object |
| `GET` | `/conversations/{id}/export` | JSON download |

Messages have `role`, `kind` (`text`, `thought`, `tool`, `system`, `error`, `notice`), `text`, `content`, `status`, `attachments`, `toolCallId`, `seq`, timestamps.

A conversation includes `projectId` when it belongs to a project. Chats join a project only when created with `projectId` or restored by undo while still unassigned.

## Projects

`rootPaths[0]` is the only primary folder. A member's cwd is that path, and `metadata.additionalDirectories` is `rootPaths[1:]` (an empty list when there are no extras). Switching agents leaves that cwd and those directories in place. Removing a project deletes no directory and no chat. A pin change does not change folders or the ACP session.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/projects` | `{projects}` |
| `POST` | `/projects` | `{name?, sources, foldersConfirmed?}` → 201 `{project}`. An empty trimmed name is the basename of `sources[0]`. Every path is new, so a non-empty list without `foldersConfirmed:true` returns 409 `confirmation_required` and writes nothing. |
| `PATCH` | `/projects/{id}` | `{name?, sources?, pinned?, updatedAt?, foldersConfirmed?}`. `sources` and/or `name` (`""` derives the basename) require `updatedAt` — a missing one is 400 `invalid`, a different stored revision returns 409 `conflict` and changes nothing. Paths absent from the stored list need `foldersConfirmed:true`, otherwise 409 `confirmation_required` with `added`. Pin alone does not change folders; an omitted pin stays as stored. |
| `DELETE` | `/projects/{id}` | `{project, conversationIds}` for undo. cwd stays. |
| `POST` | `/projects/{id}/restore` | `{name, sources, conversationIds, pinned?}` recreates that id and its pin. Returns `{project, conversationIds}` with the chats that reattached — one is skipped when its `projectId` is no longer empty. 409 `conflict` if the id exists. |
| `GET` | `/fs/list?path=` | `{path, parent, dirs:[{name, path}]}`. An empty `path` lists the user home directory. Only child directories. `parent` is empty at the filesystem root. |

Project JSON: `id`, `name`, `rootPaths`, `pinned`, `createdAt`, `updatedAt`. Timestamps are Unix milliseconds.

`confirmation_required` is `{"error","code","added"}`. Other project errors stay `{"error","code"}`: 400 `invalid`, 404 `not_found`, 409 `conflict`.

`project.updated` follows a mutation. A delete sends `{"id","deleted":true}`. Member chats whose cwd or `projectId` changed also emit `conversation.updated`.

## Approvals

| Method | Path | Notes |
|---|---|---|
| `GET` | `/approvals?limit=` | pending approvals across conversations |
| `POST` | `/approvals/{id}` | `{optionId}` (`""` cancels) |
| `POST` | `/conversations/{id}/approvals/{approvalId}` | scoped variant |
| `GET` | `/approval-rules?agentId=&conversationId=` | list |
| `DELETE` | `/approval-rules/{id}` | delete |

Approval option kinds: `allow_once`, `allow_always`, `reject_once`, `reject_always`. Resolving an unknown/already-resolved approval returns `409` with `code: approval_failed`.

## Agents

| Method | Path | Notes |
|---|---|---|
| `GET` | `/agents` | secrets redacted |
| `POST` | `/agents` | `AgentInput` |
| `GET` | `/agents/{id}` | public agent |
| `PATCH` | `/agents/{id}` | full/partial agent update; restarts runtime |
| `DELETE` | `/agents/{id}` | refuses while conversations exist |
| `POST` | `/agents/{id}/probe` | starts temporary ACP session |
| `POST` | `/agents/{id}/authenticate` | `{methodId}` |
| WebSocket | `/agents/{id}/auth-terminal?methodId=` | interactive terminal for the agent's advertised terminal login method |
| `POST` | `/agents/{id}/logout` | |
| `GET` | `/agents/{id}/logs` | runtime status, PID, uptime, stderr ring |

`AgentInput` fields: `name`, `slug`, `description`, `command`, `args`, `env`, `cwd`, `icon`, `color`, `enabled`, `autoStart`, `approvalPolicy`, `defaultModel`.

## MCP

| Method | Path | Notes |
|---|---|---|
| `GET` | `/mcp` | `{servers: [servers]}` shared by all agents; env/headers redacted |
| `POST` | `/mcp` | `MCPServer` |
| `PATCH` | `/mcp/{id}` | full object; invalidates all agent runtimes |
| `DELETE` | `/mcp/{id}` | |

## Channels

| Method | Path | Notes |
|---|---|---|
| `GET` | `/channels` | statuses |
| `POST` | `/channels/{name}/connect` | WhatsApp starts QR/connect flow |
| `POST` | `/channels/{name}/disconnect` | |
| `POST` | `/channels/{name}/logout` | unlink device |
| `GET` | `/channels/{name}/qr` | PNG, `image/png`, `404` if no QR |
| `GET` | `/channels/{name}/settings` | adapter settings |
| `PUT` | `/channels/{name}/settings` | full settings object |

WhatsApp settings:

```json
{
  "enabled": true,
  "autoStart": true,
  "allowedNumbers": [],
  "groupPolicy": "mention",
  "readReceipts": true,
  "sendTyping": true
}
```

## Uploads

| Method | Path | Notes |
|---|---|---|
| `POST` | `/uploads` | multipart `file`, optional `conversationId`; max 25 MB |
| `GET` | `/uploads/{id}` | authenticated file bytes; images inline |

## Embedded browser

Web-only embedded Chromium pane (lazy headless Chromium, one isolated browser context per session). Requires a Chrome/Chromium binary (`-browser-exec` / `NUSAMATION_BROWSER_EXEC`, otherwise PATH discovery; on Windows the standard install locations under `Program Files` and `LOCALAPPDATA` are also searched for Chrome, Chromium, and Edge).

| Method | Path | Notes |
|---|---|---|
| `POST` | `/browser/sessions` | `{url?, conversationId?, width?, height?}` → `{session}` (201) |
| `GET` | `/browser/sessions` | `{sessions}` incl. `status:"crashed"` tombstones |
| `GET` | `/browser/sessions/{id}` | `{session}` with live nav state |
| `DELETE` | `/browser/sessions/{id}` | closes the session and its browser context |
| `POST` | `/browser/sessions/{id}/navigate` | `{url}` → `{url,title,canGoBack,canGoForward}`; bare domains get `https://`; 409 `busy` during in-flight nav/pick |
| `POST` | `/browser/sessions/{id}/history` | `{action:"back"\|"forward"\|"reload"}`; missing entry is a 200 no-op |
| `POST` | `/browser/sessions/{id}/annotate` | `{x,y,marker?}` viewport CSS px → `{pickId, marker, descriptor, upload}` (full-viewport PNG with the pin ring + numbered badge); 422 `pick_missed` |
| `POST` | `/browser/sessions/{id}/annotate/preview` | `{x,y}` → `{missed, skipped, rect, tagName, name, selector}` hover probe; no paint/upload/pick; `skipped` while busy instead of 409 |
| `POST` | `/browser/sessions/{id}/annotate/clear` | clears the painted highlight; picks/uploads persist |
| WebSocket | `/browser/sessions/{id}/stream` | one live stream per session; a new attach sends the old one `closed/stream_replaced` |

Stream protocol: server→client binary messages are JPEG screencast frames; text messages are `{type:"meta"|"closed"|"pong"}`. `meta` carries `deviceWidth/deviceHeight/mobile/touch/dpr/pageScaleFactor/scrollOffsetX/scrollOffsetY/url/title/canGoBack/canGoForward` on change. Client→server text messages: `{type:"mousemove"|"click"|"wheel"|"text"|"key"|"resize"|"emulate"|"ping"}` with coordinates in viewport CSS px (the client scales the displayed frame using the latest `meta`). `resize` keeps the current device profile; `emulate` takes `{preset?:"desktop"|"mobile"|"tablet", width?, height?, dpr?, mobile?, ua?, touch?}` and applies full CDP device emulation — the pane's device bar sends these.

`annotations` on `POST /messages` reference `{pickId, comment, screenshot?}` values; `screenshot:false` keeps the pin's image out of the agent prompt (history thumbnails still work). Picks persist as JSON files under `<data>/browser-picks/` so chips survive a server restart; they are deleted when the browser session closes and swept after a week. The server resolves each pick to its descriptor + marked screenshot (400 `annotation_invalid` for unknown/stale picks), stores them in `message.metadata.annotations` (incl. `marker` — the badge number drawn on the image — and `omitScreenshot`), and adds an annotation context block with selector, nearby text, layout (rect · viewport · scroll) and computed style lines plus the marked image blocks to the agent prompt. The descriptor also carries `viewport{w,h,dpr}`, `computed` style fields, the element's opening `html` tag, `framePath`, scroll offsets and page dimensions.

## Settings and stats

| Method | Path | Notes |
|---|---|---|
| `GET` | `/settings` | routing defaults, notifications, update status, version, public URL |
| `PUT` | `/settings` | `{routing}` and/or `{notifications:{enabled}}` |
| `GET` | `/update` | current/latest versions, availability, apply state |
| `POST` | `/update/check` | synchronous release check → update status |
| `POST` | `/update/apply` | download + verify + install the latest release; 202 on start, 409 when already installing or not updatable |
| `POST` | `/notifications/test` | send a desktop test notification; 503 when no OS backend exists |
| `GET` | `/stats` | conversation/approval/channel counters |

## WebSocket

`GET /events` upgrades to an authenticated WebSocket. The server sends `hello` (with `version`) immediately, then events — including `update.status` while an update check or install runs:

```json
{
  "id": 42,
  "type": "message.delta",
  "conversationId": "cv_...",
  "at": 1760000000000,
  "data": {"messageId": "msg_...", "kind": "text", "delta": "hello"}
}
```

The socket sends protocol pings every 25s. Clients may send `{"type":"ping"}` for an application-level pong. On disconnect, reconnect with exponential backoff and call `GET /bootstrap` plus `GET /conversations/{id}` to reconcile; deltas are advisory, final `message.updated` events are authoritative. `message.deleted` carries `{"messageId": "..."}` and fires when a message is removed (for example the dropped copy of a cancelled queued prompt).

## Idempotency and queues

`POST /messages` creates a new turn, except `delivery: "steer"` while a turn is running and the agent accepts mid-turn input: that message is injected into the running turn and the response's `turn` is the one already running. A steer the agent cannot accept — web or channel — waits until the running turn's tools finish, cancels it, then sends, the same cancel-and-resend path as `after_tools`. `cancel` cancels the running turn or the first queued turn when idle. `turns/{turnId}/cancel` cancels one queued turn without stopping the running one. `retry` reuses the last user message rather than duplicating a bubble.

Queued inputs run in strict FIFO order; no input is reordered. `delivery: "after_tools"` only changes the running turn: if the new input is the queue head and a turn is running, that turn is cancel-and-resend, not native steering. The running turn is asked to stop once every reported tool is `completed` or `failed` (or immediately if it reported no tools), and only after the agent answers the cancelled prompt does the queue advance. Pending approvals are not skipped. The input's message metadata carries `delivery: "after_tools"` and, when armed, `interruptTurnId`. If the agent does not answer within 15 seconds, the conversation shows one notice and the queue keeps waiting. Tool states are best-effort reports from the agent, so the agent may start a new tool between a report and the cancel.
