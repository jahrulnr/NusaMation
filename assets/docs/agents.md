# Operating ACP agents

An *agent* is a registered ACP-over-stdio CLI: command, args, working
directory, environment variables, approval policy, optional default model, MCP
servers, and auth state. Rows live in `nusamation.db`; manage them with:

```bash
nusamation agent list                           # all registered agents
nusamation agent detect                         # popular ACP CLIs found on PATH
nusamation agent detect -add                    # …and register each one
nusamation agent get <slug|id> [-json]          # details (env masked by default)
nusamation agent add -name X -command codex -arg acp \
    [-slug x] [-cwd /work] [-policy auto_safe] [-env KEY=val] \
    [-model 'reasoning|medium'] [-enabled=false] [-auto-start]
nusamation agent set <slug|id> [-command ..] [-arg ..] [-env K=V] \
    [-env-rm K] [-policy ask|auto_safe|auto_all] [-model ..] \
    [-enabled=true] [-enabled=false] [-auto-start=false]
nusamation agent remove <slug|id>
```

Flags may appear before or after positional args. Every command accepts
`-data-dir` (default `~/.nusamation`). `list`, `get`, `add`, `set` and `detect`
support `-json` for machine-readable output.

## Default ACP and session options

Open **Control center → Defaults**, select an enabled agent, then choose any
published session options such as model, mode or thought level. Choices are
applied in order to a temporary ACP session; after each choice the UI refreshes
the available options, which may depend on earlier choices. Saving replays and
validates the sequence against the agent again. Unselected options use the
agent's own defaults. Use **Undo last choice** to change an earlier selection.

The default agent is used for new web conversations and new, unlinked WhatsApp
chats. A new conversation created with a different explicit agent uses that
agent instead. When the selected agent matches the configured default, the
chosen options are copied into the new conversation; changing application
defaults later does not rewrite existing conversations. Conversation-level
choices can override them. Switching an existing conversation to the default
agent also applies the configured defaults to the new ACP session.

The legacy `agent add/set -model` flag remains available for an individual
agent. Prefer **Defaults** for capability-checked, ordered model and other
session options. The HTTP preview and save endpoints are listed in [api.md](api.md).

## Semantics

- `-env KEY=val` merges (repeatable); `-env-rm KEY` deletes. Secrets stay
  encrypted via `secret.key` — they are masked in output unless `-show-secrets`.
- `-arg` replaces the whole arg list; repeat it for multiple args.
- `-policy`: `ask` = human approves every permission request (safe default);
  `auto_safe` = auto-approve safe requests; `auto_all` = auto-approve
  everything — deliberately powerful.
- `-enabled=false` hides the agent from pickers without deleting it.
- `-auto-start` is stored on the record but currently inert — no consumer
  spawns it yet; don't rely on it.
- On first run, `agent detect -add` semantics apply: every known ACP CLI found
  on PATH is seeded automatically — but only while no enabled agent exists;
  once one does, seeding stops.

## Runtime checks (live server)

```
GET  /api/agents                        # {"agents":[…]} — slug, status, capabilities,
                                        #   defaultModel, authMethods, lastError
POST /api/agents/{id}/probe             # spawn test: validates command/cwd/env + auth
POST /api/agents/{id}/authenticate      # start an interactive auth flow
GET  /api/agents/{id}/auth-terminal     # terminal stream for interactive auth
POST /api/agents/{id}/logout            # clear agent auth state
GET  /api/agents/{id}/logs              # recent stderr/stdout of spawned sessions
```

See [api.md](api.md) for how to authenticate HTTP calls.

## If an agent won't start

1. `POST /api/agents/{id}/probe` — the error usually names the cause (missing
   binary, bad cwd, auth required).
2. Check `GET /api/agents/{id}/logs` and `logs/nusamation.log`.
3. Re-check detection: `nusamation agent detect` lists CLIs currently on PATH.
