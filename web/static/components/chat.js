import { $, $$, TOOL_KINDS, agentById, agentName, agentOrb, clock, currentAgent, dayLabel, esc, fmtBytes, icon, parseJSON, pretty, truncate } from "./util.js?v=33";
import { S } from "./state.js?v=33";
import { enhanceMarkdown, plainMarkdown, renderMarkdown } from "./markdown.js?v=33";
import { closeDetachedPopovers, openPopover } from "./popover.js?v=33";
import { openModal } from "./modal.js?v=33";
import { approvalCard } from "./requests.js?v=33";
import { isBusyTurn, queuedTurnFor, queueStatusLabel } from "./composer.js?v=33";
import { loadConversation, renderHistoryControl } from "./conversations.js?v=33";

/* ---------------- chat ---------------- */
export function renderChat(skeleton = false) {
  const chat = $("#chat");
  if (!chat) return;
  closeDetachedPopovers();
  const sc = $("#chat-scroll");
  const wasPinned = S.scrollPinned || S.messages.length === 0;
  const focused = document.activeElement?.closest?.("[data-action][data-id]");
  const focusedAction = focused && chat.contains(focused) ? [focused.dataset.action, focused.dataset.id] : null;
  if (skeleton || S.loadingConversation) {
    chat.innerHTML = `<div class="chat-skeleton" role="status" aria-label="Loading conversation"><div class="skeleton-message user"><span></span><span></span></div><div class="skeleton-message assistant"><i></i><span></span><span></span><span></span></div><div class="skeleton-message user"><span></span><span></span></div><div class="skeleton-message assistant"><i></i><span></span><span></span></div></div>`;
    return;
  }
  if (S.loadError && S.loadError === S.current?.id) {
    chat.innerHTML = `<div class="loading-row" role="alert">${icon("alert", "sm")}<span>Could not open this conversation.</span><button class="btn btn-secondary btn-xs" data-action="retry-load" data-id="${esc(S.loadError)}">${icon("refresh", "sm")} Retry</button></div>`;
    return;
  }
  if (!S.current) {
    chat.innerHTML = welcomeHTML();
    enhanceMarkdown(chat);
    return;
  }
  if (!S.messages.length) {
    chat.innerHTML = welcomeHTML(true);
    enhanceMarkdown(chat);
    return;
  }
  const rows = [{ key: "history", html: `<div id="history-control"></div>` }];
  const plan = planEntries(S.current.plan);
  if (plan.length) rows.push({ key: "plan", html: planCard(plan) });
  let lastDay = "";
  const userSeq = new Map(S.messages.filter((m) => m.role === "user").map((m) => [m.id, m.seq]));
  const turnSeq = new Map(Object.values(S.turns).filter((t) => userSeq.has(t.userMessageId)).map((t) => [t.id, userSeq.get(t.userMessageId)]));
  const messages = S.messages.slice().sort((a, b) => (turnSeq.get(a.turnId) ?? a.seq) - (turnSeq.get(b.turnId) ?? b.seq) || a.seq - b.seq);
  const toolCounts = new Map();
  messages.forEach((m) => { if (m.kind === "tool") toolCounts.set(m.turnId, (toolCounts.get(m.turnId) || 0) + 1); });
  for (let i = 0; i < messages.length;) {
    const m = messages[i];
    const day = dayLabel(m.createdAt);
    if (day && day !== lastDay) {
      rows.push({ key: "date:" + m.id, html: `<div class="chat-date">${esc(day)}</div>` });
      lastDay = day;
    }
    if (m.turnId && (m.kind === "thought" || m.kind === "tool")) {
      const group = [];
      while (i < messages.length && messages[i].turnId === m.turnId && (messages[i].kind === "thought" || messages[i].kind === "tool")) group.push(messages[i++]);
      if (group.filter((item) => item.kind === "tool").length > 1) rows.push({ key: "activity:" + m.turnId + ":" + m.id, html: activityHTML(group, toolCounts.get(m.turnId)) });
      else group.forEach((item) => rows.push({ key: "message:" + item.id, html: messageHTML(item) }));
    } else {
      rows.push({ key: "message:" + m.id, html: messageHTML(m) });
      i++;
    }
  }
  const pending = currentApprovals();
  pending.forEach((a) => rows.push({ key: "approval:" + a.id, html: approvalCard(a) }));
  if (S.active && S.active.running && !pending.length) rows.push({ key: "typing", html: typingHTML() });

  // Reuse unchanged rows: a new tool or turn should not rebuild thousands of
  // existing messages, or discard their diagrams and interactive state.
  const existing = new Map(Array.from(chat.children).filter((el) => el.dataset.renderKey).map((el) => [el.dataset.renderKey, el]));
  const desired = new Map(rows.map((row) => [row.key, row.html]));
  const savedScroll = new Map(), savedDiagrams = new Map(), detailsState = new Map();
  for (const [key, element] of existing) {
    if (desired.get(key) === element._renderHTML) continue;
    $$(".tool-pre", element).forEach((el) => savedScroll.set(el.dataset.scrollKey, [el.scrollTop, el.scrollLeft]));
    const messageNodes = element.matches("[data-msg-id]") ? [element, ...$$("[data-msg-id]", element)] : $$("[data-msg-id]", element);
    messageNodes.forEach((el) => {
      $$("pre > code.language-mermaid, .mermaid-diagram", el).forEach((node, index) => {
        if (node.classList.contains("mermaid-diagram")) savedDiagrams.set(el.dataset.msgId + ":" + index, node);
      });
      detailsState.set(el.dataset.msgId, $$(".markdown details", el).map((d) => d.open));
    });
  }
  const changed = [];
  let cursor = chat.firstElementChild;
  for (const row of rows) {
    let node = existing.get(row.key);
    if (!node || node._renderHTML !== row.html) {
      if (node) {
        if (cursor === node) cursor = node.nextElementSibling;
        node.remove();
      }
      const wrapper = document.createElement("div");
      wrapper.innerHTML = row.html;
      node = wrapper.firstElementChild;
      node.dataset.renderKey = row.key;
      node._renderHTML = row.html;
      changed.push(node);
    }
    if (node !== cursor) chat.insertBefore(node, cursor);
    cursor = node.nextElementSibling;
  }
  while (cursor) { const next = cursor.nextElementSibling; cursor.remove(); cursor = next; }
  renderHistoryControl();
  for (const root of changed) {
    const messageNodes = root.matches("[data-msg-id]") ? [root, ...$$("[data-msg-id]", root)] : $$("[data-msg-id]", root);
    messageNodes.forEach((el) => {
      $$("pre > code.language-mermaid", el).forEach((code, index) => {
        const previous = savedDiagrams.get(el.dataset.msgId + ":" + index);
        if (previous && previous._diagramSource === code.textContent) code.parentElement.replaceWith(previous);
      });
    });
    enhanceMarkdown(root);
    messageNodes.forEach((el) => $$(".markdown details", el).forEach((d, i) => {
      const saved = detailsState.get(el.dataset.msgId);
      if (saved && i < saved.length) d.open = saved[i];
    }));
    $$(".tool-pre", root).forEach((el) => {
      const pos = savedScroll.get(el.dataset.scrollKey);
      if (pos) [el.scrollTop, el.scrollLeft] = pos;
    });
  }
  if (focusedAction) {
    const next = $$("[data-action][data-id]", chat).find((el) => el.dataset.action === focusedAction[0] && el.dataset.id === focusedAction[1]);
    if (next) next.focus({ preventScroll: true });
  }
  if (wasPinned && sc) {
    sc.scrollTop = sc.scrollHeight;
    S.scrollPinned = true;
    requestAnimationFrame(() => {
      if (S.scrollPinned) {
        sc.scrollTop = sc.scrollHeight;
      }
    });
  }
}

