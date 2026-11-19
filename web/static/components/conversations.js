import { $, agentById, clock, esc, icon, isMobile, now, parseConfigs } from "./util.js";
import { S, composers, setSelectedProject, switchComposer } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { closeModal, confirmModal, modalHead, openModal, promptModal } from "./modal.js";
import { closePopover, menuItem, openPopover } from "./popover.js";
import { applyStreamDelta, renderChat, scheduleStreamResync } from "./chat.js";
import { showPendingRequest } from "./requests.js";
import { focusComposer, prepareComposer, renderComposer, upsertTurn } from "./composer.js";
import { closeMobileSidebar, renderSidebar, scheduleSidebarRender } from "./sidebar.js";
import { renderTopbar } from "./topbar.js";
import { saveTitle } from "./convmeta.js";
import { closeSettings } from "./settings.js";
import { visibleProjectId } from "./projectmodel.js";

export async function selectInitialConversation() {
  const m = location.pathname.match(/^\/c\/([^/]+)/);
  let id = m ? decodeURIComponent(m[1]) : "";
  if (!id) {
    let last = "";
    try { last = localStorage.getItem("nusamation:lastConversation") || ""; } catch {}
    if (last && S.conversations.some((c) => c.id === last)) id = last;
  }
  if (!id && S.conversations.length && !isMobile()) id = S.conversations[0].id;
  if (id) {
    await loadConversation(id, false);
  } else {
    renderChat();
    renderComposer();
  }
}

export async function selectConversation(id, push = true) {
  if (!id) return;
  closeSettings();
  if (S.current && S.current.id === id && S.messages.length) {
    if (push) setRoute(id);
    closeMobileSidebar();
    return;
  }
  S.unread.delete(id);
  await loadConversation(id, push);
}

// A tool-heavy turn can fill the whole initial window (150 newest messages)
// with collapsed tool/thought rows. The transcript then fits the viewport, so
// no scroll event fires and the near-top trigger never runs — the view is
// stuck on "Working · N tool calls" with the turn's user message unreachable.
// Autofill loads pages until the transcript can scroll, bounded so a single
// pathologically long turn cannot hammer the endpoint; past the bound the
// control offers a manual button instead of a dead end.
const HISTORY_AUTOFILL_MAX = 8;

function scheduleHistoryAutofill() {
  if (typeof requestAnimationFrame !== "function") return;
  requestAnimationFrame(() => { autoLoadOlderMessages(); });
}

export function autoLoadOlderMessages() {
  if (!S.current || !S.historyHasMore || S.historyLoading || S.historyError || !S.messages.length) return false;
  const sc = $("#chat-scroll");
  if (!sc) return false;
  // A scrollable transcript already exposes the near-top trigger; fetching
  // pages while the reader is mid-thread would move their content.
  if (sc.scrollHeight > sc.clientHeight + 8) return false;
  if (S.historyAutoPages >= HISTORY_AUTOFILL_MAX) {
    if (!S.historyStuck) {
      S.historyStuck = true;
      renderHistoryControl();
    }
    return false;
  }
  S.historyAutoPages += 1;
  loadOlderMessages();
  return true;
}

