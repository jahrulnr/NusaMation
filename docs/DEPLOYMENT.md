# Deployment

NusaMation is designed for a trusted machine or private network. The default loopback bind keeps it off the network. When exposing it, terminate TLS at a reverse proxy and configure `NUSAMATION_PUBLIC_URL` so cookies are marked `Secure` and WebSocket origin checks allow the public host.

## Build

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nusamation ./cmd/nusamation
```

The binary contains the web UI. There is no Node/npm step. Copy `nusamation` to the host and create a data directory.

## User service (recommended for a personal machine)

For a single user on a Linux machine with a systemd user manager, the Makefile installs and supervises NusaMation without root:

```bash
make install
```

It builds and installs `~/.local/bin/nusamation`, creates `~/.nusamation` with mode `0700`, writes `~/.config/systemd/user/nusamation.service`, then enables and starts the service. An environment file is created at `~/.config/nusamation/env` only if it does not already exist:

```ini
NUSAMATION_ADDR=127.0.0.1:8787
NUSAMATION_DATA_DIR=/home/you/.nusamation
NUSAMATION_LOG_LEVEL=info
```

Edit that file to add `NUSAMATION_PUBLIC_URL` or other supported settings, then run `make service-restart`. The unit keeps `~/.local/bin` on `PATH` so agent CLIs installed there are visible to the service.

`systemctl --user enable` makes the service start with the user session. `make install` also tries `loginctl enable-linger "$USER"` so it starts at boot without a login; when that is unavailable it prints the command to run manually:

```bash
sudo loginctl enable-linger "$USER"
```

Useful targets:

```bash
make service-status
make service-logs       # follow the journal, or the data log without journalctl
make service-restart
make service-stop
make service-start
```

The first-run admin password is printed to the service journal:

```bash
journalctl --user -u nusamation.service -n 60 --no-pager
```

If you already run NusaMation manually, stop it before installing; otherwise the service cannot bind port `8787`.

`make uninstall` stops and removes the unit and binary but keeps `~/.nusamation` and `~/.config/nusamation`. `make purge` also deletes both of those directories. Use `purge` deliberately.

## Windows (install.ps1)

`install.ps1` puts the binary under `%LOCALAPPDATA%\NusaMation\bin`, adds it to the user `PATH`, and registers a hidden **NusaMation** scheduled task that runs the binary at logon (`-hide-console`) under the installing user's interactive session — no stored password, no SYSTEM context, `schtasks /End /TN NusaMation` stops the real process. The task also ignores AC/battery state, so a laptop that boots unplugged still starts it and is not killed when it switches to battery. After starting it, the installer polls `http://127.0.0.1:8787/api/health` and prints diagnostics if nothing answers; the registered principal/trigger are recorded in `%LOCALAPPDATA%\NusaMation\bin\install-info.txt` for post-mortem.

Autostart has two independent paths and one guard:

- The logon trigger **repeats every five minutes**. A healthy server makes the tick a no-op (`MultipleInstances=IgnoreNew`); a server that was killed or crashed is back within five minutes instead of waiting for the next logon.
- A `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` entry starts the same binary. It exists for images where Task Scheduler never fires — the usual casualty of stripped or custom Windows builds.
- The app holds a **per-data-directory instance lock** (named mutex on Windows, `flock` on `nusamation.lock` elsewhere) *before* it opens the database. Both autostart paths racing at logon is therefore harmless: the loser prints "already running" and exits 0, and two processes can never share one SQLite/WAL pair.

Startup bind failures are retried in-process for a few minutes before the app exits, so a port that is still being released — or sits inside a reserved range right after boot — usually heals itself; if it does not, the reason lands in `logs\nusamation.log` instead of vanishing into the hidden task's stderr.

### `localhost:8787` unreachable after a Windows reboot

```powershell
Get-Process nusamation -ErrorAction SilentlyContinue     # is it running at all?
Get-ScheduledTaskInfo NusaMation                         # LastTaskResult
(Get-ScheduledTask NusaMation).Principal.UserId; (Get-ScheduledTask NusaMation).Triggers[0].UserId; whoami
netstat -ano | findstr :8787                             # anything listening?
netsh int ipv4 show excludedportrange protocol=tcp       # 8787 inside a reserved range?
Get-Content ~\.nusamation\logs\nusamation.log -Tail 30
```

- **Not running / trigger user mismatch** — the task was registered for a different account (install ran under another context). Re-run `install.ps1` as the daily user, or check `install-info.txt`.
- **Task says `Running` but nothing listens** — Task Scheduler can hold a stale instance after an unclean shutdown; `schtasks /Run` then queues or refuses it (`LastTaskResult` `0x800710E0`) and `/End` + `/Run` cannot clear it. Re-run `install.ps1`: it stops the instance, waits for the executable to be replaceable and registers the task again, which resets the stuck state.
- **Process disappears and comes back a few minutes later** — that is the five-minute repetition trigger, not a crash loop. If it never comes back, check `Get-ScheduledTaskInfo NusaMation` and whether the `HKCU\...\Run` value `NusaMation` still exists; either path alone is enough to start it.
- **Process dies instantly, log says `listen on ... access` / reserved range** — Hyper-V/WSL/WinNAT reserved the port this boot. Reserve it permanently (admin): `net stop winnat`, then `netsh int ipv4 add excludedportrange protocol=tcp startport=8787 numberofports=1`, then `net start winnat` — the exclusion removes 8787 from the dynamic pool while explicit binds remain allowed, so winnat stops rolling over it on every boot.
- **Something else holds :8787** — `netstat -ano` gives the PID; taskkill it or move NusaMation with `NUSAMATION_ADDR`.

## system-wide systemd

Use this when NusaMation should run as a dedicated service account shared by multiple users or managed by root:

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

Stop the service, replace the binary, start it. The database schema is migrated idempotently on startup (versioned files under `migrations/`). Keep a backup before upgrading across versions that change the schema. Agent and WhatsApp processes are supervised per turn and stop with the app.

If the admin password is lost:

```bash
./nusamation reset-password --data-dir /home/nusamation/.nusamation
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