export function currentApprovals() {
  const id = S.current?.id;
  if (!id) return [];
  return Array.from(S.approvalsById.values()).filter((a) => a.conversationId === id && a.status === "pending");
}

export function messageHTML(m) {
  const kind = m.kind || "text";
  if (kind === "thought") return thoughtHTML(m);
  if (kind === "tool") return toolHTML(m);
  if (kind === "error") return errorHTML(m);
  if (kind === "system" || kind === "notice" || m.role === "system") return systemHTML(m);
  if (m.role === "user") return userHTML(m);
  return assistantHTML(m);
}

export function messageHead(m) {
  const name = m.role === "user" ? "You" : (S.current?.agentName || agentName(currentAgent()));
  return `<div class="msg-head"><span class="msg-author">${esc(name)}</span><span class="msg-time">${esc(clock(m.createdAt))}</span></div>`;
}

export function userHTML(m) {
  const match = /^(?:([^\r\n]*?: )?\[In reply to: ([^\r\n]*)\])(?:\r?\n|$)/.exec(m.text || "");
  const sender = match?.[1] ? `<div class="reply-sender">${esc(match[1])}</div>` : "";
  const format = m.channel === "whatsapp" ? "whatsapp" : "markdown";
  const quote = match ? `<div class="reply-quote"><div class="reply-quote-label">In reply to</div><div class="reply-quote-text markdown">${renderMarkdown(match[2], "quote-" + m.id, format)}</div></div>` : "";
  const text = match ? m.text.slice(match[0].length) : m.text;
  const anns = messageAnnotations(m);
  const pinUploadIds = new Set(anns.map((a) => a.uploadId).filter(Boolean));
  const visible = (m.attachments || []).filter((a) => !pinUploadIds.has(a.id));
  const attachments = attachmentsHTML(visible) + annotationsHTML(anns);
  const qt = queuedTurnFor(m);
  const qLabel = qt && qt.status === "queued" ? queueStatusLabel(m) : "";
  return `<div class="msg user" data-msg-id="${esc(m.id)}">
    <div class="msg-body">
      ${messageHead(m)}
      <div class="user-bubble">${sender}${quote}${text ? `<div class="markdown">${renderMarkdown(text, m.id, format)}</div>` : ""}${match ? attachments : ""}</div>
      ${match ? "" : attachments}
      ${qLabel ? `<div class="queue-status"><span>${esc(qLabel)}</span><span class="queue-actions"><button class="btn btn-ghost btn-xs" data-action="queue-edit" data-id="${esc(m.id)}" data-turn="${esc(qt.id)}" title="Edit this queued message">${icon("edit", "sm")} Edit</button><button class="btn btn-ghost btn-xs" data-action="queue-cancel" data-id="${esc(m.id)}" data-turn="${esc(qt.id)}" title="Cancel this queued message">${icon("x", "sm")} Cancel</button></span></div>` : ""}
    </div>
  </div>`;
}

