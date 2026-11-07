import { $, clamp, esc, icon, truncate } from "./util.js";
import { S, onComposerSwitch } from "./state.js";
import { saveLastURL, scheduleDraftSave } from "./convmeta.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { closePopover, openPopover } from "./popover.js";
import { focusComposer, renderPendingAttachments } from "./composer.js";

/* ---------------- embedded browser pane ----------------
 * Wire protocol: tmp/browser-api-contract.md
 * - REST: POST/GET/DELETE /api/browser/sessions, .../navigate, .../history,
 *   .../annotate, .../annotate/preview (hover marker probe), .../annotate/clear
 * - Stream: dedicated per-session WS at /api/browser/sessions/{id}/stream.
 *   server→client: binary JPEG frames + JSON text {type: meta|closed|pong}
 *   client→server: JSON text {type: mousemove|click|wheel|text|key|resize|ping}
 * - Coordinates are viewport CSS px; the client scales display→viewport with
 *   the latest meta {deviceWidth, deviceHeight}.
 * - The session survives a WS disconnect; only DELETE destroys it.
 */

const B = {
  open: false,
  conversationId: "",
  session: null,          // sessionView from create/list/get
  socket: null,
  meta: null,             // last meta control message
  nav: { url: "", title: "", canGoBack: false, canGoForward: false },
  status: "idle",         // idle | connecting | live | closed | error
  statusText: "",
  closedCode: "",
  annotate: false,
  pendingPick: null,      // {pickId, descriptor, upload, marker, comment, includeScreenshot, submitted}
  picking: false,
  gotFrame: false,
  frameUrl: "",
  prevFrameUrl: "",       // may still be displayed while frameUrl loads
  navigating: false,
  lastMove: null,
  moveTimer: null,
  lastMoveSent: 0,
  lastHover: null,
  hoverTimer: null,
  hoverBusy: false,
  lastHoverSent: 0,
  hoverSeq: 0,
  resizeTimer: null,
  lastResizeSent: 0,
  observer: null,
  width: 0,               // user-dragged pane width (0 = stylesheet default)
};

const MOVE_INTERVAL_MS = 33; // ~30fps pointermove forwarding
// How often viewport resizes are streamed to the server while the pane is
// being dragged. Throttled rather than debounced so the remote viewport keeps
// up with a moving pointer instead of only landing on the final size.
const RESIZE_INTERVAL_MS = 80;
const CLOSED_LABELS = {
  session_closed: "Browser session closed",
  stream_replaced: "The stream was replaced by another client",
  browser_crashed: "The browser process crashed",
  disconnected: "Connection lost",
};

export function browserIsOpen() { return B.open; }

export function toggleBrowserPane() {
  if (B.open) closeBrowserPane();
  else openBrowserPane();
}

// The shell re-rendered (login/logout, boot): drop local state. The server-side
// session, if any, stays alive and re-attaches on next open.
export function resetBrowserPane() {
  disconnectStream();
  stopResizeWatch();
  closePopover();
  if (B.frameUrl) { try { URL.revokeObjectURL(B.frameUrl); } catch {} B.frameUrl = ""; }
  if (B.prevFrameUrl) { try { URL.revokeObjectURL(B.prevFrameUrl); } catch {} B.prevFrameUrl = ""; }
  B.open = false;
  B.session = null;
  B.meta = null;
  B.nav = { url: "", title: "", canGoBack: false, canGoForward: false };
  B.status = "idle";
  B.statusText = "";
  B.closedCode = "";
  B.annotate = false;
  B.pendingPick = null;
  B.picking = false;
  B.gotFrame = false;
  B.navigating = false;
  if (B.hoverTimer) { clearTimeout(B.hoverTimer); B.hoverTimer = null; }
  B.lastHover = null;
  B.hoverBusy = false;
  B.hoverSeq++;
}

// Called whenever the composer key (visible conversation) changes. The pane is
// bound to one conversation; a switch hides it — the session keeps running
// server-side and re-attaches when the pane is reopened for that conversation.
// A pane opened on the new-conversation draft (key "") is adopted by whatever
// conversation becomes visible next — including the conversation created by
// the send itself — instead of being closed mid-flow.
onComposerSwitch((key) => {
  const next = key || "";
  if (!B.open) { B.conversationId = next; return; }
  if (next === B.conversationId) return;
  if (B.conversationId === "") { B.conversationId = next; return; }
  closeBrowserPane();
});

