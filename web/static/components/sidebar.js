import { $, $$, agentById, esc, icon, timeAgo, truncate } from "./util.js?v=33";
import { S } from "./state.js?v=33";
import { closeDetachedPopovers } from "./popover.js?v=33";

/* ---------------- sidebar ---------------- */
export function renderSidebar() {
  const el = $("#sidebar");
  if (!el) return;
  closeDetachedPopovers();
  const convs = S.conversations.filter((c) => !c.archived);
  const pinned = convs.filter((c) => c.pinned);
  const rest = convs.filter((c) => !c.pinned);
  const groups = [];
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const yesterday = new Date(today.getTime() - 864e5);
  const gToday = [], gYesterday = [], gEarlier = [];
  rest.forEach((c) => {
    const t = c.lastMessageAt || c.updatedAt || 0;
    if (t >= today.getTime()) gToday.push(c);
    else if (t >= yesterday.getTime()) gYesterday.push(c);
    else gEarlier.push(c);
  });
  if (pinned.length) groups.push(["Pinned", pinned]);
  if (gToday.length) groups.push(["Today", gToday]);
  if (gYesterday.length) groups.push(["Yesterday", gYesterday]);
  if (gEarlier.length) groups.push(["Earlier", gEarlier]);

  const prevList = $("#conv-list", el);
  const prevScroll = prevList ? prevList.scrollTop : 0;
  const prevSearch = $("#conv-search", el);
  const searchFocused = document.activeElement === prevSearch;
  const selStart = searchFocused ? prevSearch.selectionStart : 0;
  const selEnd = searchFocused ? prevSearch.selectionEnd : 0;

  el.innerHTML = `
    <div class="sidebar-top">
      <div class="brand">
        <span class="brand-mark">N</span>
        <div><div class="brand-name">NusaMation</div><div class="brand-tag">agent mesh</div></div>
        <button class="icon-btn sm" style="margin-left:auto" data-action="open-settings" title="Settings">${icon("settings", "sm")}</button>
        <button class="icon-btn sm sidebar-collapse-btn" data-action="toggle-sidebar" title="Collapse sidebar">${icon("panel-left", "sm")}</button>
      </div>
      <button class="new-chat" data-action="new-chat">${icon("plus")} New conversation</button>
      <div class="sidebar-search">${icon("search")}<input id="conv-search" type="search" placeholder="Search conversations…" value="${esc(S.query)}" data-input="conv-search" autocomplete="off"></div>
    </div>
    <div class="sidebar-scroll" id="conv-list">
      ${groups.length ? groups.map(([label, list]) => `
        <div class="conv-section" data-group>
          <div class="conv-section-title">${esc(label)}</div>
          ${list.map(convItemHTML).join("")}
        </div>`).join("") : `<div class="sidebar-empty">${S.query ? "No conversations match your search." : "No conversations yet.<br>Start one and it will show up here."}</div>`}
    </div>
    <div class="sidebar-bottom">
      <div class="connection-pill" title="Live event stream">
        <span class="status-dot ${S.wsState === "online" ? "" : S.wsState === "connecting" ? "warn" : "offline"}"></span>
        <span>${S.wsState === "online" ? "Live" : S.wsState === "connecting" ? "Connecting…" : "Offline"}</span>
        <span class="grow"></span>
        ${S.approvals.length ? `<button class="badge-soft amber" data-action="open-approvals">${icon("shield", "sm")} ${S.approvals.length}</button>` : ""}
      </div>

    </div>`;
  const list = $("#conv-list", el);
  if (list) list.scrollTop = prevScroll;
  if (searchFocused) {
    const next = $("#conv-search", el);
    next?.focus();
    try { next?.setSelectionRange(selStart, selEnd); } catch {}
  }
  filterSidebar();
}

