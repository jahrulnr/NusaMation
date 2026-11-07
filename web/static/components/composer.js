import { $, $$, agentById, agentName, agentOrb, configChoices, configLabel, currentAgent, esc, icon, isMobile, isShort, parseConfigs } from "./util.js?v=33";
import { S, composerFor, composerKey, moveComposer } from "./state.js?v=33";
import { api } from "./api.js?v=33";
import { toast } from "./toast.js?v=33";
import { closeModal, promptModal } from "./modal.js?v=33";
import { closePopover, menuItem, openPopover } from "./popover.js?v=33";
import { renderChat } from "./chat.js?v=33";
import { showAgentBlocker, showSlowAgent } from "./requests.js?v=33";
import { loadConversation, normalizeMessage, setRoute, upsertConversation, upsertMessage } from "./conversations.js?v=33";
import { clearDraft, saveDraftNow, scheduleDraftSave } from "./convmeta.js?v=33";
import { renderTopbar } from "./topbar.js?v=33";
import { openSettings } from "./settings.js?v=33";

/* ---------------- composer ---------------- */
export function renderComposer() {
  const wrap = $("#composer-wrap");
  if (!wrap) return;
  const popover = $(".composer-popover");
  const oldAnchor = $("[data-popover-anchor]", wrap);
  const anchorAction = oldAnchor?.dataset.action;
  const anchorId = oldAnchor?.dataset.id;
  const menuOpen = !!$(".send-menu-popover");
  const menuFocus = document.activeElement?.dataset?.mode || "";
  if (menuOpen) closePopover();
  const oldTa = $("#prompt");
  const wasFocused = !!oldTa && oldTa === document.activeElement;
  const selStart = oldTa ? oldTa.selectionStart : 0;
  const selEnd = oldTa ? oldTa.selectionEnd : 0;
  const agent = currentAgent();
  const conv = S.current;
  const links = (conv && conv.links || []).filter((l) => l.channel !== "web");
  const active = S.active && ["running", "waiting_approval", "cancelling"].includes(S.active.status);
  const mobileSummary = mobileConfigSummary();
  const markup = `<div class="composer-inner">
    <div class="composer" id="composer">
      <div class="composer-attachments" id="pending-attachments"></div>
      <textarea id="prompt" rows="1" placeholder="Message ${esc(agentName(agent))}…" aria-label="Message">${esc(S.draft)}</textarea>
      <div class="composer-bar">
        <button class="icon-btn sm" data-action="attach" title="Attach files">${icon("paperclip", "sm")}</button>
        <button class="composer-choice agent-choice" data-action="agent-menu" title="Choose agent">${agentOrb(agent)}<span>${esc(agentName(agent))}</span>${icon("chevron-down", "sm")}</button>
        <div class="composer-configs">${composerConfigs()}</div>
        ${mobileSummary ? `<span class="composer-mobile-summary" title="${esc(mobileSummary)}">${esc(mobileSummary)}</span>` : ""}
        <button class="composer-mobile-settings" data-action="${S.configs.length ? "composer-mobile-settings" : "prepare-composer"}" aria-label="Agent options" title="Agent options">${S.preparing === (conv?.id || "draft") ? `<span class="spinner"></span>` : "+"}</button>
        ${S.elicitations.some((e) => e.conversationId === conv?.id) ? `<button class="composer-choice" data-action="show-agent-request">Respond</button>` : ""}
        <span class="grow"></span>
        ${links.length ? `<button class="reply-toggle ${S.replyToChannel ? "on" : ""}" data-action="reply-toggle" aria-pressed="${S.replyToChannel}" aria-label="Also send the final answer to this chat" title="Also send the final answer to this chat">${icon("message", "sm")}<span class="reply-label">${S.replyToChannel ? "Reply to " + esc(links[0].channel) : "Reply to chat"}</span><span class="reply-short">${S.replyToChannel ? esc(links[0].channel) : "Chat"}</span></button>` : ""}
        ${active ? `<button class="stop-btn" data-action="stop" aria-label="Stop" title="Stop">${icon("stop", "sm")}</button>` : ""}
        <div class="send-split">
          <button class="send-btn" id="send-btn" data-action="send" title="${sendTitle()}" aria-label="${sendTitle()}">${icon("send")}</button>
          ${isBusyTurn() ? `<button class="send-menu-btn" id="send-menu-btn" data-action="send-menu" aria-haspopup="menu" aria-expanded="false" aria-label="Send options" title="Send options"${S.sending ? " disabled" : ""}>${icon("chevron-down", "sm")}</button>` : ""}
        </div>
      </div>
    </div>
    <div class="composer-foot"><span>${icon("shield", "sm")} Approvals stay in your hands</span><span>${icon("brain", "sm")} Full thoughts on web</span></div>
    <input type="file" id="file-input" multiple hidden>
    <div class="drop-overlay" id="drop-overlay">Drop files to attach</div>
  </div>`;
  const next = document.createElement("div");
  next.innerHTML = markup;
  const existing = $(".composer-inner", wrap);
  if (existing && oldTa) {
    $("#pending-attachments", existing).replaceWith($("#pending-attachments", next));
    $(".composer-bar", existing).replaceWith($(".composer-bar", next));
    $(".composer-foot", existing).replaceWith($(".composer-foot", next));
    if (oldTa.dataset.composerKey !== composerKey) oldTa.value = S.draft;
  } else {
    wrap.innerHTML = markup;
  }
  const ta = $("#prompt");
  ta.dataset.composerKey = composerKey;
  if (!oldTa || !existing) {
    ta.addEventListener("input", () => { S.draft = ta.value; autoGrow(); scheduleDraftSave(); });
    ta.addEventListener("blur", () => { saveDraftNow(); });
    ta.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey) && !isMobile() && !e.isComposing) {
        e.preventDefault();
        sendMessage();
      }
    });
    ta.addEventListener("paste", handlePaste);
  }
  if (wasFocused && ta !== oldTa) {
    ta.focus();
    try { ta.setSelectionRange(selStart, selEnd); } catch {}
  }
  autoGrow();
  $("#file-input").onchange = (e) => { uploadFiles(e.target.files); e.target.value = ""; };
  wrap.onpointerdown = (e) => {
    if (isMobile() && document.activeElement === ta && e.target.closest('[data-action="prepare-composer"]')) e.preventDefault();
  };
  wrap.ondragover = (e) => { e.preventDefault(); $("#drop-overlay")?.classList.add("show"); };
  wrap.ondragleave = (e) => { if (!wrap.contains(e.relatedTarget)) $("#drop-overlay")?.classList.remove("show"); };
  wrap.ondrop = (e) => {
    e.preventDefault();
    $("#drop-overlay")?.classList.remove("show");
    uploadFiles(e.dataTransfer.files);
  };
  renderPendingAttachments();
  updateComposerState();
  if (menuOpen) reopenSendMenu(menuFocus);
  else if (popover && oldAnchor) {
    const replacement = $$('[data-action]', wrap).find((el) => el.dataset.action === anchorAction && el.dataset.id === anchorId);
    if (replacement) popover._setAnchor(replacement);
    else closePopover();
  }
}