export async function loadConversation(id, push = true) {
  const seq = ++S.loadSeq;
  S.loadingConversation = true;
  S.loadingId = id;
  S.loadingDeltas = [];
  S.loadError = "";
  S.historyHasMore = false;
  S.historyLoading = false;
  S.historyError = "";
  switchComposer(id);
  S.scrollPinned = true;
  // The project scope follows the open conversation so the sidebar's target
  // and "New conversation in …" label match the top bar. Refresh loads of the
  // same conversation keep a deliberate scope.
  const switching = !S.current || S.current.id !== id;
  S.current = S.conversations.find((c) => c.id === id) || S.current;
  if (switching && S.current && S.current.id === id) setSelectedProject(visibleProjectId(S.current, S.projects));
  S.messages = [];
  S.turns = {};
  S.active = null;
  S.configs = [];
  renderSidebar();
  renderTopbar();
  if (S.current && S.current.id !== id) S.current = null;
  renderChat(true);
  renderComposer();
  try {
    const data = await api(`/conversations/${encodeURIComponent(id)}`);
    if (seq !== S.loadSeq) return;
    const duringLoad = S.messages.filter((message) => message.conversationId === id);
    const bufferedDeltas = S.loadingDeltas;
    S.loadingDeltas = [];
    S.current = data.conversation;
    if (switching) setSelectedProject(visibleProjectId(S.current, S.projects));
    S.messages = (data.messages || []).map(normalizeMessage);
    const received = new Set(S.messages.map((message) => message.id));
    duringLoad.forEach((message) => {
      if (!received.has(message.id)) S.messages.push(message);
      else if (message.status && message.status !== "streaming") {
        const index = S.messages.findIndex((item) => item.id === message.id);
        S.messages[index] = message;
      }
    });
    S.messages.sort((a, b) => (a.seq || 0) - (b.seq || 0));
    let streamGap = false;
    for (const delta of bufferedDeltas) {
      if (typeof delta.offsetBytes !== "number") continue;
      const message = S.messages.find((item) => item.id === delta.messageId);
      if (!message || applyStreamDelta(message, delta) === "gap") streamGap = true;
    }
    S.historyHasMore = !!data.historyHasMore;
    S.turns = data.turns || {};
    S.configs = data.configs || parseConfigs(data.conversation.configOptions);
    S.active = data.active && data.active.running ? data.active : null;
    S.currentAgent = data.agent || agentById(data.conversation.agentId);
    (data.approvals || []).forEach((a) => S.approvalsById.set(a.id, a));
    S.elicitations = data.elicitations || [];
    S.unread.delete(id);
    S.loadingConversation = false;
    try { localStorage.setItem("nusamation:lastConversation", id); } catch {}
    upsertConversation(data.conversation, false);
    if (push) setRoute(id);
    renderSidebar();
    renderTopbar();
    renderChat();
    renderComposer();
    showPendingRequest();
    closeMobileSidebar();
    if (!S.active) prepareComposer();
    if (streamGap) scheduleStreamResync(id);
    scheduleHistoryAutofill();
  } catch (e) {
    if (seq !== S.loadSeq) return;
    S.loadingConversation = false;
    S.loadingDeltas = [];
    if (e.status === 404) {
      S.conversations = S.conversations.filter((c) => c.id !== id);
      S.current = null;
      composers.delete(id);
      switchComposer("");
      S.messages = [];
      setRoute("");
      renderSidebar();
      renderChat();
      renderTopbar();
      renderComposer();
    } else {
      S.loadError = id;
      toast("Could not open conversation", e.message, "error");
      renderChat();
    }
  }
}

export async function loadOlderMessages() {
  if (!S.current || !S.historyHasMore || S.historyLoading || !S.messages.length) return;
  const id = S.current.id, seq = S.loadSeq, before = S.messages[0].seq;
  const sc = $("#chat-scroll");
  S.historyLoading = true;
  S.historyError = "";
  renderHistoryControl();
  try {
    const data = await api(`/conversations/${encodeURIComponent(id)}/messages?before=${encodeURIComponent(before)}&limit=100`);
    if (seq !== S.loadSeq || S.current?.id !== id) return;
    const oldHeight = sc.scrollHeight, oldTop = sc.scrollTop;
    const known = new Set(S.messages.map((m) => m.id));
    const older = (data.messages || []).map(normalizeMessage).filter((m) => !known.has(m.id));
    S.messages = older.concat(S.messages);
    Object.values(data.turns || {}).forEach(upsertTurn);
    S.historyHasMore = !!data.hasMore;
    S.historyLoading = false;
    S.scrollPinned = false;
    renderChat();
    sc.scrollTop = oldTop + (sc.scrollHeight - oldHeight);
    // The prepend re-anchors the viewport; that programmatic scroll must not
    // read as another near-top request. Mark the position so the handler can
    // ignore this one event.
    S.historyAdjustTop = sc.scrollTop;
    S.historyAdjustAt = Date.now();
    scheduleHistoryAutofill();
  } catch (e) {
    if (seq !== S.loadSeq || S.current?.id !== id) return;
    S.historyLoading = false;
    S.historyError = e.message;
    renderHistoryControl();
  }
}

export function renderHistoryControl() {
  const el = $("#history-control");
  if (!el) return;
  // Scrolling near the top lazy-loads older pages by itself, so there is no
  // idle button to render: the control only surfaces progress (skeleton) and
  // the one state the auto-load cannot recover from — a failed fetch, which
  // the scroll handler deliberately does not retry.
  el.innerHTML = S.historyLoading
    ? `<div class="history-skeleton" role="status" aria-label="Loading earlier messages"><span class="skeleton-line"></span><span class="skeleton-line short"></span></div>`
    : S.historyError ? `<button class="btn btn-secondary btn-sm" data-action="load-older">Retry loading earlier messages</button>`
    : S.historyStuck && S.historyHasMore && S.messages.length ? `<button class="btn btn-secondary btn-sm" data-action="load-older">Load earlier messages</button>` : "";
}

