import { $, $$, esc, icon, timeAgo } from "./util.js?v=33";
import { S } from "./state.js?v=33";
import { renderApprovalsModal } from "./requests.js?v=33";
import { newConversation, selectConversation } from "./conversations.js?v=33";
import { openSettings } from "./settings.js?v=33";

/* ---------------- command palette ---------------- */
export function openPalette() {
  const items = [
    { kind: "action", id: "new", icon: "plus", label: "New conversation", hint: "N" },
    { kind: "action", id: "settings", icon: "settings", label: "Open control center", hint: "⌘," },
    { kind: "action", id: "approvals", icon: "shield", label: "Review approvals", hint: "" },
    { kind: "action", id: "channels", icon: "message", label: "Channels & WhatsApp", hint: "" },
    ...S.conversations.map((c) => ({ kind: "conversation", id: c.id, icon: "message", label: c.title || "Untitled", hint: timeAgo(c.lastMessageAt || c.updatedAt) })),
  ];
  S.paletteItems = items;
  S.paletteIndex = 0;
  const root = $("#modal-root");
  root.innerHTML = `<div class="modal-overlay" data-palette-overlay="1"><div class="palette">
    <input class="palette-input" id="palette-input" placeholder="Search conversations and actions…" autocomplete="off">
    <div class="palette-list" id="palette-list"></div>
    <div class="palette-foot"><span><span class="key-hint">↑↓</span> navigate</span><span><span class="key-hint">↵</span> open</span><span><span class="key-hint">esc</span> close</span></div>
  </div></div>`;
  const overlay = root.firstElementChild;
  overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) closePalette(); });
  const input = $("#palette-input");
  input.addEventListener("input", () => { S.paletteIndex = 0; renderPalette(); });
  input.addEventListener("keydown", paletteKeys);
  renderPalette();
  setTimeout(() => input.focus(), 20);
}

export function renderPalette() {
  const q = ($("#palette-input")?.value || "").trim().toLowerCase();
  const all = S.paletteItems || [];
  const items = all.filter((i) => !q || i.label.toLowerCase().includes(q));
  if (S.paletteIndex >= items.length) S.paletteIndex = Math.max(0, items.length - 1);
  const list = $("#palette-list");
  if (!list) return;
  list.innerHTML = items.length ? items.map((i, idx) => `<button class="palette-item ${idx === S.paletteIndex ? "active" : ""}" data-index="${idx}" data-action="palette-run" data-kind="${esc(i.kind)}" data-id="${esc(i.id)}">${icon(i.icon)}<span>${esc(i.label)}</span><span class="shortcut">${esc(i.hint || "")}</span></button>`).join("") : `<div class="palette-empty">No matches.</div>`;
  $$(".palette-item", list).forEach((el) => el.addEventListener("mouseenter", () => {
    S.paletteIndex = Number(el.dataset.index || 0);
  }));
}

export function paletteKeys(e) {
  const items = $$(".palette-item", $("#palette-list"));
  if (e.key === "ArrowDown") { e.preventDefault(); S.paletteIndex = Math.min(items.length - 1, S.paletteIndex + 1); renderPalette(); }
  else if (e.key === "ArrowUp") { e.preventDefault(); S.paletteIndex = Math.max(0, S.paletteIndex - 1); renderPalette(); }
  else if (e.key === "Enter") {
    e.preventDefault();
    const item = $$(".palette-item", $("#palette-list"))[S.paletteIndex];
    if (item) runPalette(item.dataset.kind, item.dataset.id);
  } else if (e.key === "Escape") closePalette();
}

export function closePalette() { $("#modal-root").innerHTML = ""; }

export async function runPalette(kind, id) {
  closePalette();
  if (kind === "conversation") return selectConversation(id);
  if (id === "new") return newConversation();
  if (id === "settings") return openSettings("agents");
  if (id === "approvals") return renderApprovalsModal();
  if (id === "channels") return openSettings("channels");
}
