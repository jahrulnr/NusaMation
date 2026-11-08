import { $, $$, agentById, agentName, configChoices, currentAgent, esc, icon, parseJSON, pretty, truncate } from "./util.js?v=43";
import { S } from "./state.js?v=43";
import { api } from "./api.js?v=43";
import { toast } from "./toast.js?v=43";
import { closeModal, modalHead, openModal } from "./modal.js?v=43";
import { currentApprovals, scheduleChatRender } from "./chat.js?v=43";
import { renderSidebar } from "./sidebar.js?v=43";
import { renderTopbar } from "./topbar.js?v=43";

export function approvalModeRow() {
  const opt = S.configs.find((o) => o && o.id && o.type === "select" && String(o.category || "").toLowerCase() === "mode");
  const choices = opt ? configChoices(opt) : [];
  if (!choices.length) return "";
  return `<label class="approval-mode"><span>${esc(opt.name || "Mode")}</span><select class="select config-select" data-change="set-config" data-id="${esc(opt.id)}">${choices.map((c) => `<option value="${esc(c.value)}" ${String(c.value) === String(opt.currentValue) ? "selected" : ""}>${esc(c.name || c.value)}</option>`).join("")}</select></label>`;
}

export function refreshApprovalModal() {
  const a = S.approvalsById.get(S.modal.id);
  const body = $("#modal-root .modal-body");
  if (a && body && a.status === "pending") body.innerHTML = approvalCard(a);
}

export function approvalCard(a) {
  const risk = String(a.risk || "medium").toLowerCase();
  const opts = a.options || [];
  const resolving = S.resolving.has(a.id);
  const details = parseJSON(a.details, {}) || {};
  const tool = details.toolCall || {};
  const detailText = tool.rawInput ? pretty(tool.rawInput) : pretty(details);
  const allow = opts.filter((o) => String(o.kind || "").toLowerCase().startsWith("allow"));
  const deny = opts.filter((o) => String(o.kind || "").toLowerCase().startsWith("reject"));
  const ordered = [...allow, ...deny, ...opts.filter((o) => !allow.includes(o) && !deny.includes(o))];
  return `<div class="approval-card" data-approval-id="${esc(a.id)}">
    <div class="approval-top">
      <span class="approval-icon">${icon("shield")}</span>
      <div class="grow"><div class="approval-title">${esc(a.title || "Tool approval")}<span class="risk ${esc(risk)}">${esc(risk)}</span></div>
      <div class="approval-sub">${esc(details.toolName || a.toolCallId || a.kind || "tool call")} · the agent is waiting for your decision</div></div>
    </div>
    <div class="approval-details">${esc(truncate(detailText, 1800))}</div>
    ${a.conversationId === S.current?.id ? approvalModeRow() : ""}
    <div class="approval-actions">
      ${ordered.map((o) => {
        const k = String(o.kind || "").toLowerCase();
        const cls = k.startsWith("allow_once") ? "btn-primary" : k.startsWith("allow") ? "btn-secondary" : "btn-danger";
        return `<button class="btn ${cls} btn-sm" data-action="resolve-approval" data-id="${esc(a.id)}" data-option="${esc(o.optionId)}" ${resolving ? "disabled" : ""}>${k.startsWith("allow") ? icon("check", "sm") : icon("x", "sm")}${esc(o.name || o.optionId)}</button>`;
      }).join("")}
      ${resolving ? `<span class="loading-row" style="padding:4px 6px"><span class="spinner"></span></span>` : ""}
    </div>
  </div>`;
}

export function showSlowAgent(connecting) {
  S.modal = { type: "agent-slow", conversationId: S.current?.id, connecting };
  openModal(`${modalHead("Still waiting for the agent", agentName(currentAgent()))}<div class="modal-body" style="padding:18px"><p class="muted">${connecting ? "The agent has not finished connecting." : "The agent has not sent a response yet."} You can keep waiting, inspect its details, or stop.</p></div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(currentAgent()?.id || "")}">Agent details</button><button class="btn btn-secondary" data-action="${connecting ? "stop-preparing" : "stop-slow-turn"}">Stop</button><button class="btn btn-primary" data-action="close-modal">Keep waiting</button></div>`, { size: "md" });
}