export function composerConfigs() {
  if (S.preparing === (S.current?.id || "draft")) return `<span class="composer-loading"><span class="spinner"></span> Connecting…</span>`;
  const opts = S.configs.filter((o) => o && o.id && (o.type === "select" || o.type === "boolean"));
  if (!opts.length) return `<button class="composer-choice" data-action="prepare-composer">Load options</button>`;
  const modes = opts.filter((o) => String(o.category || "").toLowerCase() === "mode");
  const rest = opts.filter((o) => !modes.includes(o));
  const direct = modes.map((o) => `<button class="composer-choice" data-action="composer-config" data-id="${esc(o.id)}" title="${esc(o.name || o.id)}"><span>${esc(configLabel(o))}</span>${icon("chevron-down", "sm")}</button>`).join("");
  if (!rest.length) return direct;
  const model = rest.find((o) => String(o.category || "").toLowerCase() === "model");
  const effort = rest.find((o) => ["thought_level", "reasoning_effort"].includes(String(o.category || "").toLowerCase()));
  const label = model ? configLabel(model) + (effort ? " · " + configLabel(effort) : "") : "Options";
  return direct + `<button class="composer-choice composer-settings" data-action="composer-settings" title="Model and agent options"><span>${esc(label)}</span>${icon("chevron-down", "sm")}</button>`;
}

