import { $, $$, brandImage, esc, icon, previewText, timeAgo } from "./util.js";
import { S } from "./state.js";
import { closeDetachedPopovers } from "./popover.js";
import { groupProjectless, partitionProjects, sortConversations, visibleProjectId } from "./projectmodel.js";

let expandedReady = false;

function ensureExpanded() {
  if (expandedReady) return;
  if (!(S.projects || []).length) return;
  expandedReady = true;
  if (!S.expandedProjects) S.expandedProjects = new Set();
  for (const project of S.projects) S.expandedProjects.add(project.id);
}

// resetSidebarState lets a fresh login re-expand every project.
export function resetSidebarState() {
  expandedReady = false;
}

/* ---------------- sidebar ---------------- */
export function renderSidebar() {
  const el = $("#sidebar");
  if (!el) return;
  closeDetachedPopovers();
  ensureExpanded();
  const parts = partitionProjects(S.projects);
  const chatGroups = groupProjectless(S.conversations, S.projects);
  const targetProject = (S.projects || []).find((p) => p.id === S.selectedProjectId);

  const prevList = $("#conv-list", el);
  const prevScroll = prevList ? prevList.scrollTop : 0;
  const prevSearch = $("#conv-search", el);
  const searchFocused = document.activeElement === prevSearch;
  const selStart = searchFocused ? prevSearch.selectionStart : 0;
  const selEnd = searchFocused ? prevSearch.selectionEnd : 0;
  const focused = !searchFocused && el.contains(document.activeElement) ? document.activeElement : null;
  const focusKey = focused?.dataset?.action ? `${focused.dataset.action}|${focused.dataset.id || ""}` : "";

  const projectGroups = [
    parts.pinned.length ? `<div class="conv-section" data-project-group><div class="conv-section-title">Pinned</div>${parts.pinned.map(projectBlockHTML).join("")}</div>` : "",
    parts.loose.length ? `<div class="conv-section" data-project-group>${parts.loose.map(projectBlockHTML).join("")}</div>` : "",
  ].join("");

  const anyVisible = (S.conversations || []).some((c) => c && !c.archived);
  const chatsEmpty = anyVisible ? "" : `<div class="sidebar-empty" data-empty="chats"><p>No conversations yet.<br>Start one and it will show up here.</p></div>`;
  const chatsHTML = chatGroups.length ? `<div class="chats-group" data-chats><div class="conv-section-title">Chats</div>${chatGroups.map(([label, list]) => `
        <div class="conv-section" data-group>
          <div class="conv-section-title">${esc(label)}</div>
          ${list.map(convItemHTML).join("")}
        </div>`).join("")}</div>` : "";

  el.innerHTML = `
    <div class="sidebar-top">
      <div class="brand">
        ${brandImage("brand-mark")}
        <div><div class="brand-name">NusaMation</div><div class="brand-tag">agent mesh</div></div>
        <button class="icon-btn sm" style="margin-left:auto" data-action="open-settings" title="Settings">${icon("settings", "sm")}</button>
        <button class="icon-btn sm sidebar-collapse-btn" data-action="toggle-sidebar" title="Collapse sidebar">${icon("panel-left", "sm")}</button>
      </div>
      <button class="new-chat" data-action="new-chat">${icon("plus")} <span>${targetProject ? `New conversation in ${esc(targetProject.name || "project")}` : "New conversation"}</span></button>
      <div class="sidebar-search">${icon("search")}<input id="conv-search" type="search" placeholder="Search chats and projects…" value="${esc(S.query)}" data-input="conv-search" autocomplete="off"></div>
    </div>
    <div class="sidebar-scroll" id="conv-list">
      <div class="conv-section-title projects-label"><span>Projects</span><button type="button" class="project-add-btn" data-action="new-project" title="New project">${icon("plus", "sm")}<span>New project</span></button></div>
      ${projectGroups}
      ${chatsHTML}
      ${chatsEmpty}
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
  } else if (focusKey) {
    $$("[data-action]", el).find((node) => `${node.dataset.action}|${node.dataset.id || ""}` === focusKey)?.focus({ preventScroll: true });
  }
  filterSidebar();
}

function projectBlockHTML(project) {
  const open = !!S.expandedProjects?.has(project.id);
  const isTarget = S.selectedProjectId === project.id;
  const id = esc(project.id);
  const name = esc(project.name || "Untitled project");
  const chats = sortConversations((S.conversations || []).filter((c) => c && !c.archived && c.projectId === project.id));
  return `<div class="project-block ${open ? "is-open" : ""}" data-project-id="${id}" data-project-name="${esc((project.name || "").toLowerCase())}">
    <div class="project-head ${isTarget ? "is-target" : ""}" data-id="${id}">
      <button type="button" class="project-toggle" data-action="toggle-project" data-id="${id}" aria-expanded="${open ? "true" : "false"}">
        ${icon(open ? "chevron-down" : "chevron-right", "sm")}
        <span class="project-name">${name}</span>
        ${project.pinned ? icon("pin", "conv-pin") : ""}
        ${chats.length ? `<span class="project-count" title="${chats.length} chat${chats.length === 1 ? "" : "s"}">${chats.length}</span>` : ""}
      </button>
      <button type="button" class="icon-btn sm project-menu-btn" data-action="project-menu" data-id="${id}" title="Project menu" aria-label="Project menu: ${name}">${icon("more", "sm")}</button>
    </div>
    <div class="project-chats">
      ${chats.length ? chats.map(convChildHTML).join("") : `<p class="project-none">No chats yet</p>`}
      <button type="button" class="project-new" data-action="project-new-chat" data-id="${id}">${icon("plus", "sm")} New conversation</button>
    </div>
  </div>`;
}

export function toggleProjectRow(id) {
  if (!id) return;
  if (!S.expandedProjects) S.expandedProjects = new Set();
  if (S.expandedProjects.has(id)) S.expandedProjects.delete(id);
  else S.expandedProjects.add(id);
  renderSidebar();
}

function controlBadge(c) {
  const meta = c && c.metadata && typeof c.metadata === "object" ? c.metadata : {};
  return meta.orchestration && meta.orchestration.enabled
    ? `<span class="conv-control" title="Multi-agent control enabled">${icon("zap", "sm")}</span>`
    : "";
}

function spawnedBadge(c) {
  const meta = c && c.metadata && typeof c.metadata === "object" ? c.metadata : {};
  return meta.spawnedBy
    ? `<span class="conv-control" title="Delegated by another conversation">${icon("link", "sm")}</span>`
    : "";
}

export function convItemHTML(c) {
  const links = c.links || [];
  const external = links.find((l) => l.channel !== "web");
  const channel = external ? external.channel : (c.originChannel && c.originChannel !== "web" ? c.originChannel : "");
  const unread = S.unread.get(c.id) || 0;
  const preview = previewText(c.lastMessage, 70);
  return `<div class="conv-item ${S.current && S.current.id === c.id ? "active" : ""}" role="button" tabindex="0" data-action="select-conv" data-id="${esc(c.id)}" data-title="${esc((c.title || "").toLowerCase())}" data-preview="${esc((c.lastMessage || "").toLowerCase())}">
    <div class="conv-title-row">
      ${c.pinned ? icon("pin", "conv-pin") : ""}
      ${controlBadge(c)}
      ${spawnedBadge(c)}
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

// convChildHTML renders a project child as a single-line tree row (no
// preview) — VSCode-style compact nesting instead of the full card.
function convChildHTML(c) {
  const unread = S.unread.get(c.id) || 0;
  return `<div class="conv-item conv-child ${S.current && S.current.id === c.id ? "active" : ""}" role="button" tabindex="0" data-action="select-conv" data-id="${esc(c.id)}" data-title="${esc((c.title || "").toLowerCase())}" data-preview="${esc((c.lastMessage || "").toLowerCase())}" title="${esc(c.title || "Untitled")}">
    ${c.pinned ? icon("pin", "conv-pin") : ""}
    ${controlBadge(c)}
    ${spawnedBadge(c)}
    <span class="conv-title">${esc(c.title || "Untitled")}</span>
    ${unread ? `<span class="conv-badge">${unread > 9 ? "9+" : unread}</span>` : `<span class="conv-time">${timeAgo(c.lastMessageAt || c.updatedAt)}</span>`}
    <button class="icon-btn sm conv-child-menu" data-action="conv-menu" data-id="${esc(c.id)}" title="Conversation menu">${icon("more", "sm")}</button>
  </div>`;
}

export function filterSidebar() {
  const q = S.query.trim().toLowerCase();
  const list = $("#conv-list");
  if (!list) return;
  $$(".sidebar-empty[data-empty=query]", list).forEach((e) => e.remove());
  const chatsZero = $(".sidebar-empty[data-empty=chats]", list);
  if (chatsZero) chatsZero.classList.toggle("hidden", !!q);

  $$("[data-project-id]", list).forEach((block) => {
    const nameHit = !!q && (block.dataset.projectName || "").includes(q);
    let chatHits = 0;
    $$(".conv-item", block).forEach((item) => {
      const hit = !q || nameHit || (item.dataset.title || "").includes(q) || (item.dataset.preview || "").includes(q);
      item.classList.toggle("hidden", !hit);
      if (q && hit) chatHits++;
    });
    const visible = !q || nameHit || chatHits > 0;
    block.classList.toggle("hidden", !visible);
    block.classList.toggle("show-chats", !!q && visible);
    $$(".project-new, .project-none", block).forEach((node) => node.classList.toggle("hidden", !!q && !nameHit));
  });

  $$("[data-project-group]", list).forEach((group) => {
    group.classList.toggle("hidden", !$("[data-project-id]:not(.hidden)", group));
  });

  $$(".conv-section[data-group]", list).forEach((group) => {
    let visible = 0;
    $$(".conv-item", group).forEach((item) => {
      const hit = !q || (item.dataset.title || "").includes(q) || (item.dataset.preview || "").includes(q);
      item.classList.toggle("hidden", !hit);
      if (hit) visible++;
    });
    group.classList.toggle("hidden", !visible);
  });

  const chats = $("[data-chats]", list);
  if (chats) chats.classList.toggle("hidden", !$(".conv-section[data-group]:not(.hidden)", chats));

  const anyProject = $("[data-project-id]:not(.hidden)", list);
  const anyChat = $(".conv-section[data-group]:not(.hidden)", list);
  if (q && !anyProject && !anyChat) {
    list.insertAdjacentHTML("beforeend", `<div class="sidebar-empty" data-empty="query">Nothing matches “${esc(q)}”.</div>`);
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
    const host = node.closest("[data-project-id]")?.dataset.projectId || "";
    if (host !== visibleProjectId(c, S.projects)) { renderSidebar(); return; }
    const text = previewText(c.lastMessage, 70);
    const preview = $(".conv-preview", node);
    if (preview && preview.textContent !== text) preview.textContent = text;
    const title = $(".conv-title", node);
    if (title && title.textContent !== (c.title || "Untitled")) title.textContent = c.title || "Untitled";
    const time = $(".conv-time", node);
    if (time) time.textContent = timeAgo(c.lastMessageAt || c.updatedAt);
    node.dataset.title = (c.title || "").toLowerCase();
    node.dataset.preview = (c.lastMessage || "").toLowerCase();
  }
  filterSidebar();
}
