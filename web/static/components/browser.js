import { $, $$, annotationTag, clamp, esc, formatElapsed, icon, normalizeRect, truncate, uid } from "./util.js";
import { S, composerKey, onComposerSwitch } from "./state.js";
import { cachedBrowserMeta, onBrowserMetaRestored, saveBrowserOpen, saveLastURL, scheduleDraftSave, usableBrowserURL, whenBrowserMeta } from "./convmeta.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { closePopover, menuItem, openPopover } from "./popover.js";
import { focusComposer, renderPendingAttachments } from "./composer.js";
import { loadXterm } from "./terminal.js";
import { previewResource } from "./file-preview.js";

/* ---------------- embedded browser pane ----------------
 * Wire protocol: docs/API.md (browser stream)
 * - REST: POST/GET/DELETE /api/browser/sessions, .../navigate, .../history,
 *   .../annotate, .../annotate/preview (hover marker probe), .../annotate/clear
 * - Stream: dedicated per-session WS at /api/browser/sessions/{id}/stream.
 *   server→client: binary JPEG frames + JSON text {type: meta|closed|pong}
 *   client→server: JSON text {type: mousemove|click|wheel|text|key|resize|emulate|ping}
 * - Coordinates are viewport CSS px; the client scales display→viewport with
 *   the latest meta {deviceWidth, deviceHeight}.
 * - The session survives a WS disconnect; only DELETE destroys it.
 */

// Pane-level UI state — shared by every tab of the pane. Per-tab state lives
// in B, which is re-pointed to the active tab's state object on tab switches.
const P = {
  open: false,
  conversationId: "",     // room the pane is bound to (composerKey)
  observer: null,         // ResizeObserver on the shared #browser-view
  width: 0,               // user-dragged pane width (0 = stylesheet default)
  deviceBar: localStorage.getItem("nusamation:browser-device-bar") === "1", // second toolbar row, hidden until toggled
  suggest: [],            // address-bar history suggestions (global history)
  suggestIdx: -1,         // keyboard highlight inside the suggestion list
};

// One browser tab's whole view-model: its session, stream, viewport, annotate
// and recording state, and frag — the detached dynamic view children that get
// re-adopted when the tab is reactivated. The pane's visible B is whichever
// tab is active; other tabs keep state (and their server-side sessions) until
// the tab closes or the room is deleted.
function freshBrowserState() {
  return {
    session: null,        // sessionView from create/list/get
    socket: null,
    meta: null,           // last meta control message
    nav: { url: "", title: "", canGoBack: false, canGoForward: false },
    status: "idle",       // idle | connecting | live | closed | error
    statusText: "",
    closedCode: "",
    annotate: false,
    pendingPick: null,    // {pickId, descriptor, upload, marker, comment, includeScreenshot, submitted}
    pendingRec: null,     // {comment} draft while the recording-attach popover is open
    picking: false,
    gotFrame: false,
    frameUrl: "",
    prevFrameUrl: "",     // may still be displayed while frameUrl loads
    pendingPointer: null, // {x, y, kind} from browser_use, placed over the frame
    holdViewport: false,  // agent drive keeps the remote viewport that the JPEG used
    viewportGuest: false, // attached over another live viewer: adopt its viewport, don't resize
    sawMeta: false,       // first stream meta arrived (gates the attach-time emulate)
    inputOwner: false,    // this pane holds the input lease (control.owner === "you")
    agentLock: false,     // the agent holds the input lease (control.owner === "agent")
    navigating: false,
    paneGen: 0,           // per-tab async epoch — park/adopt bump it globally
    restoreURL: "",
    savedURL: "",
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
    devtools: false,      // developer tools drawer
    deviceChoice: "relative", // relative | desktop | mobile | tablet | "" (agent-set)
    recording: null,      // RecMeta of the bound session's live recording (server-owned state)
    recReady: null,       // finalized recording meta not yet sent to the agent
    recNote: "",          // one-line recording notice (e.g. it was interrupted)
    recBusy: false,       // a recording control request is in flight
    recPollTimer: null,   // ~5s meta poll while a recording may be active
    recTickTimer: null,   // 1s chip clock while the status is "recording"
    regionDrag: null,     // {pointerId, x0, y0, x1, y1, active} rubber-band annotate pick
    regionSuppress: false,// swallow the click a finished region drag leaves behind
    buttonsHeld: 0,       // primary-button bitmask forwarded to the page (drag state)
    clickCount: 1,        // clickCount for the press currently held down
    lastClickAt: 0,       // last release time — multi-click counting
    lastClickPos: null,   // last release client pos — multi-click counting
    lastClickCount: 1,
    frag: null,           // parked dynamic view children (markers, hover, region…)
    parked: true,         // no runtime yet (or shelved while hidden) — the next browser activation adopts and attaches
    newTab: false,        // created by "+" and not yet attached — create a fresh session, never adopt
    attachGen: 0,         // latest attachOrCreate run on this state wins — older runs bail at their next checkpoint
  };
}

// The active browser tab's state. Re-pointed on tab switches — never assigned
// into; assignments go through B.<field> so they land on the owning tab.
// ASYNC RULE: a function that awaits must capture `const b = B` first, write
// results through b, and gate DOM effects (toolbars, popovers, toasts) on
// `B === b` — the await may resume after the user switched tabs.
let B = freshBrowserState();

// Global monotonic epoch for B.paneGen: park/adopt bump it so a stale async
// continuation can never collide with another tab's generation number.
let paneEpoch = 1;

const MOVE_INTERVAL_MS = 33;      // ~30fps pointermove forwarding
const DRAG_MOVE_INTERVAL_MS = 55; // coarser sampling while a button/finger is held — expensive pages get steadier pacing
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

export function browserIsOpen() { return P.open; }

export function toggleBrowserPane() {
  if (P.open) closeBrowserPane();
  else { B.holdViewport = false; openBrowserPane(); }
}

// The shell re-rendered (login/logout, boot): drop local state. Server-side
// sessions stay alive — the pane re-attaches on next open.
export function resetBrowserPane() {
  closeDevToolsFrame();
  disconnectStream();
  hideSavedLogins();
  stopResizeWatch();
  closePopover();
  resetAllPaneTabs();
  disposeBrowserState(B);
  B = freshBrowserState();
  P.open = false;
  const loginOffer = $("#browser-login-offer"); if (loginOffer) { loginOffer.hidden = true; loginOffer.dataset.id = ""; }
  bootDone = false;
  bootSkip.clear();
}

// Called whenever the composer key (visible conversation) changes. The pane is
// bound to one conversation; a switch hides it — tabs and their sessions stay
// per-room in paneTabsByConv, so reopening the room restores exactly what was
// on screen. A pane opened on the new-conversation draft (key "") is adopted
// by whatever conversation becomes visible next — including the conversation
// created by the send itself — instead of being closed mid-flow.
onComposerSwitch((key) => {
  const next = key || "";
  if (!P.open) { P.conversationId = next; return; }
  if (next === P.conversationId) return;
  if (P.conversationId === "") {
    // Draft adoption: move the draft's tab state wholesale into the new room
    // so an open pane survives the send that materializes the conversation —
    // but only when the room has no tab set of its own. Overwriting a lived-in
    // room's entry would orphan its tabs (sessions, sockets, tool els leak).
    const st = paneTabsByConv.get("");
    if (st && next && !paneTabsByConv.has(next)) {
      paneTabsByConv.delete("");
      paneTabsByConv.set(next, st);
      for (const t of st.tabs) {
        t.convId = next;
        t.roots = null; // draft-scoped roots — refetch under the real room
        // Browser sessions created on the draft are conversationId "" — claim
        // them for the materialized room so drives and recordings bind.
        const s = t.state?.session;
        if (t.kind === "browser" && s?.id && !s.conversationId) {
          s.conversationId = next;
          api(`/browser/sessions/${encodeURIComponent(s.id)}/claim`, { method: "POST", body: { conversationId: next } })
            .catch((e) => console.warn("browser session claim failed", e));
        }
      }
      P.conversationId = next;
      // A draft-bound recording can never satisfy the send check — every
      // conversation id is real — so its "ready to send" state does not carry.
      B.recReady = null;
      B.recNote = "";
      renderRecBar();
      saveBrowserOpen(next, true);
      const url = usableBrowserURL(B.nav.url);
      if (url) saveLastURL(next, url);
      renderPaneTabs();
      // An attach run killed by the key switch never re-fires — restart it
      // for the browser tab still on screen or the pane dead-ends on
      // "connecting" forever.
      const live = st.tabs.find((t) => t.id === st.active);
      if (live?.kind === "browser" && B === live.state && !B.session) {
        attachOrCreate({ fresh: B.newTab });
      }
      return;
    }
  }
  // Hiding on switch is not the close control, so the pane still restores
  // when this conversation is the one on screen after a refresh.
  closeBrowserPane({ persist: false });
  P.conversationId = next;
});

export function openBrowserPane(initialURL, opts = {}) {
  if (P.open) return;
  const pane = $("#browser-pane");
  if (!pane) return;
  P.open = true;
  P.conversationId = composerKey;
  const st = convTabs(P.conversationId);
  // Later opens re-point B at the browser tab that was last on screen in this
  // room — never absorb the previous room's B into this room's tabs.
  B = browserStateOf(st);
  B.paneGen = ++paneEpoch;
  B.savedURL = "";
  const known = usableBrowserURL(initialURL) || usableBrowserURL(cachedBrowserMeta(P.conversationId)?.lastUrl);
  B.restoreURL = known;
  if (known) B.nav = { ...B.nav, url: known };
  if (P.conversationId) saveBrowserOpen(P.conversationId, true);
  // An explicit open lifts the drive suppression a close put in place.
  bootSkip.delete(P.conversationId);
  buildPane(pane);
  setPaneVisible(true);
  // activatePaneTab owns adoption: a browser target re-adopts its parked state
  // (attaching the bound session or creating a fresh-tab one), while a tool
  // tab leaves the browser parked — its own activation attaches later.
  activatePaneTab(st.active, { deferAttach: opts.deferAttach });
}

export function closeBrowserPane(opts = {}) {
  if (!P.open) return;
  closeDevToolsFrame();
  const key = P.conversationId;
  const persist = opts.persist !== false;
  parkBrowserTab({ annotate: false });
  P.open = false;
  syncTabEls();
  if (persist && key) {
    bootSkip.add(key);
    saveBrowserOpen(key, false);
    const url = usableBrowserURL(B.nav.url);
    if (url) saveLastURL(key, url);
  }
  setPaneVisible(false);
}

// parkBrowserTab shelves the live browser tab: the stream closes (the one
// expensive per-tab resource — the session stays alive server-side), timers
// and transient overlays are cancelled, in-flight async is disowned via the
// global epoch, and the view's dynamic children (markers, hover, pointer…)
// move into the state's frag so the next adopt restores them with listeners
// intact. annotate=false mirrors the old pane-close behaviour of also
// switching annotate mode off; a plain tab switch keeps it per-tab.
function parkBrowserTab({ annotate = true } = {}) {
  B.parked = true;
  if (!annotate && B.annotate) setAnnotate(false);
  // A pending pick/attach draft loses its popover on every park — it can
  // never be submitted from the other side, so abandon it rather than park
  // an orphaned pin.
  if (B.pendingPick) { B.pendingPick = null; postClearHighlight(); }
  B.pendingRec = null;
  disconnectStream();
  cancelRegionDrag();
  stopResizeWatch();
  stopRecPoll();
  stopRecTick();
  hideSavedLogins();
  hideHover();
  hideSuggest();
  closePopover();
  if (B.hoverTimer) { clearTimeout(B.hoverTimer); B.hoverTimer = null; }
  viewTouch = null; viewTouchStart = null; // a mid-touch park leaves a dead identifier
  const offer = $("#browser-login-offer"); if (offer) { offer.hidden = true; offer.dataset.id = ""; }
  const view = $("#browser-view");
  if (view) {
    for (const ch of view.querySelectorAll(".browser-pick-marker, .browser-anchor")) ch.remove();
    const frag = B.frag || (B.frag = document.createDocumentFragment());
    for (const ch of view.querySelectorAll("#browser-agent-pointer, #browser-hover, #browser-region"))
      frag.appendChild(ch);
  }
  B.paneGen = ++paneEpoch;
}

// adoptBrowserTab mounts the state's parked children and resumes its session:
// a bound session re-attaches (fresh stream + rec resync); a state flagged
// `newTab` creates its own session instead of adopting the room's first live
// one — adopting would hijack a sibling tab's surface.
function adoptBrowserTab(opts = {}) {
  B.parked = false;
  B.paneGen = ++paneEpoch;
  const view = $("#browser-view");
  if (view && B.frag) {
    view.appendChild(B.frag);
    B.frag = null;
  }
  const img = $("#browser-frame");
  if (img) img.src = B.frameUrl || "";
  view?.classList.toggle("annotate", B.annotate);
  updateAnnotateUI();
  updateToolbar();
  renderState();
  renderWatch();
  renderRecBar();
  updateDeviceBar();
  selectBrowserTab(B.devtools);
  // A sessionless state re-attaches whatever applies — a killed in-flight
  // attach (epoch-bumped while parked) must retry instead of dead-ending on
  // the "connecting" spinner. attachGen makes this call supersede that run.
  // deferAttach: a drive is about to bind the session itself — don't churn.
  if (!opts.deferAttach) {
    if (B.session) attach(B.session);
    else attachOrCreate({ fresh: B.newTab });
  }
}

// browserStateOf resolves the state object the pane should render for st —
// the last browser tab that was on screen, defaulting to the primary one.
function browserStateOf(st) {
  const t = st.tabs.includes(st.browserTab) ? st.browserTab : st.tabs.find((x) => x.kind === "browser");
  st.browserTab = t || null;
  if (t) return t.state || (t.state = freshBrowserState());
  // No browser tab — B rests on a parked placeholder so incidental writes to
  // B.* always land on a valid state object.
  return st.idle || (st.idle = freshBrowserState());
}

// disposeBrowserState kills a tab's live resources and deletes its session
// server-side — the tab itself (and its room ownership) is gone, so the
// session must not linger orphaned until the idle sweep.
function disposeBrowserState(b) {
  if (!b) return;
  try { b.socket?.close(); } catch {}
  for (const t of [b.recPollTimer, b.hoverTimer, b.moveTimer, b.resizeTimer]) if (t) clearTimeout(t);
  if (b.recTickTimer) clearInterval(b.recTickTimer);
  if (b.frameUrl) { try { URL.revokeObjectURL(b.frameUrl); } catch {} }
  if (b.prevFrameUrl) { try { URL.revokeObjectURL(b.prevFrameUrl); } catch {} }
  if (b.session?.id) api(`/browser/sessions/${encodeURIComponent(b.session.id)}`, { method: "DELETE" }).catch(() => {});
  b.session = null;
  b.frag = null;
}

