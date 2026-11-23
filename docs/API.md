# HTTP API

Base URL: `http://127.0.0.1:8787/api`

The API is private to the single admin account. All routes except `/health`, `/auth/status`, `/auth/login`, `POST /browser-use`, and `POST /conversation-control` require the `nm_session` cookie. Mutating requests additionally require:

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
| `GET` | `/conversations/{id}` | conversation + latest messages + `historyHasMore` + their turn timing + approvals + configs + active turn + agent |
| `PATCH` | `/conversations/{id}` | `{title?, label?, pinned?, archived?, routing?, agentId?}`; `label` is the mention handle (lowercase a–z/0–9/hyphen, 1–24 chars, unique across conversations, `""` clears) |
| `DELETE` | `/conversations/{id}` | cancels a live turn, then deletes |
| `DELETE` | `/conversations` | `{confirm:"DELETE"}` erases all conversations |
| `GET` | `/conversations/{id}/messages?after=&before=&limit=` | pagination by `seq`; `hasMore` indicates whether another message exists in the requested direction |
| `POST` | `/conversations/{id}/messages` | `{text, attachments:[{uploadId}], references?:[{kind,id,label,start,end}], annotations?:[{pickId, comment, screenshot?}], recordingId?, replyToChannel?, replyChannel?, delivery?: "queue" \| "after_tools" \| "steer"}` → `{message, turn}`; reference offsets are UTF-8 bytes in trimmed text; `delivery` defaults to `queue` |
| `POST` | `/conversations/{id}/cancel` | cancels running or first queued turn |
| `POST` | `/conversations/{id}/turns/{turnId}/cancel` | cancels that queued turn without touching the running one → `{turn}`; 404 unknown turn, 409 when it is no longer queued. Optional `{dropMessage:true}` also deletes the user message that queued it (used by "edit queued message"). |
| `POST` | `/conversations/{id}/retry` | `{turnId}` — queues a neutral `continue` nudge anchored to the failed turn named by `turnId` (the error card carries it); refuses completed/cancelled turns, turns of other conversations, and rooms with a running turn. The original prompt is never re-sent. |
| `POST` | `/conversations/{id}/prepare` | starts or resumes the ACP session and returns live configuration options |
| `POST` | `/conversations/{id}/config` | `{configId, value}`, `{model}`, or `{selector:"value[k=v,...]"}` — selector applies the model first, then re-reads options and applies modifiers; the response `result` reports applied/failed modifiers and ambiguity errors carry `candidates` |
| `POST` | `/conversations/{id}/elicitations/{requestId}` | `{action:"accept"|"decline"|"cancel", content?}` answers a pending agent input request |
| `POST` | `/conversations/{id}/agent` | `{agentId}`; starts a handoff recap |
| `GET` | `/conversations/{id}/meta` | file-backed UI meta: `{title?, browser?:{lastUrl, open?}, draft?:{text, attachments, references, sendDelivery, replyToChannel, updatedAt}}`; missing file → `{meta:{}}` |
| `PUT` | `/conversations/{id}/meta` | merge patch `{title?, browser?:{lastUrl, open?}, draft?}`; `lastUrl` and `open` merge independently; `about:blank` does not replace `lastUrl`; `draft:null` clears the draft (sent after a successful message send) |
| `POST` | `/conversations/{id}/routing` | full `Routing` object |
| `GET` | `/conversations/{id}/export` | JSON download |

Messages have `role`, `kind` (`text`, `thought`, `tool`, `system`, `error`, `notice`), `text`, `content`, `status`, `attachments`, `toolCallId`, `seq`, timestamps.

`POST /conversations/{id}/prepare` separates its failures: 403 `agent_auth_required` carries `authMethods`; 409 `agent_config_unavailable` means a stored model or option was rejected (not a login problem) and carries `configId`; other startup failures are 502 `prepare_failed`.