export function openBrowserPane() {
  if (B.open) return;
  B.open = true;
  B.conversationId = S.current?.id || "";
  const pane = $("#browser-pane");
  if (!pane) { B.open = false; return; }
  buildPane(pane);
  setPaneVisible(true);
  attachOrCreate();
}

export function closeBrowserPane() {
  if (!B.open) return;
  if (B.pendingPick && !B.annotate) postClearHighlight();
  setAnnotate(false);
  if (B.hoverTimer) { clearTimeout(B.hoverTimer); B.hoverTimer = null; }
  B.lastHover = null;
  B.hoverSeq++;
  $("#browser-hover")?.remove();
  disconnectStream();
  stopResizeWatch();
  closePopover();
  B.open = false;
  B.annotate = false;
  B.pendingPick = null;
  B.picking = false;
  B.meta = null;
  B.gotFrame = false;
  B.status = "idle";
  setPaneVisible(false);
}

function setPaneVisible(on) {
  const pane = $("#browser-pane");
  const app = $(".app");
  if (pane) pane.hidden = !on;
  if (app) {
    app.classList.toggle("browser-open", on);
    if (on) applyBrowserWidth();
    else app.style.removeProperty("--browser-w");
  }
  const toggle = $(`[data-action="toggle-browser"]`);
  if (toggle) { toggle.classList.toggle("active", on); toggle.setAttribute("aria-pressed", String(on)); }
}

function buildPane(pane) {
  if (pane.dataset.built) return;
  pane.dataset.built = "1";
  pane.innerHTML = `
    <div class="browser-resize" id="browser-resize" title="Drag to resize"></div>
    <div class="browser-toolbar">
      <button class="icon-btn sm" data-action="browser-back" title="Back" aria-label="Back">${icon("chevron-left", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-forward" title="Forward" aria-label="Forward">${icon("chevron-right", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-reload" title="Reload" aria-label="Reload">${icon("refresh", "sm")}</button>
      <div class="browser-addr"><input class="input" id="browser-url" placeholder="Enter a URL" spellcheck="false" autocomplete="off" aria-label="Browser address"></div>
      <span class="browser-title" id="browser-title" title=""></span>
      <button class="icon-btn sm" data-action="browser-annotate" title="Annotate an element" aria-label="Annotate an element" aria-pressed="false">${icon("pin", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-close" title="Close pane" aria-label="Close pane">${icon("x", "sm")}</button>
    </div>
    <div class="browser-view" id="browser-view" tabindex="0" aria-label="Remote browser view">
      <img class="browser-frame" id="browser-frame" alt="Remote page" draggable="false">
      <div class="browser-state" id="browser-state"></div>
    </div>`;
  bindPane(pane);
}

function bindPane(pane) {
  const view = $("#browser-view", pane);
  const url = $("#browser-url", pane);
  url.addEventListener("keydown", (e) => {
    e.stopPropagation();
    if (e.key === "Enter") { e.preventDefault(); browserNavigate(url.value); }
    else if (e.key === "Escape") { url.value = B.nav.url || ""; url.blur(); }
  });
  url.addEventListener("focus", () => url.select());
  view.addEventListener("pointerdown", () => view.focus());
  view.addEventListener("pointermove", onViewMove);
  view.addEventListener("click", onViewClick);
  view.addEventListener("auxclick", onViewAuxClick);
  view.addEventListener("contextmenu", (e) => e.preventDefault());
  view.addEventListener("wheel", onViewWheel, { passive: false });
  view.addEventListener("keydown", onViewKey);
  view.addEventListener("pointerleave", () => { if (B.annotate && !B.pendingPick) hideHover(); });
  const grip = $("#browser-resize", pane);
  grip.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    grip.setPointerCapture(e.pointerId);
    const startX = e.clientX;
    const startW = pane.getBoundingClientRect().width;
    const move = (ev) => {
      B.width = clamp(Math.round(startW + (startX - ev.clientX)), 320, Math.round(window.innerWidth * 0.8));
      applyBrowserWidth();
    };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", () => grip.removeEventListener("pointermove", move), { once: true });
    grip.addEventListener("pointercancel", () => grip.removeEventListener("pointermove", move), { once: true });
  });
}

