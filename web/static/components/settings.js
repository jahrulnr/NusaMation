import { $, $$, AGENT_COLORS, agentById, agentOrb, configChoices, esc, icon, timeAgo } from "./util.js";
import { S } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { confirmModal, modalHead, openModal } from "./modal.js";
import { closePopover } from "./popover.js";
import { renderChat } from "./chat.js";
import { renderComposer } from "./composer.js";
import { renderTopbar } from "./topbar.js";
import { automationsSettings } from "./automations.js";
import { startQRTimer, whatsappPanel } from "./whatsapp.js";

/* ---------------- settings ---------------- */
export function openSettings(tab = "") {
  closePopover();
  if (tab) S.settingsTab = tab;
  S.agentForm = null;
  S.mcpForm = null;
  S.modal = { type: "settings" };
  renderSettings();
}

export function renderSettings() {
  const tabs = [
    ["agents", "sparkles", "Agents"],
    ["defaults", "star", "Defaults"],
    ["automations", "clock", "Automations"],
    ["mcp", "zap", "MCP servers"],
    ["channels", "message", "Channels"],
    ["routing", "link", "Routing"],
    ["account", "shield", "Account"],
  ];
  let body = "";
  if (S.settingsTab === "agents") body = agentsSettings();
  else if (S.settingsTab === "defaults") body = agentDefaultsSettings();
  else if (S.settingsTab === "automations") body = automationsSettings();
  else if (S.settingsTab === "mcp") body = mcpSettings();
  else if (S.settingsTab === "channels") body = channelsSettings();
  else if (S.settingsTab === "routing") body = routingSettings();
  else body = accountSettings();
  const content = `<div class="settings-layout">
    <nav class="settings-tabs">${tabs.map(([id, ic, label]) => `<button class="settings-tab ${S.settingsTab === id ? "active" : ""}" data-action="settings-tab" data-tab="${id}">${icon(ic)}<span>${label}</span></button>`).join("")}</nav>
    <div class="settings-content" id="settings-content">${body}</div>
  </div>`;
  const layout = $("#modal-root .settings-modal .settings-layout");
  if (layout) {
    $$(".settings-tab", layout).forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === S.settingsTab));
    $("#settings-content", layout).innerHTML = body;
  } else openModal(`${modalHead("Control center", "Agents, channels, routing and account")}${content}`, { size: "settings-modal" });
  if (S.settingsTab === "channels") startQRTimer();
}

export function settingsHead(title, desc, action = "") {
  return `<div class="section-head"><div><h3>${title}</h3><div class="settings-desc" style="margin:3px 0 0">${desc}</div></div>${action}</div>`;
}

export function agentDefaultsSettings() {
  const draft = S.defaultDraft || { agentId: "", selections: [] };
  const selected = new Set(draft.selections.map((s) => s.id));
  const opts = S.defaultConfigs.filter((o) => o && o.id && !selected.has(o.id) && ["select", "boolean"].includes(o.type));
  const rows = opts.map((o) => {
    const choices = o.type === "boolean" ? [{ value: "true", name: "On" }, { value: "false", name: "Off" }] : configChoices(o);
    return `<div class="field"><label>${esc(o.name || o.id)}</label><select class="select" data-change="default-choice" data-id="${esc(o.id)}" data-type="${esc(o.type)}" ${S.defaultBusy ? "disabled" : ""}><option value="">Use agent default</option>${choices.map((c) => `<option value="${esc(c.value)}">${esc(c.groupName ? c.groupName + " · " + (c.name || c.value) : (c.name || c.value))}</option>`).join("")}</select></div>`;
  }).join("");
  return `${settingsHead("Default ACP", "Used for new chats, including new WhatsApp conversations. Choose options in order; each choice refreshes the available options.")}
    <div class="field"><label>Agent</label><select class="select" data-change="default-agent" ${S.defaultBusy ? "disabled" : ""}><option value="">Choose an agent</option>${S.agents.filter((a) => a.enabled).map((a) => `<option value="${esc(a.id)}" ${draft.agentId === a.id ? "selected" : ""}>${esc(a.name)}</option>`).join("")}</select></div>
    ${S.defaultBusy ? `<div class="settings-loading" role="status"><span class="spinner"></span><span>Loading agent options…</span></div>` : ""}
    ${S.defaultError ? `<div class="settings-desc" role="alert">${esc(S.defaultError)} <button class="btn btn-secondary btn-sm" data-action="default-retry">Retry</button></div>` : ""}
    ${draft.selections.length ? `<div class="settings-section"><div class="settings-title">Selected in order</div>${draft.selections.map((s) => { const opt = S.defaultConfigs.find((o) => o.id === s.id); const label = opt && configChoices(opt).find((c) => String(c.value) === String(s.value))?.name; return `<div class="settings-row"><div class="label">${esc(s.name || opt?.name || s.id)}</div><span class="default-value">${esc(s.label ?? label ?? s.value)}</span></div>`; }).join("")}<button class="btn btn-secondary btn-sm" data-action="default-undo" ${S.defaultBusy ? "disabled" : ""}>Undo last choice</button></div>` : ""}
    ${draft.agentId && !S.defaultError ? `<div class="form-grid" style="margin-top:16px">${rows || (!S.defaultBusy ? `<div class="settings-desc">No more configurable options.</div>` : "")}</div>` : ""}
    <div class="form-actions"><button class="btn btn-primary" data-action="save-agent-defaults" ${!draft.agentId || S.defaultBusy || S.defaultError ? "disabled" : ""}>Save defaults</button></div>`;
}

