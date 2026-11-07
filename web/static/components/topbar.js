import { $, agentName, agentOrb, configLabel, currentAgent, esc, icon } from "./util.js";
import { S } from "./state.js";
import { closeDetachedPopovers } from "./popover.js";

/* ---------------- topbar ---------------- */
export function renderTopbar() {
  const el = $("#topbar");
  if (!el) return;
  closeDetachedPopovers();
  const c = S.current;
  const agent = currentAgent();
  const model = modelLabel(c, agent);
  const pending = S.approvals.length;
  const links = (c && c.links || []).filter((l) => l.channel !== "web");
  const active = S.active && ["running", "waiting_approval", "cancelling", "queued"].includes(S.active.status);
  let sub = "Your ACP control center";
  if (c) {
    sub = `${esc(c.agentName || agentName(agent))}${active ? ` <span class="dot"></span> <span style="color:var(--accent)">${esc(activeLabel())}</span>` : ` <span class="dot"></span> ${esc(c.status || "idle")}`}`;
  }
  el.innerHTML = `
    <button class="icon-btn menu-btn" data-action="open-sidebar" aria-label="Open sidebar">${icon("menu")}</button>
    <div class="topbar-title-wrap">
      <div class="topbar-title">${esc(c ? (c.title || "Untitled") : "NusaMation")}</div>
      <div class="topbar-sub">${sub}</div>
    </div>
    <div class="topbar-actions">
      ${c ? `<button class="pill-btn agent-pill" data-action="agent-menu">${agentOrb(agent)}<span class="pill-label">${esc(agentName(agent))}</span>${icon("chevron-down", "sm chev")}</button>` : ""}
      ${c ? `<button class="pill-btn" data-action="config-menu" title="Session configuration">${icon("sparkles", "sm")}<span class="pill-label">${esc(model)}</span>${icon("chevron-down", "sm chev")}</button>` : ""}
      ${c && links.length ? `<button class="pill-btn accent" data-action="routing-menu" title="Channel routing">${icon("link", "sm")}<span class="pill-label">${esc(links[0].channel)}</span></button>` : ""}
      <button class="icon-btn approval-bell" data-action="open-approvals" title="Approvals">${icon("shield")}${pending ? `<span class="badge">${pending > 9 ? "9+" : pending}</span>` : ""}</button>
      ${c ? `<button class="icon-btn" data-action="more-menu" title="Conversation actions">${icon("more")}</button>` : `<button class="icon-btn" data-action="open-settings" title="Settings">${icon("settings")}</button>`}
    </div>`;
}

export function modelLabel(c, agent) {
  const opt = S.configs.find((o) => (o.category || "").toLowerCase() === "model");
  if (opt) return configLabel(opt);
  return agent?.defaultModel || "Default model";
}

export function activeLabel() {
  const s = S.active?.status || "";
  if (s === "waiting_approval") return "Waiting for approval";
  if (s === "queued") return "Queued";
  if (s === "cancelling") return "Stopping";
  return "Working";
}