export function showAgentBlocker(error) {
  const agent = currentAgent();
  const methods = error.data?.authMethods?.length ? error.data.authMethods : (agent?.authMethods || []);
  S.modal = { type: "agent-blocker" };
  openModal(`${modalHead("Agent needs attention", agentName(agent))}<div class="modal-body" style="padding:18px"><p class="muted">${esc(error.message)}</p>${methods.length ? `<div class="settings-title">Choose how to continue</div><div class="blocker-actions">${methods.map((m) => `<button class="btn btn-secondary" data-action="blocker-auth" data-id="${esc(agent?.id || "")}" data-method="${esc(m.id)}" data-auth-type="${esc(m.type || "agent")}">${esc(m.name || m.id)}</button>`).join("")}</div>` : ""}</div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(agent?.id || "")}">View agent details</button><button class="btn btn-primary" data-action="retry-prepare">Try again</button></div>`, { size: "md" });
}

export function renderApprovalsModal() {
  S.modal = { type: "approvals" };
  const list = S.approvals.filter((a) => a.status === "pending");
  const body = list.length
    ? `<div class="approval-list">${list.map((a) => `<div class="approval-modal-item"><div class="muted" style="font-size:10.5px;margin-bottom:5px">${esc(a.conversationTitle || "Conversation")} · ${esc(a.agentName || "agent")}</div>${approvalCard(a)}</div>`).join("")}</div>`
    : `<div class="empty-settings">${icon("shield")}<div style="margin-top:8px">No pending approvals. Your agents are behaving.</div></div>`;
  openModal(`${modalHead("Approvals", `${list.length} pending`)}<div class="modal-body" style="padding:16px">${body}</div>`, { size: "md" });
}

export function showPendingApproval() {
  if (S.modal) return;
  const approval = currentApprovals()[0];
  if (!approval) return;
  S.modal = { type: "approval", id: approval.id };
  openModal(`${modalHead("Approval needed", "The agent is waiting for your decision")}<div class="modal-body" style="padding:16px">${approvalCard(approval)}</div>`, { size: "md" });
}

export function showPendingRequest() {
  if (S.modal) return;
  const request = S.elicitations.find((e) => e.conversationId === S.current?.id);
  if (request) return showElicitation(request);
  showPendingApproval();
}

export function showElicitation(entry) {
  const req = entry.request || {};
  const agent = agentById(entry.agentId) || currentAgent();
  const schema = req.requestedSchema || {};
  const required = new Set(schema.required || []);
  const unsupported = Object.values(schema.properties || {}).some((spec) => !["string", "integer", "number", "boolean", "array"].includes(spec.type) || (spec.type === "array" && !spec.items?.enum && !spec.items?.anyOf));
  const fields = Object.entries(schema.properties || {}).map(([key, spec]) => {
    const title = esc(spec.title || key);
    const label = `<label>${title}${required.has(key) ? " *" : ""}</label>${spec.description ? `<div class="settings-desc">${esc(spec.description)}</div>` : ""}`;
    const attrs = `data-key="${esc(key)}" data-type="${esc(spec.type || "string")}" ${required.has(key) ? "required" : ""}`;
    const choices = spec.oneOf || (spec.enum || []).map((v) => ({ const: v, title: v }));
    let control = "";
    if (spec.type === "array") {
      const items = spec.items || {};
      const values = items.anyOf || (items.enum || []).map((v) => ({ const: v, title: v }));
      control = `<div class="elicitation-multi" data-key="${esc(key)}" data-min="${esc(spec.minItems ?? (required.has(key) ? 1 : 0))}" data-max="${esc(spec.maxItems ?? "")}">${values.map((v) => `<label><input type="checkbox" value="${esc(v.const)}" ${(spec.default || []).includes(v.const) ? "checked" : ""}> ${esc(v.title || v.const)}</label>`).join("")}</div>`;
    } else if (choices.length || spec.type === "boolean") {
      const options = spec.type === "boolean" ? [{ const: "true", title: "Yes" }, { const: "false", title: "No" }] : choices;
      control = `<select class="select" ${attrs}><option value="">Choose…</option>${options.map((v) => `<option value="${esc(v.const)}" ${String(spec.default) === String(v.const) ? "selected" : ""}>${esc(v.title || v.const)}</option>`).join("")}</select>`;
    } else {
      const inputType = spec.type === "number" || spec.type === "integer" ? "number" : spec.format === "email" ? "email" : spec.format === "date" ? "date" : "text";
      const bounds = `${spec.minimum != null ? `min="${esc(spec.minimum)}"` : ""} ${spec.maximum != null ? `max="${esc(spec.maximum)}"` : ""} ${spec.minLength != null ? `minlength="${esc(spec.minLength)}"` : ""} ${spec.maxLength != null ? `maxlength="${esc(spec.maxLength)}"` : ""}`;
      control = `<input class="input" type="${inputType}" ${attrs} ${bounds} ${spec.type === "number" ? 'step="any"' : ""} value="${esc(spec.default ?? "")}">`;
    }
    return `<div class="field">${label}${control}</div>`;
  }).join("");
  let body = `<p class="muted">${esc(req.message || "The agent needs your input.")}</p>`;
  if (req.mode === "url") {
    try {
      const url = new URL(req.url);
      body += `<div class="settings-row"><div><div class="label">${esc(url.host)}</div><div class="desc">${esc(url.href)}</div></div></div>`;
    } catch { body += `<p>Invalid URL from agent.</p>`; }
  } else if (unsupported) {
    body += `<p>This request contains field types that cannot be entered here.</p><pre class="tool-pre">${esc(JSON.stringify(schema, null, 2))}</pre>`;
  } else {
    body += fields;
  }
  S.modal = { type: "elicitation", id: entry.id, conversationId: entry.conversationId };
  openModal(`<form data-form="elicitation" id="elicitation-form">${modalHead("Agent needs input", agentName(agent))}<div class="modal-body" style="padding:18px">${body}</div><div class="modal-foot"><button type="button" class="btn btn-secondary" data-action="elicitation-decline">Decline</button><button type="submit" class="btn btn-primary" ${unsupported ? "disabled" : ""}>${req.mode === "url" ? "Open link" : "Continue"}</button></div></form>`, { size: "md" });
}