export async function previewAgentDefaults() {
  const draft = S.defaultDraft;
  const seq = ++S.defaultSeq;
  S.defaultError = "";
  S.defaultConfigs = [];
  if (!draft.agentId) { renderSettings(); return; }
  S.defaultBusy = true; renderSettings();
  try {
    const d = await api("/settings/agent-defaults/preview", { method: "POST", body: { agentId: draft.agentId, selections: draft.selections.map(({ id, value }) => ({ id, value })) } });
    if (seq !== S.defaultSeq) return;
    S.defaultConfigs = d.configs || [];
  } catch (e) { if (seq === S.defaultSeq) S.defaultError = e.message; }
  finally { if (seq === S.defaultSeq) { S.defaultBusy = false; if (S.settingsTab === "defaults") renderSettings(); } }
}

export async function saveAgentDefaults() {
  if (S.defaultBusy || !S.defaultDraft?.agentId) return;
  S.defaultBusy = true; renderSettings();
  try {
    const d = await api("/settings/agent-defaults", { method: "PUT", body: { agentId: S.defaultDraft.agentId, selections: S.defaultDraft.selections.map(({ id, value }) => ({ id, value })) } });
    S.agentDefaults = d.defaults;
    S.welcomeAgent = d.defaults.agentId;
    if (!S.current) { S.currentAgent = agentById(d.defaults.agentId); renderTopbar(); renderChat(); renderComposer(); }
    toast("Defaults saved", "New conversations will use these settings.", "success");
  } catch (e) { S.defaultError = e.message; }
  finally { S.defaultBusy = false; if (S.settingsTab === "defaults") renderSettings(); }
}

export function agentsSettings() {
  if (S.agentForm) return agentFormHTML();
  return `${settingsHead("Agents", "ACP CLIs are the brain. Launch commands, workspaces, policies and secrets.", `<button class="btn btn-primary btn-sm" data-action="add-agent">${icon("plus", "sm")} Add agent</button>`)}
    <div class="agent-list">${S.agents.map((a) => `<div class="agent-card">
      ${agentOrb(a)}
      <div class="agent-card-main">
        <div class="agent-card-name">${esc(a.name)} <span class="agent-status"><span class="status-dot ${a.status === "ready" ? "" : a.status === "error" ? "offline" : "warn"}"></span>${esc(a.status || "unknown")}</span>${a.autoStart ? ` <span class="badge-soft gray">idle</span>` : ""}</div>
        <div class="agent-card-sub">${esc(a.command)} ${esc((a.args || []).join(" "))} · ${esc(a.cwd)}</div>
        <div class="agent-card-sub">${esc(a.description || "")}${a.defaultModel ? " · default " + esc(a.defaultModel) : ""}</div>
      </div>
      <div class="agent-card-actions">
        <button class="icon-btn sm" data-action="probe-agent" data-id="${esc(a.id)}" title="Probe ACP">${icon("refresh", "sm")}</button>
        <button class="icon-btn sm" data-action="agent-logs" data-id="${esc(a.id)}" title="Runtime logs">${icon("terminal", "sm")}</button>
        <button class="icon-btn sm" data-action="edit-agent" data-id="${esc(a.id)}" title="Edit">${icon("edit", "sm")}</button>
        <button class="icon-btn sm" data-action="delete-agent" data-id="${esc(a.id)}" title="Delete">${icon("trash", "sm")}</button>
      </div>
    </div>`).join("") || `<div class="empty-settings">No agents configured yet.<br>Add any ACP-compatible CLI to get started.</div>`}</div>
    <div class="settings-section" style="margin-top:22px">${settingsHead("Approval policy", "Local policy is separate from the agent's own permission mode. Ask is the safest default.")}<div class="settings-row"><div><div class="label">Default workspace</div><div class="desc">${esc((S.bootstrap?.app && S.bootstrap.app.dataDir) || "the NusaMation data directory")}/workspace</div></div><code class="mono" style="font-size:10.5px;color:var(--muted)">AGENTS.md seeded</code></div></div>`;
}