function applyBrowserWidth() {
  const app = $(".app");
  if (app && B.width) app.style.setProperty("--browser-w", B.width + "px");
}

/* ---------------- session lifecycle ---------------- */

async function attachOrCreate() {
  setStatus("connecting", "Looking for a browser session…");
  try {
    const d = await api("/browser/sessions");
    if (!B.open) return;
    const sessions = d.sessions || [];
    const usable = (s) => (s.conversationId || "") === B.conversationId || !s.conversationId;
    const live = sessions.find((s) => s.status !== "crashed" && (s.conversationId || "") === B.conversationId)
      || sessions.find((s) => s.status !== "crashed" && !s.conversationId);
    if (live) { attach(live); return; }
    const crashed = sessions.find((s) => s.status === "crashed" && usable(s));
    if (crashed) {
      try { await api(`/browser/sessions/${encodeURIComponent(crashed.id)}`, { method: "DELETE" }); } catch {}
    }
    await createSession();
  } catch (e) {
    if (B.open) setStatus("error", e.message || "Could not reach the browser service.");
  }
}

async function createSession() {
  setStatus("connecting", "Starting the browser…");
  const box = viewBox();
  try {
    const d = await api("/browser/sessions", {
      method: "POST",
      body: {
        conversationId: B.conversationId || "",
        width: clamp(Math.round(box.w) || 1280, 200, 4096),
        height: clamp(Math.round(box.h) || 800, 200, 4096),
      },
    });
    if (!B.open) {
      // The pane closed while Chromium launched — release the fresh session.
      try { await api(`/browser/sessions/${encodeURIComponent(d.session.id)}`, { method: "DELETE" }); } catch {}
      return;
    }
    attach(d.session);
  } catch (e) {
    if (B.open) setStatus("error", e.message || "The browser could not be started.");
  }
}

function attach(session) {
  B.session = session;
  B.meta = null;
  B.gotFrame = false;
  applyNavState(session);
  connectStream();
}

// Exported for the "Reconnect" affordances — re-runs attach-or-create.
export function browserReconnect() { attachOrCreate(); }

/* ---------------- stream ---------------- */

function connectStream() {
  disconnectStream();
  const id = B.session?.id;
  if (!id) return;
  setStatus("connecting", "Connecting the live view…");
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  let ws;
  try {
    ws = new WebSocket(`${proto}//${location.host}/api/browser/sessions/${encodeURIComponent(id)}/stream`);
  } catch {
    setStatus("error", "Could not open the browser stream.");
    return;
  }
  ws.binaryType = "arraybuffer";
  B.socket = ws;
  ws.onopen = () => {
    if (B.socket !== ws) return;
    setStatus("live");
    startResizeWatch();
    sendViewportResize();
  };
  ws.onmessage = (e) => {
    if (B.socket !== ws) return;
    if (e.data instanceof ArrayBuffer) { renderFrame(e.data); return; }
    let msg;
    try { msg = JSON.parse(e.data); } catch { return; }
    handleStreamMessage(msg);
  };
  ws.onerror = () => {};
  ws.onclose = () => {
    if (B.socket !== ws) return;
    B.socket = null;
    stopResizeWatch();
    if (!B.open || B.status === "closed") return;
    // Dropped without a `closed` message — the session survives server-side.
    setStatus("closed", "The live view disconnected. The page is still running.", "disconnected");
  };
}

function disconnectStream() {
  const ws = B.socket;
  B.socket = null;
  B.lastMove = null;
  if (B.moveTimer) { clearTimeout(B.moveTimer); B.moveTimer = null; }
  if (B.resizeTimer) { clearTimeout(B.resizeTimer); B.resizeTimer = null; }
  if (ws) {
    try { ws.onclose = null; } catch {}
    try { ws.close(); } catch {}
  }
}

function handleStreamMessage(msg) {
  switch (msg.type) {
    case "meta":
      B.meta = msg;
      applyMeta();
      break;
    case "closed":
      setStatus("closed", msg.message || "", msg.code || "");
      break;
    case "pong":
    default:
      break;
  }
}