export function mobileConfigSummary() {
  const model = S.configs.find((o) => String(o?.category || "").toLowerCase() === "model");
  const effort = S.configs.find((o) => ["thought_level", "reasoning_effort"].includes(String(o?.category || "").toLowerCase()));
  return model ? [configLabel(model), effort && configLabel(effort)].filter(Boolean).join(" · ") : "";
}

export function autoGrow() {
  const ta = $("#prompt");
  if (!ta) return;
  ta.style.height = "auto";
  const viewportHeight = Math.min(window.innerHeight, window.visualViewport?.height || window.innerHeight);
  const limit = isShort() ? Math.max(44, Math.floor(viewportHeight * 0.25)) : isMobile() ? Math.min(220, Math.max(60, Math.floor(viewportHeight * 0.28))) : 220;
  ta.style.height = Math.min(limit, ta.scrollHeight) + "px";
  updateComposerState();
}

export function updateComposerState() {
  const ta = $("#prompt");
  const btn = $("#send-btn");
  if (!ta || !btn) return;
  const has = ta.value.trim().length > 0 || S.attachments.length > 0;
  btn.disabled = !has || S.sending || (!S.current && S.preparing === "draft");
  btn.innerHTML = S.sending ? `<span class="spinner"></span>` : icon("send");
  btn.title = sendTitle();
  btn.setAttribute("aria-label", sendTitle());
  const menuBtn = $("#send-menu-btn");
  if (menuBtn) menuBtn.disabled = S.sending;
  const agent = currentAgent();
  ta.placeholder = isMobile() ? "Message…" : `Message ${agentName(agent)}…`;
}

export const TURN_RANK = { queued: 0, running: 1, waiting_approval: 1, cancelling: 1 };

// A stale queued snapshot never replaces a started or finished turn; later
// terminal states replace earlier ones.
export function upsertTurn(turn) {
  if (!turn || !turn.id) return;
  const rank = (s) => (s in TURN_RANK ? TURN_RANK[s] : 2);
  const known = S.turns[turn.id];
  if (known && rank(turn.status) < rank(known.status)) return;
  S.turns[turn.id] = turn;
}

export function isBusyTurn() {
  return !!S.active && !!S.active.running && ["running", "waiting_approval"].includes(S.active.status);
}

export function sendTitle() {
  if (!isBusyTurn()) return "Send";
  if (S.sendDelivery === "after_tools") return "Send after tools";
  if (S.sendDelivery === "queue") return "Add to queue";
  return "Steer";
}

export function openSendMenu(anchor) {
  if (!isBusyTurn()) return;
  if ($(".send-menu-popover")) { closePopover(); return; }
  const choice = (mode, label, help) => `<button class="menu-item" role="menuitemradio" aria-checked="${S.sendDelivery === mode}" data-action="send-mode" data-mode="${mode}"><span class="popover-main"><b>${label}</b><small>${help}</small></span>${S.sendDelivery === mode ? icon("check", "sm") : ""}</button>`;
  openPopover(anchor, `<div class="popover-list send-menu" role="menu" aria-label="Send mode">${choice("steer", "Steer", "The agent sees this while it is working")}${choice("queue", "Add to queue", "Runs after the current turn finishes")}${choice("after_tools", "Send after tools", "Finish current tools, cancel, then send")}</div>`, { width: 280, class: "composer-popover send-menu-popover" });
  anchor.setAttribute("aria-expanded", "true");
  const el = $(".send-menu-popover");
  el.addEventListener("keydown", (e) => sendMenuKeys(e, anchor, el));
  el.querySelector('[aria-checked="true"]')?.focus();
}