export function envRowsHTML(list, prefix) {
  return (list || []).map((e, i) => `<div class="secret-row" data-env-row="${prefix}">
    <input class="input" name="env-name" value="${esc(e.name || "")}" placeholder="NAME">
    <input class="input" name="env-value" type="password" value="" placeholder="${e.hasValue ? "•••••••• (saved)" : "value"}" ${e.hasValue ? 'data-has-value="1"' : ""}>
    <button class="icon-btn sm" data-action="remove-env" data-prefix="${prefix}" title="Remove">${icon("x", "sm")}</button>
  </div>`).join("");
}

export function agentFormHTML() {
  const f = S.agentForm;
  const a = f.agent || {};
  return `<div class="section-head"><div><h3>${f.id ? "Edit agent" : "Add ACP agent"}</h3><div class="settings-desc" style="margin:3px 0 0">A command that speaks the Agent Client Protocol over stdio.</div></div><button class="btn btn-ghost btn-sm" data-action="cancel-agent">Cancel</button></div>
    <form data-form="agent" data-id="${esc(f.id || "")}">
      <div class="form-grid">
        <div class="field"><label>Name</label><input class="input" name="name" value="${esc(a.name || "")}" placeholder="Devin" required></div>
        <div class="field"><label>Slug</label><input class="input" name="slug" value="${esc(a.slug || "")}" placeholder="devin" required></div>
        <div class="field full"><label>Command</label><input class="input" name="command" value="${esc(a.command || "")}" placeholder="/usr/local/bin/devin" required></div>
        <div class="field full"><label>Arguments (one per line)</label><textarea class="textarea" name="args" rows="3" placeholder="acp">${esc((a.args || []).join("\n"))}</textarea></div>
        <div class="field full"><label>Working directory</label><input class="input" name="cwd" value="${esc(a.cwd || ((S.bootstrap?.app && S.bootstrap.app.dataDir) || "") + "/workspace")}" placeholder="/home/me/.nusamation/workspace" required></div>
        <div class="field full"><label>Description</label><input class="input" name="description" value="${esc(a.description || "")}" placeholder="What this agent is good at"></div>
        <div class="field"><label>Icon</label><select class="select" name="icon">${["sparkles", "zap", "brain", "terminal", "globe", "star"].map((i) => `<option value="${i}" ${a.icon === i ? "selected" : ""}>${i}</option>`).join("")}</select></div>
        <div class="field"><label>Color</label><select class="select" name="color">${Object.keys(AGENT_COLORS).map((c) => `<option value="${c}" ${a.color === c ? "selected" : ""}>${c}</option>`).join("")}</select></div>
        <div class="field"><label>Approval policy</label><select class="select" name="approvalPolicy">${["ask", "auto_safe", "auto_all"].map((p) => `<option value="${p}" ${a.approvalPolicy === p ? "selected" : ""}>${p.replace("_", " ")}</option>`).join("")}</select></div>
        <div class="field"><label class="checkbox"><input type="checkbox" name="enabled" ${a.enabled !== false ? "checked" : ""}> Enabled</label></div>
        <div class="field"><label class="checkbox"><input type="checkbox" name="autoStart" ${a.autoStart ? "checked" : ""}> Idle mode — keep the agent process running</label></div>
        <div class="field full"><small class="muted">Idle mode starts the agent with NusaMation and keeps a session warm, so model and option changes don't wait for a cold launch.</small></div>
      </div>
      <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Environment secrets <span class="badge-soft gray">AES-GCM at rest</span><span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="agent">+ Add variable</button></div>
        <div id="agent-env-list">${envRowsHTML(a.env || [], "agent")}</div>
      </div>
      <div class="form-actions"><button class="btn btn-secondary" type="button" data-action="cancel-agent">Cancel</button><button class="btn btn-primary" type="submit">${f.id ? "Save agent" : "Add agent"}</button></div>
    </form>`;
}