function renderFrame(data) {
  const img = $("#browser-frame");
  if (!img) return;
  const url = URL.createObjectURL(new Blob([data], { type: "image/jpeg" }));
  // Keep the current and previous blob alive (the previous one may still be
  // displayed until this frame finishes loading); revoke anything older —
  // an onload callback can't be trusted since a superseded load may never
  // fire it.
  const stale = B.prevFrameUrl;
  B.prevFrameUrl = B.frameUrl;
  B.frameUrl = url;
  if (stale) { try { URL.revokeObjectURL(stale); } catch {} }
  img.src = url;
  if (!B.gotFrame) {
    B.gotFrame = true;
    renderState();
  }
}

function applyMeta() {
  const m = B.meta;
  if (!m) return;
  const img = $("#browser-frame");
  if (img && m.deviceWidth && m.deviceHeight) img.style.aspectRatio = `${m.deviceWidth} / ${m.deviceHeight}`;
  if (typeof m.url === "string" && m.url) {
    B.nav.url = m.url;
    B.nav.title = m.title || "";
  }
  if (typeof m.canGoBack === "boolean") {
    B.nav.canGoBack = m.canGoBack;
    B.nav.canGoForward = !!m.canGoForward;
  }
  updateToolbar();
}

function applyNavState(v) {
  if (!v) return;
  if (typeof v.url === "string") B.nav.url = v.url;
  if (typeof v.title === "string") B.nav.title = v.title;
  B.nav.canGoBack = !!v.canGoBack;
  B.nav.canGoForward = !!v.canGoForward;
  updateToolbar();
}

/* ---------------- REST actions ---------------- */

export async function browserNavigate(url) {
  url = String(url || "").trim();
  if (!url || !B.session || B.navigating) return;
  B.navigating = true;
  updateToolbar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(B.session.id)}/navigate`, { method: "POST", body: { url } });
    applyNavState(d);
    saveLastURL(B.conversationId, B.nav.url || url);
  } catch (e) {
    browserApiError(e, "Navigation failed");
  } finally {
    B.navigating = false;
    updateToolbar();
  }
}

export async function browserHistory(action) {
  if (!B.session || B.navigating) return;
  B.navigating = true;
  updateToolbar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(B.session.id)}/history`, { method: "POST", body: { action } });
    applyNavState(d);
    saveLastURL(B.conversationId, B.nav.url);
  } catch (e) {
    browserApiError(e, action === "reload" ? "Reload failed" : "Could not navigate history");
  } finally {
    B.navigating = false;
    updateToolbar();
  }
}

function browserApiError(e, what) {
  if (e.status === 404) {
    setStatus("closed", "The browser session no longer exists.", "session_closed");
    return;
  }
  if (e.status === 409) { toast("Browser is busy", "Another navigation or pick is in flight — try again.", "info"); return; }
  toast(what, e.message, "error");
}

/* ---------------- input forwarding ---------------- */

function canInput() {
  return B.open && !B.annotate && B.status === "live" && B.socket && B.socket.readyState === WebSocket.OPEN;
}

