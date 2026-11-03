# Agents and ACP

## The model

NusaMation is the **client**; your agent CLI is the **agent**. The client starts the command, speaks ACP over stdio and owns the conversation state around it.

```text
NusaMation ── initialize ──► agent
           ◄─ agentInfo, capabilities, authMethods
           ── session/new {cwd, mcpServers} ──►
           ◄─ sessionId, modes, configOptions
           ── session/prompt {prompt blocks} ──►
           ◄─ session/update (thoughts, text, tools, plan, usage)
           ◄─ session/request_permission
           ── permission response ──►
           ◄─ prompt result {stopReason}
```

The adapter must implement ACP v1 and use newline-delimited JSON-RPC on stdio. NusaMation advertises filesystem, terminal and permission handling capabilities.

## Add an agent

**Control center → Agents → Add agent**

| Field | Notes |
|---|---|
| Name / slug | Display name and stable identifier |
| Command | Absolute path is most reliable (`which devin`) |
| Arguments | One per line; e.g. `acp` |
| Working directory | Absolute path; must exist and be a directory |
| Approval policy | `ask` (default), `auto_safe`, `auto_all` |
| Default model | Optional; applied when the session exposes a model config |
| Environment | Encrypted secrets passed to the process |
| Enabled | Disabled agents cannot be selected |

Press **Probe** to start a temporary ACP session and capture capabilities, models, config options and auth methods. Probing does not disturb a live conversation.

In a conversation, **Connect agent** prepares the ACP session before the first message. The composer shows the mode separately and groups the model, reasoning, and other advertised settings under the model/options menu. The choices come from the current session; a long list can be searched. On narrow screens the composer keeps the selected model summary and moves all advertised options, including mode, into the **+** menu. The textarea grows within the available viewport.

Permission requests and ACP form/URL input requests appear in a dialog. Login methods advertised by the agent can be started there; terminal-based methods run in a terminal in the dialog, and the agent reconnects after a successful login. A request that the agent never sends over ACP cannot be answered by the client. If a connection or prompt takes a long time, the dialog offers details, wait and stop actions.

### Devin

```text
Command: devin
Args:    acp
Cwd:     /path/to/workspace
```

Devin is auto-seeded when `devin` is on `PATH`. Log in first with `devin login` (or use the **Authentication** buttons in the agent logs dialog). Devin publishes model, mode, thought level and speed config options; the web model picker uses the live list.

If Devin is slow to start, run **Probe** once to pre-populate the UI, then send the first prompt. Devin's global MCP config may produce noisy stderr; NusaMation keeps it in a bounded ring buffer and never floods the chat.

### Claude Code / Codex / Gemini

Use the ACP adapter/version appropriate to your installation. The command must:

- speak ACP over stdio;
- accept `initialize` and `session/new`;
- stream `session/update` notifications;
- return a `session/prompt` result with a stop reason.

Exact adapter package names change; probe after adding. A command that only implements a chat REPL is not ACP.

### Fake agent

`cmd/fake-acp-agent` is a deterministic ACP agent for tests and demos. Build it:

```bash
go build -o fake-acp ./cmd/fake-acp-agent
```

Add with command `/absolute/path/to/fake-acp`, any existing workspace, policy `ask`. It streams thought → plan → tool call → permission request → tool result → final text. This is the easiest way to verify the web approval flow and WhatsApp digest without a model account.

## Approval policies

| Policy | Behaviour |
|---|---|
| `ask` | Every `session/request_permission` waits for web/channel approval. |
| `auto_safe` | `read`, `search`, `fetch` and `think` auto-approve; everything else asks. |
| `auto_all` | Every option is auto-approved. Use only in a trusted workspace. |

`allow_always` choices create a conversation-scoped rule for the tool name + kind. Manage/delete rules through `/api/approval-rules`; add agent-scoped rules via the API for a broader trust boundary.

Important: an agent's own permission mode is separate. If an agent is configured to bypass permission requests (for example a “bypass” mode), NusaMation never sees a request to approve. Set the agent's mode to a safe mode if you need app-level approvals.

## Sessions and handoff

- NusaMation prefers `session/load` when advertised, then `session/resume`, then a new session.
- When an existing ACP session is missing, a new session is created and the next prompt includes a bounded transcript recap. The web shows a “Session recovered” notice.
- Switching agents keeps the conversation history and creates a handoff recap for the new agent.
- `/new` on a channel starts a fresh conversation and moves the chat link.
- On server restart, interrupted turns are marked and queued turns resume. A failed side-effecting turn is **not** replayed automatically; use Retry.

## MCP servers

**Control center → MCP servers**, attached per agent:

- **stdio** — command + args + env.
- **http / sse** — URL + headers.

NusaMation filters MCP servers by the agent's advertised ACP MCP capabilities and passes them to `session/new`. Secrets are encrypted at rest and never returned by the API. Editing an agent/MCP server invalidates its runtime so the next turn restarts with the new configuration.

The agent process inherits the NusaMation process environment, with per-agent environment values added on top. Start NusaMation with the environment needed by the configured agents.

## Workspace

The first run seeds `data/workspace/README.md` and `AGENTS.md`. This is the shared working directory for all agents unless a conversation has a different cwd. Client-side ACP filesystem requests check the session roots and sensitive paths. For terminal requests, NusaMation checks the working directory but does not sandbox the command; the agent's own tools and sandbox have their own access rules.

The agent's own sandbox controls its direct filesystem and command access; the ACP client-side filesystem checks do not confine commands run in a terminal.

## Troubleshooting

| Symptom | Check |
|---|---|
| Probe fails | Command path, `--version`, ACP mode flag, agent login |
| `agent authentication required` | Use the auth methods in the agent logs dialog |
| Model list empty | Probe or start a session; the agent publishes config options after `session/new` |
| Prompt hangs | Agent may be waiting for approval; check the Approvals inbox |
| Process exits | Agent logs dialog shows stderr and exit state |
| Old session lost | NusaMation falls back to a new session + transcript recap |
| MCP tools missing | Agent capability `mcpCapabilities.http/sse`, server enabled, restart runtime |

Use `NUSAMATION_LOG_LEVEL=debug` for more detail. Agent stderr is deliberately kept out of chat to avoid flooding the UI.