export function setRoute(id) {
  const path = id ? `/c/${encodeURIComponent(id)}` : "/";
  if (location.pathname !== path) window.history.pushState({ id }, "", path);
}

export function upsertConversation(conv, render = true) {
  if (!conv || !conv.id) return;
  // Full conversation records omit an empty projectId. A present createdAt marks
  // that shape so a chat moved out of a project leaves its old group.
  const projectId = "projectId" in conv ? (conv.projectId || "") : ("createdAt" in conv ? "" : undefined);
  const i = S.conversations.findIndex((c) => c.id === conv.id);
  const next = { ...(i >= 0 ? S.conversations[i] : {}), ...conv };
  if (projectId !== undefined) next.projectId = projectId;
  if (i >= 0) S.conversations[i] = next;
  else S.conversations.unshift(next);
  S.conversations.sort((a, b) => (Number(b.pinned) - Number(a.pinned)) || ((b.lastMessageAt || b.updatedAt || 0) - (a.lastMessageAt || a.updatedAt || 0)));
  if (S.current && S.current.id === conv.id) {
    const prevProjectId = S.current.projectId || "";
    S.current = { ...S.current, ...conv };
    if (projectId !== undefined) S.current.projectId = projectId;
    if ((S.current.projectId || "") !== prevProjectId) setSelectedProject(visibleProjectId(S.current, S.projects));
  }
  if (render) { renderSidebar(); renderTopbar(); }
}

export function normalizeMessage(m) {
  if (!m) return m;
  return {
    ...m,
    kind: m.kind === "assistant" ? "text" : m.kind,
    status: m.status === "completed" ? "complete" : m.status,
    text: m.text || "",
  };
}

export function upsertMessage(m) {
  if (!m || !m.id) return;
  if (!S.current || m.conversationId !== S.current.id) return;
  m = normalizeMessage(m);
  const i = S.messages.findIndex((x) => x.id === m.id);
  if (i >= 0) {
    const previous = S.messages[i];
    S.messages[i] = { ...previous, ...m, attachments: m.attachments || previous.attachments };
    delete S.messages[i]._streamBytes;
  } else {
    S.messages.push(m);
    S.messages.sort((a, b) => (a.seq || 0) - (b.seq || 0));
  }
  const conv = S.current;
  if (conv && m.text) {
    conv.lastMessage = m.text;
    conv.lastMessageAt = m.createdAt || now();
    upsertConversation(conv, false);
  }
}

export function touchConversationFromMessage(m) {
  if (!m.conversationId) return;
  const c = S.conversations.find((x) => x.id === m.conversationId);
  if (!c) return;
  if (!c.lastMessageAt || (m.createdAt || 0) >= (c.lastMessageAt || 0)) {
    c.lastMessage = m.text || c.lastMessage;
    c.lastMessageAt = m.createdAt || c.lastMessageAt || now();
  }
  scheduleSidebarRender();
}

export function openRoutingMenu(anchor) {
  if (!S.current) return;
  const r = S.current.routing || { replyToOrigin: true, mirrorFinal: false, notifyApprovals: true };
  const links = (S.current.links || []).filter((l) => l.channel !== "web");
  openPopover(anchor, `<div class="popover-title">Channel routing</div>
    <div class="popover-note">The web always receives the full stream. WhatsApp receives one message when the turn finishes.</div>
    <div class="settings-row"><div><div class="label">Reply to origin</div><div class="desc">Send assistant replies back to the chat where the turn started.</div></div><button role="switch" aria-checked="${r.replyToOrigin}" class="switch ${r.replyToOrigin ? "on" : ""}" data-action="toggle-routing" data-key="replyToOrigin"></button></div>
    <div class="settings-row"><div><div class="label">Mirror final answers</div><div class="desc">Copy the final answer to every linked chat.</div></div><button role="switch" aria-checked="${r.mirrorFinal}" class="switch ${r.mirrorFinal ? "on" : ""}" data-action="toggle-routing" data-key="mirrorFinal"></button></div>
    <div class="settings-row"><div><div class="label">Approval notifications</div><div class="desc">Ping the origin chat when a tool needs a decision.</div></div><button role="switch" aria-checked="${r.notifyApprovals}" class="switch ${r.notifyApprovals ? "on" : ""}" data-action="toggle-routing" data-key="notifyApprovals"></button></div>
    <div class="popover-note" style="margin-top:8px">${links.length ? "Linked: " + links.map((l) => esc(l.channel + (l.displayName ? " · " + l.displayName : ""))).join(", ") : "No external chats linked yet."}</div>`, { width: 340 });
}