let bootDone = false;
// Rooms whose pane the user explicitly closed this page session: neither a
// boot restore nor a browser.drive may reopen them. A manual open lifts it.
const bootSkip = new Set();

function stillPane(gen, key) {
  return P.open && B.paneGen === gen && P.conversationId === key;
}

function noteURL(url) {
  const u = usableBrowserURL(url);
  if (!u || !P.conversationId || u === B.savedURL) return;
  B.savedURL = u;
  saveLastURL(P.conversationId, u);
}

// querySuggest results land on the shared suggest box — drop a response that
// belongs to a tab that is no longer on screen (profiles can differ per tab).

async function startURL(key) {
  const pinned = usableBrowserURL(B.restoreURL);
  if (pinned) return pinned;
  const browser = await whenBrowserMeta(key);
  return usableBrowserURL(browser?.lastUrl);
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
    <div class="browser-tabs" role="tablist" aria-label="Panel views">
      <button class="browser-tab pane-home-tab" role="tab" aria-selected="false" data-action="pane-home" title="Pane home — pick a view" aria-label="Pane home">${icon("panel-left", "sm")}</button>
      <span class="browser-tabs-end"><button class="icon-btn sm" data-action="browser-new-tab" title="New tab" aria-label="New tab" aria-haspopup="true" aria-expanded="false">${icon("plus", "sm")}</button><button class="icon-btn sm browser-tabs-close" data-action="browser-close" title="Close pane" aria-label="Close pane">${icon("x", "sm")}</button></span>
    </div>
    <div class="browser-toolbar">
      <button class="icon-btn sm" data-action="browser-back" title="Back" aria-label="Back">${icon("chevron-left", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-forward" title="Forward" aria-label="Forward">${icon("chevron-right", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-reload" title="Reload" aria-label="Reload">${icon("refresh", "sm")}</button>
      <div class="browser-addr"><input class="input" id="browser-url" placeholder="Enter a URL" spellcheck="false" autocomplete="off" aria-label="Browser address"><div class="browser-suggest" id="browser-suggest" hidden></div></div>
      <button class="icon-btn sm" id="browser-vault-button" title="Saved logins" aria-label="Saved logins" aria-expanded="false">${icon("key", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-annotate" title="Annotate an element" aria-label="Annotate an element" aria-pressed="false">${icon("pin", "sm")}</button>
      <button class="icon-btn sm" id="browser-annotate-page" data-action="browser-annotate-page" title="Annotate the whole page" aria-label="Annotate the whole page" hidden>${icon("file", "sm")}</button>
      <button class="icon-btn sm browser-rec-start" data-action="browser-record" title="Record this session" aria-label="Record this session">${icon("video", "sm")}</button>
      <button class="icon-btn sm" id="browser-devtools-btn" data-action="browser-devtools" title="Dev tools" aria-label="Dev tools">${icon("terminal", "sm")}</button>
      <button class="icon-btn sm" data-action="browser-device" title="Device emulation" aria-label="Device emulation" aria-pressed="true">${icon("smartphone", "sm")}</button>
      <span class="browser-profile-wrap" title="Persistent browser profile"><button class="device-chip browser-profile" id="browser-profile" data-action="browser-profile-picker" aria-label="Browser profile" title="Browser profile">${icon("user", "sm")}</button></span>
      <button class="icon-btn sm browser-menu-btn" data-action="browser-menu" title="More browser actions" aria-label="More browser actions">${icon("more-v", "sm")}</button>
    </div>
    <div class="browser-rec-bar" id="browser-rec-bar" hidden></div>
    <div class="browser-login-offer" id="browser-login-offer" hidden></div>
    <div class="browser-vault-pop" id="browser-vault-pop" hidden></div>
    <div class="browser-device-bar" id="browser-device-bar">
      <button class="device-chip" data-action="browser-device-mode" data-device="relative" title="The remote viewport follows this pane — no fixed size, no letterboxing">Relative</button>
      <button class="device-chip" data-action="browser-device-mode" data-device="desktop" title="Fixed 1280×800 desktop profile">Desktop</button>
      <button class="device-chip" data-action="browser-device-mode" data-device="mobile" title="Fixed 390×844 mobile profile with a touch UA">Mobile</button>
      <button class="device-chip" data-action="browser-device-mode" data-device="tablet" title="Fixed 768×1024 tablet profile with a touch UA">Tablet</button>
      <span class="browser-device-readout" id="browser-device-readout"></span>
    </div>
    <iframe class="browser-devtools-frame" id="browser-devtools-frame" title="Chrome DevTools" hidden></iframe>
    <div class="browser-view" id="browser-view" tabindex="0" aria-label="Remote browser view">
      <img class="browser-frame" id="browser-frame" alt="Remote page" draggable="false">
      <textarea id="browser-touch-input" class="browser-touch-input" tabindex="-1" aria-label="Remote browser keyboard" autocomplete="off" autocapitalize="off" spellcheck="false"></textarea>
      <div class="browser-state" id="browser-state"></div>
      <div class="browser-watch" id="browser-watch"></div>
    </div>
    <div class="pane-tools" id="pane-tools">
      <div class="pane-home" id="pane-home" hidden>
        <div class="pane-home-head">
          <div class="pane-home-title">Side panel</div>
          <div class="pane-home-sub">Pick a view — nothing runs until you open one.</div>
        </div>
        <div class="pane-home-cards">
          <button class="pane-card" data-action="pane-card" data-kind="browser">
            <span class="pane-card-icon">${icon("globe")}</span>
            <span class="pane-card-text"><span class="pane-card-title">Browser</span><span class="pane-card-desc">Embedded web browsing</span></span>
            ${icon("chevron-right", "sm pane-card-go")}
          </button>
          <button class="pane-card" data-action="pane-card" data-kind="terminal">
            <span class="pane-card-icon">${icon("terminal")}</span>
            <span class="pane-card-text"><span class="pane-card-title">Terminal</span><span class="pane-card-desc">Shell rooted in this workspace</span></span>
            ${icon("chevron-right", "sm pane-card-go")}
          </button>
          <button class="pane-card" data-action="pane-card" data-kind="files">
            <span class="pane-card-icon">${icon("folder")}</span>
            <span class="pane-card-text"><span class="pane-card-title">File manager</span><span class="pane-card-desc">Browse and preview files</span></span>
            ${icon("chevron-right", "sm pane-card-go")}
          </button>
        </div>
      </div>
    </div>`;
  bindPane(pane);
}

function bindPane(pane) {
  const offer = $("#browser-login-offer", pane);
  offer.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-offer-action]");
    if (!button || !B.session || !offer.dataset.id) return;
    const session = B.session.id, id = offer.dataset.id;
    const action = button.dataset.offerAction;
    if (action === "toggle-password") {
      const input = $("[data-offer-password]", offer);
      if (input) { input.type = input.type === "password" ? "text" : "password"; button.textContent = input.type === "password" ? "Show" : "Hide"; }
      return;
    }
    if (action === "edit") {
      button.disabled = true;
      try {
        const data = await api(`/browser/sessions/${encodeURIComponent(session)}/login-offer?id=${encodeURIComponent(id)}`);
        if (B.session?.id !== session || offer.dataset.id !== id) return;
        const fields = $(".browser-login-fields", offer);
        fields.hidden = false;
        $("[data-offer-username]", fields).value = data.username || "";
        $("[data-offer-password]", fields).value = data.password || "";
        button.hidden = true;
        $("[data-offer-username]", fields).focus();
      } catch (e) { if (B.session?.id === session && offer.dataset.id === id) { button.disabled = false; toast("Could not edit login", e.message, "error"); } }
      return;
    }
    const save = action === "save";
    const fields = $(".browser-login-fields", offer);
    const body = { id, save };
    if (save && fields && !fields.hidden) {
      body.username = $("[data-offer-username]", fields).value;
      body.password = $("[data-offer-password]", fields).value;
      if (!body.password) { $("[data-offer-password]", fields).focus(); toast("Password required", "Enter a password to save this login.", "error"); return; }
    }
    for (const el of offer.querySelectorAll("button")) el.disabled = true;
    try { await api(`/browser/sessions/${encodeURIComponent(session)}/login-offer`, { method: "POST", body }); }
    catch (e) { if (B.session?.id === session && offer.dataset.id === id) { for (const el of offer.querySelectorAll("button")) el.disabled = false; toast("Could not update password manager", e.message, "error"); } }
  });
  $("#browser-vault-button", pane).addEventListener("click", openSavedLogins);
  $("#browser-vault-pop", pane).addEventListener("click", async (event) => {
    const close = event.target.closest("[data-vault-close]");
    if (close) { hideSavedLogins(); return; }
    const button = event.target.closest("[data-vault-id]");
    const box = $("#browser-vault-pop");
    if (!button || !B.session || !box?.dataset.origin) return;
    const session = B.session.id, origin = box.dataset.origin;
    for (const el of box.querySelectorAll("button")) el.disabled = true;
    try {
      const result = await api(`/browser/sessions/${encodeURIComponent(session)}/autofill`, { method: "POST", body: { id: button.dataset.vaultId, origin } });
      if (B.session?.id !== session) return;
      hideSavedLogins();
      if (!result.filled) toast("Could not fill login", "No editable login field was found on this page.", "info");
      else $("#browser-view")?.focus();
    } catch (e) {
      if (B.session?.id === session && box.dataset.origin === origin) { for (const el of box.querySelectorAll("button")) el.disabled = false; toast("Could not fill login", e.message, "error"); }
    }
  });
  const view = $("#browser-view", pane);
  const url = $("#browser-url", pane);
  url.addEventListener("keydown", (e) => {
    e.stopPropagation();
    const rows = $$(".browser-suggest-row", $("#browser-suggest") || document.createElement("div"));
    const pick = () => { const i = P.suggestIdx >= 0 ? P.suggestIdx : 0; const s = P.suggest[i]; if (rows[i] && s) { url.value = s.url; browserNavigate(s.url); return true; } return false; };
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      if (P.suggest.length) {
        e.preventDefault();
        const step = e.key === "ArrowDown" ? 1 : -1;
        P.suggestIdx = (P.suggestIdx + step + P.suggest.length + 1) % (P.suggest.length + 1) - 1;
        renderSuggest();
      }
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      if (!pick()) browserNavigate(url.value);
      hideSuggest();
    } else if (e.key === "Escape") {
      if (!hideSuggest()) { url.value = B.nav.url || ""; url.blur(); }
    }
  });
  url.addEventListener("focus", () => { url.select(); querySuggest(""); });
  url.addEventListener("input", () => querySuggest(url.value));
  url.addEventListener("blur", () => setTimeout(hideSuggest, 150));
  view.addEventListener("pointerdown", (e) => {
    if (e.pointerType !== "touch") view.focus();
    B.regionSuppress = false;
    // Region annotate: in annotate mode a press on the frame may become a
    // drag; the 5px threshold in onViewMove decides band vs plain click pick.
    if (!B.annotate || B.picking || B.regionDrag || e.button !== 0) return;
    if (e.target.id !== "browser-frame") return;
    B.regionDrag = { pointerId: e.pointerId, x0: e.clientX, y0: e.clientY, x1: e.clientX, y1: e.clientY, active: false };
  });
  // Normal input: forward the press halves separately so press→move→release
  // reaches the page as a real drag instead of an atomic click. Touch has its
  // own phased path; aux buttons stay on the atomic click frame.
  view.addEventListener("pointerdown", (e) => {
    if (B.annotate || !canInput() || e.target.id !== "browser-frame" || e.button !== 0 || e.pointerType === "touch") return;
    const p = viewportPoint(e);
    if (!p) return;
    const now = Date.now(), lp = B.lastClickPos;
    B.clickCount = (now - B.lastClickAt < 500 && lp && Math.hypot(e.clientX - lp.x, e.clientY - lp.y) < 5) ? B.lastClickCount + 1 : 1;
    B.buttonsHeld |= 1;
    sendCtl({ type: "down", x: p.x, y: p.y, button: "left", clickCount: B.clickCount });
  });
  // The matching release — also on pointerleave/cancel so a drag exiting the
  // view cannot leave the page holding a phantom button down.
  const releaseHeld = (e) => {
    if (!(B.buttonsHeld & 1)) return;
    B.buttonsHeld &= ~1;
    B.lastClickAt = Date.now();
    B.lastClickPos = { x: e.clientX, y: e.clientY };
    B.lastClickCount = B.clickCount;
    const p = e.clientX === undefined ? null : viewportPoint(e);
    if (p) sendCtl({ type: "up", x: p.x, y: p.y, button: "left", clickCount: B.lastClickCount });
  };
  // A held press always owes the page its release, even if annotate mode was
  // toggled mid-drag — buttonsHeld is the gate, not the mode.
  view.addEventListener("pointerup", (e) => { if (e.button === 0) releaseHeld(e); });
  view.addEventListener("pointercancel", releaseHeld);
  view.addEventListener("pointerleave", releaseHeld);
  view.addEventListener("pointerup", onRegionUp);
  view.addEventListener("pointercancel", onRegionCancel);
  view.addEventListener("pointermove", onViewMove);
  for (const type of ["touchstart", "touchmove", "touchend", "touchcancel"]) {
    view.addEventListener(type, onViewTouch, { passive: false });
  }
  view.addEventListener("click", onViewClick);
  view.addEventListener("auxclick", onViewAuxClick);
  view.addEventListener("contextmenu", (e) => e.preventDefault());
  view.addEventListener("wheel", onViewWheel, { passive: false });
  view.addEventListener("keydown", onViewKey);
  const touchInput = $("#browser-touch-input", pane);
  touchInput.addEventListener("input", onTouchInput);
  touchInput.addEventListener("compositionend", onTouchInput);
  touchInput.addEventListener("beforeinput", (e) => {
    if (!canInput() || e.isComposing) return;
    if (e.inputType === "deleteContentBackward" || e.inputType === "deleteContentForward") {
      sendCtl({ type: "key", key: e.inputType === "deleteContentBackward" ? "Backspace" : "Delete", modifiers: 0 });
      e.preventDefault();
    } else if (e.inputType === "insertLineBreak" || e.inputType === "insertParagraph") {
      sendCtl({ type: "key", key: "Enter", modifiers: 0 });
      e.preventDefault();
    }
  });
  view.addEventListener("pointerleave", () => { if (B.annotate && !B.pendingPick) hideHover(); });
  const profileBtn = $("#browser-profile", pane);
  if (profileBtn) profileBtn.addEventListener("keydown", (e) => e.stopPropagation());
  const grip = $("#browser-resize", pane);
  grip.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    grip.setPointerCapture(e.pointerId);
    const startX = e.clientX;
    const startW = pane.getBoundingClientRect().width;
    const move = (ev) => {
      P.width = clamp(Math.round(startW + (startX - ev.clientX)), 320, Math.round(window.innerWidth * 0.8));
      applyBrowserWidth();
    };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", () => grip.removeEventListener("pointermove", move), { once: true });
    grip.addEventListener("pointercancel", () => grip.removeEventListener("pointermove", move), { once: true });
  });
}

function applyBrowserWidth() {
  const app = $(".app");
  if (app && P.width) app.style.setProperty("--browser-w", P.width + "px");
}

/* ---------------- session lifecycle ---------------- */

async function attachOrCreate(opts = {}) {
  const b = B;
  // Every new run supersedes the previous one on this state: a stale run bails
  // at its next checkpoint instead of finishing a second session underneath
  // the live one (double Reconnect clicks, adopt racing an in-flight attach).
  const agen = (b.attachGen = (b.attachGen || 0) + 1);
  const gen = b.paneGen;
  const key = P.conversationId;
  const still = () => stillPane(gen, key) && b.attachGen === agen;
  setStatus("connecting", "Looking for a browser session…");
  try {
    // A sibling tab's bound session must never be adopted here — it would
    // pull that session's stream/view into this tab too. opts.fresh (new-tab
    // attach) skips adoption entirely: the point of a new tab is a new page.
    const d = opts.fresh ? { sessions: [] } : await api("/browser/sessions");
    if (!still()) return;
    // Bound-set computed after the list await — a sibling tab binding a
    // session mid-flight must still be excluded here.
    const bound = new Set();
    if (!opts.fresh) {
      const st = curTabs();
      for (const t of st.tabs) {
        if (t?.state && t.state !== b && t.state.session?.id) bound.add(t.state.session.id);
      }
    }
    const sessions = d.sessions || [];
    const usable = (s) => (s.conversationId || "") === key || !s.conversationId;
    const live = sessions.find((s) => s.status !== "crashed" && !bound.has(s.id) && (s.conversationId || "") === key)
      || sessions.find((s) => s.status !== "crashed" && !bound.has(s.id) && !s.conversationId);
    // An unclaimed session adopted into a real room must be claimed — else
    // its recordings stay orphaned and other rooms can adopt it too.
    const claimIfOrphaned = (live) => {
      if (!key || live.conversationId) return;
      live.conversationId = key;
      api(`/browser/sessions/${encodeURIComponent(live.id)}/claim`, { method: "POST", body: { conversationId: key } })
        .catch((e) => console.warn("browser session claim failed", e));
    };
    if (live) {
      claimIfOrphaned(live);
      const blank = !usableBrowserURL(live.url);
      if (blank) {
        const saved = await startURL(key);
        if (!still()) return;
        if (saved) {
          B.restoreURL = saved;
          B.nav = { ...B.nav, url: saved };
        }
        attach(live);
        if (saved) await browserNavigate(saved);
        return;
      }
      attach(live);
      return;
    }
    const crashed = sessions.find((s) => s.status === "crashed" && usable(s));
    if (crashed) {
      try { await api(`/browser/sessions/${encodeURIComponent(crashed.id)}`, { method: "DELETE" }); } catch {}
    }
    if (!still()) return;
    await createSession(gen, key, still);
  } catch (e) {
    if (still()) setStatus("error", e.message || "Could not reach the browser service.");
  }
}

async function createSession(gen, key, still) {
  setStatus("connecting", "Starting the browser…");
  const url = await startURL(key);
  if (!still()) return;
  if (url) {
    B.restoreURL = url;
    B.nav = { ...B.nav, url };
    updateToolbar();
  }
  const box = viewBox();
  const body = {
    conversationId: key || "",
    width: clamp(Math.round(box.w) || 1280, 200, 4096),
    height: clamp(Math.round(box.h) || 800, 200, 4096),
  };
  if (url) body.url = url;
  try {
    const d = await api("/browser/sessions", { method: "POST", body });
    if (!still()) {
      // The pane closed or the conversation changed while Chromium launched.
      try { await api(`/browser/sessions/${encodeURIComponent(d.session.id)}`, { method: "DELETE" }); } catch {}
      return;
    }
    B.restoreURL = "";
    attach(d.session);
  } catch (e) {
    if (still()) setStatus("error", e.message || "The browser could not be started.");
  }
}

function attach(session) {
  B.session = session;
  B.newTab = false;
  if (B.devtools) selectBrowserTab(true);
  B.meta = null;
  B.gotFrame = false;
  B.viewportGuest = false;
  B.sawMeta = false;
  B.inputOwner = false;
  B.agentLock = false;
  applyNavState(session);
  connectStream();
  refreshProfiles();
  // Recording state is per session — resync on every attach (pane reopen,
  // stream replace, session swap) since the backend owns it.
  resetRecState();
  recSync();
}

// Exported for the "Reconnect" affordances — re-runs attach-or-create.
export function browserReconnect() { attachOrCreate({ fresh: B.newTab }); }

// showAgentBrowser opens this conversation's pane onto the session the
// browser_use tool is driving, and remembers a pointer until a frame can place it.
export async function showAgentBrowser(sessionId, pointer, tool, convId) {
  if (!convId || composerKey !== convId) return;
  if (!P.open) {
    // A pane the user closed stays closed — reopening it on every tool call
    // steals the screen (on mobile the pane covers the whole view). bootSkip
    // covers this page session; the persisted flag survives a refresh.
    if (bootSkip.has(convId) || cachedBrowserMeta(convId)?.open === false) return;
    // deferAttach: the fetch below attaches the driving session itself, so a
    // parallel attachOrCreate cannot adopt a different session for the room
    // or steer it to a stale lastUrl mid-drive.
    openBrowserPane(undefined, { deferAttach: true });
    // A drive-opened pane must land on the page view — restoring a persisted
    // terminal/files tab would stream the drive invisibly in the background.
    activatePaneTab("browser", { deferAttach: true });
  } else {
    // Multi-tab: the session the agent is driving may already be bound to a
    // tab — put that tab on screen instead of hijacking the visible one.
    // deferAttach: the fetch below (re)attaches the driven session itself.
    const st = curTabs();
    const owner = st.tabs.find((t) => t?.kind === "browser" && t.state?.session?.id === sessionId);
    if (owner && st.active !== owner.id) activatePaneTab(owner.id, { deferAttach: true });
    else if (!owner && st.tabs.find((t) => t.id === st.active)?.kind !== "browser")
      activatePaneTab(st.browserTab?.id || "browser", { deferAttach: true });
  }
  // Drive-side device/pointer state applies to the session's owning tab —
  // resolved below — so it lands on B only after any owner swap.
  const applyDriveOpts = () => {
    if (tool === "emulate") {
      // The agent changed the device profile itself — drop the user's chip.
      B.deviceChoice = "";
      updateDeviceBar();
    }
    // Hold the pane's viewport override only while the agent owns the device
    // (its own emulate). A user choice — relative or preset — keeps driving
    // the viewport even during a drive, so the pane and agent stay in sync.
    B.holdViewport = B.deviceChoice === "";
    if (B.holdViewport) stopResizeWatch();
    else applyDeviceChoice();
    if (pointer && Number.isFinite(pointer.x) && Number.isFinite(pointer.y)) {
      B.pendingPointer = pointer;
    }
  };
  if (!sessionId || B.session?.id === sessionId) {
    applyDriveOpts();
    placeAgentPointer();
    if (!sessionId && !B.session) attachOrCreate({ fresh: B.newTab });
    return;
  }
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(sessionId)}`);
    if (!P.open || !d.session) return;
    // The room on screen may have changed while the fetch was in flight —
    // only attach a session that belongs to the driving conversation.
    if (P.conversationId !== convId) return;
    if ((d.session.conversationId || "") !== convId) return;
    if (B.session?.id !== d.session.id) {
      // One session per tab: a driven session bound to no visible tab gets
      // its own — stealing the on-screen tab's session would orphan the page
      // the user was browsing.
      const st = convTabs(P.conversationId);
      let owner = st.tabs.find((t) => t?.kind === "browser" && t.state?.session?.id === d.session.id);
      if (!owner) {
        const n = st.tabs.filter((t) => t.kind === "browser").length + 1;
        owner = { id: uid("tab"), kind: "browser", title: n === 1 ? "Browser" : `Browser ${n}`, convId: P.conversationId, el: null, state: freshBrowserState() };
        st.tabs.push(owner);
      }
      activatePaneTab(owner.id, { deferAttach: true });
      // Adopt may have skipped attaching (deferAttach) — (re)bind the stream
      // unless this state is already streaming the driven session.
      if (B.session?.id !== d.session.id || !B.socket) attach(d.session);
    }
    applyDriveOpts();
    placeAgentPointer();
  } catch {}
}

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
    // The emulate decision waits for the first meta: a stream that replaced
    // another viewer adopts the established viewport instead of resizing.
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
    if (B.devtools) closeDevToolsFrame();
    stopResizeWatch();
    if (!P.open || B.status === "closed") return;
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
    case "keyboard_focus": {
      if (msg.request !== keyboardRequest || !canInput()) break;
      const input = $("#browser-touch-input");
      if (msg.editable) input?.focus({ preventScroll: true });
      else if (document.activeElement === input) input.blur();
      break;
    }
    case "meta":
      B.meta = msg;
      if (!B.sawMeta) {
        B.sawMeta = true;
        // This attach replaced another live viewer — the remote viewport is
        // already sized for that client's pane. Forcing our own emulate here
        // thrashes the shared surface and shows a transitional frame where
        // the old render sits beside the new one ("mirror"). Adopt instead:
        // the held size arrives in this meta and frames letterbox to fit.
        if (msg.viewportHeld) B.viewportGuest = true;
        if (!B.holdViewport && !B.viewportGuest) applyDeviceChoice();
      }
      applyMeta();
      break;
    case "control": {
      const wasOwner = B.inputOwner;
      B.inputOwner = msg.owner === "you";
      B.agentLock = msg.owner === "agent";
      if (!B.inputOwner) {
        if (B.devtools) closeDevToolsFrame();
        hideSavedLogins();
        // An ex-controller stops resizing the shared viewport; its pane
        // resizes would otherwise spam the socket and stretch the frame.
        stopResizeWatch();
      } else if (!wasOwner && !B.viewportGuest && !B.holdViewport) {
        // The lease grant is the sizing push: control arrives after the
        // attach meta, so a first-time owner emulates here, not on open.
        applyDeviceChoice();
      }
      renderWatch();
      updateToolbar();
      break;
    }
    case "login_offer": {
      const box = $("#browser-login-offer");
      if (!box) break;
      // Only the input owner answers a save prompt — a watching pane's
      // answer would silently take the other side's credentials.
      const offer = B.inputOwner ? msg.offer : null;
      if (offer && box.dataset.id === offer.id) break;
      box.hidden = !offer;
      box.dataset.id = offer?.id || "";
      box.innerHTML = offer ? `<div class="browser-login-summary"><b>Save to Password Manager?</b><span>${esc(offer.origin)}${offer.username ? ` · ${esc(offer.username)}` : ""}</span></div><div class="browser-login-fields" hidden><label>Username or email<input class="input" data-offer-username autocomplete="off"></label><label>Password<div class="browser-login-password"><input class="input" type="password" data-offer-password autocomplete="new-password"><button type="button" class="btn btn-sm" data-offer-action="toggle-password">Show</button></div></label></div><div class="browser-login-actions"><button class="btn btn-sm" data-offer-action="edit">Edit</button><button class="btn btn-sm" data-offer-action="dismiss">Not now</button><button class="btn btn-sm btn-primary" data-offer-action="save">Save</button></div>` : "";
      break;
    }
    case "login_choices":
      if (B.inputOwner && !B.agentLock && sameBrowserOrigin(msg.origin, B.nav.url)) showSavedLogins(msg.origin, msg.credentials || []);
      break;
    case "closed":
      setStatus("closed", msg.message || "", msg.code || "");
      break;
    case "pointer":
      B.pendingPointer = { x: msg.x, y: msg.y, kind: msg.kind || "move" };
      placeAgentPointer();
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
  placeAgentPointer();
  if (!B.gotFrame) {
    B.gotFrame = true;
    renderState();
  }
  renderWatch();
}

function placeAgentPointer() {
  const p = B.pendingPointer;
  const view = $("#browser-view");
  const img = $("#browser-frame");
  if (!p || !view || !img) return;
  const r = img.getBoundingClientRect();
  const vr = view.getBoundingClientRect();
  if (!r.width || !r.height) return;
  const dw = B.meta?.deviceWidth || B.session?.width || r.width;
  const dh = B.meta?.deviceHeight || B.session?.height || r.height;
  let dot = $("#browser-agent-pointer");
  if (!dot) {
    // LightsOut arrow cursor (cursor-agent.png): the CSS offsets the image so
    // its tip hotspot, not its top-left corner, lands on the coordinates.
    dot = document.createElement("img");
    dot.id = "browser-agent-pointer";
    dot.className = "browser-agent-pointer";
    dot.src = "/static/components/cursor-agent.png";
    dot.alt = "";
    dot.draggable = false;
    view.appendChild(dot);
  }
  dot.style.left = `${(r.left - vr.left) + (p.x * r.width) / dw}px`;
  dot.style.top = `${(r.top - vr.top) + (p.y * r.height) / dh}px`;
  dot.classList.toggle("click", p.kind === "click");
  dot.hidden = false;
}

function applyMeta() {
  const m = B.meta;
  if (!m) return;
  updateDeviceBar();
  const img = $("#browser-frame");
  if (img && m.deviceWidth && m.deviceHeight) img.style.aspectRatio = `${m.deviceWidth} / ${m.deviceHeight}`;
  if (typeof m.url === "string" && m.url && (usableBrowserURL(m.url) || !usableBrowserURL(B.nav.url))) {
    const pop = $("#browser-vault-pop");
    if (pop && !pop.hidden && !sameBrowserOrigin(pop.dataset.origin, m.url)) hideSavedLogins();
    const hadTitle = B.nav.title;
    B.nav.url = m.url;
    B.nav.title = m.title || "";
    if (B.nav.title !== hadTitle) renderPaneTabs();
    noteURL(m.url);
  }
  if (typeof m.canGoBack === "boolean") {
    B.nav.canGoBack = m.canGoBack;
    B.nav.canGoForward = !!m.canGoForward;
  }
  updateToolbar();
}

// applyNavState lands a navigation result on state b (the tab that asked for
// it). Only the on-screen tab feeds the room's lastUrl — a parked tab's page
// is not "where the room was last".
function applyNavState(v, b = B) {
  if (!v) return;
  const pop = $("#browser-vault-pop");
  if (pop && !pop.hidden && v.url && !sameBrowserOrigin(pop.dataset.origin, v.url)) hideSavedLogins();
  if (typeof v.url === "string" && (usableBrowserURL(v.url) || !usableBrowserURL(b.restoreURL))) b.nav.url = v.url;
  if (typeof v.title === "string") b.nav.title = v.title;
  b.nav.canGoBack = !!v.canGoBack;
  b.nav.canGoForward = !!v.canGoForward;
  if (B === b) noteURL(b.nav.url);
  updateToolbar();
  renderPaneTabs(); // browser tab labels track the page they show
}

/* ---------------- REST actions ---------------- */

function sameBrowserOrigin(origin, url) {
  try { return !!origin && new URL(url).origin === origin; } catch { return false; }
}

function hideSavedLogins() {
  const box = $("#browser-vault-pop");
  if (box) { box.hidden = true; box.dataset.origin = ""; box.innerHTML = ""; }
  $("#browser-vault-button")?.setAttribute("aria-expanded", "false");
}

function showSavedLogins(origin, credentials) {
  const box = $("#browser-vault-pop");
  if (!box) return;
  box.dataset.origin = origin || "";
  box.hidden = false;
  box.innerHTML = `<div class="browser-vault-heading"><b>Saved logins</b><button class="icon-btn sm" type="button" data-vault-close aria-label="Close saved logins">${icon("x", "sm")}</button></div><div class="browser-vault-origin">${esc(origin || "This page")}</div><div class="browser-vault-list">${credentials.length ? credentials.map((c) => `<button type="button" class="browser-vault-account" data-vault-id="${esc(c.id)}">${icon("key", "sm")}<span>${esc(c.username || "Password-only login")}</span></button>`).join("") : `<div class="browser-vault-empty">No saved logins for this page.</div>`}</div>`;
  $("#browser-vault-button")?.setAttribute("aria-expanded", "true");
}

export async function openSavedLogins() {
  const box = $("#browser-vault-pop");
  if (!B.session || !B.inputOwner || !box) return;
  if (!box.hidden) { hideSavedLogins(); return; }
  const session = B.session.id, url = B.nav.url;
  box.hidden = false;
  box.innerHTML = `<div class="browser-vault-empty">Loading saved logins…</div>`;
  $("#browser-vault-button")?.setAttribute("aria-expanded", "true");
  try {
    const result = await api(`/browser/sessions/${encodeURIComponent(session)}/autofill`);
    if (B.session?.id !== session || box.hidden || B.nav.url !== url) return;
    showSavedLogins(result.origin, result.credentials || []);
  } catch (e) {
    if (B.session?.id === session && !box.hidden) { hideSavedLogins(); toast("Could not load saved logins", e.message, "error"); }
  }
}

export async function browserNavigate(url) {
  url = String(url || "").trim();
  const b = B, sid = b.session?.id;
  if (!url || !sid || b.navigating) return;
  hideSavedLogins();
  b.navigating = true;
  if (B === b) updateToolbar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(sid)}/navigate`, { method: "POST", body: { url } });
    applyNavState(d, b);
  } catch (e) {
    if (B === b) browserApiError(e, "Navigation failed");
  } finally {
    // Flags live on the originating state — clearing through B after a tab
    // swap would leave b.navigating stuck and deaden that tab's address bar.
    b.navigating = false;
    if (B === b) updateToolbar();
  }
}

export async function browserHistory(action) {
  const b = B, sid = b.session?.id;
  if (!sid || b.navigating) return;
  hideSavedLogins();
  b.navigating = true;
  if (B === b) updateToolbar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(sid)}/history`, { method: "POST", body: { action } });
    applyNavState(d, b);
  } catch (e) {
    if (B === b) browserApiError(e, action === "reload" ? "Reload failed" : "Could not navigate history");
  } finally {
    b.navigating = false;
    if (B === b) updateToolbar();
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

/* ---------------- address bar suggestions ---------------- */

let suggestTimer = null;
let suggestSeq = 0;

function browserProfile() { return B.session?.profile || "default"; }

async function querySuggest(q) {
  const box = $("#browser-suggest");
  if (!box || !B.session?.id) return;
  const seq = ++suggestSeq;
  const b = B;
  clearTimeout(suggestTimer);
  suggestTimer = setTimeout(async () => {
    try {
      const d = await api(`/browser/history?profile=${encodeURIComponent(b.session?.profile || "default")}&q=${encodeURIComponent(q.trim())}`);
      if (seq !== suggestSeq || !P.open || B !== b) return;
      P.suggest = (d.entries || []).slice(0, 8);
      P.suggestIdx = -1;
      renderSuggest();
      // Omnibox inline completion: fill the input with the first history hit
      // that extends the typed text (scheme and leading www. ignored) and
      // select just the suggested tail — the next keystroke replaces the
      // tail, Enter navigates to it.
      const input = $("#browser-url");
      const typed = q.trim();
      if (input && typed && document.activeElement === input && input.value === q) {
        for (const hit of P.suggest) {
          const bare = (hit.url || "").replace(/^https?:\/\//i, "").replace(/^www\./i, "");
          if (bare.length > typed.length && bare.toLowerCase().startsWith(typed.toLowerCase())) {
            input.value = bare;
            input.setSelectionRange(typed.length, bare.length);
            break;
          }
        }
      }
    } catch { /* history is advisory */ }
  }, q.trim() ? 120 : 0);
}

function renderSuggest() {
  const box = $("#browser-suggest");
  if (!box) return;
  if (!P.suggest.length) { box.hidden = true; box.innerHTML = ""; return; }
  box.hidden = false;
  box.innerHTML = P.suggest.map((e, i) => `
    <div class="browser-suggest-row${i === P.suggestIdx ? " active" : ""}" data-action="browser-suggest" data-url="${esc(e.url || "")}">
      <span class="browser-suggest-title">${esc(e.title || e.url || "")}</span>
      <span class="browser-suggest-url">${esc(e.url || "")}</span>
    </div>`).join("");
  for (const row of $$(".browser-suggest-row", box)) {
    row.addEventListener("pointerdown", (e) => e.preventDefault());
    row.addEventListener("click", (e) => {
      e.stopPropagation();
      const picked = row.dataset.url;
      const input = $("#browser-url");
      if (input) input.value = picked;
      hideSuggest();
      browserNavigate(picked);
    });
  }
}

function hideSuggest() {
  ++suggestSeq;
  clearTimeout(suggestTimer);
  const box = $("#browser-suggest");
  const was = box && !box.hidden;
  if (box) { box.hidden = true; box.innerHTML = ""; }
  P.suggest = [];
  P.suggestIdx = -1;
  return was;
}

/* ---------------- developer tools drawer ---------------- */

export function toggleDevtools() {
  selectBrowserTab(true);
}

function closeDevToolsFrame() {
  // Keep the iframe loaded: the DevTools frontend holds its own CDP session
  // and per-tab state (Network waterfall, breakpoints). Blanking src would
  // throw all of it away on every tab toggle. selectBrowserTab reloads only
  // when the session id actually changes.
  selectBrowserTab(false);
}

export function selectBrowserTab(devtools = false) {
  B.devtools = devtools;
  const pane = $("#browser-pane");
  pane?.classList.toggle("devtools-active", devtools);
  const frame = $("#browser-devtools-frame");
  if (frame) {
    frame.hidden = !devtools;
    if (devtools && B.session?.id && frame.dataset.session !== B.session.id) {
      frame.dataset.session = B.session.id;
      // Chrome's remote inspector enables its own page screencast by default.
      // The Browser tab already supplies the page, so start with the inspector
      // filling this tab. DevTools can still toggle its screencast manually.
      try { localStorage.setItem("screencast-enabled", "false"); } catch {}
      const base = `/api/browser/sessions/${encodeURIComponent(B.session.id)}/devtools/`;
      const scheme = location.protocol === "https:" ? "wss" : "ws";
      frame.src = `${base}inspector.html?${scheme}=${encodeURIComponent(location.host + base + "socket")}`;
    }
  }
  const devBtn = $(`[data-action="browser-devtools"]`);
  devBtn?.classList.toggle("active", devtools);
  devBtn?.setAttribute("aria-selected", String(devtools));
  renderPaneTabs(); // the active browser tab shows inactive while DevTools owns the surface
}

/* ---------------- profile picker ---------------- */

let knownProfiles = ["default"];

async function refreshProfiles() {
  const btn = $("#browser-profile");
  if (!btn) return;
  try {
    const d = await api("/browser/profiles");
    const names = (d.profiles || []).map((p) => p.name || p);
    knownProfiles = [...new Set(["default", browserProfile(), ...names])];
  } catch { /* keep the last known list */ }
  const label = `Browser profile: ${browserProfile()}`;
  btn.setAttribute("aria-label", label);
  btn.title = label;
}

// The collapsed-toolbar menu (narrow panes): rows mirror the real buttons'
// disabled/pressed state and dispatch through the same data-action map, so
// the menu can't drift from toolbar behavior.
export function openBrowserMenu(anchor) {
  const rows = [
    { sel: `[data-action="browser-back"]`, icon: "chevron-left", label: "Back" },
    { sel: `[data-action="browser-forward"]`, icon: "chevron-right", label: "Forward" },
    { sel: `[data-action="browser-reload"]`, icon: "refresh", label: "Reload" },
    { sep: true },
    { sel: `[data-action="browser-record"]`, icon: "video", label: "Record session" },
    { sel: "#browser-devtools-btn", icon: "terminal", label: "Dev tools", active: () => $("#browser-pane")?.classList.contains("devtools-active") },
    { sel: "#browser-vault-button", icon: "key", label: "Saved logins", action: "browser-vault" },
    { sel: "#browser-annotate-page", icon: "file", label: "Annotate page" },
    { sel: `[data-action="browser-device"]`, icon: "smartphone", label: "Device emulation" },
    { sep: true },
    { sel: "#browser-profile", icon: "user", label: `Profile: ${browserProfile()}` },
  ];
  const html = [`<div class="browser-menu">`];
  for (const r of rows) {
    if (r.sep) { html.push(`<div class="popover-sep"></div>`); continue; }
    const btn = $(r.sel);
    const action = r.action || btn?.dataset.action || "";
    const attrs = btn?.disabled ? "disabled" : "";
    const active = r.active ? r.active() : btn?.getAttribute("aria-pressed") === "true";
    html.push(`<button class="menu-item${active ? " selected" : ""}" data-action="${action}" ${attrs}>${icon(r.icon, "sm")}<span>${esc(r.label)}</span></button>`);
  }
  html.push(`</div>`);
  openPopover(anchor, html.join(""), { width: 210 });
}

export function openProfilePicker() {
  const anchor = $("#browser-profile");
  if (!anchor) return;
  const cur = browserProfile();
  openPopover(anchor, `<div class="browser-profile-pop">${knownProfiles.map((n) =>
    `<button class="browser-profile-row${n === cur ? " active" : ""}" data-profile="${esc(n)}">${esc(n)}${n === cur ? ` ${icon("check", "sm")}` : ""}</button>`).join("")}</div>`, { width: 180 });
  for (const row of $$(".browser-profile-row")) {
    row.addEventListener("click", () => { closePopover(); switchProfile(row.dataset.profile); });
  }
}

async function switchProfile(name) {
  const b = B, sid = b.session?.id;
  if (!sid || !name || !knownProfiles.includes(name) || name === (b.session?.profile || "default")) return;
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(sid)}/profile`, { method: "POST", body: { profile: name } });
    // Rebind the requesting state's session — writing through B after a tab
    // swap would point the wrong tab at this session (and its DELETE).
    if (b.session?.id === sid) b.session = d.session || b.session;
    if (B === b) { closeDevToolsFrame(); refreshProfiles(); toast("Profile switched", `Now browsing as "${name}".`, "info"); }
  } catch (e) { if (B === b) { toast("Profile switch failed", e.message, "error"); refreshProfiles(); } }
}

/* ---------------- input forwarding ---------------- */

function canInput() {
  return P.open && !B.annotate && B.inputOwner && B.status === "live" && B.socket && B.socket.readyState === WebSocket.OPEN;
}

function sendCtl(obj) {
  const ws = B.socket;
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(obj));
}

// Display px on the <img> → viewport CSS px (contract §4.5).
function viewportPoint(e) {
  return viewportPointAt(e.clientX, e.clientY);
}

// Same mapping for a raw client coordinate pair — the region drag needs it
// for both band corners, which are not the current pointer position.
function viewportPointAt(clientX, clientY) {
  const img = $("#browser-frame");
  if (!img) return null;
  const r = img.getBoundingClientRect();
  if (!r.width || !r.height) return null;
  const dw = B.meta?.deviceWidth || B.session?.width || r.width;
  const dh = B.meta?.deviceHeight || B.session?.height || r.height;
  const scale = r.width / dw;
  return {
    x: clamp((clientX - r.left) / scale, 0, dw),
    y: clamp((clientY - r.top) / scale, 0, dh),
  };
}

function onViewMove(e) {
  const drag = B.regionDrag;
  if (drag && e.pointerId === drag.pointerId) {
    drag.x1 = e.clientX;
    drag.y1 = e.clientY;
    if (!drag.active && Math.hypot(drag.x1 - drag.x0, drag.y1 - drag.y0) < REGION_DRAG_PX) return;
    if (!drag.active) { drag.active = true; try { $("#browser-view")?.setPointerCapture(drag.pointerId); } catch {} hideHover(); }
    drawRegionBand(drag);
    return;
  }
  if (e.pointerType === "touch") return;
  if (e.target.id !== "browser-frame") { if (B.annotate && !B.pendingPick) hideHover(); return; }
  if (B.annotate) { onAnnotateMove(e); return; }
  if (!canInput()) return;
  const p = viewportPoint(e);
  if (p) queueMove(p, false);
}

// Continuous moves (mouse and touch) share one throttle bucket — while a
// button is held the rate drops so expensive pages (canvas apps whose
// pointermove handler costs more than a frame) see fewer, fresher samples.
function queueMove(p, touch) {
  B.lastMove = { x: p.x, y: p.y, touch };
  const now = Date.now();
  const interval = (B.buttonsHeld || touch) ? DRAG_MOVE_INTERVAL_MS : MOVE_INTERVAL_MS;
  const wait = interval - (now - B.lastMoveSent);
  if (wait <= 0) flushMove();
  else if (!B.moveTimer) B.moveTimer = setTimeout(flushMove, wait);
}

let viewTouch = null;
let viewTouchStart = null;
let keyboardRequest = 0;
function onViewTouch(e) {
  if (B.annotate || !canInput()) { viewTouch = null; return; }
  if (e.type === "touchstart" && viewTouch === null && e.target.id === "browser-frame") {
    ++keyboardRequest;
    viewTouch = e.changedTouches[0].identifier;
    viewTouchStart = { x: e.changedTouches[0].clientX, y: e.changedTouches[0].clientY };
  }
  if (viewTouch === null) return;
  const touch = Array.from(e.changedTouches).find((t) => t.identifier === viewTouch);
  e.preventDefault();
  if (!touch) return;
  const p = viewportPoint(touch);
  if (!p) return;
  const type = { touchstart: "touchStart", touchmove: "touchMove", touchend: "touchEnd", touchcancel: "touchCancel" }[e.type];
  const tap = e.type === "touchend" && viewTouchStart && Math.hypot(touch.clientX - viewTouchStart.x, touch.clientY - viewTouchStart.y) < 10;
  if (type === "touchMove") { queueMove(p, true); return; }
  sendCtl({ type: "touch", phase: type, x: p.x, y: p.y, ...(tap ? { keyboardRequest } : {}) });
  if (e.type === "touchend" || e.type === "touchcancel") viewTouch = null;
}

function onTouchInput(e) {
  if (e.isComposing) return; // the committed text arrives on the next event
  const input = e.currentTarget;
  if (!canInput()) {
    input.value = ""; // don't accumulate keys for a dead stream
    return;
  }
  const text = input.value;
  if (text) sendCtl({ type: "text", text });
  input.value = "";
}

function flushMove() {
  B.moveTimer = null;
  const p = B.lastMove;
  if (!p || !canInput()) return;
  B.lastMove = null;
  B.lastMoveSent = Date.now();
  if (p.touch) sendCtl({ type: "touch", phase: "touchMove", x: p.x, y: p.y });
  else sendCtl({ type: "mousemove", x: p.x, y: p.y, buttons: B.buttonsHeld });
}

const AUX_BUTTONS = { 1: "middle", 2: "right" };

function onViewClick(e) {
  if (!P.open) return;
  // The pointerup that finished a region band still produces a click — that
  // release is the band's end, not an element pick.
  if (B.regionSuppress) { B.regionSuppress = false; return; }
  if (e.target.id !== "browser-frame") return;
  const p = viewportPoint(e);
  if (!p) return;
  if (B.annotate) { pickAt(p, { x: e.clientX, y: e.clientY }); return; }
  // Primary input reaches the page via the down/up frames; the click that a
  // press+release pair produces needs no extra frame of its own.
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
  if (e.target.id === "browser-touch-input" && ((e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) || e.key === "Unidentified" || e.key === "Process")) return;
  if (B.annotate && e.key === "Escape") {
    e.preventDefault();
    if (B.regionDrag) cancelRegionDrag();
    else setAnnotate(false);
    return;
  }
  const vault = $("#browser-vault-pop");
  if (vault && !vault.hidden) {
    if (e.key === "Escape") { hideSavedLogins(); e.preventDefault(); return; }
    if (e.key.length === 1 || e.key === "Backspace" || e.key === "Delete") hideSavedLogins();
  }
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

/* ---------------- device emulation ---------------- */

// The second toolbar row: how the remote viewport's size and device profile
// are chosen. "relative" streams the pane's own size (the default); the fixed
// presets emulate a concrete device with UA and touch.
export function toggleDeviceBar() {
  P.deviceBar = !P.deviceBar;
  localStorage.setItem("nusamation:browser-device-bar", P.deviceBar ? "1" : "0");
  updateDeviceBar();
}

export function browserDeviceMode(mode) {
  if (!["relative", "desktop", "mobile", "tablet"].includes(mode)) return;
  B.deviceChoice = mode;
  B.holdViewport = false;
  updateDeviceBar();
  applyDeviceChoice();
}

// applyDeviceChoice pushes the user's selection to the stream — on chip
// clicks and on (re)connect, so intent is never lost when the socket was not
// yet open. Relative clears the whole profile (plain desktop at pane size,
// not a mobile UA stretched to fit); presets pin a fixed device.
function applyDeviceChoice() {
  // A watching pane owns neither input nor sizing — keep letterboxing the
  // held viewport even if something (meta, a stale chip click) calls this.
  if (!B.inputOwner) return;
  // "" is the agent's own viewport: a (re)attach has nothing to push, and
  // resizing to the pane here would stomp the emulation the agent relies on.
  // Only a real chip click — which always sets a concrete choice — may take
  // the viewport back.
  if (B.deviceChoice === "") return;
  // Any explicit call — device chip, preset, agent-drive release — is the
  // user taking viewport control back from a guest attach.
  B.viewportGuest = false;
  if (!B.socket || B.socket.readyState !== WebSocket.OPEN) return;
  if (B.deviceChoice && B.deviceChoice !== "relative") {
    stopResizeWatch();
    sendCtl({ type: "emulate", preset: B.deviceChoice });
    return;
  }
  const box = viewBox();
  if (box.w && box.h) {
    sendCtl({ type: "emulate", mobile: false, ua: "", touch: false, dpr: 1, width: Math.round(box.w), height: Math.round(box.h) });
  }
  startResizeWatch();
}

function updateDeviceBar() {
  const bar = $("#browser-device-bar");
  if (!bar) return;
  bar.hidden = !P.deviceBar;
  const toggle = $(`[data-action="browser-device"]`);
  if (toggle) toggle.setAttribute("aria-pressed", String(P.deviceBar));
  for (const chip of $$(".device-chip", bar)) {
    chip.classList.toggle("active", chip.dataset.device === B.deviceChoice);
    // Chips emulate the shared viewport — a watcher keeps them read-only.
    chip.disabled = !B.inputOwner;
  }
  const w = B.meta?.deviceWidth || B.session?.width || 0;
  const h = B.meta?.deviceHeight || B.session?.height || 0;
  const ro = $("#browser-device-readout");
  if (ro) {
    const tag = B.meta?.mobile ? "mobile" : B.meta?.touch ? "touch" : "";
    ro.textContent = w ? `${w}×${h}${tag ? " · " + tag : ""}` : "";
  }
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
  P.observer = new ResizeObserver(() => {
    // A guest viewer letterboxes the held viewport; its pane resizes must not
    // resize the shared session or stretch the frame to the wrong ratio.
    if (B.viewportGuest) return;
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
  P.observer.observe(view);
}

function stopResizeWatch() {
  if (P.observer) { try { P.observer.disconnect(); } catch {} P.observer = null; }
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
  const b = B;
  if (b.hoverTimer) { clearTimeout(b.hoverTimer); b.hoverTimer = null; }
  const p = b.lastHover;
  b.lastHover = null;
  if (!p || !b.annotate || !b.session || b.pendingPick || b.picking) return;
  b.lastHoverSent = Date.now();
  b.hoverBusy = true;
  const seq = ++b.hoverSeq;
  api(`/browser/sessions/${encodeURIComponent(b.session.id)}/annotate/preview`, {
    method: "POST",
    body: { x: p.x, y: p.y },
  }).then((d) => {
    if (B === b && seq === b.hoverSeq && b.annotate && !b.pendingPick) showHover(d);
  }).catch(() => {
  }).finally(() => {
    b.hoverBusy = false;
    if (B === b && !b.parked && b.lastHover && !b.hoverTimer) b.hoverTimer = setTimeout(sendHover, HOVER_INTERVAL_MS);
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
  label.textContent = d.kind === "region" || d.kind === "page"
    ? annotationTag(d)
    : d.tagName ? `<${d.tagName}>${d.name ? " " + d.name : ""}` : d.name || d.selector || "element";
  label.classList.toggle("below", d.rect.y * scale < 26);
  box.classList.add("show");
}

function hideHover() {
  B.hoverSeq++;
  $("#browser-hover")?.classList.remove("show", "pinned");
}

// Keeps the picked element marked while its comment popover is open — the
// server removes the painted pin right after the marked screenshot, so this
// client-side box is the persistent highlight. Region picks pin their drawn
// rect; page picks carry no rect and pin nothing.
function pinHover(d) {
  B.hoverSeq++;
  if (!d || !d.rect || !(d.rect.width > 0) || !(d.rect.height > 0)) { hideHover(); return; }
  showHover(d);
  $("#browser-hover.show")?.classList.add("pinned");
}

export function toggleAnnotate() {
  if (B.annotate) setAnnotate(false);
  else if (B.status === "live" && !B.recording) setAnnotate(true);
}

// Escape key affordance for the global keydown chain: exits annotate mode.
// Returns true when the key was consumed.
export function browserEscape() {
  if (!P.open) return false;
  const box = $("#browser-vault-pop");
  if (box && !box.hidden) { hideSavedLogins(); return true; }
  if (B.regionDrag) { cancelRegionDrag(); return true; }
  if (!B.annotate) return false;
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
    cancelRegionDrag();
    B.regionSuppress = false;
    $("#browser-hover")?.remove();
    closePopover();
    postClearHighlight();
  }
}

function updateAnnotateUI() {
  $("#browser-view")?.classList.toggle("annotate", B.annotate);
  const btn = $(`[data-action="browser-annotate"]`);
  if (btn) { btn.classList.toggle("active", B.annotate); btn.setAttribute("aria-pressed", String(B.annotate)); }
  const pageBtn = $(`[data-action="browser-annotate-page"]`);
  if (pageBtn) pageBtn.hidden = !B.annotate;
  updateModeExclusion();
}

// Annotate and record are exclusive modes: the annotate buttons disable
// while a recording is live, the record button disables while annotating.
function updateModeExclusion() {
  const recLive = !!B.recording;
  for (const sel of ["browser-annotate", "browser-annotate-page"]) {
    const b = $(`[data-action="${sel}"]`);
    if (b) b.disabled = recLive;
  }
  const recBtn = $(`[data-action="browser-record"]`);
  if (recBtn) recBtn.disabled = B.annotate;
}

function postClearHighlight() {
  const id = B.session?.id;
  if (!id) return;
  api(`/browser/sessions/${encodeURIComponent(id)}/annotate/clear`, { method: "POST", body: {} }).catch(() => {});
}

// Shared pick tail for every annotate kind — element (x/y hit test), region
// (a drawn rect), page (the whole viewport): numbered pin → POST →
// pendingPick → the comment popover. One in-flight pick at a time.
async function annotatePick(body, clientPoint, anchorEl) {
  const b = B;
  if (b.picking || !b.session) return;
  b.picking = true;
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
    const d = await api(`/browser/sessions/${encodeURIComponent(b.session.id)}/annotate`, {
      method: "POST",
      body: { ...body, marker: pinNo },
    });
    // Pane closed, parked, or another tab owns the surface now — a
    // pendingPick parked without its popover would block hovers with no way
    // to submit or cancel.
    if (!P.open || B !== b || !b.annotate || b.parked) return;
    b.pendingPick = { pickId: d.pickId, descriptor: d.descriptor || {}, upload: d.upload || null, marker: d.marker || pinNo, submitted: false, comment: "", includeScreenshot: true };
    pinHover(d.descriptor || {});
    openAnnotatePopover(clientPoint, anchorEl);
  } catch (e) {
    if (!P.open || B !== b) return;
    if (e.code === "pick_missed") toast("No element there", "Click directly on a page element.", "info", 2600);
    else browserApiError(e, "Could not annotate that point");
  } finally {
    b.picking = false;
    marker.remove();
  }
}

function pickAt(p, clientPoint) {
  annotatePick({ x: p.x, y: p.y }, clientPoint);
}

const REGION_DRAG_PX = 5; // press→drag threshold: below it stays a click pick

// "Annotate page" toolbar button — a whole-viewport pick, no hit test; the
// numbered badge alone marks the screenshot. The popover anchors on the
// button since the page has no pick point.
export function annotatePage() {
  if (!B.annotate || B.picking || !B.session) return;
  const btn = $(`[data-action="browser-annotate-page"]`);
  const img = $("#browser-frame");
  const r = img?.getBoundingClientRect();
  const vr = $("#browser-view")?.getBoundingClientRect();
  const point = r && r.width
    ? { x: r.left + r.width / 2, y: r.top + r.height / 2 }
    : { x: (vr?.left || 0) + (vr?.width || 0) / 2, y: (vr?.top || 0) + (vr?.height || 0) / 2 };
  annotatePick({ kind: "page" }, point, btn || undefined);
}

function drawRegionBand(drag) {
  const view = $("#browser-view");
  if (!view) return;
  let band = $("#browser-region");
  if (!band) {
    band = document.createElement("div");
    band.id = "browser-region";
    band.className = "browser-region";
    view.appendChild(band);
  }
  const vr = view.getBoundingClientRect();
  band.style.left = Math.min(drag.x0, drag.x1) - vr.left + "px";
  band.style.top = Math.min(drag.y0, drag.y1) - vr.top + "px";
  band.style.width = Math.abs(drag.x1 - drag.x0) + "px";
  band.style.height = Math.abs(drag.y1 - drag.y0) + "px";
}

function onRegionUp(e) {
  const drag = B.regionDrag;
  if (!drag || e.pointerId !== drag.pointerId) return;
  cancelRegionDrag();
  if (!drag.active) return; // below threshold: the click pick handles it
  B.regionSuppress = true;
  const a = viewportPointAt(drag.x0, drag.y0);
  const b = viewportPointAt(drag.x1, drag.y1);
  if (!a || !b) return;
  const rect = normalizeRect(a, b);
  if (rect.width < 1 || rect.height < 1) return;
  annotatePick({ kind: "region", ...rect }, { x: e.clientX, y: e.clientY });
}

function onRegionCancel(e) {
  if (!B.regionDrag || e.pointerId !== B.regionDrag.pointerId) return;
  cancelRegionDrag();
}

function cancelRegionDrag() {
  const drag = B.regionDrag;
  B.regionDrag = null;
  $("#browser-region")?.remove();
  if (drag) { try { $("#browser-view")?.releasePointerCapture(drag.pointerId); } catch {} }
}

function openAnnotatePopover(clientPoint, anchorEl) {
  const view = $("#browser-view");
  const pick = B.pendingPick;
  if (!view || !pick) return;
  closePopover();
  // Element picks anchor at the pick point inside the view; page/region
  // picks may pass a real element (the toolbar button for page picks).
  let anchor = anchorEl && anchorEl.isConnected ? anchorEl : null;
  let owned = false;
  if (!anchor) {
    anchor = document.createElement("div");
    anchor.className = "browser-anchor";
    const vr = view.getBoundingClientRect();
    anchor.style.left = clientPoint.x - vr.left + "px";
    anchor.style.top = clientPoint.y - vr.top + "px";
    view.appendChild(anchor);
    owned = true;
  }
  const d = pick.descriptor || {};
  const tag = d.kind === "region" || d.kind === "page" ? annotationTag(d) : d.tagName ? `<${d.tagName}>` : "element";
  const label = d.name || d.immediateText || "";
  const up = pick.upload || {};
  openPopover(anchor, `<div class="annotate-pop">
    <div class="annotate-head">
      ${up.id ? `<img src="/api/uploads/${encodeURIComponent(up.id)}" alt="">` : icon("pin", "sm")}
      <div class="annotate-id"><b>${esc(tag)}</b><small>${esc(truncate(label, 60) || d.selector || d.pageUrl || "")}</small></div>
    </div>
    <textarea class="input annotate-comment" rows="2" placeholder="Comment for the agent…" aria-label="Annotation comment" autofocus></textarea>
    <label class="annotate-shot"><input type="checkbox" class="annotate-shot-input" checked> Include screenshot <small>(pin ${pick.marker || 1} — the agent sees the marked page)</small></label>
    <div class="annotate-actions">
      <button class="btn btn-ghost btn-xs" data-action="annotation-cancel">Cancel</button>
      <button class="btn btn-primary btn-xs" data-action="annotation-submit">Add note</button>
    </div>
  </div>`, { width: 264, class: "annotate-popover" });
  const pop = $(".annotate-popover");
  if (!pop) { if (owned) anchor.remove(); return; }
  const prev = pop._cleanup;
  // The global click delegation runs closePopover() BEFORE dispatching the
  // submit action, so the cancel decision is deferred a tick — a submit that
  // already consumed the pick must not post an extra clear.
  pop._cleanup = () => {
    prev?.();
    if (owned) anchor.remove();
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
    label: (d.kind === "region" || d.kind === "page") ? annotationTag(d) : (d.name || d.immediateText || ""),
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
  B.pendingRec = null;
  closePopover();
}

/* ---------------- session recording ---------------- */

const REC_POLL_MS = 5000;
const REC_LIVE = new Set(["recording", "paused", "finalizing"]);

// resetRecState drops the bound session's recording UI. recReady survives:
// a stopped recording is conversation-scoped for the send, so a pane close or
// session reattach must not strand it — the conversation switch, a send, a
// dismissal, or a new recording clears it instead.
function resetRecState() {
  stopRecPoll();
  stopRecTick();
  B.recording = null;
  B.recNote = "";
  B.recBusy = false;
  B.pendingRec = null;
  renderRecBar();
}

function scheduleRecPoll() {
  stopRecPoll();
  // Poll while the pane is open — not only while a recording is already
  // known — so a recording started from another pane or the API surfaces
  // here. A parked state's runtime is shelved: no polling for a hidden tab.
  if (P.open && B.session && !B.parked) B.recPollTimer = setTimeout(recSync, REC_POLL_MS);
}

function stopRecPoll() {
  if (B.recPollTimer) { clearTimeout(B.recPollTimer); B.recPollTimer = null; }
}

// The chip's mm:ss ticks client-side from startedAt — the 5s poll only
// resyncs status, it is not the clock.
function ensureRecTick() {
  const want = P.open && !B.parked && B.recording?.status === "recording";
  if (want && !B.recTickTimer) {
    B.recTickTimer = setInterval(() => {
      if (!P.open || B.recording?.status !== "recording") { stopRecTick(); return; }
      const el = $("#browser-rec-time");
      if (!el) { stopRecTick(); return; }
      el.textContent = formatElapsed(Date.now() - (B.recording.startedAt || Date.now()));
    }, 1000);
  } else if (!want) {
    stopRecTick();
  }
}

function stopRecTick() {
  if (B.recTickTimer) { clearInterval(B.recTickTimer); B.recTickTimer = null; }
}

// Sync the recording UI with the session's live recorder: on attach, after
// every control action, and on the ~5s poll while one may be active.
async function recSync() {
  const b = B;
  stopRecPoll();
  const session = b.session?.id;
  if (!P.open || !session) return;
  if (b.recBusy) { scheduleRecPoll(); return; } // a control POST owns the state right now
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(session)}/recording`);
    if (b.session?.id !== session) return;
    b.recording = REC_LIVE.has(d.recording?.status) ? d.recording : null;
    if (b.recording) b.recNote = "";
  } catch (e) {
    if (b.session?.id !== session) return;
    if (e.status === 404) await recGone(session, b);
    // Other failures keep last-known state; the next poll retries.
  }
  if (B === b) { renderRecBar(); scheduleRecPoll(); }
}