export function reopenSendMenu(focusMode) {
  const anchor = $("#send-menu-btn");
  if (!anchor) return;
  openSendMenu(anchor);
  if (focusMode) $(`.send-menu-popover [data-mode="${focusMode}"]`)?.focus();
  else anchor.focus();
}

export function sendMenuKeys(e, anchor, el) {
  const items = $$("[role=menuitemradio]", el);
  const i = items.indexOf(document.activeElement);
  const go = (n) => { e.preventDefault(); items[n]?.focus(); };
  if (e.key === "ArrowDown") go((i + 1) % items.length);
  else if (e.key === "ArrowUp") go((i - 1 + items.length) % items.length);
  else if (e.key === "Home") go(0);
  else if (e.key === "End") go(items.length - 1);
  else if (e.key === "Escape") { e.preventDefault(); closePopover(); anchor.focus(); }
}

export function setSendMode(mode) {
  S.sendDelivery = mode;
  closePopover();
  updateComposerState();
  scheduleDraftSave();
  $("#send-menu-btn")?.focus();
}

export function queuedTurnFor(m) {
  return Object.values(S.turns || {}).find((t) => t.userMessageId === m.id);
}

export function queueStatusLabel(m) {
  const turn = queuedTurnFor(m);
  if (!turn || turn.status !== "queued") return "";
  const target = (m.metadata || {}).interruptTurnId;
  if (target && S.active && S.active.turnId === target) {
    return S.active.status === "cancelling" ? "Waiting for agent to stop" : "Waiting for tools";
  }
  return "Queued · waiting for the agent";
}

export function renderPendingAttachments() {
  const el = $("#pending-attachments");
  if (!el) return;
  el.innerHTML = S.attachments.map((a) => {
    if (a.type === "annotation") {
      const u = a.upload || {};
      const label = `${a.tagName || a.label || "element"}${a.comment ? ": " + a.comment : ""}`;
      const title = [a.label, a.comment, a.selector, a.shot === false ? "no screenshot" : ""].filter(Boolean).join(" · ");
      return `<div class="pending-attach annot" title="${esc(title || label)}">
        ${u.id ? `<span class="thumb"><img src="/api/uploads/${encodeURIComponent(u.id)}" alt="">${a.marker ? `<span class="n">${esc(a.marker)}</span>` : ""}</span>` : icon("pin", "sm")}
        <span class="name">${esc(label)}</span>
        <button data-action="annotation-edit" data-id="${esc(a.pickId)}" aria-label="Edit comment" title="Edit comment">${icon("edit", "sm")}</button>
        <button data-action="remove-attachment" data-id="${esc(u.id || a.pickId)}" aria-label="Remove">${icon("x", "sm")}</button>
      </div>`;
    }
    const u = a.upload || a;
    const isImg = String(u.mime || "").startsWith("image/");
    return `<div class="pending-attach" title="${esc(u.name)}">
      ${isImg ? `<img src="/api/uploads/${encodeURIComponent(u.id)}" alt="">` : icon("file", "sm")}
      <span class="name">${esc(u.name)}</span>
      <button data-action="remove-attachment" data-id="${esc(u.id)}" aria-label="Remove">${icon("x", "sm")}</button>
    </div>`;
  }).join("");
  updateComposerState();
}

export function editAnnotationComment(pickId) {
  const chip = S.attachments.find((a) => a.type === "annotation" && a.pickId === pickId);
  if (!chip) return;
  promptModal("Edit annotation comment", "Comment", chip.comment || "", async (value) => {
    chip.comment = (value || "").trim();
    renderPendingAttachments();
    scheduleDraftSave();
    closeModal();
  }, "Comment for the agent…", true);
}

