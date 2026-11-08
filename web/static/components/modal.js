import { $, esc, icon } from "./util.js?v=35";
import { S } from "./state.js?v=35";
import { respondElicitation, showPendingRequest } from "./requests.js?v=35";

/* ---------------- modals ---------------- */
export function openModal(html, opts = {}) {
  const root = $("#modal-root");
  root.innerHTML = `<div class="modal-overlay" data-modal-overlay="1"><div class="modal ${opts.size || ""}" role="dialog" aria-modal="true">${html}</div></div>`;
  const overlay = root.firstElementChild;
  overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) closeModal(); });
  setTimeout(() => {
    const focus = root.querySelector("[autofocus]");
    if (focus) focus.focus();
  }, 30);
}

export function closeModal(cancelRequest = true) {
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
  const root = $("#modal-root");
  if (root) root.innerHTML = "";
  if (previousType === "agent-slow") setTimeout(showPendingRequest, 0);
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