// The session-level GET went 404 — the recorder is gone. Classify the one we
// were tracking via the conversation's metas: completed means it was stopped
// elsewhere (still sendable), anything else is an interruption worth a note.
async function recGone(session, b = B) {
  const was = b.recording;
  b.recording = null;
  if (!was || b.recReady) return;
  const convId = was.conversationId || (B === b ? P.conversationId : "");
  const classify = (d) => {
    const meta = (d.recordings || []).find((r) => r.id === was.id);
    if (meta && meta.status === "completed") b.recReady = meta;
    else b.recNote = "The recording was interrupted.";
  };
  if (!convId) { b.recNote = "The recording was interrupted."; return; }
  try {
    const d = await api(`/conversations/${encodeURIComponent(convId)}/recordings`);
    if (b.session?.id !== session) return;
    classify(d);
  } catch {
    if (b.session?.id === session) b.recNote = "The recording was interrupted.";
  }
}

function renderRecBar() {
  const bar = $("#browser-rec-bar");
  if (!bar) return;
  const rec = B.recording;
  // Recording wins the mode race: an annotate session (even one started by
  // an external recording begin) ends as soon as a live recording appears.
  if (rec && B.annotate) setAnnotate(false);
  // A finalized meta only shows while it still belongs to the bound room.
  const ready = B.recReady && (B.recReady.conversationId || "") === P.conversationId ? B.recReady : null;
  const show = P.open && (!!rec || !!ready || !!B.recNote);
  bar.hidden = !show;
  const recBtn = $(`[data-action="browser-record"]`);
  if (recBtn) recBtn.hidden = show;
  // Rebuilt only when the bar's shape changes — the ticking text updates in
  // place below so a 1s rewrite can't eat an in-flight click on the buttons.
  const sig = `${show}:${!!rec}:${rec?.status || ""}:${rec?.startedAt || 0}:${ready?.id || ""}:${B.recNote || ""}`;
  if (sig !== bar.dataset.sig) {
    bar.dataset.sig = sig;
    const dis = B.recBusy ? " disabled" : "";
    if (!show) bar.innerHTML = "";
    else if (rec) {
      const paused = rec.status === "paused";
      const elapsed = formatElapsed(Date.now() - (rec.startedAt || Date.now()));
      const chip = paused
        ? `<span class="browser-rec-chip paused">‖ PAUSED ${esc(elapsed)}</span>`
        : `<span class="browser-rec-chip"><span class="browser-rec-dot"></span>REC <span id="browser-rec-time">${esc(elapsed)}</span></span>`;
      const toggle = paused
        ? `<button class="icon-btn sm" data-action="browser-rec-resume" title="Resume recording" aria-label="Resume recording"${dis}>${icon("play", "sm")}</button>`
        : `<button class="icon-btn sm" data-action="browser-rec-pause" title="Pause recording" aria-label="Pause recording"${dis}>${icon("pause", "sm")}</button>`;
      bar.innerHTML = `${chip}${toggle}
        <button class="icon-btn sm" data-action="browser-rec-stop" title="Stop recording" aria-label="Stop recording"${dis}>${icon("stop", "sm")}</button>
        <span class="grow"></span>
        <button class="btn btn-primary btn-xs" data-action="browser-rec-attach" title="Stop the recording and attach it to the composer"${dis}>${icon("paperclip", "sm")} Attach</button>`;
    } else if (ready) {
      const dur = formatElapsed((ready.endedAt || Date.now()) - (ready.startedAt || 0));
      bar.innerHTML = `<span class="browser-rec-chip ready">${icon("check", "sm")} ${esc(dur)}</span>
        <span class="browser-rec-note">Recording ready</span>
        <span class="grow"></span>
        <button class="btn btn-primary btn-xs" data-action="browser-rec-attach" title="Attach this recording to the composer"${dis}>${icon("paperclip", "sm")} Attach</button>
        <button class="icon-btn sm" data-action="browser-rec-dismiss" title="Dismiss" aria-label="Dismiss recording"${dis}>${icon("x", "sm")}</button>`;
    } else {
      bar.innerHTML = `${icon("info", "sm")}<span class="browser-rec-note">${esc(B.recNote)}</span>
        <span class="grow"></span>
        <button class="icon-btn sm" data-action="browser-rec-dismiss" title="Dismiss" aria-label="Dismiss"${dis}>${icon("x", "sm")}</button>`;
    }
  }
  // The live elapsed text ticks in place — paused/ready/not-shown don't.
  if (rec && rec.status !== "paused") {
    const t = $("#browser-rec-time", bar);
    if (t) t.textContent = formatElapsed(Date.now() - (rec.startedAt || Date.now()));
  }
  updateRecButtons();
  updateModeExclusion();
  ensureRecTick();
}