export function mcpSettings() {
  if (S.mcpForm) return mcpFormHTML();
  const list = S.mcp || [];
  const cards = list.map((m) => `<div class="mcp-card"><span class="tool-icon kind-execute">${icon(m.transport === "stdio" ? "terminal" : "globe")}</span><div class="grow" style="min-width:0"><div class="mcp-name">${esc(m.name)} <span class="status-dot ${m.enabled ? "" : "warn"}"></span></div><div class="mcp-sub">${esc(m.transport)} · ${esc(m.transport === "stdio" ? m.command : m.url)} ${esc((m.args || []).join(" "))}</div></div><div class="mcp-actions"><button class="icon-btn sm" data-action="edit-mcp" data-id="${esc(m.id)}" title="Edit">${icon("edit", "sm")}</button><button class="icon-btn sm" data-action="delete-mcp" data-id="${esc(m.id)}" title="Delete">${icon("trash", "sm")}</button></div></div>`).join("");
  return `${settingsHead("MCP servers", "Shared by all agents. Saving changes restarts active agent runtimes.")}<div class="settings-section"><div class="section-head"><div><h3>Shared servers</h3><div class="settings-desc">${list.length} server${list.length === 1 ? "" : "s"}</div></div><button class="btn btn-secondary btn-sm" data-action="add-mcp">${icon("plus", "sm")} Add server</button></div>${cards || `<div class="mcp-empty"><span>No servers configured</span></div>`}</div>`;
}

export function mcpFormHTML() {
  const f = S.mcpForm;
  const m = f.server || {};
  return `<div class="section-head"><div><h3>${f.id ? "Edit MCP server" : "Add MCP server"}</h3><div class="settings-desc" style="margin:3px 0 0">Stdio runs a local command; HTTP/SSE connects to a URL.</div></div><button class="btn btn-ghost btn-sm" data-action="cancel-mcp">Cancel</button></div>
    <form data-form="mcp" data-id="${esc(f.id || "")}">
      <div class="form-grid">
        <div class="field"><label>Name</label><input class="input" name="name" value="${esc(m.name || "")}" placeholder="filesystem" required></div>
        <div class="field"><label>Transport</label><select class="select" name="transport"><option value="stdio" ${m.transport !== "http" && m.transport !== "sse" ? "selected" : ""}>stdio</option><option value="http" ${m.transport === "http" ? "selected" : ""}>HTTP</option><option value="sse" ${m.transport === "sse" ? "selected" : ""}>SSE</option></select></div>
        <div class="field full"><label>Command <span class="muted">(stdio)</span></label><input class="input" name="command" value="${esc(m.command || "")}" placeholder="npx"></div>
        <div class="field full"><label>Arguments (one per line)</label><textarea class="textarea" name="args" rows="2">${esc((m.args || []).join("\n"))}</textarea></div>
        <div class="field full"><label>URL <span class="muted">(HTTP/SSE)</span></label><input class="input" name="url" value="${esc(m.url || "")}" placeholder="https://mcp.example.com/mcp"></div>
        <div class="field full"><label class="checkbox"><input type="checkbox" name="enabled" ${m.enabled !== false ? "checked" : ""}> Enabled</label></div>
      </div>
      <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Environment <span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="mcp">+ Add variable</button></div><div id="mcp-env-list">${envRowsHTML(m.env || [], "mcp")}</div></div>
      <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Headers <span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="mcp-header">+ Add header</button></div><div id="mcp-header-list">${envRowsHTML(m.headers || [], "mcp-header")}</div></div>
      <div class="form-actions"><button class="btn btn-secondary" type="button" data-action="cancel-mcp">Cancel</button><button class="btn btn-primary" type="submit">${f.id ? "Save server" : "Add server"}</button></div>
    </form>`;
}

