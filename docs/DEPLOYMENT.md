# Deployment

NusaMation is designed for a trusted machine or private network. The default loopback bind keeps it off the network. When exposing it, terminate TLS at a reverse proxy and configure `NUSAMATION_PUBLIC_URL` so cookies are marked `Secure` and WebSocket origin checks allow the public host.

## Build

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nusamation ./cmd/nusamation
```

The binary contains the web UI. There is no Node/npm step. Copy `nusamation` to the host and create a data directory.

## systemd

```ini
# /etc/systemd/system/nusamation.service
[Unit]
Description=NusaMation personal agent mesh
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nusamation
Group=nusamation
WorkingDirectory=/home/nusamation
EnvironmentFile=/etc/nusamation/env
ExecStart=/usr/local/bin/nusamation
Restart=on-failure
RestartSec=3
# Agent CLI needs a writable HOME/workspace and the data dir.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/home/nusamation/.nusamation
ProtectHome=read-only

[Install]
WantedBy=multi-user.target
```

`/etc/nusamation/env`:

```ini
NUSAMATION_ADDR=127.0.0.1:8787
NUSAMATION_DATA_DIR=/home/nusamation/.nusamation
NUSAMATION_PUBLIC_URL=https://agent.example.com
NUSAMATION_LOG_LEVEL=info
```

The agent command (Devin, etc.) must be installed for the service user and authenticated. Run the agent's login once as that user so its config/credentials live in its home.

## Caddy

```caddy
agent.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

Caddy handles WebSocket upgrades automatically.

## Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name agent.example.com;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }
}
```

Set `NUSAMATION_PUBLIC_URL=https://agent.example.com`. The app checks `Origin` against the request host or the configured public URL. If a proxy rewrites `Host`, use the public URL setting.

## Data and backups

Back up the whole data directory while the app is stopped (or use SQLite online backup):

```text
nusamation.db, whatsapp.db, secret.key, uploads/, workspace/
```

Without `secret.key`, encrypted agent/MCP secrets cannot be decrypted. Without `whatsapp.db`, the WhatsApp device must be linked again.

## Security checklist

- Set a strong `NUSAMATION_PASSWORD` before first run.
- Keep the bind address on loopback or a private interface.
- Use HTTPS; the session cookie becomes `Secure`.
- Set a WhatsApp allowlist before sharing the number.
- Keep approval policy `ask` or `auto_safe`; `auto_all` means the agent can execute tools without a human.
- Restrict the OS user's access to the data directory and workspace.
- Back up `secret.key` securely; it is the root of agent/MCP secret decryption.
- Do not expose the app to the public internet without a proxy, TLS and network controls. It is a single-admin application, not a multi-tenant service.

## Updating

Stop the service, replace the binary, start it. The database schema is migrated idempotently (`schemaV1`). Keep a backup before upgrading across versions that change the schema. Agent and WhatsApp processes are supervised per turn and stop with the app.

If the admin password is lost:

```bash
./nusamation reset-password -data-dir /home/nusamation/.nusamation
```

## Troubleshooting

| Issue | Action |
|---|---|
| Login loop / cookie rejected | Ensure `NUSAMATION_PUBLIC_URL` matches the public HTTPS origin |
| WebSocket fails behind proxy | Forward `Upgrade`/`Connection` headers; use HTTP/1.1 |
| WhatsApp QR not appearing | Press Connect; check channel detail and logs; only one process may use a device session |
| Agent cannot start | Check command path, executable bit, service user's HOME/PATH and agent login |
| Permission denied on workspace | Ensure the service user owns `data/workspace` |
| `database is locked` | One NusaMation process per data directory; WAL/busy timeout are configured |
