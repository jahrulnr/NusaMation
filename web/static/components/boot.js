import { $, esc, icon } from "./util.js?v=44";
import { S, setSelectedProject } from "./state.js?v=44";
import { api } from "./api.js?v=44";
import { selectInitialConversation } from "./conversations.js?v=44";
import { renderShell } from "./shell.js?v=44";
import { renderSidebar } from "./sidebar.js?v=44";
import { renderTopbar } from "./topbar.js?v=44";
import { connectWS } from "./ws.js?v=44";
import { renderLogin } from "./auth.js?v=44";

/* ---------------- boot / auth ---------------- */
export async function boot() {
  try {
    const st = await api("/auth/status");
    S.authInitialized = !!st.initialized;
    S.appVersion = st.version || "";
    if (st.authenticated && st.csrf) {
      S.csrf = st.csrf;
      await startApp();
    } else {
      renderLogin();
    }
  } catch (e) {
    renderLogin("Cannot reach the NusaMation server. Is it running?");
    S.authInitialized = true;
  }
}

/* ---------------- app start ---------------- */
export function bootSkeletonHTML() {
  return `<div class="boot-skeleton" role="status" aria-label="Loading NusaMation"><aside><span class="skeleton-line short"></span><span class="skeleton-line"></span><span class="skeleton-line"></span><span class="skeleton-line short"></span></aside><main><header><span class="skeleton-line short"></span></header><div class="chat-skeleton"><div class="skeleton-message user"><span></span><span></span></div><div class="skeleton-message assistant"><i></i><span></span><span></span><span></span></div><div class="skeleton-message user"><span></span><span></span></div></div><footer><span class="skeleton-line"></span></footer></main></div>`;
}

export async function startApp() {
  S.booted = true;
  S.current = null;
  S.messages = [];
  S.active = null;
  S.attachments = [];
  S.draft = "";
  S.approvals = [];
  S.approvalsById = new Map();
  const app = $("#app");
  app.className = "";
  app.innerHTML = bootSkeletonHTML();
  try {
    const data = await api("/bootstrap");
    applyBootstrap(data);
    S.booted = true;
    renderShell();
    connectWS();
    await selectInitialConversation();
  } catch (e) {
    if (e.status === 401) return;
    renderBootError(e.message || "Could not load NusaMation.");
  }
}

export function renderBootError(message) {
  $("#app").innerHTML = `<div class="boot"><div class="boot-orb"></div><div class="boot-name">NusaMation</div><div class="boot-sub" role="alert">${esc(message)}</div><button class="btn btn-secondary" data-action="retry-boot">${icon("refresh", "sm")} Retry</button></div>`;
}

export function applyBootstrap(data) {
  S.bootstrap = data;
  // Version is frozen at page load: if a refresh ever sees a newer server
  // (post self-update), the hello event triggers a full reload instead of
  // silently retargeting the baseline it compares against.
  S.appVersion = S.appVersion || data.app?.version || "";
  S.agents = data.agents || [];
  S.mcp = data.mcp || [];
  S.channels = data.channels || [];
  S.conversations = data.conversations || [];
  S.projects = data.projects || [];
  if (S.selectedProjectId && !S.projects.some((p) => p.id === S.selectedProjectId)) setSelectedProject("");
  S.approvals = data.approvals || [];
  S.approvalsById = new Map(S.approvals.map((a) => [a.id, a]));
  S.agentDefaults = data.agentDefaults || { agentId: "", selections: [] };
  S.welcomeAgent = S.welcomeAgent || (S.agents.find((a) => a.id === S.agentDefaults.agentId && a.enabled) || {}).id || (S.agents.find((a) => a.enabled) || {}).id || "";
  S.channels.forEach((c) => { if (c.name === "whatsapp") S.whatsapp = c; });
  S.whatsappSettings = data.whatsapp || S.whatsappSettings || null;
  S.routingDefaults = data.routingDefaults || S.routingDefaults || null;
  if (!S.current && data.conversations && data.conversations.length) {
    // The initial route may select another conversation.
    S.current = data.conversations[0];
  }
}

export async function refreshBootstrap() {
  const data = await api("/bootstrap");
  applyBootstrap(data);
  renderSidebar();
  renderTopbar();
}