// Disabled-state pass kept separate from renderRecBar's markup rebuild so a
// control/meta message cannot eat an in-flight click by swapping innerHTML.
function updateRecButtons() {
  const bar = $("#browser-rec-bar");
  if (!bar || bar.hidden) return;
  const active = !!B.session && B.status !== "closed" && B.status !== "error";
  const finalizing = B.recording?.status === "finalizing";
  for (const b of $$("button", bar)) {
    const a = b.dataset.action;
    if (a === "browser-rec-dismiss") { b.disabled = false; continue; }
    if (a === "browser-rec-attach") { b.disabled = B.recBusy || finalizing; continue; }
    b.disabled = B.recBusy || !active || finalizing;
  }
}

// Recording calls map errors a little differently than navigation: a 404 can
// be a dead session OR just "no_active_recording", and recording_active means
// our state is stale rather than busy.
function recApiError(e, what) {
  if (e.code === "no_active_recording") { B.recording = null; renderRecBar(); scheduleRecPoll(); return; }
  if (e.code === "recording_active") { toast("Already recording", "This session already has an active recording.", "info"); recSync(); return; }
  if (e.status === 404) { browserApiError(e, what); return; }
  if (e.status === 409) { toast("Browser is busy", "Another browser action is in flight — try again.", "info"); return; }
  toast(what, e.message, "error");
}