export function convItemHTML(c) {
  const agent = agentById(c.agentId);
  const links = c.links || [];
  const external = links.find((l) => l.channel !== "web");
  const channel = external ? external.channel : (c.originChannel && c.originChannel !== "web" ? c.originChannel : "");
  const unread = S.unread.get(c.id) || 0;
  const preview = c.lastMessage ? truncate(c.lastMessage.replace(/\s+/g, " "), 70) : "No messages yet";
  return `<div class="conv-item ${S.current && S.current.id === c.id ? "active" : ""}" role="button" tabindex="0" data-action="select-conv" data-id="${esc(c.id)}" data-title="${esc((c.title || "").toLowerCase())}" data-preview="${esc((c.lastMessage || "").toLowerCase())}">
    <div class="conv-title-row">
      ${c.pinned ? icon("pin", "conv-pin") : ""}
      <span class="conv-title">${esc(c.title || "Untitled")}</span>
      ${unread ? `<span class="conv-badge">${unread > 9 ? "9+" : unread}</span>` : `<span class="conv-time">${timeAgo(c.lastMessageAt || c.updatedAt)}</span>`}
    </div>
    <div class="conv-meta">
      <span class="conv-preview">${esc(preview)}</span>
      ${channel ? `<span class="conv-channel" title="${esc(channel)}">${channel === "whatsapp" ? icon("message") : icon("globe")}</span>` : ""}
      <button class="icon-btn sm" data-action="conv-menu" data-id="${esc(c.id)}" title="Conversation menu">${icon("more", "sm")}</button>
    </div>
  </div>`;
}

export function filterSidebar() {
  const q = S.query.trim().toLowerCase();
  const list = $("#conv-list");
  if (!list) return;
  $$(".sidebar-empty", list).forEach((e) => e.remove());
  $$(".conv-section", list).forEach((group) => {
    let visible = 0;
    $$(".conv-item", group).forEach((item) => {
      const hit = !q || (item.dataset.title || "").includes(q) || (item.dataset.preview || "").includes(q);
      item.classList.toggle("hidden", !hit);
      if (hit) visible++;
    });
    group.classList.toggle("hidden", !visible);
  });
  if (!$(".conv-section:not(.hidden)", list) && q) {
    list.insertAdjacentHTML("beforeend", `<div class="sidebar-empty">No conversations match “${esc(q)}”.</div>`);
  }
}

const SIDEBAR_COLLAPSED_KEY = "nusamation:sidebar-collapsed";

export function sidebarCollapsed() {
  try { return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1"; } catch { return false; }
}

export function setSidebarCollapsed(on) {
  $(".app")?.classList.toggle("sidebar-collapsed", on);
  try { localStorage.setItem(SIDEBAR_COLLAPSED_KEY, on ? "1" : "0"); } catch {}
}

export function toggleSidebarCollapsed() {
  setSidebarCollapsed(!$(".app")?.classList.contains("sidebar-collapsed"));
}

export function openMobileSidebar() {
  // The topbar menu button is the expand affordance on collapsed desktop too.
  setSidebarCollapsed(false);
  $("#sidebar")?.classList.add("open");
  $("#scrim")?.classList.add("show");
}

export function closeMobileSidebar() {
  $("#sidebar")?.classList.remove("open");
  $("#scrim")?.classList.remove("show");
}

export let sidebarRenderTimer = null;

export function scheduleSidebarRender() {
  if (sidebarRenderTimer) return;
  sidebarRenderTimer = setTimeout(() => {
    sidebarRenderTimer = null;
    patchSidebarConversations();
  }, 220);
}

// Updates only preview/time text inside existing rows so rapid message
// streams don't recreate the DOM under the cursor; falls back to a full
// render when the visible set no longer matches.
export function patchSidebarConversations() {
  const list = $("#conv-list");
  if (!list) { renderSidebar(); return; }
  const convs = S.conversations.filter((c) => !c.archived);
  if ($$(".conv-item", list).length !== convs.length) { renderSidebar(); return; }
  for (const c of convs) {
    const node = list.querySelector(`.conv-item[data-id="${CSS.escape(c.id)}"]`);
    if (!node) { renderSidebar(); return; }
    const previewText = c.lastMessage ? truncate(c.lastMessage.replace(/\s+/g, " "), 70) : "No messages yet";
    const preview = $(".conv-preview", node);
    if (preview && preview.textContent !== previewText) preview.textContent = previewText;
    const title = $(".conv-title", node);
    if (title && title.textContent !== (c.title || "Untitled")) title.textContent = c.title || "Untitled";
    const time = $(".conv-time", node);
    if (time) time.textContent = timeAgo(c.lastMessageAt || c.updatedAt);
    node.dataset.title = (c.title || "").toLowerCase();
    node.dataset.preview = (c.lastMessage || "").toLowerCase();
  }
  filterSidebar();
}
