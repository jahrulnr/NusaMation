# HTTP API for scripts

Prefer the CLI — it needs no session and works offline. Use HTTP when the
server is remote or you need live-only endpoints (probe, QR, reconnect,
realtime events). Default bind: `127.0.0.1:8787`.

## Auth

```bash
curl -c jar -H 'Content-Type: application/json' \
     -d '{"password":"…","remember":false}' http://127.0.0.1:8787/api/auth/login
# → {"ok":true,"csrf":"…","expiresAt":"…"} and a nm_session cookie in jar
```

Every mutating request then needs the cookie plus header
`X-CSRF-Token: <csrf>`; Origin must match the Host (a missing Origin also
passes). `POST /api/browser-use` and `POST /api/conversation-control` are the
exceptions: the embedded MCP children call them with
`Authorization: Bearer <token>` and no cookie. Errors are flat JSON
`{"error":"…","code":"…"}` with a 4xx/5xx status.

`POST /api/browser-use` resolves `[[env:NAME]]` references in `type`/`fill`
text against the conversation agent's saved env (fail-closed preflight:
`unknown_secret`, `invalid_secret_reference`, `invalid_secret_target`;
`\[[` escapes the opener). Resolved raw values are masked back to their tag in
that endpoint's JSON responses only — exact match, no encoded/transformed or
screenshot masking.

## Endpoint map

```
GET  /api/bootstrap                      # config, version, whatsapp settings
GET|POST /api/automations
GET|PUT|DELETE /api/automations/{id}      # GET includes run history
PATCH /api/automations/{id}/enabled
POST /api/settings/agent-defaults/preview
PUT  /api/settings/agent-defaults
GET|POST /api/conversations
GET|PATCH|DELETE /api/conversations/{id}
GET|POST /api/projects
PATCH|DELETE /api/projects/{id}
POST /api/projects/{id}/restore
GET  /api/fs/list?path=                 # child directories; empty path is the home directory
GET|POST /api/conversations/{id}/messages
POST /api/conversations/{id}/cancel|retry|config|agent|routing|prepare
POST /api/conversation-control             # loopback MCP bridge, bearer token
GET  /api/conversation-control/activity    # in-memory control audit trail
POST /api/conversations/{id}/turns/{turnId}/cancel   # queued turn only; {"dropMessage":true} also removes the queued message
POST /api/conversations/{id}/approvals/{approvalId}
GET  /api/conversations/{id}/export
GET  /api/approvals   POST /api/approvals/{id}    # answer permission requests
GET|POST /api/agents  GET|PATCH|DELETE /api/agents/{id}
POST /api/agents/{id}/probe|authenticate|logout
GET  /api/agents/{id}/logs|auth-terminal
GET|POST /api/mcp     PATCH|DELETE /api/mcp/{id}
GET  /api/channels
POST /api/channels/whatsapp/send          # native poll, reply, reaction, sticker, location, contact, event
POST /api/channels/{name}/connect|disconnect|logout
GET  /api/channels/{name}/qr|settings
PUT  /api/channels/{name}/settings
GET|PUT /api/settings   GET|DELETE /api/approval-rules/{id}
GET  /api/update      POST /api/update/check|apply   # self-update status + actions
POST /api/notifications/test              # desktop notification smoke test
POST /api/uploads     GET /api/uploads/{id}  # ?download=1 forces download
GET  /api/conversations/{id}/files?path=... # local artifact bytes; HEAD/ranges supported
GET  /api/events                          # WebSocket realtime stream
GET  /api/health                          # unauthenticated
```

Conversation detail includes `historyHasMore` for the latest message window. Message pagination returns `hasMore` when another message exists in the requested direction.

## Automation payloads

`GET /api/automations` returns `automations` and the server's `timezone`.
Create a job with `POST /api/automations`:

```json
{
  "name": "Daily review",
  "prompt": "Review the workspace",
  "agentId": "<agent-id>",
  "schedule": "cron",
  "cron": "0 9 * * *",
  "timezone": "Asia/Jakarta",
  "sessionMode": "same",
  "expiresAt": 0
}
```

Use an enabled agent ID from `/api/agents`; an agent slug is accepted by the
CLI but is not the API's `agentId`. `schedule` is `cron` or `once`; for `once`,
provide `runAt` as Unix milliseconds instead of `cron`. `expiresAt` is Unix
milliseconds or `0` for no expiry. `sessionMode` is `same` or `isolated`.
Creation enables the job. The response contains `automation`.

`GET /api/automations/{id}` returns `automation` and `runs`. For `PUT`, send
the complete editable job, including `enabled`; include the `updatedAt` from
the latest GET to detect concurrent edits. A stale update is rejected: reload
before retrying. `PATCH /api/automations/{id}/enabled` takes
`{"enabled":false}` or `{"enabled":true}`. `DELETE` removes the job and its
run history, leaving chat transcripts intact. See [automations.md](automations.md)
for scheduling and recovery behavior.

## Projects

`POST /api/conversations` accepts optional `projectId`. When it is set, the
chat's cwd and `additionalDirectories` come from that project; an unknown id
is 404 `not_found`. When it is omitted, cwd still falls back to the agent cwd,
then the service workspace. Switching agents leaves a member's cwd and
`additionalDirectories` in place. A chat joins a project only at create time
or when undo restores it while `projectId` is still empty.

`POST /api/projects` takes `{name?, sources, foldersConfirmed?}`. `sources` is
the full folder list and `sources[0]` is the only primary. An empty name is
stored as that folder's basename. New paths (every path on create) need
`foldersConfirmed:true`; otherwise the response is 409 `confirmation_required`
with `added` and nothing is written. `PATCH /api/projects/{id}` requires `updatedAt` to save `name`
and/or `sources`; a missing `updatedAt` is 400 `invalid`, a stale revision is
409 `conflict`, and either way nothing changes. Pin alone does not change
folders; an omitted pin stays as stored. `DELETE /api/projects/{id}` returns
`{project, conversationIds}` and deletes no directory and no chat.
`POST /api/projects/{id}/restore` recreates that id from
`{name, sources, conversationIds}` plus optional `pinned`, and reattaches a
chat only when it still has no project. The response is
`{project, conversationIds}` with the chats that actually reattached.

`GET /api/bootstrap` includes `projects`. Mutations emit `project.updated`.

## Default ACP payloads

Both `POST /api/settings/agent-defaults/preview` and
`PUT /api/settings/agent-defaults` take an agent ID and an ordered selection
array, for example `{"agentId":"<agent-id>","selections":[]}`. Preview
returns `defaults` and the current `configs`. With idle mode on, options
replay on the agent's warm session; otherwise they merge into the agent's
cached catalog (a live probe refreshes it when missing or disputed). Choose
IDs and values from that response, append one selection, and preview again
before selecting a dependent option. A selection has the shape
`{"id":"<config-id>","value":"<advertised-value>"}`; boolean options use a
JSON boolean value. PUT validates the full sequence against a live agent
session before saving — it never answers from the cached catalog. Invalid
payloads return 400; ACP startup or validation failures return 502.

The repo's `docs/API.md` contains additional endpoint details; call
`GET /api/bootstrap` for the server's own view.

Local artifact paths are resolved on the server: relative to the conversation cwd,
or absolute (including `/tmp`). `file://` paths and editor `:line:column` suffixes
are supported. Missing files return 404; non-regular files return 422. Both local
files and uploads accept `download=1`.