export function assistantHTML(m) {
  const agent = currentAgent();
  const streaming = m.status === "streaming";
  const text = m.text || "";
  return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
    <div class="msg-avatar">${agentOrb(agent, "agent-orb")}</div>
    <div class="msg-body">
      ${messageHead(m)}
      <div class="markdown">${renderMarkdown(text, m.id)}${streaming ? `<span class="streaming-caret"></span>` : ""}</div>
      ${attachmentsHTML(m.attachments)}
    </div>
  </div>`;
}

export function thoughtHTML(m) {
  const open = S.openThoughts.has(m.id);
  const streaming = m.status === "streaming";
  const preview = truncate(plainMarkdown(m.text || ""), 110);
  return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
    <div class="msg-avatar" style="background:rgba(180,155,255,.1);border-color:rgba(180,155,255,.22);color:var(--purple)">${icon("brain")}</div>
    <div class="msg-body">
      <div class="thought ${open ? "open" : ""}" data-thought-id="${esc(m.id)}">
        <button class="thought-head" data-action="toggle-thought" data-id="${esc(m.id)}" aria-expanded="${open}">
          ${icon("brain")}<span class="label">${streaming ? "Thinking" : "Thought process"}</span>
          <span class="preview">${esc(preview || (streaming ? "…" : ""))}</span>
          ${icon("chevron-right", "chev")}
        </button>
        <div class="thought-body markdown">${renderMarkdown(m.text || "", "thought-" + m.id)}</div>
      </div>
    </div>
  </div>`;
}