export function focusComposer() {
  setTimeout(() => $("#prompt")?.focus(), 30);
}

export function handlePaste(e) {
  const items = Array.from(e.clipboardData?.items || []);
  const files = items.filter((i) => i.kind === "file").map((i) => i.getAsFile()).filter(Boolean);
  if (files.length) {
    e.preventDefault();
    uploadFiles(files);
  }
}

export async function uploadFiles(files) {
  if (!files || !files.length) return;
  const owner = composerFor(composerKey);
  const conversationID = S.current?.id;
  for (const file of Array.from(files).slice(0, 8)) {
    if (file.size > 25 << 20) { toast("File is too large", `${file.name} exceeds 25 MB.`, "error"); continue; }
    const fd = new FormData();
    fd.append("file", file);
    if (conversationID) fd.append("conversationId", conversationID);
    try {
      const d = await api("/uploads", { method: "POST", body: fd });
      owner.attachments.push({ upload: d.upload });
      if (composerFor(composerKey) === owner) renderPendingAttachments();
      scheduleDraftSave(composerKey);
    } catch (e) {
      toast("Upload failed", e.message, "error");
    }
  }
}

export async function sendMessage() {
  if (S.sending || (!S.current && S.preparing === "draft")) return;
  const ta = $("#prompt");
  if (!ta) return;
  const text = ta.value.trim();
  if (!text && !S.attachments.length) return;
  const owner = composerFor(composerKey);
  const sourceKey = composerKey;
  let conversation = S.current;
  const delivery = isBusyTurn() ? owner.sendDelivery : "queue";
  const attachments = owner.attachments.slice();
  owner.sending = true;
  updateComposerState();
  try {
    if (!conversation) {
      const ag = agentById(S.welcomeAgent) || S.agents.find((a) => a.enabled);
      if (!ag) { openSettings("agents"); throw new Error("Add an agent before sending a message."); }
      owner.pendingSelections = S.draftSelections.slice();
      const d = await api("/conversations", { method: "POST", body: { agentId: ag.id } });
      conversation = d.conversation;
      moveComposer(sourceKey, conversation.id);
      upsertConversation(d.conversation);
      if (composerFor(composerKey) === owner) {
        ++S.loadSeq;
        S.loadingConversation = false;
        S.current = conversation;
        S.messages = [];
        S.configs = [];
        S.currentAgent = ag;
        S.active = null;
        setRoute(conversation.id);
      }
    }
    if (owner.pendingSelections?.length) {
      await api(`/conversations/${encodeURIComponent(conversation.id)}/prepare`, { method: "POST", body: {} });
      for (const choice of owner.pendingSelections) {
        await api(`/conversations/${encodeURIComponent(conversation.id)}/config`, { method: "POST", body: { configId: choice.id, value: choice.value } });
      }
      owner.pendingSelections = [];
    }
    const links = (conversation.links || []).filter((l) => l.channel !== "web");
    // Annotation chips are NOT uploads: they ride as annotations[{pickId,comment,
    // screenshot}] and the server binds each pick's marked upload itself (contract §3.9/§6.3).
    const files = attachments.filter((a) => a.type !== "annotation");
    const annotations = attachments.filter((a) => a.type === "annotation");
    const body = {
      text,
      attachments: files.map((a) => ({ uploadId: (a.upload || a).id })),
      replyToChannel: !!(owner.replyToChannel && links.length),
      replyChannel: links.length ? links[0].channel : "",
      delivery,
    };
    if (annotations.length) body.annotations = annotations.map((a) => ({ pickId: a.pickId, comment: a.comment || "", screenshot: a.shot !== false }));
    const d = await api(`/conversations/${encodeURIComponent(conversation.id)}/messages`, { method: "POST", body });
    owner.sending = false;
    if (owner.draft.trim() === text) { owner.draft = ""; if (composerFor(composerKey) === owner) $("#prompt").value = ""; }
    owner.attachments = owner.attachments.filter((a) => !attachments.includes(a));
    // Clear only the fields that actually went out; leftover text/attachments
    // (edited mid-send or added while sending) stay persisted as the draft.
    clearDraft(conversation.id);
    if (owner.draft || owner.attachments.length) scheduleDraftSave(conversation.id);
    if (composerFor(composerKey) !== owner || S.current?.id !== conversation.id) return;
    renderPendingAttachments();
    upsertTurn(d.turn);
    upsertMessage(normalizeMessage({ ...d.message }));
    if (!S.active || !S.active.running) S.active = { status: "queued", running: false, turnId: d.turn.id };
    S.scrollPinned = true;
    renderChat();
    renderTopbar();
    renderComposer();
    focusComposer();
  } catch (e) {
    owner.sending = false;
    if (composerFor(composerKey) === owner) updateComposerState();
    toast("Could not send", e.message, "error");
  }
}