export function channelsSettings() {
  return `${settingsHead("Channels", "Web always sees everything. WhatsApp receives one message when the turn finishes, plus approval pings.")}
    <div class="agent-list" id="channel-cards">${S.channels.map(channelCard).join("")}</div>
    <div id="wa-panel" style="margin-top:20px">${whatsappPanel()}</div>`;
}

export function channelCard(ch) {
  const statusClass = ch.loggedIn ? "" : ch.state === "error" ? "offline" : "warn";
  return `<div class="agent-card"><span class="tool-icon kind-execute" style="width:34px;height:34px">${ch.name === "whatsapp" ? icon("message") : icon("globe")}</span><div class="agent-card-main"><div class="agent-card-name">${esc(ch.label || ch.name)} <span class="status-dot ${statusClass}"></span></div><div class="agent-card-sub">${esc(ch.state || "unknown")}${ch.detail ? " · " + esc(ch.detail) : ""}</div></div>${ch.selfID ? `<span class="badge-soft green">${esc(ch.selfID)}</span>` : ""}</div>`;
}

export function routingSettings() {
  const r = S.routingDefaults || (S.bootstrap && S.bootstrap.routingDefaults) || { replyToOrigin: true, mirrorFinal: false, notifyApprovals: true };
  return `${settingsHead("Routing defaults", "These apply to new conversations. Each conversation can override them from the top bar.")}
    <div class="settings-row"><div><div class="label">Reply to origin</div><div class="desc">Send assistant replies back to the chat where the prompt came from.</div></div><button role="switch" aria-checked="${r.replyToOrigin}" class="switch ${r.replyToOrigin ? "on" : ""}" data-action="default-routing" data-key="replyToOrigin"></button></div>
    <div class="settings-row"><div><div class="label">Mirror final answers</div><div class="desc">Copy final answers to every linked channel. Off by default to avoid noise.</div></div><button role="switch" aria-checked="${r.mirrorFinal}" class="switch ${r.mirrorFinal ? "on" : ""}" data-action="default-routing" data-key="mirrorFinal"></button></div>
    <div class="settings-row"><div><div class="label">Notify approvals</div><div class="desc">Ping the origin chat when a tool needs a decision.</div></div><button role="switch" aria-checked="${r.notifyApprovals}" class="switch ${r.notifyApprovals ? "on" : ""}" data-action="default-routing" data-key="notifyApprovals"></button></div>
    <div class="settings-section" style="margin-top:26px"><div class="settings-title">How it works</div><div class="settings-card" style="border:1px solid var(--border);border-radius:11px;padding:12px 14px;color:var(--muted);font-size:12px;line-height:1.7">
      <div><b style="color:var(--text-soft)">Web</b> receives every message, thought, tool call, plan, approval and usage update in real time.</div>
      <div><b style="color:var(--text-soft)">External channels</b> receive one message when the turn finishes. Thoughts and raw tool output stay on the web.</div>
      <div><b style="color:var(--text-soft)">Approvals</b> are always required in web unless the agent policy or a remembered rule allows the call.</div>
    </div></div>`;
}

export function accountSettings() {
  const app = (S.bootstrap && S.bootstrap.app) || {};
  return `${settingsHead("Account", "Single-admin local control center.")}
    <div class="about-grid">
      <div class="about-item"><div class="k">Version</div><div class="v">${esc(app.version || S.appVersion || "unknown")}</div></div>
      <div class="about-item"><div class="k">Mode</div><div class="v">${app.dev ? "Development" : "Local-first"}</div></div>
      <div class="about-item"><div class="k">Public URL</div><div class="v">${esc(app.publicUrl || "not configured")}</div></div>
      <div class="about-item"><div class="k">WhatsApp</div><div class="v">${app.whatsappDisabled ? "disabled" : "available"}</div></div>
    </div>
    <div class="settings-section" style="margin-top:24px">
      <div class="settings-title">Change password</div>
      <form data-form="password" style="max-width:420px">
        <div class="field"><label>Current password</label><input class="input" type="password" name="current" autocomplete="current-password" required></div>
        <div class="field"><label>New password</label><input class="input" type="password" name="new" autocomplete="new-password" minlength="4" required></div>
        <div class="field"><label>Confirm new password</label><input class="input" type="password" name="confirm" autocomplete="new-password" minlength="4" required></div>
        <button class="btn btn-secondary" type="submit">Update password</button>
      </form>
    </div>
    <div class="settings-section">
      <div class="settings-title">Session</div>
      <div class="settings-row"><div><div class="label">Sign out</div><div class="desc">End this browser session. Agents keep running.</div></div><button class="btn btn-secondary btn-sm" data-action="logout">Sign out</button></div>
    </div>
    <div class="settings-section">
      <div class="danger-zone"><div class="settings-title" style="color:var(--danger)">Danger zone</div><div class="settings-desc">Erase every conversation, message, approval and channel link. Agents, MCP servers and channel pairings are kept.</div><button class="btn btn-danger btn-sm" data-action="delete-all-conversations">Erase conversations</button></div>
    </div>`;
}