function sendCtl(obj) {
  const ws = B.socket;
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

// Display px on the <img> → viewport CSS px (contract §4.5).
function viewportPoint(e) {
  const img = $("#browser-frame");
  if (!img) return null;
  const r = img.getBoundingClientRect();
  if (!r.width || !r.height) return null;
  const dw = B.meta?.deviceWidth || B.session?.width || r.width;
  const dh = B.meta?.deviceHeight || B.session?.height || r.height;
  const scale = r.width / dw;
  return {
    x: clamp((e.clientX - r.left) / scale, 0, dw),
    y: clamp((e.clientY - r.top) / scale, 0, dh),
  };
}

function onViewMove(e) {
  if (e.target.id !== "browser-frame") { if (B.annotate && !B.pendingPick) hideHover(); return; }
  if (B.annotate) { onAnnotateMove(e); return; }
  if (!canInput()) return;
  B.lastMove = viewportPoint(e);
  if (!B.lastMove) return;
  const now = Date.now();
  const wait = MOVE_INTERVAL_MS - (now - B.lastMoveSent);
  if (wait <= 0) flushMove();
  else if (!B.moveTimer) B.moveTimer = setTimeout(flushMove, wait);
}

function flushMove() {
  B.moveTimer = null;
  const p = B.lastMove;
  if (!p || !canInput()) return;
  B.lastMove = null;
  B.lastMoveSent = Date.now();
  sendCtl({ type: "mousemove", x: p.x, y: p.y });
}

const AUX_BUTTONS = { 1: "middle", 2: "right" };

function onViewClick(e) {
  if (!B.open || e.target.id !== "browser-frame") return;
  const p = viewportPoint(e);
  if (!p) return;
  if (B.annotate) { pickAt(p, { x: e.clientX, y: e.clientY }); return; }
  if (!canInput()) return;
  sendCtl({ type: "click", x: p.x, y: p.y, button: "left", clickCount: e.detail || 1 });
}

function onViewAuxClick(e) {
  if (!canInput() || e.target.id !== "browser-frame") return;
  const button = AUX_BUTTONS[e.button];
  if (!button) return;
  e.preventDefault();
  const p = viewportPoint(e);
  if (!p) return;
  sendCtl({ type: "click", x: p.x, y: p.y, button, clickCount: e.detail || 1 });
}

function onViewWheel(e) {
  if (!canInput() || e.target.id !== "browser-frame") return;
  e.preventDefault();
  const p = viewportPoint(e);
  if (!p) return;
  let dx = e.deltaX, dy = e.deltaY;
  if (e.deltaMode === 1) { dx *= 16; dy *= 16; }        // lines → px
  else if (e.deltaMode === 2) { dx *= viewBox().w; dy *= viewBox().h; } // pages → px
  sendCtl({ type: "wheel", x: p.x, y: p.y, deltaX: dx, deltaY: dy });
}

function onViewKey(e) {
  if (e.isComposing) return;
  e.stopPropagation(); // keys typed at the remote page must not fire app shortcuts
  if (B.annotate && e.key === "Escape") { e.preventDefault(); setAnnotate(false); return; }
  if (!canInput()) return;
  const modifiers = (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
  if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
    sendCtl({ type: "text", text: e.key });
  } else {
    sendCtl({ type: "key", key: e.key, modifiers });
  }
  e.preventDefault();
}

function viewBox() {
  const v = $("#browser-view");
  return { w: v?.clientWidth || 0, h: v?.clientHeight || 0 };
}

function sendViewportResize() {
  if (!B.socket || B.socket.readyState !== WebSocket.OPEN) return;
  const box = viewBox();
  if (!box.w || !box.h) return;
  sendCtl({ type: "resize", width: clamp(Math.round(box.w), 200, 4096), height: clamp(Math.round(box.h), 200, 4096) });
}

function startResizeWatch() {
  stopResizeWatch();
  const view = $("#browser-view");
  if (!view || typeof ResizeObserver === "undefined") return;
  B.observer = new ResizeObserver(() => {
    // Stretch the last frame into the new box so the pane does not letterbox
    // with black bars while the resized screencast stream catches up; the
    // next real frame replaces the aspect ratio with the true viewport one.
    const box = viewBox();
    const img = $("#browser-frame");
    if (img && box.w && box.h) img.style.aspectRatio = `${box.w} / ${box.h}`;
    const now = Date.now();
    const since = now - (B.lastResizeSent || 0);
    clearTimeout(B.resizeTimer);
    B.resizeTimer = null;
    if (since >= RESIZE_INTERVAL_MS) {
      B.lastResizeSent = now;
      sendViewportResize();
      return;
    }
    B.resizeTimer = setTimeout(() => {
      B.resizeTimer = null;
      B.lastResizeSent = Date.now();
      sendViewportResize();
    }, RESIZE_INTERVAL_MS - since);
  });
  B.observer.observe(view);
}

function stopResizeWatch() {
  if (B.observer) { try { B.observer.disconnect(); } catch {} B.observer = null; }
  if (B.resizeTimer) { clearTimeout(B.resizeTimer); B.resizeTimer = null; }
  B.lastResizeSent = 0;
}

/* ---------------- annotate mode ---------------- */

const HOVER_INTERVAL_MS = 60;

// Annotate-mode hover: probe the element under the cursor so a marker can be
// drawn before the user clicks (Codex-style). The server only hit-tests —
// the marker is a DOM overlay, so no screencast frame is needed.
function onAnnotateMove(e) {
  if (B.pendingPick) return; // pinned marker stays while the popover is open
  if (B.picking || B.status !== "live") { hideHover(); return; }
  const p = viewportPoint(e);
  if (!p) { hideHover(); return; }
  B.lastHover = p;
  if (B.hoverBusy) return;
  const wait = HOVER_INTERVAL_MS - (Date.now() - B.lastHoverSent);
  if (wait <= 0) sendHover();
  else if (!B.hoverTimer) B.hoverTimer = setTimeout(sendHover, wait);
}

function sendHover() {
  if (B.hoverTimer) { clearTimeout(B.hoverTimer); B.hoverTimer = null; }
  const p = B.lastHover;
  B.lastHover = null;
  if (!p || !B.annotate || !B.session || B.pendingPick || B.picking) return;
  B.lastHoverSent = Date.now();
  B.hoverBusy = true;
  const seq = ++B.hoverSeq;
  api(`/browser/sessions/${encodeURIComponent(B.session.id)}/annotate/preview`, {
    method: "POST",
    body: { x: p.x, y: p.y },
  }).then((d) => {
    if (seq === B.hoverSeq && B.annotate && !B.pendingPick) showHover(d);
  }).catch(() => {
  }).finally(() => {
    B.hoverBusy = false;
    if (B.lastHover && !B.hoverTimer) B.hoverTimer = setTimeout(sendHover, HOVER_INTERVAL_MS);
  });
}

function showHover(d) {
  if (!d || d.skipped) return; // busy server: keep the last marker
  if (d.missed || !d.rect) { hideHover(); return; }
  const view = $("#browser-view"), img = $("#browser-frame");
  if (!view || !img) return;
  const vr = view.getBoundingClientRect(), ir = img.getBoundingClientRect();
  if (!ir.width || !ir.height) return;
  const dw = B.meta?.deviceWidth || B.session?.width || ir.width;
  const scale = ir.width / dw;
  let box = $("#browser-hover");
  if (!box) {
    box = document.createElement("div");
    box.id = "browser-hover";
    box.className = "browser-hover";
    box.innerHTML = `<div class="browser-hover-label"></div>`;
    view.appendChild(box);
  }
  box.classList.remove("pinned");
  box.style.left = ir.left - vr.left + d.rect.x * scale + "px";
  box.style.top = ir.top - vr.top + d.rect.y * scale + "px";
  box.style.width = Math.max(2, Math.round(d.rect.width * scale)) + "px";
  box.style.height = Math.max(2, Math.round(d.rect.height * scale)) + "px";
  const label = box.firstElementChild;
  label.textContent = d.tagName ? `<${d.tagName}>${d.name ? " " + d.name : ""}` : d.name || d.selector || "element";
  label.classList.toggle("below", d.rect.y * scale < 26);
  box.classList.add("show");
}

function hideHover() {
  B.hoverSeq++;
  $("#browser-hover")?.classList.remove("show", "pinned");
}

// Keeps the picked element marked while its comment popover is open — the
// server removes the painted pin right after the marked screenshot, so this
// client-side box is the persistent highlight.
function pinHover(d) {
  B.hoverSeq++;
  showHover(d);
  $("#browser-hover.show")?.classList.add("pinned");
}

export function toggleAnnotate() {
  if (B.annotate) setAnnotate(false);
  else if (B.status === "live") setAnnotate(true);
}

// Escape key affordance for the global keydown chain: exits annotate mode.
// Returns true when the key was consumed.
export function browserEscape() {
  if (!B.open || !B.annotate) return false;
  setAnnotate(false);
  return true;
}

function setAnnotate(on) {
  on = !!on;
  if (B.annotate === on) return;
  B.annotate = on;
  updateAnnotateUI();
  if (!on) {
    if (B.pendingPick) B.pendingPick = null;
    if (B.hoverTimer) { clearTimeout(B.hoverTimer); B.hoverTimer = null; }
    B.lastHover = null;
    B.hoverSeq++;
    $("#browser-hover")?.remove();
    closePopover();
    postClearHighlight();
  }
}

function updateAnnotateUI() {
  $("#browser-view")?.classList.toggle("annotate", B.annotate);
  const btn = $(`[data-action="browser-annotate"]`);
  if (btn) { btn.classList.toggle("active", B.annotate); btn.setAttribute("aria-pressed", String(B.annotate)); }
}

function postClearHighlight() {
  const id = B.session?.id;
  if (!id) return;
  api(`/browser/sessions/${encodeURIComponent(id)}/annotate/clear`, { method: "POST", body: {} }).catch(() => {});
}

async function pickAt(p, clientPoint) {
  if (B.picking || !B.session) return;
  B.picking = true;
  const view = $("#browser-view");
  const marker = document.createElement("div");
  marker.className = "browser-pick-marker";
  const vr = view.getBoundingClientRect();
  marker.style.left = clientPoint.x - vr.left + "px";
  marker.style.top = clientPoint.y - vr.top + "px";
  view.appendChild(marker);
  try {
    // Marker number = next free pin number so the badge baked into the
    // screenshot stays unique even after chips are removed/reordered.
    const pinNo = Math.max(0, ...S.attachments.filter((a) => a.type === "annotation").map((a) => a.marker || 0)) + 1;
    const d = await api(`/browser/sessions/${encodeURIComponent(B.session.id)}/annotate`, {
      method: "POST",
      body: { x: p.x, y: p.y, marker: pinNo },
    });
    if (!B.open || !B.annotate) return; // pane closed / annotate exited while the pick was in flight
    B.pendingPick = { pickId: d.pickId, descriptor: d.descriptor || {}, upload: d.upload || null, marker: d.marker || pinNo, submitted: false, comment: "", includeScreenshot: true };
    pinHover(d.descriptor || {});
    openAnnotatePopover(clientPoint);
  } catch (e) {
    if (e.code === "pick_missed") toast("No element there", "Click directly on a page element.", "info", 2600);
    else browserApiError(e, "Could not annotate that point");
  } finally {
    B.picking = false;
    marker.remove();
  }
}

function openAnnotatePopover(clientPoint) {
  const view = $("#browser-view");
  const pick = B.pendingPick;
  if (!view || !pick) return;
  closePopover();
  const anchor = document.createElement("div");
  anchor.className = "browser-anchor";
  const vr = view.getBoundingClientRect();
  anchor.style.left = clientPoint.x - vr.left + "px";
  anchor.style.top = clientPoint.y - vr.top + "px";
  view.appendChild(anchor);
  const d = pick.descriptor || {};
  const label = d.name || d.immediateText || "";
  const up = pick.upload || {};
  openPopover(anchor, `<div class="annotate-pop">
    <div class="annotate-head">
      ${up.id ? `<img src="/api/uploads/${encodeURIComponent(up.id)}" alt="">` : icon("pin", "sm")}
      <div class="annotate-id"><b>${esc(d.tagName ? `<${d.tagName}>` : "element")}</b><small>${esc(truncate(label, 60) || d.selector || "")}</small></div>
    </div>
    <textarea class="input annotate-comment" rows="2" placeholder="Comment for the agent…" aria-label="Annotation comment" autofocus></textarea>
    <label class="annotate-shot"><input type="checkbox" class="annotate-shot-input" checked> Include screenshot <small>(pin ${pick.marker || 1} — the agent sees the marked page)</small></label>
    <div class="annotate-actions">
      <button class="btn btn-ghost btn-xs" data-action="annotation-cancel">Cancel</button>
      <button class="btn btn-primary btn-xs" data-action="annotation-submit">Add note</button>
    </div>
  </div>`, { width: 264, class: "annotate-popover" });
  const pop = $(".annotate-popover");
  if (!pop) { anchor.remove(); return; }
  const prev = pop._cleanup;
  // The global click delegation runs closePopover() BEFORE dispatching the
  // submit action, so the cancel decision is deferred a tick — a submit that
  // already consumed the pick must not post an extra clear.
  pop._cleanup = () => {
    prev?.();
    anchor.remove();
    hideHover();
    setTimeout(() => {
      if (B.pendingPick && !B.pendingPick.submitted) {
        B.pendingPick = null;
        postClearHighlight();
      }
    }, 0);
  };
  const ta = $(".annotate-comment", pop);
  ta?.addEventListener("input", () => { if (B.pendingPick) B.pendingPick.comment = ta.value; });
  $(".annotate-shot-input", pop)?.addEventListener("change", (e) => {
    if (B.pendingPick) B.pendingPick.includeScreenshot = !!e.target.checked;
  });
  ta?.addEventListener("keydown", (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); submitAnnotation(); }
    else if (e.key === "Escape") { e.preventDefault(); cancelAnnotation(); }
  });
  setTimeout(() => ta?.focus(), 20);
}

