import { $, esc, icon } from "./util.js";
import { S } from "./state.js";
import { renderChat } from "./chat.js";
import { renderComposer } from "./composer.js";
import { loadOlderMessages } from "./conversations.js";
import { renderSidebar, sidebarCollapsed } from "./sidebar.js";
import { renderTopbar } from "./topbar.js";
import { resetBrowserPane } from "./browser.js";

/* ---------------- shell ---------------- */
// Scroll-follow state for the chat transcript. A user gesture timestamps the
// intent; the scroll handler then decides pin state by DIRECTION: any upward
// move (wheel, drag, keyboard, momentum) unpins the follow-to-bottom pull,
// and only returning within ~40px of the tail re-pins it. A distance-only
// threshold fights slow upward scrolls during streaming — each small notch
// stayed "pinned" and the next render snapped the view back to the bottom.
let userScrollAt = 0;
let lastChatTop = -1;

export function noteChatScrollGesture() { userScrollAt = Date.now(); }

export function handleChatScroll() {
  const sc = $("#chat-scroll");
  if (!sc) return;
  const top = sc.scrollTop;
  const movedUp = lastChatTop >= 0 && top < lastChatTop - 1;
  lastChatTop = top;
  if (Date.now() - userScrollAt > 1000) return;
  if (movedUp) S.scrollPinned = false;
  else S.scrollPinned = sc.scrollHeight - top - sc.clientHeight < 40;
  if (top < 160 && S.historyHasMore && !S.historyError && !S.loadingConversation) loadOlderMessages();
}

export function renderShell() {
  const app = $("#app");
  resetBrowserPane();
  app.className = "";
  app.innerHTML = `
    <div class="mobile-scrim" id="scrim" data-action="close-sidebar"></div>
    <div class="app">
      <aside class="sidebar" id="sidebar"></aside>
      <main class="main">
        <header class="topbar" id="topbar"></header>
        <div id="offline-banner"></div>
        <div class="chat-scroll" id="chat-scroll"><div class="chat-inner" id="chat"></div></div>
        <div class="plan-dock" id="plan-dock"></div>
        <div class="composer-wrap" id="composer-wrap"></div>
      </main>
      <aside class="browser-pane" id="browser-pane" hidden></aside>
    </div>`;
  $(".app")?.classList.toggle("sidebar-collapsed", sidebarCollapsed());
  document.title = "NusaMation";
  const sc = $("#chat-scroll");
  for (const type of ["touchstart", "pointerdown", "keydown"]) {
    sc.addEventListener(type, noteChatScrollGesture, { passive: true });
  }
  sc.addEventListener("wheel", (e) => {
    noteChatScrollGesture();
    // A wheel-up is an explicit "stop following" — unpin right away, even
    // for a small notch near the bottom; otherwise the next streamed chunk
    // yanks the viewport back down and the user fights the scroller.
    if (e.deltaY < 0) S.scrollPinned = false;
  }, { passive: true });
  sc.addEventListener("scroll", handleChatScroll, { passive: true });
  renderSidebar();
  renderTopbar();
  renderOffline();
  renderChat();
  renderComposer();
}

export function renderOffline() {
  const el = $("#offline-banner");
  if (!el) return;
  if (S.wsState === "online") {
    el.innerHTML = "";
    el.className = "";
    return;
  }
  const msg = !navigator.onLine ? "You are offline. Messages will wait." : "Reconnecting to the live stream…";
  el.className = "offline-banner";
  el.innerHTML = `${icon("wifi-off")}<span>${esc(msg)}</span>`;
}