`GET /references?conversationId=...&q=...` searches the conversation's roots and registered MCPs. For a new draft, pass `agentId` and optionally `projectId` instead. It returns `{items:[{kind,id,name,detail}],truncated}`; search is bounded. In Git worktrees, when `git` is available, file and folder results follow Git ignore rules while retaining tracked files. Without Git or outside a worktree, the regular filesystem search is used. References on a message are explicit selections, validated against the message text and retained in its metadata. Draft reference offsets are browser UTF-16 positions; message reference offsets are UTF-8 bytes.

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
| `GET` | `/fs/list?path=` | `{path, parent, dirs:[{name, path}]}`. An empty `path` lists the user home directory. Only child directories. `parent` is empty at the filesystem root. `?files=1` also returns `files:[{name, path, size, mtime}]` for regular files. |
| `GET` | `/conversations/{id}/roots` | `{roots:[path]}` — the conversation's directories that currently exist, in preference order. A project member gets its cwd plus its project's `rootPaths` (and additionalDirectories); a standalone chat gets cwd, workspace, additionalDirectories. The pane's Terminal and Files tabs offer these as a path picker. |
| `GET` | `/projects/{id}/roots` | `{roots:[path]}` — the project's `rootPaths` that currently exist. Feeds the pick list in draft rooms, where no conversation owns roots yet. |
| `GET` | `/fs/file?path=` | Streams a local file — same pipeline as `/conversations/{id}/files` (relative paths resolve against the home directory). The draft room's file tab uses it for previews and downloads. |
| WebSocket | `/conversations/{id}/terminal?cwd=` | interactive shell; `cwd` may be any existing absolute directory (the pane offers the conversation's roots; a spawned shell can `cd` anywhere anyway), empty picks the first existing conversation root. Same protocol as `auth-terminal`: `{type:"input"|"resize"}` in, binary output plus a final `{type:"exit"}` out |
| WebSocket | `/terminal?cwd=` | conversation-less shell for draft rooms; `cwd` may be any existing directory, empty falls back to the home directory. Same wire protocol. |

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
| `POST` | `/agents/reload` | restarts every running agent process → `{reloaded, skipped}`; busy agents are skipped |
| `POST` | `/agents/{id}/probe` | starts temporary ACP session |
| `POST` | `/agents/{id}/authenticate` | `{methodId}` |
| WebSocket | `/agents/{id}/auth-terminal?methodId=` | interactive terminal for the agent's advertised terminal login method |
| `POST` | `/agents/{id}/logout` | |
| `GET` | `/agents/{id}/logs` | runtime status, PID, uptime, stderr ring |

`AgentInput` fields: `name`, `slug` (optional on create — derived from `name` when omitted), `description`, `command`, `args`, `env`, `cwd`, `icon`, `color`, `enabled`, `autoStart`, `approvalPolicy`, `defaultModel`.

## MCP

| Method | Path | Notes |
|---|---|---|
| `GET` | `/mcp` | `{servers: [servers]}` shared by all agents; env/headers redacted |
| `POST` | `/mcp` | `MCPServer` |
| `PATCH` | `/mcp/{id}` | full object; affected sessions are refreshed per room when idle |
| `DELETE` | `/mcp/{id}` | |

## Conversation control

`POST /conversation-control` is the loopback bridge for the
`nusamation_conversation` MCP child. Like `/browser-use` it uses
`Authorization: Bearer <token>` instead of the admin cookie; the token selects
the caller conversation and the body cannot retarget it. Every conversation
may use every tool — there is no opt-in.

The body is `{tool, ...}`. Supported tools: `list`, `projects_list`, `search`, `export`, `read`, `wait`, `send`, `stop`, `spawn`, `report`. Control tools work during delegated turns too — a turn started by another conversation may itself call them. Read responses exclude thoughts and raw tool payloads by default and cap each message and the whole response; `read`/`wait` include `seq` cursors for pagination. The target tools — `read`, `wait`, `send`, `stop` — accept a conversation id or its `label` (the room's mention handle).

`list` accepts `project` (id, exact name, or folder basename) and returns `label`, `projectId`/`projectName` plus a compact `lastTurn` status/prompt/answer preview on each row. `projects_list` accepts `query` and returns project ids, names, root paths, pin state and active conversation counts. `spawn` accepts `project` or `projectId`.

`search` runs the SQLite FTS5 index (`messages_fts`) and returns ranked message hits with `conversationId`, `title`, `projectName`, `seq`, `role`, `kind`, `snippet` (matches in brackets), `score` and `at`. Filters: `project`, `agent`, `channel`, `conversationId`, `kinds` (default `text,error`), `after`/`before` Unix ms, `archived`, `limit`, `offset`.

`export` writes a JSONL file (`format:"jsonl"`, default) for a conversation or project and returns `{path, messages, bytes, conversations, truncated, expiresAt}`. Files are `0600` under `<data>/exports/`, expire after ten minutes, and never point at the SQLite database or secret files. `includeThoughts` and `includeToolOutput` opt into thought rows and raw tool content; `limit` caps messages (default 10000).

`send` takes `{conversationId, text, delivery, requestId}`. `delivery` is `queue` (default), `steer`, or `after_tools`; cross-conversation `steer` becomes `after_tools` for a running target and `queue` for an idle target. `requestId` deduplicates retries for ten minutes in memory. `stop` takes `{conversationId, turnId?}`. `spawn` takes `{agent, title?, prompt?, project|projectId?}` (10 active children, three creates per minute). `report` takes `{text}` (one per minute, maximum 500 bytes).

`GET /conversation-control/activity?caller=&target=&limit=` returns the in-memory audit trail. Writes are rate limited (20/minute, burst 5); `spawn` uses its own tighter bucket. Target turns created by the bridge use origin `agent`, so their final answer is not delivered to the target's own channel and `mirrorFinal` is suppressed; target approvals notify the target's first linked chat. The bridge never answers an approval itself.

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
| `GET` | `/uploads/{id}` | authenticated file bytes; media and PDF inline; `?download=1` forces a download |
| `GET` | `/conversations/{id}/files?path=` | authenticated local file bytes; absolute, relative or `file://` paths; `?download=1` forces a download |

Local artifact paths resolve on the server. Relative paths use the conversation cwd (or the default workspace when missing); absolute paths may point outside the workspace, such as generated files in `/tmp`. Regular files support HEAD and byte ranges for media playback. Editor suffixes `:line` and `:line:column` are resolved after checking for an exact filename. Missing files return 404; directories and non-regular files return 422.

## Embedded browser

Web-only embedded Chromium pane (lazy headless Chromium, one isolated browser context per session, under a per-profile Chromium process — every named profile gets its own persistent user-data dir under `<data>/browser-profiles/<name>`, so cookies, storage, history, and saved logins survive restarts and stay isolated between profiles). Requires a Chrome/Chromium binary (`-browser-exec` / `NUSAMATION_BROWSER_EXEC`, otherwise PATH discovery, then the `<data>/chrome/` self-install dir; on Windows the standard install locations under `Program Files` and `LOCALAPPDATA` are also searched for Chrome, Chromium, and Edge). When no executable resolves, the pane's error state offers to install one (`download` = Chrome for Testing `chrome-headless-shell` unpacked under `<data>/chrome/`, or `pkg:<pm>` = the detected distro package manager — the only path on linux/arm64 such as proot-distro, where Chromium also launches with `--no-sandbox`); `nusamation browser status|install` is the CLI equivalent.

Each Chromium process opens a local debugging port so its bundled DevTools can inspect the current page. The pane serves the frontend through the authenticated session route.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/browser/install` | → `{missing, execPath?, searched?, platform, proot?, methods:[{id,label,detail,needsRoot?}]}` — viable install methods when no Chromium executable resolves |
| `POST` | `/browser/install` | `{method}` runs one offered method synchronously → `{ok, execPath, log}`; 502 `install_failed` with the installer log tail when the method fails or still leaves no executable; non-whitelisted methods never run |
| `POST` | `/browser/sessions` | `{url?, conversationId?, profile?, width?, height?}` → `{session}` (201); `profile` selects the persistent browser profile (default `"default"`) |
| `GET` | `/browser/sessions` | `{sessions}` incl. `status:"crashed"` tombstones |
| `GET` | `/browser/sessions/{id}` | `{session}` with live nav state |
| `DELETE` | `/browser/sessions/{id}` | closes the session and its browser context |
| `POST` | `/browser/sessions/{id}/claim` | `{conversationId}` → `{ok}`; binds a draft-created (`""`-owned) session to a materialized conversation — 404 unknown conversation, 409 `session_claimed` if already owned |
| `POST` | `/browser/sessions/{id}/navigate` | `{url}` → `{url,title,canGoBack,canGoForward}`; scheme-less hosts get implicit `https://` with an `http://` retry on transport failure, scheme-less non-hosts become a Google search, `file:`/`about:`/`data:` pass through; 409 `busy` during in-flight nav/pick |
| `POST` | `/browser/sessions/{id}/profile` | `{profile}` → `{session}`; migrates the tab into that profile's Chromium and reloads the current URL |
| `GET` | `/browser/sessions/{id}/console` | `{entries}` drained from the page's console-tap buffer |
| `GET` | `/browser/sessions/{id}/netlog` | `{entries}` — the session's always-on bounded network ring (recent request/response metadata; recordings keep their own sidecar) |
| `POST` | `/browser/sessions/{id}/eval` | `{expression}` → `{ok, result}` or `{ok:false, error}`; same surface as `browser_evaluate` |
| `GET` | `/browser/sessions/{id}/login-offer?id=...` | returns the current offer's username and password for editing in the pane |
| `POST` | `/browser/sessions/{id}/login-offer` | `{id, save, username?, password?}` accepts, edits or dismisses the current offer |
| `GET` | `/browser/sessions/{id}/autofill` | returns `{origin, credentials}` for the current profile and page without passwords |
| `POST` | `/browser/sessions/{id}/autofill` | `{id, origin}` explicitly fills the selected account → `{filled}` |
| `GET` | `/browser/history` | `?profile=&q=&limit=` → `{entries}`; per-profile visit history for address-bar suggestions |
| `GET` | `/browser/profiles` | `{profiles}` profile names seen (default plus every profile a session/credential was created under) |
| `GET` | `/browser/credentials` | `?profile=` → `{credentials}` with passwords masked |
| `DELETE` | `/browser/credentials/{id}` | deletes one saved login |
| `POST` | `/browser/sessions/{id}/history` | `{action:"back"\|"forward"\|"reload"}`; missing entry is a 200 no-op |
| `POST` | `/browser/sessions/{id}/annotate` | `{kind?,x,y,width?,height?,marker?}` viewport CSS px → `{pickId, marker, descriptor, upload}` (full-viewport PNG with the numbered badge); `kind` is `element` (default — hit-tests x/y into the pin ring, 422 `pick_missed`), `region` (strokes the drawn `{x,y,width,height}` rect — positive size required, no hit test), or `page` (whole viewport — badge only, no coordinates) |
| `POST` | `/browser/sessions/{id}/annotate/preview` | `{x,y}` → `{missed, skipped, rect, tagName, name, selector}` hover probe; no paint/upload/pick; `skipped` while busy instead of 409 |
| `POST` | `/browser/sessions/{id}/annotate/clear` | clears the painted highlight; picks/uploads persist |
| `POST` | `/browser/sessions/{id}/recording` | `{env?}` → `{recording}` (201); starts a session recording (409 `recording_active`); `env` is a free-form label |
| `GET` | `/browser/sessions/{id}/recording` | `{recording}` meta for the active recording; 404 `no_active_recording` |
| `POST` | `/browser/sessions/{id}/recording/pause` | pauses event capture; appends while paused are dropped (state markers still bound the gap) |
| `POST` | `/browser/sessions/{id}/recording/resume` | resumes a paused recording |
| `POST` | `/browser/sessions/{id}/recording/stop` | finalizes: writes `summary.txt`, status `completed` → `{recording}` |
| `GET` | `/browser/sessions/{id}/recording/events` | `?after=&limit=&types=` → `{events, next, done}`; `types` is comma-separated; `next` is the seq cursor for the next poll; `done` means the log is finalized and fully read |
| `GET` | `/browser/sessions/{id}/recording/network` | `?after=&limit=` → `{events, next, done}` — pages `network.jsonl` over its own seq space |
| `GET` | `/browser/sessions/{id}/recording/summary` | `{summary}` — exists once finalized (404 `summary_unavailable` while live) |
| `GET` | `/conversations/{id}/recordings` | `{recordings}` — metas for the conversation, newest first; live recordings merge over disk snapshots |
| `GET` | `/recordings/{recId}/events` | same `{events, next, done}` — works after the session is gone |
| `GET` | `/recordings/{recId}/network` | `?after=&limit=` → `{events, next, done}` — the sidecar for a stored recording |
| `GET` | `/recordings/{recId}/summary` | `{summary}` |
| `GET` | `/recordings/{recId}/artifact/{name}` | serves a file from the recording's `artifacts/` dir (404 `recording_not_found`); `downloads/{guid}` reaches captured downloads |
| WebSocket | `/browser/sessions/{id}/stream` | shared live view: every attached client receives the same frames; input is leased to one controller |
| `GET` | `/browser/sessions/{id}/devtools/{path...}` | serves Chrome's DevTools frontend and its session-scoped debugging connection |

Stream protocol: server→client binary messages are JPEG screencast frames; text messages include `meta`, `control`, `login_offer`, `login_choices`, `closed` and `pong`. Login choices contain account metadata, not passwords. `meta` carries `deviceWidth/deviceHeight/mobile/touch/dpr/pageScaleFactor/scrollOffsetX/scrollOffsetY/url/title/canGoBack/canGoForward` on change. The attach-time meta adds `viewportHeld` when other viewers already own the viewport; clients should adopt the reported viewport instead of emulating their own pane size, which avoids thrash and the transitional "mirrored" frame a mid-stream resize can leave on static pages.

The controlling viewer forwards single-finger touch gestures using `touch` messages with `phase` (`touchStart`, `touchMove`, `touchEnd`, or `touchCancel`) and viewport coordinates `x` and `y`. A `touchEnd` may include a nonzero `keyboardRequest` identifier; the controller receives `{type:"keyboard_focus", request, editable}` after the touch is dispatched so it can focus the local keyboard for an editable page element. The identifier lets the client discard stale responses.

`control` carries the input lease: `{type:"control", owner:"you"|"other"|"agent"|"none"}` — sent on attach and whenever the lease changes. `you` = this connection controls; `other` = another human viewer does; `agent` = the agent holds the lock; `none` = no controller. Only `you` may send input.

Client→server text messages: `{type:"mousemove"|"click"|"wheel"|"text"|"key"|"resize"|"emulate"|"takeover"|"ping"}` with coordinates in viewport CSS px (the client scales the displayed frame using the latest `meta`). `takeover` claims the lease — a human claim always wins, including over an agent lock; when the controller disconnects the lease returns to the oldest attached viewer. `resize` keeps the current device profile; `emulate` takes `{preset?:"desktop"|"mobile"|"tablet", width?, height?, dpr?, mobile?, ua?, touch?}` and applies full CDP device emulation — the pane's device bar sends these.

`POST /api/browser-use` additionally resolves `[[env:NAME]]` references in `type`/`fill` outbound text against the token-selected conversation's agent env (preflighted together; unknown/malformed fail closed, `\[[` escapes the opener, non-text targets return `invalid_secret_target`). Resolved raw values register on the session and are masked back to their tag in that endpoint's JSON responses only — exact-match replacement, longest-first, non-recursive; human REST, WS frames, and screenshots are not masked. Secret fills use real text input with `-webkit-text-security` best effort.

`POST /api/browser-use` also answers the recording read tools: `browser_get_recording_status`, `browser_get_recording_summary`, `browser_get_timeline` (`after_seq`, `limit` default 200/max 1000, `types` array filter), `browser_get_annotation` (`annotation_id` — the matching timeline event plus its pick's descriptor and marked-screenshot upload ref, 404 `annotation_not_found`), and `browser_get_artifact` (`name` — `downloads/<guid>` reaches captured downloads; images come back as `image`/`mimeType` blocks, text caps at 256KB, other binaries 415 `artifact_unsupported`, traversal 404 `artifact_not_found`). The token-selected conversation picks the recording — the live session's active recorder first, else the newest completed/interrupted — so the agent never sends a recording id, and these calls never launch or resurrect a session. A conversation with no recording gets a `detail` note, not an error.

`POST /api/browser-use` also serves the network-control tools: `browser_route` (400 `invalid_route` for a bad pattern, action, header, status, or target; `fulfill`/`abort`/`delay`/`continue`, optional `times`), `browser_route_list`, `browser_route_clear` (`id`; omitted clears every rule), `browser_route_ws` and `browser_route_ws_clear` (document-start WebSocket shim; `reload` defaults to true), `browser_add_init_script` and `browser_remove_init_script`, `browser_network_log` (`pattern`, `regex`, `method`, `status`, `errors`, `after`, `limit`), and `browser_wait_for_response` (`pattern`, `regex`, `method`, `status`, `after`, `timeout`; 408 `wait_timeout` when nothing matches, 400 `invalid_pattern` for a bad pattern). Rules are session-scoped, first-match-wins for requests, and Fetch interception is armed lazily: no rule means no paused request.

`annotations` on `POST /messages` reference `{pickId, comment, screenshot?}` values; `screenshot:false` keeps the pin's image out of the agent prompt (history thumbnails still work). Picks persist as JSON files under `<data>/browser-picks/` so chips survive a server restart; they are deleted when the browser session closes and swept after a week. The server resolves each pick to its descriptor + marked screenshot (400 `annotation_invalid` for unknown/stale picks), stores them in `message.metadata.annotations` (incl. `marker` — the badge number drawn on the image — and `omitScreenshot`), and adds an annotation context block with selector, nearby text, layout (rect · viewport · scroll) and computed style lines plus the marked image blocks to the agent prompt. The descriptor also carries `viewport{w,h,dpr}`, `computed` style fields, the element's opening `html` tag, `framePath`, scroll offsets and page dimensions. `descriptor.kind` says which fields are filled: `element` gets the full element descriptor, `region` only `rect` + page fields, `page` only page fields — region/page lines in the prompt name the kind and geometry instead of a tag/selector.

`recordingId` on `POST /messages` attaches one of the conversation's browser recordings to the send: the server resolves the id (400 `recording_invalid` for an unknown id or another conversation's recording), stores `metadata.recording{id,status,summary}` and adds a `<recording-context>` block with the recording id, status and `summary.txt` text — a live recording sends a brief built from its meta + counts instead, and a missing or unreadable recording file degrades to a note rather than failing the send. Recording files are never attached; the agent reads detail through the `browser_get_*` tools.

Recordings live under `<data>/browser-recordings/<recId>/`: `meta.json` (RecMeta — `{id, sessionId, conversationId, profile, status, env, createdAt, startedAt, endedAt, lastSeq, counts{events,annotations,errors}, interruptedReason?, truncated?}`), `events.jsonl` (one `{seq, ts, origin, type, data}` per line, seq monotonic, flushed ~1s), `network.jsonl` (the request-metadata sidecar — one `{seq, ts, stage:"request"|"response"|"redirect"|"finished"|"failed", requestId, method?, url, status?|errorText?, mimeType?, type?, initiatorUrl?, durationMs?}` per line; **never** bodies, cookies, or auth headers), `artifacts/` (plus `artifacts/downloads/` for captured downloads, named by guid), and `summary.txt` once finalized. Status runs `recording → paused → recording → finalizing → completed`; a session crash/close or a server restart leaves the recording `interrupted` (boot recovery rewrites stale live metas). A profile migration keeps recording and logs a `state` `tab_swapped` event. Recorded text values are masked with the session's registered secrets; a password input's value is recorded as `"[password]"`. Recording dirs are swept after a week, never while a live writer owns them.

Timeline `origin`/`type` vocabulary: `dom` covers in-page events (`click`, `dblclick`, `input`, `change`, `submit`, `scroll`, `key`, `navigate{navType:"spa"}`, `console`, `annotation`) captured by an always-installed tap that buffers in-page (cap 500, keeps collecting while idle) and delivers through the `__nusaRecPush` binding with a ~1s drain fallback; `agent` is `tool` — every `browser_use` dispatch `{tool, op, args, ok, code|error}` — plus `route` on the first hit of a request rule; `browser` covers `navigate{navType:"full"}`, `network_error` (status ≥400 or load failure — full request metadata goes to the sidecar), `download` `{guid,url,suggestedFilename,state:"started"|"progress"|"completed"|"canceled",file?}`, and `viewport` (also `system`-origin for pane-driven changes); `system` is lifecycle `state` markers plus input-lease states (`takeover`, `agent_lock{on}`). Downloads are allowed only while a recording runs (headless Chromium would drop them otherwise): the behavior is set on the profile's shared browser context while recording and restored on stop — sibling sessions' downloads redirect into the active recording's dir during that window but are not logged against it.

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

`GET /events` upgrades to an authenticated WebSocket. The server sends `hello` (with `version`) immediately, then events — including `update.status` while an update check or install runs and `conversation.control` after a conversation-control MCP write:

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
