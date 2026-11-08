import { $, esc, icon } from "./util.js";
import { S } from "./state.js";
import { renderChat } from "./chat.js";
import { renderComposer } from "./composer.js";
import { loadOlderMessages } from "./conversations.js";
import { renderSidebar, sidebarCollapsed } from "./sidebar.js";
import { renderTopbar } from "./topbar.js";
import { resetBrowserPane } from "./browser.js";

/* ---------------- shell ---------------- */
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
        <div class="composer-wrap" id="composer-wrap"></div>
      </main>
      <aside class="browser-pane" id="browser-pane" hidden></aside>
    </div>`;
  $(".app")?.classList.toggle("sidebar-collapsed", sidebarCollapsed());
  document.title = "NusaMation";
  const sc = $("#chat-scroll");
  let userScrollAt = 0;
  for (const type of ["wheel", "touchstart", "pointerdown", "keydown"]) {
    sc.addEventListener(type, () => { userScrollAt = Date.now(); }, { passive: true });
  }
  sc.addEventListener("scroll", () => {
    if (Date.now() - userScrollAt < 1000) {
      S.scrollPinned = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 140;
      if (sc.scrollTop < 160 && S.historyHasMore && !S.historyError && !S.loadingConversation) loadOlderMessages();
    }
  }, { passive: true });
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