export async function recordingStart() {
  const b = B, session = b.session?.id;
  if (!session || b.recBusy || b.annotate) return;
  b.recBusy = true;
  if (B === b) renderRecBar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(session)}/recording`, { method: "POST", body: {} });
    if (b.session?.id !== session) return;
    b.recording = REC_LIVE.has(d.recording?.status) ? d.recording : null;
    b.recReady = null;
    b.recNote = "";
  } catch (e) {
    if (b.session?.id === session && B === b) recApiError(e, "Could not start recording");
  } finally {
    // recBusy belongs to the state that launched the call — clearing through B
    // after a swap could eat a legitimately in-flight flag on the new tab.
    b.recBusy = false;
    if (B === b) { renderRecBar(); scheduleRecPoll(); }
  }
}

async function recAction(op) {
  const b = B, session = b.session?.id;
  if (!session || b.recBusy) return;
  b.recBusy = true;
  if (B === b) renderRecBar();
  try {
    const d = await api(`/browser/sessions/${encodeURIComponent(session)}/recording/${op}`, { method: "POST", body: {} });
    if (b.session?.id !== session) return;
    if (op === "stop") {
      b.recording = null;
      b.recReady = d.recording || null;
      b.recNote = "";
    } else {
      b.recording = REC_LIVE.has(d.recording?.status) ? d.recording : b.recording;
      b.recNote = "";
    }
  } catch (e) {
    if (b.session?.id === session && B === b) recApiError(e, `Could not ${op === "stop" ? "stop" : op} the recording`);
  } finally {
    b.recBusy = false;
    if (B === b) { renderRecBar(); scheduleRecPoll(); }
  }
}

export async function recordingPause() { await recAction("pause"); }

export async function recordingResume() { await recAction("resume"); }

// Stop (and send-to-agent below) refuse while a pick POST is in flight —
// simpler than queueing the stop behind it, and the pick lands in a moment.
export async function recordingStop() {
  if (B.picking) { toast("Annotation in flight", "Finish the annotation first, then stop the recording.", "info"); return; }
  await recAction("stop");
}

export function recordingDismiss() {
  B.recReady = null;
  B.recNote = "";
  B.pendingRec = null;
  closePopover();
  renderRecBar();
}

// "Attach" on the bar: stop the recorder if it is still live, then open the
// comment popover — the same shape as the annotation popover — so the user
// sees where the comment goes before the chip lands on the composer.
export async function recordingAttach() {
  const b = B;
  const rec = b.recording || b.recReady;
  if (!rec || b.recBusy) return;
  if (b.picking) { toast("Annotation in flight", "Finish the annotation first, then attach the recording.", "info"); return; }
  // The server re-resolves the id and binds it to one conversation (400
  // recording_invalid on mismatch) — fail here with a clearer message. A
  // recording bound to a session with no conversation can never match.
  if (!composerKey || (rec.conversationId || "") !== composerKey) {
    toast("Recording belongs elsewhere", "This recording isn't bound to this conversation — it can't be sent here.", "info");
    return;
  }
  b.recBusy = true;
  if (B === b) renderRecBar();
  try {
    if (b.recording) {
      const session = b.session?.id;
      if (!session) return;
      const d = await api(`/browser/sessions/${encodeURIComponent(session)}/recording/stop`, { method: "POST", body: {} });
      if (b.session?.id !== session) return;
      b.recReady = d.recording || rec;
      b.recording = null;
    }
  } catch (e) {
    if (B === b) recApiError(e, "Could not finish the recording");
    return;
  } finally {
    b.recBusy = false;
    // The bar rebuilds to the ready state here so the popover anchors to the
    // fresh Attach button, not the node the click just consumed.
    if (B === b) { renderRecBar(); scheduleRecPoll(); }
  }
  if (B === b && b.recReady) openRecPopover($('[data-action="browser-rec-attach"]', $("#browser-rec-bar")));
}

function openRecPopover(anchorEl) {
  const meta = B.recReady;
  if (!meta) return;
  closePopover();
  const anchor = anchorEl && anchorEl.isConnected ? anchorEl : $("#browser-rec-bar");
  if (!anchor) return;
  B.pendingRec = { comment: "" };
  const c = meta.counts || {};
  const dur = formatElapsed((meta.endedAt || Date.now()) - (meta.startedAt || 0));
  openPopover(anchor, `<div class="annotate-pop">
    <div class="annotate-head">
      ${icon("circle", "sm")}
      <div class="annotate-id"><b>rec ${esc(dur)}</b><small>${esc(`${c.events || 0} events · ${c.annotations || 0} annotations · ${c.errors || 0} errors`)}</small></div>
    </div>
    <textarea class="input annotate-comment" rows="2" placeholder="Comment for the agent…" aria-label="Recording comment" autofocus></textarea>
    <div class="annotate-actions">
      <button class="btn btn-ghost btn-xs" data-action="annotation-cancel">Cancel</button>
      <button class="btn btn-primary btn-xs" data-action="recording-attach-submit">Attach</button>
    </div>
  </div>`, { width: 264, class: "annotate-popover" });
  const pop = $(".annotate-popover");
  if (!pop) { B.pendingRec = null; return; }
  const ta = $(".annotate-comment", pop);
  ta?.addEventListener("input", () => { if (B.pendingRec) B.pendingRec.comment = ta.value; });
  ta?.addEventListener("keydown", (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); recordingAttachSubmit(); }
    else if (e.key === "Escape") { e.preventDefault(); B.pendingRec = null; closePopover(); }
  });
  setTimeout(() => ta?.focus(), 20);
}

// Popover submit: park the ready recording as a composer chip — same shape as
// an annotation chip. The comment rides on the chip and becomes the message
// text when the draft is empty; the send stays the composer's own decision.
export function recordingAttachSubmit() {
  const meta = B.recReady;
  const comment = (B.pendingRec?.comment || "").trim();
  B.pendingRec = null;
  closePopover();
  if (!meta) return;
  if (!composerKey || (meta.conversationId || "") !== composerKey) {
    toast("Recording belongs elsewhere", "This recording isn't bound to this conversation — it can't be sent here.", "info");
    return;
  }
  const c = meta.counts || {};
  const dur = formatElapsed((meta.endedAt || Date.now()) - (meta.startedAt || 0));
  S.attachments.push({
    type: "recording",
    id: meta.id,
    recordingId: meta.id,
    comment,
    label: `rec ${dur}`,
    detail: `${c.events || 0} events · ${c.annotations || 0} annotations · ${c.errors || 0} errors`,
  });
  B.recReady = null;
  B.recNote = "";
  renderPendingAttachments();
  scheduleDraftSave();
  focusComposer();
  toast("Recording attached", "Review it in the composer and send when ready.", "success");
}

/* ---------------- status rendering ---------------- */

function setStatus(status, text = "", code = "") {
  B.status = status;
  B.statusText = text;
  B.closedCode = code || "";
  // Only dead ends kill annotate mode — the transient "connecting" of a tab
  // re-attach must not wipe an annotation session that was parked live.
  if ((status === "closed" || status === "error") && B.annotate) setAnnotate(false);
  renderState();
  renderWatch();
  updateToolbar();
}

// The share-screen affordance: while this pane watches a stream it does not
// control, a small floating bar offers Take over instead of swallowing the
// whole view behind an overlay. Hidden while input is owned, the stream is
// not live, or the first frame has not arrived.
function renderWatch() {
  const el = $("#browser-watch");
  if (!el) return;
  const show = P.open && B.status === "live" && B.gotFrame && !B.inputOwner;
  // Rebuilt only when the shape changes — a per-frame rewrite eats the
  // in-flight click on Take over.
  const sig = `${show}:${!!B.agentLock}`;
  if (el.dataset.sig === sig) return;
  el.dataset.sig = sig;
  el.classList.toggle("show", show);
  if (!show) { el.innerHTML = ""; return; }
  const text = B.agentLock
    ? "The agent is holding control — your input is paused"
    : "Shared view — another pane is in control";
  el.innerHTML = `<span>${esc(text)}</span>
    <button class="btn btn-secondary btn-sm" data-action="browser-takeover">${icon("refresh", "sm")} Take over</button>`;
}

// The Take over affordance claims the input lease on the live socket — a
// human claim always wins, including over an agent lock.
export function browserTakeover() {
  if (!B.socket || B.socket.readyState !== WebSocket.OPEN) return;
  sendCtl({ type: "takeover" });
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
  const live = B.status === "live";
  const active = B.session && B.status !== "closed" && B.status !== "error";
  // A watching pane observes the shared session — every drive affordance
  // (navigation, annotation picks, emulation, profile swap, devtools REPL)
  // is reserved for the controller until Take over grants the lease.
  const drive = B.inputOwner;
  const back = $(`[data-action="browser-back"]`);
  const fwd = $(`[data-action="browser-forward"]`);
  const reload = $(`[data-action="browser-reload"]`);
  if (back) back.disabled = !live || !drive || !B.nav.canGoBack || B.navigating;
  if (fwd) fwd.disabled = !live || !drive || !B.nav.canGoForward || B.navigating;
  if (reload) reload.disabled = !active || !drive || B.navigating;
  if (url) url.disabled = !active || !drive;
  const annotate = $(`[data-action="browser-annotate"]`);
  if (annotate) annotate.disabled = !live || !drive;
  const annotatePage = $(`[data-action="browser-annotate-page"]`);
  if (annotatePage) annotatePage.disabled = !live || !drive || B.picking;
  // Recording captures the session, not input — allowed while watching an
  // agent drive (the QA case) as long as the session is alive.
  const record = $(`[data-action="browser-record"]`);
  if (record) record.disabled = !active || B.recBusy;
  updateRecButtons();
  const device = $(`[data-action="browser-device"]`);
  if (device) device.disabled = !active || !drive;
  const profile = $("#browser-profile");
  if (profile) profile.disabled = !active || !drive;
  const vault = $("#browser-vault-button");
  if (vault) vault.disabled = !live || !drive;
  const devtools = $(`[data-action="browser-devtools"]`);
  if (devtools) devtools.disabled = !active || !drive;
  updateAnnotateUI();
  updateDeviceBar();
}

// First meta load of the conversation on screen. Later switches hide the pane
// and leave it hidden until the user opens it; a refresh is a new page, so
// bootDone starts false again and this runs once.
onBrowserMetaRestored((key, browser) => {
  if (bootDone || composerKey !== key) return;
  bootDone = true;
  if (bootSkip.has(key)) return;
  if (!browser || browser.open === false) return;
  const url = usableBrowserURL(browser.lastUrl);
  if (!url) return;
  openBrowserPane(url);
});

/* ---------------- pane tabs ----------------
 * Views in the pane's tab strip — browsers, shells, and file browsing. The
 * pane opens on a launcher; every view (browser included) is an on-demand
 * tab, so nothing runs until picked. Tabs are keyed per conversation:
 * a room switch hides but does not kill the other room's tabs, so a terminal
 * keeps its shell alive through its socket and a browser tab keeps its
 * server-side session. Tool-tab elements live under #pane-tools inside the
 * pane and persist across open/close; browser tabs swap their whole state
 * object (session, stream, annotate/recording, parked view children) through
 * B via parkBrowserTab/adoptBrowserTab — only the live tab streams. Only
 * conversation deletion or a shell reset disposes tabs (browser tabs also
 * DELETE their session so nothing lingers).
 */

// Tabs are room-owned: each conversation key gets a primary "Browser" tab
// (unclosable) plus any Terminal/Files/Browser tabs the user added. Every
// browser tab carries its own full view-model in `state` — B is whichever of
// those states is on screen. `browserTab` points at the last browser tab that
// was active, so pane reopen lands on it.
const paneTabsByConv = new Map(); // convKey → {tabs:[tab], active:"home"|tabId, browserTab, idle}

const TAB_KINDS = {
  browser: { icon: "globe", label: "Browser" },
  terminal: { icon: "terminal", label: "Terminal" },
  files: { icon: "folder", label: "Files" },
};

function convTabs(key) {
  let st = paneTabsByConv.get(key);
  if (!st) {
    // No fixed Browser tab — the browser is an on-demand tab like any tool.
    // `idle` is a parked placeholder B rests on while no browser tab exists.
    st = { tabs: [], active: "home", browserTab: null, idle: null };
    paneTabsByConv.set(key, st);
  }
  return st;
}

// curTabs is read-only: callers that must create the entry (addPaneTab) use
// convTabs directly. Read paths must not resurrect entries dropped for a
// deleted conversation.
function curTabs() {
  return paneTabsByConv.get(P.conversationId || "") || { tabs: [], active: "home" };
}

export function renderPaneTabs() {
  const strip = $(".browser-tabs");
  if (!strip) return;
  const st = curTabs();
  const homeActive = st.active === "home";
  const parts = [`<button class="browser-tab pane-home-tab${homeActive ? " active" : ""}" role="tab" aria-selected="${homeActive}" data-action="pane-home" title="Pane home — pick a view" aria-label="Pane home">${icon("panel-left", "sm")}</button>`];
  for (const t of st.tabs) {
    // An active browser tab renders inactive while its DevTools drawer owns
    // the surface — re-clicking it exits back to the page view.
    const active = st.active === t.id && !(t.kind === "browser" && t.state === B && B.devtools);
    const title = t.kind === "browser" ? (t.state?.nav?.title || t.state?.nav?.url || t.title) : t.title;
    parts.push(`<span class="browser-tab pane-tab${active ? " active" : ""}" role="tab" aria-selected="${active}"><button class="pane-tab-body" data-action="pane-tab" data-tab="${t.id}" title="${esc(title)}">${icon(TAB_KINDS[t.kind]?.icon || "file", "sm")}<span class="pane-tab-label">${esc(title)}</span></button><button class="pane-tab-x" data-action="pane-tab-close" data-tab="${t.id}" aria-label="Close ${esc(title)}">${icon("x", "sm")}</button></span>`);
  }
  parts.push(`<span class="browser-tabs-end"><button class="icon-btn sm" data-action="browser-new-tab" title="New tab" aria-label="New tab" aria-haspopup="true" aria-expanded="false">${icon("plus", "sm")}</button><button class="icon-btn sm browser-tabs-close" data-action="browser-close" title="Close pane" aria-label="Close pane">${icon("x", "sm")}</button></span>`);
  strip.innerHTML = parts.join("");
}

// syncTabEls keeps exactly the active tab of the current conversation visible.
// Elements stay mounted so terminals and file views hold their state.
function syncTabEls() {
  const host = $("#pane-tools");
  const home = $("#pane-home");
  if (home) home.hidden = curTabs().active !== "home";
  for (const [key, st] of paneTabsByConv) {
    const current = key === (P.conversationId || "");
    for (const t of st.tabs) {
      if (!t.el) continue;
      if (current && !t.el.isConnected) host?.appendChild(t.el);
      t.el.hidden = !(current && st.active === t.id);
    }
  }
}

export function activatePaneTab(id = "browser", opts = {}) {
  const st = convTabs(P.conversationId);
  if (id === "browser") {
    // "browser" is a magic id: the browser surface is on demand — reuse the
    // last browser tab (or the first), creating one when the room has none.
    let t = st.browserTab;
    if (!t || t.kind !== "browser" || !st.tabs.includes(t)) t = st.tabs.find((x) => x.kind === "browser");
    if (!t) {
      t = { id: uid("tab"), kind: "browser", title: "Browser", convId: P.conversationId, el: null, state: freshBrowserState() };
      st.tabs.push(t);
    }
    st.browserTab = t;
    id = t.id;
  }
  if (id !== "home" && !st.tabs.some((t) => t.id === id)) id = "home";
  if (id === "home") {
    // The launcher surface: no tool owns the pane and the browser runtime
    // stays parked, so opening the pane never spawns Chrome on its own.
    st.active = "home";
    parkBrowserTab();
    $("#browser-pane")?.classList.toggle("tool-active", true);
    syncTabEls();
    renderPaneTabs();
    return;
  }
  const target = st.tabs.find((t) => t.id === id);
  st.active = id;
  if (target?.kind === "browser") {
    if (opts.exitDevTools) selectBrowserTab(false);
    if (target.state !== B) {
      st.browserTab = target;
      parkBrowserTab();
      B = target.state || (target.state = freshBrowserState());
      adoptBrowserTab(opts);
    } else {
      // Re-clicking the live browser tab exits DevTools back to the page view.
      if (B.devtools) selectBrowserTab(false);
      if (B.parked) {
        // Same state object — its runtime was shelved while a tool tab (or
        // the hidden pane) owned the surface. Re-attach without swapping.
        adoptBrowserTab(opts);
      }
    }
  } else {
    // A non-browser tab owns the surface — park the browser runtime so its
    // stream and timers stop burning work nobody can see. parkBrowserTab is
    // idempotent, so repeated hops between tool tabs stay cheap.
    parkBrowserTab();
    const tab = target;
    if (tab && !tab.el) initTabEl(tab);
    else if (tab?.kind === "terminal") fitTermTab(tab);
  }
  $("#browser-pane")?.classList.toggle("tool-active", !!target && target.kind !== "browser");
  syncTabEls();
  renderPaneTabs();
}

export function openNewTabMenu(anchor) {
  openPopover(anchor, `<div class="browser-menu">${
    menuItem("terminal", "Terminal", "pane-new", 'data-kind="terminal"')}${
    menuItem("globe", "Browser", "pane-new", 'data-kind="browser"')}${
    menuItem("folder", "File manager", "pane-new", 'data-kind="files"')}</div>`, { width: 200 });
}

// openPaneCard launches a home card: the browser card re-uses the primary tab
// (adopting its live session when one exists); tool cards re-activate an
// existing tab of that kind instead of spawning duplicates.
export function openPaneCard(kind) {
  if (kind === "browser") { activatePaneTab("browser"); return; }
  const st = convTabs(P.conversationId);
  const existing = st.tabs.find((t) => t.kind === kind);
  if (existing) activatePaneTab(existing.id);
  else addPaneTab(kind);
}

export function addPaneTab(kind) {
  const meta = TAB_KINDS[kind];
  // Draft rooms get conv-less variants (project roots / home) — the tab state
  // is adopted into the room once the draft sends.
  if (!meta) return;
  const st = convTabs(P.conversationId);
  const n = st.tabs.filter((t) => t.kind === kind).length + 1;
  const tab = { id: uid("tab"), kind, title: n === 1 ? meta.label : `${meta.label} ${n}`, convId: P.conversationId, el: null };
  if (kind === "browser") {
    // A new browser tab gets its own state and its own session — adopting an
    // existing one would show the same page a sibling tab already owns.
    tab.state = freshBrowserState();
    tab.state.newTab = true;
  }
  st.tabs.push(tab);
  activatePaneTab(tab.id);
}

export function closePaneTab(id) {
  const st = curTabs();
  const idx = st.tabs.findIndex((t) => t.id === id);
  if (idx < 0) return;
  const tab = st.tabs[idx];
  // Activating the surviving neighbor first lets parkBrowserTab shelve the
  // live view properly — disposing while B still points at the dead state
  // would tear down the visible surface mid-swap.
  const next = st.tabs[idx + 1] || st.tabs[idx - 1];
  if (st.active === id) activatePaneTab(next ? next.id : preferredPaneTab(st, tab));
  disposeTab(tab);
  st.tabs.splice(idx, 1);
  if (st.active === id) activatePaneTab(preferredPaneTab(st)); // nothing else was open
  else renderPaneTabs();
}

// preferredPaneTab picks where the pane lands when the active tab goes away:
// a live browser tab keeps its session on screen (primary first); with no
// live browser the launcher is the honest fallback — it never spawns Chrome.
function preferredPaneTab(st, exclude) {
  const live = st.tabs.find((t) => t !== exclude && t?.kind === "browser" && t.state?.session);
  return live ? live.id : "home";
}

function disposeTab(tab) {
  if (tab.kind === "browser") {
    const st = paneTabsByConv.get(tab.convId || "");
    if (st?.browserTab === tab) st.browserTab = st.tabs.find((x) => x !== tab && x.kind === "browser") || null;
    if (tab.state === B) B = freshBrowserState();
    disposeBrowserState(tab.state);
    tab.state = null;
  }
  disposeTabRuntime(tab);
  try { tab.ro?.disconnect(); } catch {}
  tab.el?.remove();
  tab.el = null;
}

// dropPaneTabs kills every tab of one conversation (deleted or reset) —
// including its parked placeholder — so its sessions do not linger.
export function dropPaneTabs(key) {
  const st = paneTabsByConv.get(key);
  if (!st) return;
  st.tabs.forEach(disposeTab);
  if (st.idle === B) B = freshBrowserState();
  disposeBrowserState(st.idle);
  st.idle = null;
  st.browserTab = null;
  paneTabsByConv.delete(key);
  // No activatePaneTab here — the room is gone; activating would just
  // recreate the entry we are dropping via convTabs' create semantics.
  if (key === (P.conversationId || "")) {
    $("#browser-pane")?.classList.remove("tool-active");
    renderPaneTabs();
  }
}

function resetAllPaneTabs() {
  for (const st of paneTabsByConv.values()) {
    st.tabs.forEach(disposeTab);
    if (st.idle !== B) disposeBrowserState(st.idle);
    st.idle = null;
    st.browserTab = null;
  }
  paneTabsByConv.clear();
  $("#browser-pane")?.classList.remove("tool-active");
}

function initTabEl(tab) {
  const el = document.createElement("div");
  el.className = `pane-tool pane-${tab.kind}`;
  el.hidden = true;
  tab.el = el;
  $("#pane-tools")?.appendChild(el);
  syncTabEls();
  if (tab.kind === "terminal") initTermTab(tab);
  else if (tab.kind === "files") loadFilesTab(tab);
}

/* ---- terminal tab ---- */

function termDims(host) {
  const w = host.clientWidth, h = host.clientHeight;
  return { cols: Math.max(20, Math.floor((w - 16) / 8)), rows: Math.max(4, Math.floor(h / 17)) };
}

function fitTermTab(tab) {
  if (!tab.term || !tab.el) return;
  const host = $(".pane-term-host", tab.el);
  if (!host || !host.clientWidth) return;
  const { cols, rows } = termDims(host);
  if (cols !== tab.term.cols || rows !== tab.term.rows) {
    tab.term.resize(cols, rows);
    if (tab.ws?.readyState === WebSocket.OPEN) tab.ws.send(JSON.stringify({ type: "resize", cols, rows }));
  }
}

function setTermState(tab, state, note = "") {
  const status = $(".pane-term-status", tab.el);
  if (!status) return;
  status.dataset.state = state;
  $(".pane-term-note", status).textContent = state === "live" ? "" : note;
  const btn = $("[data-term-reconnect]", status);
  if (btn) btn.hidden = state !== "dead";
}

function initTermTab(tab) {
  const el = tab.el;
  // A slim status bar stays under the tab strip; the terminal surface keeps
  // its last output visible instead of a full-pane overlay.
  el.innerHTML = `<div class="pane-term-bar"><button class="pane-term-cwd" data-term-cwd title="Working directory" aria-haspopup="true">${icon("folder", "sm")}<span class="pane-term-cwd-path">…</span><span class="pane-term-cwd-caret" hidden>${icon("chevron-down", "sm")}</span></button><span class="pane-term-status"><span class="pane-term-note"></span><button class="btn btn-secondary btn-xs" data-term-reconnect hidden>Restart</button></span></div><div class="pane-term-host"></div>`;
  $("[data-term-reconnect]", el).addEventListener("click", () => { disposeTabRuntime(tab); initTermRuntime(tab); });
  $("[data-term-cwd]", el).addEventListener("click", async (e) => {
    const anchor = e.currentTarget; // currentTarget is null after await
    const roots = await convRoots(tab, true);
    if (!tab.el?.isConnected || !roots.length) return;
    updateTermCwd(tab);
    openRootsMenu(anchor, roots, tab.cwd, (path) => {
      if (path === tab.cwd) return;
      tab.cwd = path;
      updateTermCwd(tab);
      disposeTabRuntime(tab);
      initTermRuntime(tab);
    });
  });
  convRoots(tab).then((roots) => {
    if (!tab.el) return; // disposed while roots resolved
    if (!tab.cwd && roots[0]) tab.cwd = roots[0];
    updateTermCwd(tab);
  });
  tab.ro = new ResizeObserver(() => fitTermTab(tab));
  tab.ro.observe($(".pane-term-host", el));
  initTermRuntime(tab);
}

// convRoots caches the conversation's usable directories on the tab. A picker
// open refetches (fresh=true) so project joins, root edits and new folders
// surface without a page reload — a stale cache once pinned the list empty.
async function convRoots(tab, fresh) {
  if (!fresh && tab.roots) return tab.roots;
  try {
    // Drafts (no conversation yet) scope to the selected project's roots;
    // an unscoped draft offers the home directory.
    let url = tab.convId ? `/conversations/${encodeURIComponent(tab.convId)}/roots`
      : S.selectedProjectId ? `/projects/${encodeURIComponent(S.selectedProjectId)}/roots` : null;
    if (url) {
      const d = await api(url);
      tab.roots = d.roots || [];
    } else {
      const d = await api("/fs/list");
      tab.roots = d.path ? [d.path] : [];
    }
  } catch { return tab.roots || []; }
  return tab.roots;
}

function updateTermCwd(tab) {
  const pathEl = $(".pane-term-cwd-path", tab.el);
  if (pathEl) {
    const b = document.createElement("bdi"); // isolate: keep leading / while rtl truncates the head
    b.textContent = tab.cwd || "…";
    pathEl.replaceChildren(b);
    pathEl.title = tab.cwd || "";
  }
  const caret = $(".pane-term-cwd-caret", tab.el);
  if (caret) caret.hidden = (tab.roots || []).length < 2;
}

// openRootsMenu shows the conversation's pickable directories — its project's
// working dirs for members, its roots otherwise — with a check on the
// current one.
function openRootsMenu(anchor, roots, current, onPick) {
  const parts = [];
  for (const r of roots) {
    const name = r.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || r;
    parts.push(`<button class="menu-item pane-root" data-root="${esc(r)}"><span class="pane-root-check">${r === current ? icon("check", "sm") : ""}</span><span class="pane-root-item"><span class="pane-root-name">${esc(name)}</span><span class="pane-root-dir" title="${esc(r)}"><bdi>${esc(r)}</bdi></span></span></button>`);
  }
  openPopover(anchor, `<div class="browser-menu pane-roots">${parts.join("")}</div>`, { width: 280 });
  $$(".pane-root", $(".popover")).forEach((b) => b.addEventListener("click", () => {
    closePopover();
    onPick(b.dataset.root);
  }));
}

// disposeTabRuntime drops a terminal tab's live resources. Bumping tab.gen
// invalidates any initTermRuntime still awaiting loadXterm, so a superseded
// runtime can never attach its socket/xterm to the tab.
function disposeTabRuntime(tab) {
  tab.gen = (tab.gen || 0) + 1;
  tab.exitNote = "";
  try { tab.ws?.close(); } catch {}
  tab.ws = null;
  try { tab.term?.dispose(); } catch {}
  tab.term = null;
}

async function initTermRuntime(tab) {
  if (!tab.el) return;
  const host = $(".pane-term-host", tab.el);
  if (!host) return;
  const gen = (tab.gen = tab.gen || 0);
  setTermState(tab, "connecting", "Connecting…");
  try { await loadXterm(); }
  catch { setTermState(tab, "dead", "Terminal could not be loaded."); return; }
  if (!tab.el || tab.gen !== gen) return; // disposed or restarted while loading
  try {
    host.innerHTML = "";
    const term = new Terminal({ cursorBlink: true, fontSize: 13, theme: { background: "#0b0c0e", foreground: "#e8e8e8" } });
    if (typeof WebLinksAddon !== "undefined") term.loadAddon(new WebLinksAddon.WebLinksAddon());
    tab.term = term;
    term.open(host);
    const { cols, rows } = termDims(host);
    term.resize(cols, rows);
    const url = new URL(tab.convId
      ? `/api/conversations/${encodeURIComponent(tab.convId)}/terminal`
      : `/api/terminal`, location.href);
    if (tab.cwd) url.searchParams.set("cwd", tab.cwd);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    tab.ws = socket;
    socket.onopen = () => {
      if (tab.ws !== socket) return;
      setTermState(tab, "live");
      socket.send(JSON.stringify({ type: "resize", cols, rows }));
      term.focus();
    };
    socket.onmessage = (event) => {
      if (tab.ws !== socket) return;
      if (event.data instanceof ArrayBuffer) { term.write(new Uint8Array(event.data)); return; }
      try {
        const msg = JSON.parse(event.data);
        if (msg.type === "exit") {
          // onclose fires right after; keep the server's reason on the overlay.
          tab.exitNote = msg.message || "Terminal session ended.";
          setTermState(tab, "dead", tab.exitNote);
        }
      } catch {}
    };
    socket.onclose = () => {
      if (tab.ws !== socket) return;
      setTermState(tab, "dead", tab.exitNote || "Connection lost.");
    };
    socket.onerror = () => {
      if (tab.ws !== socket) return;
      setTermState(tab, "dead", tab.exitNote || "Connection failed.");
    };
    term.onData((data) => { if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "input", data })); });
  } catch {
    try { tab.ws?.close(); tab.ws = null; } catch {}
    if (tab.gen === gen) setTermState(tab, "dead", "Terminal could not be started.");
  }
}

/* ---- files tab ---- */

function fmtSize(n) {
  if (!Number.isFinite(n)) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

async function loadFilesTab(tab, path) {
  const el = tab.el;
  if (!el) return;
  if (!el.dataset.built) {
    el.dataset.built = "1";
    el.innerHTML = `<div class="pane-files-bar">
      <button class="pane-term-cwd" data-files-roots title="Directory" aria-haspopup="true">${icon("folder", "sm")}<span class="pane-term-cwd-path pane-files-path"></span><span class="pane-term-cwd-caret" data-files-caret hidden>${icon("chevron-down", "sm")}</span></button>
      <span class="pane-files-actions"><button class="icon-btn sm" data-files-up title="Up one folder" aria-label="Up one folder">${icon("chevron-up", "sm")}</button><button class="icon-btn sm" data-files-refresh title="Refresh" aria-label="Refresh">${icon("refresh", "sm")}</button></span>
    </div><div class="pane-file-list"></div>`;
    convRoots(tab).then((roots) => {
      const c = tab.el && $("[data-files-caret]", tab.el);
      if (c) c.hidden = roots.length < 2;
    });
    $("[data-files-roots]", el).addEventListener("click", async (e) => {
      const anchor = e.currentTarget;
      const roots = await convRoots(tab, true);
      if (!tab.el?.isConnected || !roots.length) return;
      const caret = $("[data-files-caret]", tab.el);
      if (caret) caret.hidden = roots.length < 2;
      openRootsMenu(anchor, roots, tab.path, (path) => loadFilesTab(tab, path));
    });
    $("[data-files-up]", el).addEventListener("click", () => { if (tab.parent) loadFilesTab(tab, tab.parent); });
    $("[data-files-refresh]", el).addEventListener("click", () => loadFilesTab(tab, tab.path));
    $(".pane-file-list", el).addEventListener("click", (e) => {
      const row = e.target.closest(".pane-file-row");
      if (!row || !tab.el) return;
      if (row.dataset.dir === "1") loadFilesTab(tab, row.dataset.path);
      else previewResource(tab.convId
        ? `/api/conversations/${encodeURIComponent(tab.convId)}/files?path=${encodeURIComponent(row.dataset.path)}`
        : `/api/fs/file?path=${encodeURIComponent(row.dataset.path)}`, row.dataset.name || "File", { conversationId: tab.convId });
    });
  }
  const q = new URLSearchParams({ files: "1" });
  const wanted = path ?? tab.path ?? (await convRoots(tab))[0] ?? S.current?.cwd ?? "";
  if (wanted) q.set("path", wanted);
  const gen = (tab.filesGen = (tab.filesGen || 0) + 1); // last request wins
  const list = $(".pane-file-list", el);
  list.innerHTML = `<div class="pane-files-empty">Loading…</div>`;
  try {
    const d = await api(`/fs/list?${q}`);
    if (!tab.el || tab.filesGen !== gen) return;
    tab.path = d.path || wanted;
    tab.parent = d.parent || "";
    const bar = $(".pane-files-path", el);
    const bdi = document.createElement("bdi");
    bdi.textContent = d.path || "";
    bar.replaceChildren(bdi);
    bar.title = d.path || "";
    $("[data-files-up]", el).disabled = !d.parent;
    const rows = [];
    for (const dir of d.dirs || []) rows.push(`<button class="pane-file-row" data-dir="1" data-path="${esc(dir.path)}" title="${esc(dir.path)}">${icon("folder", "sm")}<span class="pane-file-name">${esc(dir.name)}</span></button>`);
    for (const f of d.files || []) rows.push(`<button class="pane-file-row" data-path="${esc(f.path)}" data-name="${esc(f.name)}" title="${esc(f.path)}">${icon("file", "sm")}<span class="pane-file-name">${esc(f.name)}</span><span class="pane-file-size">${esc(fmtSize(f.size))}</span></button>`);
    list.innerHTML = rows.join("") || `<div class="pane-files-empty">Empty folder</div>`;
  } catch (err) {
    if (!tab.el || tab.filesGen !== gen) return;
    list.innerHTML = `<div class="pane-files-empty">${esc(err.message || "Could not list this folder.")}</div>`;
  }
}