export async function respondElicitation(conversationId, id, response) {
  try {
    await api(`/conversations/${encodeURIComponent(conversationId)}/elicitations/${encodeURIComponent(id)}`, { method: "POST", body: response });
    S.elicitations = S.elicitations.filter((e) => e.id !== id);
    if (S.modal?.type === "elicitation" && S.modal.id === id) closeModal(false);
    showPendingRequest();
  } catch (e) { toast("Could not answer agent", e.message, "error"); }
}

export function submitElicitation(form) {
  const entry = S.elicitations.find((e) => e.id === S.modal?.id);
  if (!entry) return;
  const req = entry.request || {};
  const content = {};
  if (req.mode === "url") {
    try { const url = new URL(req.url); if (!["http:", "https:"].includes(url.protocol)) throw new Error("invalid protocol"); window.open(url.href, "_blank", "noopener"); } catch { toast("Invalid link", "The agent provided an invalid URL.", "error"); return; }
  } else {
    for (const input of $$('[data-key][data-type]', form)) {
      if (input.value === "") continue;
      content[input.dataset.key] = input.dataset.type === "boolean" ? input.value === "true" : ["number", "integer"].includes(input.dataset.type) ? Number(input.value) : input.value;
    }
    for (const group of $$(".elicitation-multi", form)) {
      const selected = $$("input:checked", group).map((input) => input.value);
      if (selected.length < Number(group.dataset.min || 0) || (group.dataset.max && selected.length > Number(group.dataset.max))) { toast("Check your selection", "Choose the requested number of options.", "error"); return; }
      if (selected.length) content[group.dataset.key] = selected;
    }
  }
  respondElicitation(entry.conversationId, entry.id, req.mode === "url" ? { action: "accept" } : { action: "accept", content });
}

export async function resolveApproval(id, optionId, card) {
  if (S.resolving.has(id)) return;
  S.resolving.add(id);
  if (card) card.innerHTML = `<div class="loading-row"><span class="spinner"></span> Sending decision…</div>`;
  try {
    const d = await api(`/approvals/${encodeURIComponent(id)}`, { method: "POST", body: { optionId } });
    S.approvalsById.set(d.approval.id, d.approval);
    S.approvals = S.approvals.filter((a) => a.id !== id);
    renderTopbar(); renderSidebar();
    if (S.modal?.type === "approvals") renderApprovalsModal();
    scheduleChatRender();
    toast("Decision sent", d.approval.status === "approved" ? "The agent can continue." : "The tool call was not allowed.", "success");
  } catch (e) {
    toast("Could not answer approval", e.message, "error");
    scheduleChatRender();
  } finally {
    S.resolving.delete(id);
  }
}