export async function openAgentLogs(id, fromSettings = false) {
  try {
    const d = await api(`/agents/${encodeURIComponent(id)}/logs`);
    const a = d.agent;
    const rt = d.runtime || {};
    S.modal = { type: "agentlogs", id };
    const methods = (a.authMethods || []).map((m) => `<button class="btn btn-secondary btn-sm" data-action="auth-agent" data-id="${esc(a.id)}" data-method="${esc(m.id)}" data-auth-type="${esc(m.type || "agent")}">${esc(m.name || m.id)}</button>`).join("");
    openModal(`${modalHead(`Runtime · ${a.name}`, "stderr ring buffer and live process state")}<div class="modal-body" style="padding:16px">
      <div class="about-grid" style="margin-bottom:14px"><div class="about-item"><div class="k">Status</div><div class="v">${esc(rt.status || a.status || "unknown")}</div></div><div class="about-item"><div class="k">PID</div><div class="v">${rt.pid || "—"}</div></div><div class="about-item"><div class="k">Uptime</div><div class="v">${rt.uptimeSeconds ? rt.uptimeSeconds + "s" : "—"}</div></div><div class="about-item"><div class="k">Last probe</div><div class="v">${a.lastProbeAt ? timeAgo(a.lastProbeAt) + " ago" : "never"}</div></div></div>
      ${methods ? `<div class="settings-section"><div class="settings-title">Authentication</div><div style="display:flex;gap:7px;flex-wrap:wrap">${methods}</div><div class="settings-desc">Some agents open a browser or terminal login flow. Finish it, then probe again.</div></div>` : ""}
      <div class="tool-label">stderr</div><pre class="tool-pre" style="max-height:360px">${esc((rt.logs || []).join("\n") || "No stderr captured yet.")}</pre>
    </div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(a.id)}">${icon("refresh", "sm")} Refresh</button><button class="btn btn-primary" data-action="close-modal">Close</button></div>`, { size: "md" });
  } catch (e) {
    toast("Could not load logs", e.message, "error");
  }
}

export async function probeAgent(id) {
  toast("Probing agent", "Starting a temporary ACP session…", "info");
  try {
    await api(`/agents/${encodeURIComponent(id)}/probe`, { method: "POST" });
    const d = await api("/agents");
    S.agents = d.agents || S.agents;
    const m = await api("/mcp");
    S.mcp = m.servers || S.mcp;
    toast("Agent is ready", "Capabilities and model list refreshed.", "success");
    if (S.modal?.type === "settings" && !S.agentForm && !S.mcpForm) renderSettings();
    renderTopbar();
  } catch (e) {
    toast("Probe failed", e.message, "error");
  }
}