export function submitAnnotation() {
  const pick = B.pendingPick;
  if (!pick) { closePopover(); return; }
  const comment = (pick.comment || "").trim();
  pick.submitted = true;
  const d = pick.descriptor || {};
  S.attachments.push({
    type: "annotation",
    id: pick.pickId,             // used by the remove-attachment filter when no upload exists
    pickId: pick.pickId,
    comment,
    marker: pick.marker || 0,
    shot: pick.includeScreenshot !== false,
    tagName: d.tagName || "",
    label: d.name || d.immediateText || "",
    selector: d.selector || "",
    upload: pick.upload ? { id: pick.upload.id, mime: pick.upload.mime, name: pick.upload.name, size: pick.upload.size } : null,
  });
  B.pendingPick = null;
  postClearHighlight();
  closePopover();
  renderPendingAttachments();
  scheduleDraftSave();
  focusComposer();
}

export function cancelAnnotation() {
  if (B.pendingPick) {
    B.pendingPick = null;
    postClearHighlight();
  }
  closePopover();
}

/* ---------------- status rendering ---------------- */

function setStatus(status, text = "", code = "") {
  B.status = status;
  B.statusText = text;
  B.closedCode = code || "";
  if (status !== "live" && B.annotate) setAnnotate(false);
  renderState();
  updateToolbar();
}

