import { $, esc, icon } from "./util.js?v=35";
import { S } from "./state.js?v=35";

/* ---------------- toasts ---------------- */
export function toast(title, description = "", type = "info", ttl = 5200) {
  const root = $("#toasts");
  if (!root) return;
  const key = title + "|" + description;
  if (S.lastNotice === key) return;
  S.lastNotice = key;
  setTimeout(() => { if (S.lastNotice === key) S.lastNotice = ""; }, 300);
  const el = document.createElement("div");
  el.className = "toast " + (type || "info");
  const ic = type === "error" ? "alert" : type === "success" ? "check" : "info";
  el.innerHTML = `${icon(ic)}<div class="grow"><div class="toast-title">${esc(title)}</div>${description ? `<div class="toast-desc">${esc(description)}</div>` : ""}</div><button class="toast-close" data-action="toast-close" aria-label="Dismiss">${icon("x", "sm")}</button>`;
  root.appendChild(el);
  const timer = setTimeout(() => el.remove(), ttl);
  el.querySelector("[data-action=toast-close]").addEventListener("click", () => { clearTimeout(timer); el.remove(); });
}
