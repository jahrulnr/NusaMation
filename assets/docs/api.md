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
passes). Errors are flat JSON `{"error":"…","code":"…"}` with a 4xx/5xx
status.

## Endpoint map

```
GET  /api/bootstrap                      # config, version, whatsapp settings
GET|POST /api/conversations
GET|PATCH|DELETE /api/conversations/{id}
GET|POST /api/conversations/{id}/messages
POST /api/conversations/{id}/cancel|retry|config|agent|routing|prepare
POST /api/conversations/{id}/approvals/{approvalId}
GET  /api/conversations/{id}/export
GET  /api/approvals   POST /api/approvals/{id}    # answer permission requests
GET|POST /api/agents  GET|PATCH|DELETE /api/agents/{id}
POST /api/agents/{id}/probe|authenticate|logout
GET  /api/agents/{id}/logs|auth-terminal
GET|POST /api/mcp     PATCH|DELETE /api/mcp/{id}
GET  /api/channels
POST /api/channels/{name}/connect|disconnect|logout
GET  /api/channels/{name}/qr|settings
PUT  /api/channels/{name}/settings
GET|PUT /api/settings   GET|DELETE /api/approval-rules/{id}
POST /api/uploads     GET /api/uploads/{id}
GET  /api/events                          # WebSocket realtime stream
GET  /api/health                          # unauthenticated
```

Payload shapes are documented in the repo's `docs/API.md`; the list above is
for orientation — call `GET /api/bootstrap` first for the server's own view.