export function activityHTML(messages, allTurnTools) {
  const id = messages[0].turnId + ":" + messages[0].id;
  const tools = messages.filter((m) => m.kind === "tool");
  // Follow the turn, not the messages: message status goes idle between tool calls and made the label flicker.
  const running = S.active?.turnId === messages[0].turnId && isBusyTurn();
  const failed = tools.some((m) => ["failed", "error"].includes(String(parseJSON(m.content, {})?.status || m.status)));
  // Collapsed by default. Only the user's toggle opens it, so appending tool calls never flips it.
  const open = S.activityExpanded.get(id) === true;
  const turn = S.turns[messages[0].turnId];
  const seconds = tools.length === allTurnTools && turn?.startedAt && turn?.endedAt && turn.endedAt >= turn.startedAt ? Math.round((turn.endedAt - turn.startedAt) / 1000) : null;
  return `<div class="activity ${open ? "open" : ""}">
    <button class="activity-head" data-action="toggle-activity" data-id="${esc(id)}" aria-expanded="${open}">
      ${icon("chevron-right", "chev")}<span>${running ? "Working" : seconds !== null ? `Worked for ${seconds}s` : "Activity"}</span>
      <span class="activity-count">${tools.length} tool calls${failed ? " · failed" : ""}</span>
    </button><div class="activity-items">${messages.map(messageHTML).join("")}</div>
  </div>`;
}