export async function stopTurn() {
  if (!S.current) return;
  try {
    await api(`/conversations/${encodeURIComponent(S.current.id)}/cancel`, { method: "POST" });
    if (S.active) S.active.status = "cancelling";
    renderTopbar();
    renderComposer();
    toast("Stopping", "The agent is being asked to stop.", "info");
  } catch (e) {
    toast("Nothing to stop", e.message, "error");
  }
}

export async function retryTurn() {
  if (!S.current) return;
  try {
    const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/retry`, { method: "POST" });
    S.active = { status: "queued", running: false, turnId: d.turn.id };
    renderChat(); renderTopbar(); renderComposer();
  } catch (e) {
    toast("Could not retry", e.message, "error");
  }
}

export function openAgentMenu(anchor) {
  const current = S.current ? currentAgent() : (agentById(S.welcomeAgent) || currentAgent());
  const agents = S.agents.filter((a) => a.enabled);
  openPopover(anchor, `<div class="popover-title">Agent</div>
    <div class="popover-list">
      ${agents.map((a) => `<button class="menu-item ${current && a.id === current.id ? "selected" : ""}" data-action="pick-agent" data-id="${esc(a.id)}">${agentOrb(a)}<span class="popover-main"><b>${esc(a.name)}</b><small>${esc(a.description || a.slug)}</small></span>${current && a.id === current.id ? icon("check", "sm") : ""}</button>`).join("") || `<div class="popover-empty">No enabled agents.</div>`}
    </div>
    <div class="popover-foot">${menuItem("settings", "Manage agents", "open-settings", 'data-tab="agents"')}</div>`, { width: 290 });
}

export async function openConfigMenu(anchor) {
  if (!S.current) { toast("Start a conversation first", "Session options appear once a conversation exists.", "info"); return; }
  if (!S.configs.length) {
    try {
      const d = await api(`/conversations/${encodeURIComponent(S.current.id)}`);
      S.current = d.conversation;
      S.configs = d.configs || parseConfigs(d.conversation.configOptions);
      if (d.agent) S.currentAgent = d.agent;
    } catch {}
  }
  const opts = S.configs;
  const usage = S.current.usage || {};
  openPopover(anchor, `<div class="popover-title">Session configuration <span class="muted" style="font-weight:400;font-size:10px">live</span></div>
    ${opts.length ? `<div class="config-list">${opts.map((o) => configControl(o)).join("")}</div>` : `<div class="popover-empty">No session options yet.<br>Send a message once; the agent will publish its model and mode.</div>`}
    <div class="config-usage">${usage.totalTokens ? `${usage.totalTokens.toLocaleString()} tokens` : ""}${usage.inputTokens ? ` · ↑${usage.inputTokens.toLocaleString()}` : ""}${usage.outputTokens ? ` · ↓${usage.outputTokens.toLocaleString()}` : ""}${usage.costAmount ? ` · $${usage.costAmount}` : ""}</div>`, { width: 330 });
}

export function configControl(o) {
  const id = esc(o.id);
  const label = esc(o.name || o.id);
  const current = o.currentValue;
  const type = String(o.type || "").toLowerCase();
  if (type === "boolean") {
    const on = current === true || current === "true";
    return `<div class="config-row"><div class="config-label">${label}<small>${esc(o.description || "")}</small></div><button role="switch" aria-checked="${on}" class="switch ${on ? "on" : ""}" data-action="set-config-toggle" data-id="${id}" data-value="${on ? "false" : "true"}"></button></div>`;
  }
  return `<div class="config-row"><div class="config-label">${label}<small>${esc(o.description || "")}</small></div>
    <select class="select config-select" data-change="set-config" data-id="${id}">
      ${configChoices(o).map((c) => `<option value="${esc(c.value)}" ${String(c.value) === String(current) ? "selected" : ""}>${esc(c.groupName ? c.groupName + " · " + (c.name || c.value) : (c.name || c.value))}</option>`).join("") || `<option value="${esc(current)}">${esc(current)}</option>`}
    </select></div>`;
}

export function openComposerSettings(anchor, includeMode = false) {
  const opts = S.configs.filter((o) => o && o.id && ["select", "boolean"].includes(o.type) && (includeMode || String(o.category || "").toLowerCase() !== "mode"));
  const links = (S.current?.links || []).filter((l) => l.channel !== "web");
  const compact = isShort() ? [
    links.length ? menuItem(S.replyToChannel ? "check" : "message", S.replyToChannel ? "Stop replying to chat" : "Reply to chat", "reply-toggle") : "",
    S.elicitations.some((e) => e.conversationId === S.current?.id) ? menuItem("info", "Respond", "show-agent-request") : "",
  ].join("") : "";
  openPopover(anchor, `<div class="popover-title">Agent options</div><div class="popover-list composer-options">${compact}${opts.map((o) => `<button class="menu-item" data-action="composer-subconfig" data-id="${esc(o.id)}"><span class="popover-main"><b>${esc(o.name || o.id)}</b>${o.description ? `<small>${esc(o.description)}</small>` : ""}</span><span class="composer-option-value">${esc(configLabel(o))}</span>${icon("chevron-right", "sm")}</button>`).join("")}</div>`, { width: 300, class: "composer-popover" });
}

export function openComposerConfig(anchor, id, nested = false) {
  const opt = S.configs.find((o) => o.id === id);
  if (!opt) return;
  const options = opt.type === "boolean" ? [{ value: "true", name: "On" }, { value: "false", name: "Off" }] : configChoices(opt);
  openPopover(anchor, `${nested ? `<button class="menu-item" data-action="composer-settings-back">‹ &nbsp; Agent options</button>` : ""}<div class="popover-title">${esc(opt.name || opt.id)}</div>
    ${options.length > 8 ? `<input class="input composer-search" placeholder="Search ${esc(opt.name || "options")}" aria-label="Search options">` : ""}
    <div class="popover-list composer-options">${options.map((c) => `<button class="menu-item ${String(c.value) === String(opt.currentValue) ? "selected" : ""}" data-action="pick-config" data-id="${esc(opt.id)}" data-value="${esc(c.value)}" data-type="${esc(opt.type)}"><span class="popover-main"><b>${esc(c.name || c.value)}</b>${c.description || c.groupName ? `<small>${esc([c.groupName, c.description].filter(Boolean).join(" · "))}</small>` : ""}</span>${String(c.value) === String(opt.currentValue) ? icon("check", "sm") : ""}</button>`).join("") || `<div class="popover-empty">No choices published by this agent.</div>`}</div>`, { width: 300, class: "composer-popover" });
  const search = $(".composer-search");
  if (search) {
    search.addEventListener("input", () => $$(".composer-options .menu-item").forEach((item) => { item.hidden = !item.textContent.toLowerCase().includes(search.value.toLowerCase()); }));
    if (!isMobile()) search.focus();
  }
}

export async function prepareComposer() {
  if (!S.current) {
    await previewDraft();
    return;
  }
  const id = S.current.id;
  const agentId = S.current.agentId;
  if (S.preparing === id) return;
  S.preparing = id;
  const controller = new AbortController();
  S.prepareController = controller;
  const slowTimer = setTimeout(() => { if (S.preparing === id && S.current?.id === id && !S.modal) showSlowAgent(true); }, 30000);
  renderComposer();
  try {
    const d = await api(`/conversations/${encodeURIComponent(id)}/prepare`, { method: "POST", body: {}, signal: controller.signal });
    if (S.current?.id !== id || S.current?.agentId !== agentId) return;
    S.current = d.conversation;
    S.configs = d.configs || [];
    upsertConversation(d.conversation, false);
  } catch (e) {
    if (!controller.signal.aborted && S.current?.id === id && S.current?.agentId === agentId) showAgentBlocker(e);
  } finally {
    clearTimeout(slowTimer);
    if (S.modal?.type === "agent-slow" && S.modal.conversationId === id && S.modal.connecting) closeModal();
    if (S.prepareController === controller) S.prepareController = null;
    if (S.preparing === id) S.preparing = "";
    if (S.current?.id === id) { renderTopbar(); renderComposer(); }
  }
}

export async function previewDraft() {
  const agentId = S.welcomeAgent;
  if (!agentId) return false;
  const seq = ++S.draftPreviewSeq;
  S.preparing = "draft";
  renderComposer();
  try {
    const chosenIds = new Set(S.draftSelections.map((choice) => choice.id));
    const defaults = agentId === S.agentDefaults.agentId ? (S.agentDefaults.selections || []).filter((choice) => !chosenIds.has(choice.id)) : [];
    const d = await api("/settings/agent-defaults/preview", { method: "POST", body: { agentId, selections: [...defaults, ...S.draftSelections] } });
    if (seq !== S.draftPreviewSeq || S.current || S.welcomeAgent !== agentId) return false;
    S.configs = d.configs || [];
    return true;
  } catch (e) {
    if (seq === S.draftPreviewSeq && !S.current) showAgentBlocker(e);
    return false;
  } finally {
    if (seq === S.draftPreviewSeq) {
      S.preparing = "";
      if (!S.current) renderComposer();
    }
  }
}

export async function setConfig(id, value) {
  if (!S.current) {
    const previous = S.draftSelections.slice();
    S.draftSelections = previous.filter((choice) => choice.id !== id).concat({ id, value });
    closePopover();
    const seq = S.draftPreviewSeq + 1;
    if (!await previewDraft() && seq === S.draftPreviewSeq && !S.current) S.draftSelections = previous;
    return;
  }
  try {
    const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/config`, { method: "POST", body: { configId: id, value } });
    S.current = d.conversation;
    S.configs = d.configs || parseConfigs(d.conversation.configOptions);
    upsertConversation(d.conversation, false);
    renderTopbar();
    renderComposer();
    closePopover();
  } catch (e) {
    toast("Could not change setting", e.message, "error");
  }
}

export async function pickAgent(id) {
  const ag = agentById(id);
  if (!ag) return;
  if (S.current && S.preparing === S.current.id) { toast("Connecting", "Wait for the agent to finish connecting.", "info"); return; }
  S.welcomeAgent = id;
  if (!S.current) {
    closePopover();
    S.currentAgent = ag;
    S.configs = [];
    S.draftSelections = [];
    ++S.draftPreviewSeq;
    S.preparing = "";
    renderTopbar();
    renderChat();
    renderComposer();
    focusComposer();
    return;
  }
  if (S.current.agentId === id) { closePopover(); return; }
  try {
    const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/agent`, { method: "POST", body: { agentId: id } });
    S.current = d.conversation;
    S.currentAgent = ag;
    S.messages = [];
    S.active = null;
    closePopover();
    await loadConversation(d.conversation.id, false);
    prepareComposer();
    toast("Agent switched", `${ag.name} will receive a recap on the next message.`, "success");
  } catch (e) {
    toast("Could not switch agent", e.message, "error");
  }
}