export async function saveAgent(form) {
  const fd = new FormData(form);
  const id = form.dataset.id;
  const env = [];
  $$("#agent-env-list .secret-row", form).forEach((row) => {
    const name = row.querySelector("[name=env-name]").value.trim();
    const valueInput = row.querySelector("[name=env-value]");
    const value = valueInput.value;
    if (!name) return;
    if (value) env.push({ name, value });
    else if (valueInput.dataset.hasValue) env.push({ name, hasValue: true });
  });
  const body = {
    name: String(fd.get("name") || "").trim(),
    slug: String(fd.get("slug") || "").trim(),
    command: String(fd.get("command") || "").trim(),
    args: String(fd.get("args") || "").split("\n").map((s) => s.trim()).filter(Boolean),
    cwd: String(fd.get("cwd") || "").trim(),
    description: String(fd.get("description") || "").trim(),
    icon: String(fd.get("icon") || "sparkles"),
    color: String(fd.get("color") || "amber"),
    approvalPolicy: String(fd.get("approvalPolicy") || "ask"),
    defaultModel: S.agentForm?.agent?.defaultModel || "",
    enabled: !!fd.get("enabled"),
    autoStart: !!fd.get("autoStart"),
    env,
  };
  try {
    const d = await api(id ? `/agents/${encodeURIComponent(id)}` : "/agents", { method: id ? "PATCH" : "POST", body });
    const list = await api("/agents");
    S.agents = list.agents || S.agents;
    S.agentForm = null;
    renderSettings();
    renderTopbar();
    toast(id ? "Agent updated" : "Agent added", d.agent.name + " is ready.", "success");
  } catch (e) {
    toast("Could not save agent", e.message, "error", 8000);
  }
}

export async function saveMCP(form) {
	if (form.dataset.saving) return;
	form.dataset.saving = "true";
	const submit = form.querySelector('[type="submit"]');
	if (submit) submit.disabled = true;
  const fd = new FormData(form);
  const id = form.dataset.id;
  const collect = (sel) => {
    const out = [];
    $$(sel + " .secret-row", form).forEach((row) => {
      const name = row.querySelector("[name=env-name]").value.trim();
      const valueInput = row.querySelector("[name=env-value]");
      if (!name) return;
      if (valueInput.value) out.push({ name, value: valueInput.value });
      else if (valueInput.dataset.hasValue) out.push({ name, hasValue: true });
    });
    return out;
  };
  const body = {
    name: String(fd.get("name") || "").trim(),
    transport: String(fd.get("transport") || "stdio"),
    command: String(fd.get("command") || "").trim(),
    args: String(fd.get("args") || "").split("\n").map((s) => s.trim()).filter(Boolean),
    url: String(fd.get("url") || "").trim(),
    enabled: !!fd.get("enabled"),
    env: collect("#mcp-env-list"),
    headers: collect("#mcp-header-list"),
  };
  try {
    await api(id ? `/mcp/${encodeURIComponent(id)}` : "/mcp", { method: id ? "PATCH" : "POST", body });
    const m = await api("/mcp");
    S.mcp = m.servers || S.mcp;
    S.mcpForm = null;
    renderSettings();
    toast(id ? "MCP server saved" : "MCP server added", "", "success");
  } catch (e) {
    toast("Could not save MCP server", e.message, "error", 8000);
  } finally {
    delete form.dataset.saving;
    if (submit) submit.disabled = false;
  }
}

export async function savePassword(form) {
  const fd = new FormData(form);
  if (fd.get("new") !== fd.get("confirm")) { toast("Passwords do not match", "Check the new password and try again.", "error"); return; }
  try {
    const d = await api("/auth/password", { method: "POST", body: { current: fd.get("current"), new: fd.get("new") } });
    S.csrf = d.csrf;
    toast("Password updated", "Other sessions were signed out.", "success");
    form.reset();
  } catch (e) {
    toast("Could not change password", e.message, "error");
  }
}

export async function deleteAgent(id) {
  const a = agentById(id);
  confirmModal("Delete agent?", `Remove ${a?.name || "this agent"} from NusaMation. Its current conversations must be deleted first. Sessions in conversations that switched agents may remain in ACP storage when those conversations are archived or deleted. Reconnecting an agent with the same slug allows NusaMation to attempt their deletion.`, async () => {
    try {
      await api(`/agents/${encodeURIComponent(id)}`, { method: "DELETE" });
      const d = await api("/agents");
      S.agents = d.agents || [];
      if (S.agentForm?.id === id) S.agentForm = null;
      renderSettings();
      toast("Agent removed", "", "success");
    } catch (e) {
      toast("Could not remove agent", e.message, "error");
    }
  }, "Delete");
}

export async function deleteMCP(id) {
  confirmModal("Delete MCP server?", "All agent runtimes will restart and lose access to this server.", async () => {
    try {
      await api(`/mcp/${encodeURIComponent(id)}`, { method: "DELETE" });
      const d = await api("/mcp");
      S.mcp = d.servers || [];
      renderSettings();
      toast("MCP server removed", "", "success");
    } catch (e) { toast("Could not remove server", e.message, "error"); }
  }, "Delete");
}