export function toolHTML(m) {
  const t = parseJSON(m.content, {}) || {};
  if (!t.toolCallId) t.toolCallId = m.toolCallId || m.id;
  const open = S.openTools.has(m.id);
  const kind = String(t.kind || "").toLowerCase();
  const status = String(t.status || m.status || "pending");
  const title = t.title || t.name || m.text || "Tool call";
  const iconName = toolIcon(kind);
  const iconClass = `kind-${TOOL_KINDS.has(kind) ? kind : "other"}`;
  const sections = toolSections(t, m.id);
  const preview = toolPreview(t);
  const exits = Object.values(t.terminals || {}).map((term) => term.exitStatus).filter(Boolean);
  const exitText = exits.map((exit) => exit.exitCode !== undefined && exit.exitCode !== null ? `Exit ${exit.exitCode}` : exit.signal ? `Signal ${exit.signal}` : "").filter(Boolean).join(", ");
  return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
    <div class="msg-avatar" style="background:rgba(131,184,255,.08);border-color:rgba(131,184,255,.2);color:var(--blue)">${icon("zap")}</div>
    <div class="msg-body">
      <div class="tool-card ${open ? "open" : ""} ${status === "failed" ? "failed" : ""}" data-tool-id="${esc(t.toolCallId || m.toolCallId || m.id)}">
        <button class="tool-head" data-action="toggle-tool" data-id="${esc(m.id)}" aria-expanded="${open}">
          <span class="tool-icon ${iconClass}">${icon(iconName)}</span>
          <span class="tool-main">
            <span class="tool-title-row"><span class="tool-title">${esc(title)}</span></span>
            <span class="tool-sub">${esc(t.name || kind || "tool")}${t.locations && t.locations.length ? ` · ${t.locations.length} location${t.locations.length === 1 ? "" : "s"}` : ""}${exitText ? ` · ${esc(exitText)}` : ""}</span>
          </span>
          <span class="tool-status ${esc(status)}"><span class="dot"></span>${esc(statusLabel(status))}</span>
          ${icon("chevron-right", "chev")}
        </button>
        ${preview ? `<div class="tool-preview">${esc(preview)}</div>` : ""}
        <div class="tool-body">${sections || `<div class="tool-text">No tool details were provided.</div>`}</div>
      </div>
    </div>
  </div>`;
}

export function toolIcon(kind) {
  if (kind === "execute") return "terminal";
  if (kind === "edit" || kind === "move") return "edit";
  if (kind === "delete") return "trash";
  if (kind === "read") return "file";
  if (kind === "search") return "search";
  if (kind === "fetch") return "globe";
  if (kind === "think") return "brain";
  return "zap";
}

export function statusLabel(s) {
  s = String(s || "").toLowerCase();
  if (s === "in_progress") return "Running";
  if (s === "completed" || s === "complete") return "Done";
  if (s === "failed" || s === "error") return "Failed";
  if (s === "pending") return "Pending";
  return s || "Working";
}

export function toolSections(t, messageId) {
  const out = [];
  const contentTexts = [];
  const diffs = [];
  const content = Array.isArray(t.content) ? t.content : t.content ? [t.content] : [];
  content.forEach((item) => {
    if (!item || typeof item !== "object") { contentTexts.push(pretty(item)); return; }
    if (item.type === "content" && item.content) {
      if (item.content.type === "text" && item.content.text) contentTexts.push(item.content.text);
      else if (item.content.type === "resource" && item.content.resource?.text) contentTexts.push(item.content.resource.text);
      else contentTexts.push(pretty(item));
    } else if (item.type === "diff") {
      diffs.push(item);
    } else if (item.type === "terminal" && t.terminals?.[item.terminalId]) {
      // Rendered from the persistent terminal snapshot below.
    } else if (item.type === "text" && item.text) {
      contentTexts.push(item.text);
    } else contentTexts.push(pretty(item));
  });
  const section = (label, value, language = "") => {
    const key = messageId + ":" + label;
    const wrap = !S.unwrappedTools.has(key);
    return `<div class="tool-section"><div class="tool-section-head"><div class="tool-label">${esc(label)}</div><div class="tool-actions">
      <button type="button" data-action="tool-wrap" title="Toggle line wrapping">${wrap ? "No wrap" : "Wrap"}</button>
      <button type="button" data-action="tool-start" title="Go to the start of this output">Start</button>
      <button type="button" data-action="tool-end" title="Go to the end of this output">End</button>
      <button type="button" data-action="copy-tool" title="Copy the complete output">Copy</button>
    </div><button type="button" class="tool-menu-button" data-action="tool-menu" aria-label="${esc(label)} options" title="${esc(label)} options">+</button></div><pre class="tool-pre ${wrap ? "" : "no-wrap"}" data-scroll-key="${esc(key)}"><code class="${language ? `language-${language}` : "nohighlight"}">${esc(value)}</code></pre></div>`;
  };
  if (t.rawInput !== undefined && t.rawInput !== null) {
    out.push(section("Input", typeof t.rawInput === "string" ? t.rawInput : pretty(t.rawInput), typeof t.rawInput === "object" ? "json" : ""));
  }
  if (contentTexts.length) {
    out.push(section("Output", contentTexts.join("\n")));
  }
  if (diffs.length) {
    diffs.forEach((d, i) => out.push(section(`Changes ${i + 1}`, String(d.path || "") + "\n" + diffText(d), "diff")));
  }
  Object.values(t.terminals || {}).forEach((term) => out.push(section(`Terminal ${term.id}`, term.output || "")));
  if (t.rawOutput !== undefined && t.rawOutput !== null) {
    out.push(section("Result", typeof t.rawOutput === "string" ? t.rawOutput : pretty(t.rawOutput), typeof t.rawOutput === "object" ? "json" : ""));
  }
  if (Array.isArray(t.locations) && t.locations.length) {
    out.push(`<div class="tool-section"><div class="tool-label">Locations</div><div class="tool-locs">${t.locations.map((l) => `<span class="tool-loc">${esc((l.path || "") + (l.line ? ":" + l.line : ""))}</span>`).join("")}</div></div>`);
  }
  return out.join("");
}

export function toolPreview(t) {
  const content = Array.isArray(t.content) ? t.content : [];
  const texts = content.map((item) => item?.type === "content" ? item.content?.text : item?.type === "text" ? item.text : "").filter(Boolean);
  const terminalOutput = Object.values(t.terminals || {}).map((term) => term.output || "").filter(Boolean).join("\n");
  const text = terminalOutput || texts.join("\n") || (typeof t.rawOutput === "string" ? t.rawOutput : "");
  return text ? truncate(text.trim(), 280) : "";
}

export function diffText(d) {
  const old = String(d.oldText || "").split("\n").map((l) => "- " + l);
  const neu = String(d.newText || "").split("\n").map((l) => "+ " + l);
  return old.concat(neu).join("\n");
}

export function planEntries(plan) {
  let p = plan;
  if (typeof p === "string") p = parseJSON(p, p);
  if (!p) return [];
  if (Array.isArray(p)) return p;
  if (Array.isArray(p.entries)) return p.entries;
  if (Array.isArray(p.plan)) return p.plan;
  return [];
}

export function planCard(entries) {
  return `<div class="plan-card">
    <div class="plan-head">${icon("zap")} Plan <span class="grow"></span><span class="muted" style="font-weight:400">${entries.filter((e) => e.status === "completed").length}/${entries.length}</span></div>
    <div class="plan-list">${entries.map((e) => {
      const st = String(e.status || "pending").toLowerCase();
      return `<div class="plan-step ${esc(st)}"><span class="box">${st === "completed" ? icon("check") : ""}</span><span>${esc(e.title || e.content || e.text || "")}</span></div>`;
    }).join("")}</div>
  </div>`;
}

export function systemHTML(m) {
  return `<div class="msg system ${m.kind === "error" ? "error" : ""}" data-msg-id="${esc(m.id)}"><div class="msg-body">${esc(m.text)}</div></div>`;
}

export function errorHTML(m) {
  return `<div class="msg system error" data-msg-id="${esc(m.id)}"><div class="msg-body"><div class="error-card" style="border:1px solid rgba(255,118,118,.22);background:var(--danger-dim);border-radius:10px;padding:9px 13px;text-align:left;display:flex;gap:8px;align-items:flex-start">${icon("alert", "sm")}<span>${esc(m.text)}</span></div>${S.current && !S.active ? `<button class="btn btn-ghost btn-xs" data-action="retry" style="margin-top:6px">${icon("refresh", "sm")} Retry</button>` : ""}</div></div>`;
}

export function attachmentsHTML(atts) {
  if (!atts || !atts.length) return "";
  return `<div class="attach-grid">${atts.map((a) => {
    const url = `/api/uploads/${encodeURIComponent(a.id)}`;
    const isImg = String(a.mime || "").startsWith("image/");
    if (isImg) return `<a class="attach-thumb" href="${url}" target="_blank" rel="noopener"><img src="${url}" alt="${esc(a.name)}" loading="lazy"></a>`;
    return `<a class="attach-file" href="${url}" target="_blank" rel="noopener">${icon("file", "sm")}<span class="name">${esc(a.name)}</span><span class="size">${fmtBytes(a.size)}</span></a>`;
  }).join("")}</div>`;
}

// Page annotations sent with the message (contract §6.4): metadata.annotations
// carries {pickId, comment, marker, uploadId, descriptor}; the pick's marked
// upload is also bound as a regular attachment but renders inside the chip,
// not as a standalone thumb (caller filters it out of the attachment list).
export function messageAnnotations(m) {
  const meta = parseJSON(m.metadata, {}) || {};
  const anns = Array.isArray(meta.annotations) ? meta.annotations : [];
  return anns.filter((a) => a && a.pickId);
}

export function annotationsHTML(anns) {
  if (!anns.length) return "";
  return `<div class="annot-grid">${anns.map((a) => {
    const d = a.descriptor || {};
    const tag = d.tagName || "element";
    const label = d.name || d.immediateText || "";
    const title = [label, d.selector, d.pageUrl].filter(Boolean).join(" · ");
    const caption = [tag, a.comment].filter(Boolean).join(": ").replace(/\s+/g, " ");
    const inner = `${a.uploadId ? `<span class="thumb"><img src="/api/uploads/${encodeURIComponent(a.uploadId)}" alt="" loading="lazy">${a.marker ? `<span class="n">${esc(a.marker)}</span>` : ""}</span>` : icon("pin", "sm")}
      <span class="annot-chip-text"><b>${esc(tag)}</b>${a.comment ? `<span>: ${esc(a.comment)}</span>` : ""}</span>`;
    if (a.uploadId) return `<button type="button" class="annot-chip" data-action="preview-annotation" data-upload="${esc(a.uploadId)}" data-caption="${esc(caption)}" title="${esc(title)}" aria-label="Preview annotation screenshot">${inner}</button>`;
    return `<span class="annot-chip" title="${esc(title)}">${inner}</span>`;
  }).join("")}</div>`;
}

export function previewAnnotation(uploadId, caption) {
  if (!uploadId) return;
  const src = `/api/uploads/${encodeURIComponent(uploadId)}`;
  S.modal = { type: "image-preview" };
  openModal(`<button type="button" class="icon-btn image-preview-close" data-action="close-modal" aria-label="Close preview" autofocus>${icon("x")}</button>
    <div class="image-preview-status" role="status">Loading screenshot…</div>
    <img alt="${esc(caption || "Annotation screenshot")}" hidden>
    <p class="image-preview-error" hidden>Could not load this screenshot.</p>
    ${caption ? `<div class="image-preview-caption">${esc(caption)}</div>` : ""}`, { size: "image-preview" });
  const root = $("#modal-root");
  const img = $("img", root);
  const status = $(".image-preview-status", root);
  const err = $(".image-preview-error", root);
  if (!img) return;
  const show = () => { if (status) status.hidden = true; img.hidden = false; };
  const fail = () => { if (status) status.hidden = true; img.hidden = true; if (err) err.hidden = false; };
  img.addEventListener("load", show);
  img.addEventListener("error", fail);
  img.src = src;
  if (img.complete && img.naturalWidth) show();
}

export function typingHTML() {
  const agent = currentAgent();
  return `<div class="msg assistant"><div class="msg-avatar">${agentOrb(agent, "agent-orb")}</div><div class="msg-body"><div class="typing"><span></span><span></span><span></span></div></div></div>`;
}

export function welcomeHTML(compact = false) {
  const agents = S.agents.filter((a) => a.enabled);
  const selected = S.current ? currentAgent() : (agentById(S.welcomeAgent) || agents[0]);
  if (!agents.length) {
    return `<div class="welcome"><div class="welcome-orb">N</div><h1>Add your first <em>agent.</em></h1><p>NusaMation connects to ACP agents such as Devin, Claude Code, Codex or Gemini CLI. Tell the control center how to launch yours.</p><button class="btn btn-primary" data-action="add-agent">${icon("plus")} Add an ACP agent</button></div>`;
  }
  return `<div class="welcome ${compact ? "compact" : ""}">
    <div class="welcome-orb">N</div>
    <div class="agent-strip">${agents.map((a) => `<button class="agent-chip ${selected && a.id === selected.id ? "active" : ""}" data-action="pick-agent" data-id="${esc(a.id)}">${agentOrb(a)}<span>${esc(a.name)}</span></button>`).join("")}</div>
    <h1>${compact ? "This thread is <em>ready.</em>" : "What should we <em>build</em> today?"}</h1>
    <p>${compact ? `Message ${esc(agentName(selected))} below. The web control center shows every thought, tool call and approval; chat channels only receive the final answer.` : "Your agent has hands on this workspace, full tool visibility in the web, and a concise presence on WhatsApp. Start with a sentence — or pick a thread below."}</p>
    <div class="suggestion-grid">
      ${[
        ["search", "Explore the workspace", "List files and explain the project layout."],
        ["terminal", "Run a careful check", "Check the workspace health and report findings."],
        ["edit", "Build something small", "Create a small tool or script and verify it."],
        ["globe", "Research something", "Look up a topic and summarize the useful parts."],
      ].map(([ic, title, prompt]) => `<button class="suggestion" data-action="suggestion" data-prompt="${esc(prompt)}">${icon(ic)}<span><b>${esc(title)}</b><span>${esc(prompt)}</span></span></button>`).join("")}
    </div>
  </div>`;
}

export const streamEncoder = new TextEncoder();

export const streamDecoder = new TextDecoder();

export let streamResyncTimer = null;

export let streamResyncRoom = "";

export function scheduleStreamResync(conversationId) {
  if (streamResyncTimer && streamResyncRoom === conversationId) return;
  clearTimeout(streamResyncTimer);
  streamResyncRoom = conversationId;
  streamResyncTimer = setTimeout(() => {
    streamResyncTimer = null;
    streamResyncRoom = "";
    if (S.current?.id === conversationId && !S.loadingConversation) loadConversation(conversationId, false);
  }, 100);
}

export function applyStreamDelta(message, data) {
  let delta = data.delta || "";
  if (typeof data.offsetBytes === "number") {
    const existing = message._streamBytes ?? streamEncoder.encode(message.text || "").length;
    const bytes = streamEncoder.encode(delta);
    if (data.offsetBytes > existing) return "gap";
    const overlap = existing - data.offsetBytes;
    if (overlap >= bytes.length) return "duplicate";
    if (overlap > 0) delta = streamDecoder.decode(bytes.subarray(overlap));
    message._streamBytes = existing + bytes.length - overlap;
  } else {
    delete message._streamBytes;
  }
  message.text = (message.text || "") + delta;
  message.status = "streaming";
  return "changed";
}

export const pendingStreamMessages = new Set();

export let streamRenderTimer = null;

export function scheduleStreamRender(id) {
  pendingStreamMessages.add(id);
  if (streamRenderTimer) return;
  streamRenderTimer = setTimeout(() => {
    streamRenderTimer = null;
    const ids = Array.from(pendingStreamMessages);
    pendingStreamMessages.clear();
    for (const messageId of ids) {
      const message = S.messages.find((item) => item.id === messageId);
      if (message && !renderStreamMessage(message)) { scheduleChatRender(); break; }
    }
  }, 80);
}

export function renderStreamMessage(message) {
  const chat = $("#chat"), sc = $("#chat-scroll");
  if (!chat || !sc || message.role !== "assistant") return false;
  const node = chat.querySelector(`[data-msg-id="${CSS.escape(message.id)}"]`);
  if (!node) return false;
  if (message.kind === "tool") {
    const positions = new Map($$(".tool-pre", node).map((pre) => [pre.dataset.scrollKey, [pre.scrollTop, pre.scrollLeft]]));
    const focus = document.activeElement;
    const action = node.contains(focus) ? focus?.dataset?.action : null;
    const actionId = node.contains(focus) ? focus?.dataset?.id : null;
    const wrapper = document.createElement("div");
    wrapper.innerHTML = toolHTML(message);
    const replacement = wrapper.firstElementChild;
    const activity = node.closest(".activity");
    node.replaceWith(replacement);
    $$(".tool-pre", replacement).forEach((pre) => { const pos = positions.get(pre.dataset.scrollKey); if (pos) [pre.scrollTop, pre.scrollLeft] = pos; });
    if (action) $$('[data-action]', replacement).find((el) => el.dataset.action === action && el.dataset.id === actionId)?.focus({ preventScroll: true });
    if (activity) {
      const ids = new Set($$("[data-msg-id]", activity).map((el) => el.dataset.msgId));
      const tools = S.messages.filter((item) => ids.has(item.id) && item.kind === "tool");
      const failed = tools.some((item) => ["failed", "error"].includes(String(parseJSON(item.content, {})?.status || item.status)));
      const count = $(".activity-count", activity);
      if (count) count.textContent = `${tools.length} tool calls${failed ? " · failed" : ""}`;
    }
    enhanceMarkdown(replacement);
    if (S.scrollPinned) sc.scrollTop = sc.scrollHeight;
    return true;
  }
  if (!["text", "thought"].includes(message.kind)) return false;
  const body = message.kind === "thought" ? $(".thought-body", node || chat) : $(".msg-body > .markdown", node || chat);
  if (!body) return false;
  const oldDetails = $$("details", body).map((d) => d.open);
  const diagrams = $$("pre > code.language-mermaid, .mermaid-diagram", body).map((el) => el.classList.contains("mermaid-diagram") ? el : null);
  const text = message.text || "";
  body.innerHTML = renderMarkdown(text, message.kind === "thought" ? "thought-" + message.id : message.id)
    + (message.kind === "text" && message.status === "streaming" ? `<span class="streaming-caret"></span>` : "");
  $$("pre > code.language-mermaid", body).forEach((code, i) => {
    const previous = diagrams[i];
    if (previous && previous._diagramSource === code.textContent) code.parentElement.replaceWith(previous);
  });
  $$("details", body).forEach((detail, i) => { if (i < oldDetails.length) detail.open = oldDetails[i]; });
  if (message.kind === "thought") {
    const preview = $(".thought-head .preview", node);
    if (preview) preview.textContent = truncate(plainMarkdown(text.slice(0, 1000)), 110) || "…";
    const label = $(".thought-head .label", node);
    if (label) label.textContent = message.status === "streaming" ? "Thinking" : "Thought process";
  }
  enhanceMarkdown(body);
  if (S.scrollPinned) sc.scrollTop = sc.scrollHeight;
  return true;
}

export function scheduleChatRender() {
  if (S.renderTimer) return;
  S.renderTimer = requestAnimationFrame(() => {
    S.renderTimer = null;
    renderChat();
  });
}

export function toolPreFor(target) {
  return target.closest(".tool-section")?.querySelector(".tool-pre") ||
    $$(".tool-pre", $("#chat")).find((pre) => pre.dataset.scrollKey === target.dataset.toolKey);
}

export function openToolMenu(anchor) {
  const pre = toolPreFor(anchor);
  if (!pre) return;
  const key = esc(pre.dataset.scrollKey);
  const wrapped = !pre.classList.contains("no-wrap");
  const item = (action, label) => `<button class="menu-item" data-action="${action}" data-tool-key="${key}"><span>${label}</span></button>`;
  openPopover(anchor, `<div class="popover-list">${item("tool-wrap", wrapped ? "Disable line wrap" : "Enable line wrap")}${item("tool-start", "Go to start")}${item("tool-end", "Go to end")}${item("copy-tool", "Copy full text")}</div>`, { width: 205 });
}
