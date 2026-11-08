import { esc, icon } from "./util.js?v=45";

/* ---------------- popovers ---------------- */
export function closePopover() {
  document.querySelectorAll(".popover").forEach((e) => {
    try { e._cleanup?.(); } catch {}
    e.remove();
  });
  document.querySelectorAll("[data-popover-anchor]").forEach((e) => e.removeAttribute("data-popover-anchor"));
  document.querySelectorAll('[aria-haspopup][aria-expanded="true"]').forEach((e) => e.setAttribute("aria-expanded", "false"));
}

export function closeDetachedPopovers() {
  for (const e of document.querySelectorAll(".popover")) {
    if (e._anchor && !e._anchor.isConnected) { closePopover(); return; }
  }
}

export function openPopover(anchor, html, opts = {}) {
  closePopover();
  if (!anchor) return;
  const el = document.createElement("div");
  el.className = "popover " + (opts.class || "");
  el.innerHTML = html;
  document.body.appendChild(el);
  anchor.setAttribute("data-popover-anchor", "1");
  el._anchor = anchor;
  el._setAnchor = (next) => {
    anchor.removeAttribute("data-popover-anchor");
    anchor = next;
    el._anchor = next;
    anchor.setAttribute("data-popover-anchor", "1");
    el._reposition();
  };
  el._reposition = () => {
    const r = anchor.getBoundingClientRect();
    const viewport = window.visualViewport;
    const topEdge = viewport?.offsetTop || 0;
    const height = Math.min(window.innerHeight, viewport?.height || window.innerHeight);
    const width = Math.min(opts.width || 320, window.innerWidth - 20);
    el.style.width = width + "px";
    if (el.classList.contains("composer-popover")) el.style.maxHeight = Math.min(440, Math.max(60, height - 20)) + "px";
    const er = el.getBoundingClientRect();
    const left = Math.min(Math.max(10, r.right - width), window.innerWidth - width - 10);
    const bottom = topEdge + height;
    let top = r.bottom + 7;
    if (top + er.height > bottom - 10) top = r.top - er.height - 7;
    top = Math.max(topEdge + 10, Math.min(top, bottom - er.height - 10));
    el.style.left = left + "px";
    el.style.top = top + "px";
  };
  el._reposition();
  const onDoc = (e) => {
    if (!el.contains(e.target) && e.target !== anchor && !anchor.contains(e.target)) closePopover();
  };
  document.addEventListener("click", onDoc, true);
  el._cleanup = () => document.removeEventListener("click", onDoc, true);
}

export function menuItem(iconName, label, action, attrs = "", danger = false) {
  return `<button class="menu-item ${danger ? "danger" : ""}" data-action="${action}" ${attrs}>${icon(iconName, "sm")}<span>${esc(label)}</span></button>`;
}