function closedLabel(code) {
  return CLOSED_LABELS[code] || "Browser session closed";
}

function renderState() {
  const el = $("#browser-state");
  if (!el) return;
  const show = B.status !== "live" || !B.gotFrame;
  el.classList.toggle("show", show);
  if (!show) { el.innerHTML = ""; return; }
  if (B.status === "connecting") {
    el.innerHTML = `<span class="spinner"></span><span>${esc(B.statusText || "Connecting…")}</span>`;
  } else if (B.status === "live") {
    el.innerHTML = `<span class="spinner"></span><span>Waiting for the first frame…</span>`;
  } else if (B.status === "closed") {
    const code = B.closedCode;
    el.innerHTML = `<div class="browser-msg">
      ${icon(code === "browser_crashed" ? "alert" : "info", "sm")}
      <b>${esc(closedLabel(code))}</b>
      ${B.statusText ? `<p>${esc(B.statusText)}</p>` : ""}
      <button class="btn btn-secondary btn-sm" data-action="browser-reconnect">${icon("refresh", "sm")} ${code === "stream_replaced" ? "Take over" : code === "disconnected" ? "Reconnect" : "Restart browser"}</button>
    </div>`;
  } else if (B.status === "error") {
    el.innerHTML = `<div class="browser-msg">
      ${icon("alert", "sm")}
      <b>Browser unavailable</b>
      ${B.statusText ? `<p>${esc(B.statusText)}</p>` : ""}
      <button class="btn btn-secondary btn-sm" data-action="browser-reconnect">${icon("refresh", "sm")} Try again</button>
    </div>`;
  } else {
    el.innerHTML = `<span>${esc(B.statusText || "…")}</span>`;
  }
}

function updateToolbar() {
  const url = $("#browser-url");
  if (url && document.activeElement !== url) url.value = B.nav.url || "";
  const title = $("#browser-title");
  if (title) { title.textContent = B.nav.title || ""; title.title = B.nav.title || ""; }
  const live = B.status === "live";
  const active = B.session && B.status !== "closed" && B.status !== "error";
  const back = $(`[data-action="browser-back"]`);
  const fwd = $(`[data-action="browser-forward"]`);
  const reload = $(`[data-action="browser-reload"]`);
  if (back) back.disabled = !live || !B.nav.canGoBack || B.navigating;
  if (fwd) fwd.disabled = !live || !B.nav.canGoForward || B.navigating;
  if (reload) reload.disabled = !active || B.navigating;
  if (url) url.disabled = !active;
  const annotate = $(`[data-action="browser-annotate"]`);
  if (annotate) annotate.disabled = !live;
  updateAnnotateUI();
}
