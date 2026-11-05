# HTTP API

Base URL: `http://127.0.0.1:8787/api`

The API is private to the single admin account. All routes except `/health`, `/auth/status` and `/auth/login` require the `nm_session` cookie. Mutating requests additionally require:

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
  "app": {"name": "NusaMation", "version": "0.1.0", "publicUrl": "...", "dataDir": "...", "dev": false},
  "agents": [], "mcp": {}, "channels": [], "conversations": [], "approvals": [],
  "routingDefaults": {"replyToOrigin": true, "mirrorFinal": false, "notifyApprovals": true},
  "whatsapp": {"enabled": true, "groupPolicy": "mention", "...": "..."},
  "stats": {"conversations": 0, "active": 0, "pendingApprovals": 0}
}
```

## Conversations

| Method | Path | Notes |
|---|---|---|
| `GET` | `/conversations?q=&agentId=&channel=&archived=&limit=&offset=` | list |
| `POST` | `/conversations` | `{agentId?, title?, cwd?}` |
| `GET` | `/conversations/{id}` | conversation + latest messages + their turn timing + approvals + configs + active turn + agent |
| `PATCH` | `/conversations/{id}` | `{title?, pinned?, archived?, routing?, agentId?}` |
| `DELETE` | `/conversations/{id}` | cancels a live turn, then deletes |
| `DELETE` | `/conversations` | `{confirm:"DELETE"}` erases all conversations |
| `GET` | `/conversations/{id}/messages?after=&before=&limit=` | pagination by `seq` |
| `POST` | `/conversations/{id}/messages` | `{text, attachments:[{uploadId}], replyToChannel?, replyChannel?, delivery?: "queue" \| "after_tools" \| "steer"}` → `{message, turn}`; `delivery` defaults to `queue`, unknown values return 400 before anything is written |
| `POST` | `/conversations/{id}/cancel` | cancels running or first queued turn |
| `POST` | `/conversations/{id}/retry` | re-queues the last user message |
| `POST` | `/conversations/{id}/prepare` | starts or resumes the ACP session and returns live configuration options |
| `POST` | `/conversations/{id}/config` | `{configId, value}` or `{model}` |
| `POST` | `/conversations/{id}/elicitations/{requestId}` | `{action:"accept"|"decline"|"cancel", content?}` answers a pending agent input request |
| `POST` | `/conversations/{id}/agent` | `{agentId}`; starts a handoff recap |
| `POST` | `/conversations/{id}/routing` | full `Routing` object |
| `GET` | `/conversations/{id}/export` | JSON download |

Messages have `role`, `kind` (`text`, `thought`, `tool`, `system`, `error`, `notice`), `text`, `content`, `status`, `attachments`, `toolCallId`, `seq`, timestamps.

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
| `GET` | `/mcp?agentId=` | map `agentId → [servers]`; env/headers redacted |
| `POST` | `/mcp` | `MCPServer` |
| `PATCH` | `/mcp/{id}` | full object; invalidates agent runtime |
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

## Settings and stats

| Method | Path | Notes |
|---|---|---|
| `GET` | `/settings` | routing defaults, version, public URL |
| `PUT` | `/settings` | `{routing}` |
| `GET` | `/stats` | conversation/approval/channel counters |

## WebSocket

`GET /events` upgrades to an authenticated WebSocket. The server sends `hello` immediately, then events:

```json
{
  "id": 42,
  "type": "message.delta",
  "conversationId": "cv_...",
  "at": 1760000000000,
  "data": {"messageId": "msg_...", "kind": "text", "delta": "hello"}
}
```

The socket sends protocol pings every 25s. Clients may send `{"type":"ping"}` for an application-level pong. On disconnect, reconnect with exponential backoff and call `GET /bootstrap` plus `GET /conversations/{id}` to reconcile; deltas are advisory, final `message.updated` events are authoritative.

## Idempotency and queues

`POST /messages` creates a new turn, except `delivery: "steer"` while a turn is running and the agent accepts mid-turn input: that message is injected into the running turn and the response's `turn` is the one already running. A web steer the agent cannot accept waits in the queue. A channel steer the agent cannot accept, while a turn is running, waits until that turn's tools finish, cancels it, then sends. `cancel` cancels the running turn or the first queued turn when idle. `retry` reuses the last user message rather than duplicating a bubble.

Queued inputs run in strict FIFO order; no input is reordered. `delivery: "after_tools"` only changes the running turn: if the new input is the queue head and a turn is running, that turn is cancel-and-resend, not native steering. The running turn is asked to stop once every reported tool is `completed` or `failed` (or immediately if it reported no tools), and only after the agent answers the cancelled prompt does the queue advance. Pending approvals are not skipped. The input's message metadata carries `delivery: "after_tools"` and, when armed, `interruptTurnId`. If the agent does not answer within 15 seconds, the conversation shows one notice and the queue keeps waiting. Tool states are best-effort reports from the agent, so the agent may start a new tool between a report and the cancel.
