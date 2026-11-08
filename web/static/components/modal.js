import { $, esc, icon } from "./util.js?v=45";
import { S } from "./state.js?v=45";
import { respondElicitation, showPendingRequest } from "./requests.js?v=45";

/* ---------------- modals ---------------- */
const modalCloseHooks = new Set();

export function onModalClose(fn) {
  modalCloseHooks.add(fn);
  return () => modalCloseHooks.delete(fn);
}

function modalShell(html, opts = {}) {
  const overlay = document.createElement("div");
  overlay.className = "modal-overlay" + (opts.fit ? " modal-fit" : "");
  overlay.dataset.modalOverlay = "1";
  const dialogClass = ["modal", opts.size || "", opts.dialogClass || ""].filter(Boolean).join(" ");
  overlay.innerHTML = `<div class="${dialogClass}" role="dialog" aria-modal="true">${html}</div>`;
  overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) closeModal(); });
  return overlay;
}

function focusIn(root) {
  setTimeout(() => {
    const focus = root?.querySelector?.("[autofocus]");
    if (focus) focus.focus();
  }, 30);
}

export function openModal(html, opts = {}) {
  const root = $("#modal-root");
  const overlay = modalShell(html, opts);
  root.innerHTML = "";
  root.appendChild(overlay);
  focusIn(overlay);
}

export function stackModal(html, opts = {}) {
  const root = $("#modal-root");
  if (!root) return null;
  const overlay = modalShell(html, opts);
  root.appendChild(overlay);
  focusIn(overlay);
  return overlay;
}

export function closeModal(cancelRequest = true) {
  const root = $("#modal-root");
  const overlays = root ? [...root.children].filter((node) => node.classList?.contains("modal-overlay")) : [];
  if (overlays.length > 1) {
    const active = document.activeElement;
    overlays[overlays.length - 1].remove();
    if (!active || !root.contains(active)) {
      const focus = root.querySelector("[autofocus]") || root.querySelector("button, input, textarea, select");
      focus?.focus();
    }
    return;
  }
  const previousType = S.modal?.type;
  if (cancelRequest && S.modal?.type === "elicitation") {
    const { id, conversationId } = S.modal;
    respondElicitation(conversationId, id, { action: "cancel" });
  }
  if (S.authTerminal) {
    S.authTerminal.socket.close();
    S.authTerminal.terminal.dispose();
    S.authTerminal = null;
  }
  if (S.qrTimer) { clearInterval(S.qrTimer); S.qrTimer = null; }
  S.modal = null;
  S.confirmAction = null;
  if (root) root.innerHTML = "";
  for (const fn of modalCloseHooks) {
    try { fn(); } catch (err) { console.warn("modal close hook failed", err); }
  }
  if (previousType === "agent-slow") setTimeout(showPendingRequest, 0);
}

export function closeAllModals() {
  const root = $("#modal-root");
  if (root) {
    const overlays = [...root.querySelectorAll(".modal-overlay")];
    for (let i = overlays.length - 1; i > 0; i--) overlays[i].remove();
  }
  closeModal();
}

export function modalHead(title, sub, close = true) {
  return `<div class="modal-head"><div><div class="modal-title">${esc(title)}</div>${sub ? `<div class="modal-sub">${esc(sub)}</div>` : ""}</div>${close ? `<button class="icon-btn modal-close" data-action="close-modal" aria-label="Close">${icon("x")}</button>` : ""}</div>`;
}

export function confirmModal(title, message, onConfirm, confirmLabel = "Confirm") {
  S.confirmAction = onConfirm;
  openModal(`${modalHead(title, "")}<div class="modal-body" style="padding:18px"><p class="muted" style="margin:0;font-size:13px;line-height:1.6">${esc(message)}</p></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button><button class="btn btn-danger" data-action="confirm-action">${esc(confirmLabel)}</button></div>`, { size: "sm" });
}

export function promptModal(title, label, value, onSave, placeholder = "", multiline = false) {
  S.promptAction = onSave;
  const field = multiline
    ? `<textarea class="input" id="prompt-modal-input" rows="3" placeholder="${esc(placeholder)}" autofocus>${esc(value || "")}</textarea>`
    : `<input class="input" id="prompt-modal-input" value="${esc(value || "")}" placeholder="${esc(placeholder)}" autofocus>`;
  openModal(`${modalHead(title, "")}<div class="modal-body" style="padding:18px"><div class="field"><label>${esc(label)}</label>${field}</div></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button><button class="btn btn-primary" data-action="prompt-save">Save</button></div>`, { size: "sm" });
}