export async function toggleRouting(key) {
  if (!S.current) return;
  const r = { ...(S.current.routing || {}) };
  r[key] = !r[key];
  try {
    const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/routing`, { method: "POST", body: r });
    S.current = d.conversation;
    const btn = document.querySelector(`[data-action="routing-menu"]`);
    if (btn) openRoutingMenu(btn);
    renderTopbar();
  } catch (e) {
    toast("Could not update routing", e.message, "error");
  }
}

export function openMoreMenu(anchor) {
  if (!S.current) return;
  const c = S.current;
  openPopover(anchor, `<div class="popover-list">
    ${menuItem(c.pinned ? "pin" : "pin", c.pinned ? "Unpin conversation" : "Pin conversation", "toggle-pin", `data-id="${esc(c.id)}"`)}
    ${menuItem("edit", "Rename", "rename-conv", `data-id="${esc(c.id)}"`)}
    ${menuItem("zap", `Set label${c.label ? ` (@${c.label})` : ""}`, "set-conv-label", `data-id="${esc(c.id)}"`)}
    ${menuItem("clock", "Control activity", "orchestration-activity", `data-id="${esc(c.id)}"`)}
    ${menuItem("git", "Team", "team-panel", `data-id="${esc(c.id)}"`)}
    ${menuItem("download", "Export JSON", "export-conv", `data-id="${esc(c.id)}"`)}
    ${menuItem("file", c.archived ? "Unarchive" : "Archive", "archive-conv", `data-id="${esc(c.id)}"`)}
    <div class="popover-sep"></div>
    ${menuItem("trash", "Delete conversation", "delete-conv", `data-id="${esc(c.id)}"`, true)}
  </div>`, { width: 230 });
}

export function openTeamPanel(id) {
  const parent = S.conversations.find((x) => x.id === id) || S.current;
  if (!parent) return;
  const children = S.conversations.filter((c) => c.metadata && typeof c.metadata === "object" && c.metadata.spawnedBy === parent.id);
  const rows = children.map((c) => `<button class="menu-item" data-action="team-open" data-id="${esc(c.id)}">${icon("link", "sm")}<span>${esc(c.title || "Untitled")} · ${esc(c.status || "idle")}</span></button>`).join("");
  openModal(`${modalHead("Team", parent.title || "Conversation")}<div class="modal-body" style="padding:16px">${rows || `<div class="settings-desc">No delegated chats yet.</div>`}</div>`, { size: "sm" });
}

export async function openControlActivity(id) {
  const c = S.conversations.find((x) => x.id === id) || S.current;
  if (!c) return;
  try {
    const d = await api(`/conversation-control/activity?caller=${encodeURIComponent(c.id)}&limit=50`);
    const rows = (d.activity || []).map((e) => `<div class="settings-row"><div><div class="label">${esc(e.tool)} · ${esc(e.outcome)}</div><div class="desc">${esc(e.target || "no target")} · ${esc(String(e.durationMs || 0))} ms</div></div><span class="conv-time">${clock(e.at)}</span></div>`).join("");
    openModal(`${modalHead("Control activity", c.title || "Conversation")}<div class="modal-body" style="padding:16px;max-height:60vh;overflow:auto">${rows || `<div class="settings-desc">No control calls recorded yet.</div>`}</div>`, { size: "sm" });
  } catch (e) {
    toast("Could not load control activity", e.message, "error");
  }
}

export async function togglePin(id) {
  const c = S.conversations.find((x) => x.id === id);
  if (!c) return;
  try {
    const d = await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { pinned: !c.pinned } });
    upsertConversation(d.conversation);
    closePopover();
  } catch (e) { toast("Could not update pin", e.message, "error"); }
}

export async function archiveConversation(id) {
  try {
    await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { archived: true } });
    S.conversations = S.conversations.filter((c) => c.id !== id);
    if (S.current && S.current.id === id) { S.current = null; switchComposer(""); S.messages = []; setRoute(""); renderChat(); renderTopbar(); renderComposer(); }
    closePopover(); renderSidebar();
  } catch (e) { toast("Could not archive", e.message, "error"); }
}

export async function deleteConversation(id) {
  confirmModal("Delete conversation?", "This removes the transcript, approvals and channel links from NusaMation. It cannot be undone.", async () => {
    try {
      await api(`/conversations/${encodeURIComponent(id)}`, { method: "DELETE" });
      S.conversations = S.conversations.filter((c) => c.id !== id);
      composers.delete(id);
      if (S.current && S.current.id === id) { S.current = null; switchComposer(""); S.messages = []; setRoute(""); renderChat(); renderTopbar(); renderComposer(); }
      renderSidebar(); closeModal(); toast("Conversation deleted", "", "success");
    } catch (e) { toast("Could not delete", e.message, "error"); }
  }, "Delete");
}

export function exportConversation(id) {
  window.open(`/api/conversations/${encodeURIComponent(id)}/export`, "_blank", "noopener");
  closePopover();
}

export function renameConversation(id) {
  const c = S.conversations.find((x) => x.id === id);
  if (!c) return;
  closePopover();
  promptModal("Rename conversation", "Title", c.title, async (value) => {
    if (!value.trim()) return;
    try {
      const d = await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { title: value.trim() } });
      upsertConversation(d.conversation);
      saveTitle(id, value.trim());
      closeModal();
    } catch (e) { toast("Could not rename", e.message, "error"); }
  });
}

// setLabelConversation renames the room's @mention handle — the short name
// the composer autocomplete offers and other rooms resolve through the
// conversation-control tools.
export function setLabelConversation(id) {
  const c = S.conversations.find((x) => x.id === id);
  if (!c) return;
  closePopover();
  promptModal("Set mention label", "Label (lowercase letters, digits, hyphens)", c.label || "", async (value) => {
    const label = value.trim().toLowerCase();
    if (!label) return;
    try {
      const d = await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { label } });
      upsertConversation(d.conversation);
      if (S.current && S.current.id === id) S.current = d.conversation;
      renderSidebar();
      closeModal();
      toast("Label saved", `Mention this room with @${d.conversation.label}`, "success");
    } catch (e) { toast("Could not set label", e.message, "error"); }
  }, "e.g. cat");
}

/* ---------------- actions ---------------- */
export function newProjectChat(id) {
  if (!id || !(S.projects || []).some((p) => p.id === id)) return;
  if (!S.expandedProjects) S.expandedProjects = new Set();
  S.expandedProjects.add(id);
  setSelectedProject(id);
  newConversation();
}

export async function newConversation() {
  if (S.selectedProjectId && !(S.projects || []).some((p) => p.id === S.selectedProjectId)) setSelectedProject("");
  closeSettings();
  ++S.loadSeq;
  ++S.draftPreviewSeq;
  S.prepareController?.abort();
  S.preparing = "";
  S.current = null;
  S.currentAgent = null;
  S.messages = [];
  S.turns = {};
  S.active = null;
  S.configs = [];
  S.draftSelections = [];
  S.welcomeAgent = (agentById(S.agentDefaults.agentId)?.enabled ? S.agentDefaults.agentId : "") || S.agents.find((a) => a.enabled)?.id || "";
  switchComposer("");
  S.scrollPinned = true;
  S.loadingConversation = false;
  setRoute("");
  renderSidebar(); renderTopbar(); renderChat(); renderComposer();
  closeMobileSidebar();
  focusComposer();
}

export function openConversationMenu(anchor, id) {
  const c = S.conversations.find((x) => x.id === id);
  if (!c) return;
  openPopover(anchor, `<div class="popover-list">
    ${menuItem("pin", c.pinned ? "Unpin conversation" : "Pin conversation", "toggle-pin", `data-id="${esc(c.id)}"`)}
    ${menuItem("edit", "Rename", "rename-conv", `data-id="${esc(c.id)}"`)}
    ${menuItem("zap", `Set label${c.label ? ` (@${c.label})` : ""}`, "set-conv-label", `data-id="${esc(c.id)}"`)}
    ${menuItem("clock", "Control activity", "orchestration-activity", `data-id="${esc(c.id)}"`)}
    ${menuItem("git", "Team", "team-panel", `data-id="${esc(c.id)}"`)}
    ${menuItem("download", "Export JSON", "export-conv", `data-id="${esc(c.id)}"`)}
    ${menuItem("file", "Archive", "archive-conv", `data-id="${esc(c.id)}"`)}
    <div class="popover-sep"></div>
    ${menuItem("trash", "Delete", "delete-conv", `data-id="${esc(c.id)}"`, true)}
  </div>`, { width: 220 });
}
