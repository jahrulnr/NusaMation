/* NusaMation web control center — vanilla JS, no build step. */
(() => {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const clamp = (n, a, b) => Math.max(a, Math.min(b, n));
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const now = () => Date.now();
  const uid = (p = "x") => p + "_" + Math.random().toString(36).slice(2, 10);
  const isMobile = () => window.matchMedia("(max-width: 800px)").matches;
  const isShort = () => window.matchMedia("(max-height: 500px)").matches;
  const REQUEST_TIMEOUT_MS = 20000;
  function readActivityState() {
    try { return JSON.parse(sessionStorage.getItem("nusamation:activity-state") || "{}"); } catch { return {}; }
  }
  const activityState = readActivityState();

  const ICONS = {};
  function icon(name, cls = "") {
    return `<svg class="icon ${cls}" aria-hidden="true"><use href="#i-${name}"></use></svg>`;
  }

  const AGENT_COLORS = {
    amber: "linear-gradient(145deg,#ffd27b,#d99125)",
    violet: "linear-gradient(145deg,#c8b6ff,#7a5cff)",
    emerald: "linear-gradient(145deg,#9af0d6,#2fae8d)",
    blue: "linear-gradient(145deg,#a9d2ff,#4b8fe8)",
    rose: "linear-gradient(145deg,#ffc0cb,#e5678f)",
    slate: "linear-gradient(145deg,#c5ccd8,#6c7a8e)",
  };

  // Tool kinds the UI knows how to style. t.kind is agent-supplied, so anything
  // outside this set falls back to "other" before it reaches a class attribute.
  const TOOL_KINDS = new Set(["read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"]);

  const S = {
    authInitialized: false,
    csrf: "",
    me: null,
    bootstrap: null,
    agents: [],
    mcp: {},
    channels: [],
    conversations: [],
    approvals: [],
    approvalsById: new Map(),
    elicitations: [],
    current: null,
    currentAgent: null,
    messages: [],
    turns: {},
    configs: [],
    active: null,
    commands: [],
    unread: new Map(),
    openThoughts: new Set(activityState.thoughts || []),
    openTools: new Set(activityState.tools || []),
    activityExpanded: new Map(activityState.groups || []),
    unwrappedTools: new Set(activityState.unwrapped || []),
    resolving: new Set(),
    attachments: [],
    replyToChannel: false,
    draft: "",
    whatsappSettings: null,
    routingDefaults: null,
    agentDefaults: { agentId: "", selections: [] },
    defaultDraft: null,
    defaultConfigs: [],
    defaultBusy: false,
    defaultError: "",
    defaultSeq: 0,
    automations: [],
    automationView: "list",
    automationJob: null,
    automationRuns: [],
    automationLoading: false,
    automationError: "",
    automationServerZone: "",
    ws: null,
    wsState: "offline",
    reconnectAttempt: 0,
    reconnectTimer: null,
    route: location.pathname,
    query: "",
    welcomeAgent: "",
    settingsTab: "agents",
    agentForm: null,
    mcpForm: null,
    editingAgent: null,
    editingMCP: null,
    modal: null,
    qrTimer: null,
    renderTimer: null,
    sending: false,
    sendDelivery: "queue",
    scrollPinned: true,
    draftSelections: [],
    draftPreviewSeq: 0,
    booted: false,
    loadingConversation: false,
    loadError: "",
    loadSeq: 0,
    preparing: "",
    prepareController: null,
    slowTimer: null,
    authTerminal: null,
    lastNotice: "",
    channelsRefreshTimer: null,
  };

  // Composer state belongs to a conversation; the empty key is the welcome draft.
  const composers = new Map();
  let composerKey = "";
  function composerFor(key) {
    if (!composers.has(key)) composers.set(key, { draft: "", attachments: [], sendDelivery: "queue", replyToChannel: false, sending: false });
    return composers.get(key);
  }
  for (const field of ["draft", "attachments", "sendDelivery", "replyToChannel", "sending"]) {
    Object.defineProperty(S, field, {
      get: () => composerFor(composerKey)[field],
      set: (value) => { composerFor(composerKey)[field] = value; },
    });
  }
  function switchComposer(key) { composerKey = key || ""; }
  function moveComposer(from, to) {
    composers.set(to, composerFor(from));
    composers.delete(from);
    if (composerKey === from) switchComposer(to);
  }

  /* ---------------- time / formatting ---------------- */
  function timeAgo(ms) {
    if (!ms) return "";
    const d = Math.max(0, now() - ms);
    if (d < 45e3) return "now";
    if (d < 36e5) return Math.floor(d / 60e3) + "m";
    if (d < 864e5) return Math.floor(d / 36e5) + "h";
    if (d < 7 * 864e5) return Math.floor(d / 864e5) + "d";
    return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }
  function clock(ms) {
    if (!ms) return "";
    return new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }
  function dayLabel(ms) {
    if (!ms) return "";
    const d = new Date(ms);
    const today = new Date();
    const y = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
    if (d.toDateString() === today.toDateString()) return "Today";
    if (d.toDateString() === y.toDateString()) return "Yesterday";
    return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" });
  }
  function fmtBytes(n) {
    n = Number(n || 0);
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / (1024 * 1024)).toFixed(1) + " MB";
  }
  function truncate(s, n) {
    s = String(s || "");
    return s.length > n ? s.slice(0, n - 1).trimEnd() + "…" : s;
  }
  function debounce(fn, ms) {
    let t;
    return (...args) => {
      clearTimeout(t);
      t = setTimeout(() => fn(...args), ms);
    };
  }

  function initials(name) {
    const s = String(name || "?").trim();
    if (!s) return "?";
    const parts = s.split(/\s+/).slice(0, 2);
    return parts.map((p) => p[0]).join("").toUpperCase();
  }
  function agentOrb(agent, cls = "agent-orb") {
    const color = AGENT_COLORS[(agent && agent.color) || "amber"] || AGENT_COLORS.amber;
    return `<span class="${cls}" style="background:${color}">${esc(initials(agent && agent.name))}</span>`;
  }
  function agentName(agent) {
    return (agent && agent.name) || "Agent";
  }
  function currentAgent() {
    if (S.current) return S.agents.find((a) => a.id === S.current.agentId) || (S.currentAgent?.id === S.current.agentId ? S.currentAgent : null);
    return S.agents.find((a) => a.id === S.welcomeAgent) || S.agents.find((a) => a.enabled) || S.agents[0] || null;
  }
  function agentById(id) {
    return S.agents.find((a) => a.id === id);
  }

  /* ---------------- API ---------------- */
  class ApiError extends Error {
    constructor(message, status, code, data = {}) {
      super(message);
      this.status = status;
      this.code = code;
      this.data = data;
    }
  }

  async function api(path, opts = {}) {
    const headers = new Headers(opts.headers || {});
    let body = opts.body;
    if (body !== undefined && !(body instanceof FormData)) {
      headers.set("Content-Type", "application/json");
      body = JSON.stringify(body);
    }
    if (S.csrf) headers.set("X-CSRF-Token", S.csrf);
    const timeout = opts.timeout ?? ((opts.method || "GET") === "GET" ? REQUEST_TIMEOUT_MS : 0);
    let res, text;
    try {
      res = await fetch("/api" + path, { ...opts, body, headers, credentials: "same-origin", signal: timeout ? AbortSignal.timeout?.(timeout) : opts.signal });
      text = await res.text();
    } catch (e) {
      if (e.name === "TimeoutError") throw new ApiError("The server took too long to respond.", 0, "timeout");
      throw new ApiError("Network error. Check your connection.", 0, "network");
    }
    let data = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      if (res.ok) throw new ApiError("Unexpected response from server.", res.status, "bad_response");
    }
    if (res.status === 401 && !opts.noAuthRedirect) {
      handleLoggedOut();
      throw new ApiError(data.error || "Your session expired.", 401, data.code);
    }
    if (!res.ok) throw new ApiError(data.error || `Request failed (${res.status})`, res.status, data.code, data);
    return data;
  }

  function handleLoggedOut() {
    if (!S.booted) return;
    S.booted = false;
    closeWS();
    closeModal();
    renderLogin("Your session has expired. Sign in again to continue.");
  }

  /* ---------------- toasts ---------------- */
  function toast(title, description = "", type = "info", ttl = 5200) {
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

  /* ---------------- markdown ---------------- */
  function renderMarkdown(text) {
    if (window.marked && window.DOMPurify) {
      try {
        window.marked.setOptions({ gfm: true, breaks: true });
        let html = window.marked.parse(String(text || ""));
        html = window.DOMPurify.sanitize(html, { ADD_ATTR: ["target", "rel"] });
        return html;
      } catch (e) {
        console.warn("markdown failed", e);
      }
    }
    return `<p>${esc(text).replace(/\n/g, "<br>")}</p>`;
  }

  function enhanceMarkdown(root) {
    if (!root) return;
    $$("a", root).forEach((a) => {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
    });
    $$("pre", root).forEach((pre) => {
      if (pre.dataset.enhanced) return;
      pre.dataset.enhanced = "1";
      pre.style.position = "relative";
      if (!pre.classList.contains("tool-pre")) {
        const btn = document.createElement("button");
        btn.className = "code-copy";
        btn.type = "button";
        btn.dataset.action = "copy-code";
        btn.title = "Copy code";
        btn.innerHTML = icon("copy", "sm");
        pre.appendChild(btn);
      }
      if (window.hljs) {
        $$("code", pre).forEach((code) => {
          if (code.dataset.highlighted) return;
          try {
            window.hljs.highlightElement(code);
            code.dataset.highlighted = "1";
          } catch {}
        });
      }
    });
  }

  /* ---------------- boot / auth ---------------- */
  async function boot() {
    try {
      const st = await api("/auth/status");
      S.authInitialized = !!st.initialized;
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

  function renderLogin(error = "") {
    S.booted = false;
    document.title = "Sign in · NusaMation";
    $("#modal-root").innerHTML = "";
    $("#app").className = "";
    $("#app").innerHTML = `
      <div class="login">
        <div class="login-art">
          <div class="login-brand"><span class="brand-mark">N</span><span>NusaMation</span></div>
          <div class="login-copy">
            <h1>Your agent.<br>Everywhere.<br><em>One mesh.</em></h1>
            <p>ACP-native personal agents with full-fidelity web controls and a concise, human-sized presence on WhatsApp. Thoughts, tools, approvals and models — all in one place.</p>
          </div>
          <div class="login-points">
            <span><i class="dot"></i> Agent Client Protocol</span>
            <span><i class="dot"></i> WhatsApp + Web</span>
            <span><i class="dot"></i> Local-first</span>
          </div>
        </div>
        <div class="login-panel">
          <div class="login-card">
            <div class="login-mobile-brand"><span class="brand-mark">N</span><span>NusaMation</span></div>
            <h2>Welcome back</h2>
            <p class="lead">Sign in to your control center.</p>
            ${error ? `<div class="login-error">${icon("alert", "sm")}<span>${esc(error)}</span></div>` : ""}
            ${!S.authInitialized ? `<div class="login-error">${icon("info", "sm")}<span>First-run setup has not completed. Restart NusaMation and check the terminal for the bootstrap password.</span></div>` : ""}
            <form data-form="login" autocomplete="on">
              <div class="field">
                <label for="login-password">Password</label>
                <div class="password-wrap">
                  <input class="input" id="login-password" name="password" type="password" placeholder="Your admin password" autocomplete="current-password" autofocus required>
                  <button class="password-toggle" type="button" data-action="toggle-password" aria-label="Show password">${icon("key", "sm")}</button>
                </div>
              </div>
              <div class="login-row">
                <label class="checkbox"><input type="checkbox" name="remember" checked> Keep me signed in</label>
              </div>
              <button class="btn btn-primary btn-block" type="submit" data-login-submit>Sign in ${icon("chevron-right", "sm")}</button>
            </form>
            <div class="login-foot">NusaMation ${esc((S.bootstrap && S.bootstrap.app && S.bootstrap.app.version) || "0.1.0")} · local-first · your data stays here</div>
          </div>
        </div>
      </div>`;
  }

  async function doLogin(form) {
    const btn = form.querySelector("[data-login-submit]");
    const pw = form.querySelector("[name=password]").value;
    const remember = form.querySelector("[name=remember]").checked;
    if (!pw) return;
    btn.disabled = true;
    btn.innerHTML = `<span class="spinner"></span> Signing in…`;
    try {
      const data = await api("/auth/login", { method: "POST", body: { password: pw, remember }, noAuthRedirect: true, timeout: REQUEST_TIMEOUT_MS });
      S.csrf = data.csrf;
      await startApp();
    } catch (e) {
      btn.disabled = false;
      btn.innerHTML = `Sign in ${icon("chevron-right", "sm")}`;
      const card = form.closest(".login-card");
      let box = card.querySelector(".login-error");
      if (!box) {
        box = document.createElement("div");
        box.className = "login-error";
        card.querySelector("form").before(box);
      }
      box.innerHTML = `${icon("alert", "sm")}<span>${esc(e.message || "Sign in failed.")}</span>`;
      form.querySelector("[name=password]").select();
    }
  }

  /* ---------------- app start ---------------- */
  async function startApp() {
    S.booted = true;
    S.current = null;
    S.messages = [];
    S.active = null;
    S.attachments = [];
    S.draft = "";
    S.approvals = [];
    S.approvalsById = new Map();
    const app = $("#app");
    app.className;
    app.innerHTML = `<div class="boot"><div class="boot-orb"></div><div class="boot-name">NusaMation</div><div class="boot-sub">loading your agent mesh…</div></div>`;
    try {
      const data = await api("/bootstrap");
      applyBootstrap(data);
      S.booted = true;
      renderShell();
      connectWS();
      await selectInitialConversation();
      if (S.whatsappNeedRefresh) refreshChannels();
    } catch (e) {
      if (e.status === 401) return;
      renderBootError(e.message || "Could not load NusaMation.");
    }
  }

  function renderBootError(message) {
    $("#app").innerHTML = `<div class="boot"><div class="boot-orb"></div><div class="boot-name">NusaMation</div><div class="boot-sub" role="alert">${esc(message)}</div><button class="btn btn-secondary" data-action="retry-boot">${icon("refresh", "sm")} Retry</button></div>`;
  }

  function applyBootstrap(data) {
    S.bootstrap = data;
    S.agents = data.agents || [];
    S.mcp = data.mcp || {};
    S.channels = data.channels || [];
    S.conversations = data.conversations || [];
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

  async function refreshBootstrap() {
    const data = await api("/bootstrap");
    applyBootstrap(data);
    renderSidebar();
    renderTopbar();
  }

  async function selectInitialConversation() {
    const m = location.pathname.match(/^\/c\/([^/]+)/);
    let id = m ? decodeURIComponent(m[1]) : "";
    if (!id) {
      const last = localStorage.getItem("nusamation:lastConversation");
      if (last && S.conversations.some((c) => c.id === last)) id = last;
    }
    if (!id && S.conversations.length && !isMobile()) id = S.conversations[0].id;
    if (id) {
      await loadConversation(id, false);
    } else {
      renderChat();
      renderComposer();
    }
  }

  async function selectConversation(id, push = true) {
    if (!id) return;
    if (S.current && S.current.id === id && S.messages.length) {
      if (push) setRoute(id);
      closeMobileSidebar();
      return;
    }
    S.unread.delete(id);
    await loadConversation(id, push);
  }

  async function loadConversation(id, push = true) {
    const seq = ++S.loadSeq;
    S.loadingConversation = true;
    S.loadError = "";
    switchComposer(id);
    S.scrollPinned = true;
    S.current = S.conversations.find((c) => c.id === id) || S.current;
    S.configs = [];
    renderSidebar();
    renderTopbar();
    if (S.current && S.current.id !== id) S.current = null;
    renderChat(true);
    renderComposer();
    try {
      const data = await api(`/conversations/${encodeURIComponent(id)}`);
      if (seq !== S.loadSeq) return;
      S.current = data.conversation;
      S.messages = (data.messages || []).map(normalizeMessage);
      S.turns = data.turns || {};
      S.configs = data.configs || parseConfigs(data.conversation.configOptions);
      S.active = data.active && data.active.running ? data.active : null;
      S.currentAgent = data.agent || agentById(data.conversation.agentId);
      (data.approvals || []).forEach((a) => S.approvalsById.set(a.id, a));
      S.elicitations = data.elicitations || [];
      S.unread.delete(id);
      S.loadingConversation = false;
      localStorage.setItem("nusamation:lastConversation", id);
      upsertConversation(data.conversation, false);
      if (push) setRoute(id);
      renderSidebar();
      renderTopbar();
      renderChat();
      renderComposer();
      showPendingRequest();
      closeMobileSidebar();
      if (!S.active) prepareComposer();
    } catch (e) {
      if (seq !== S.loadSeq) return;
      S.loadingConversation = false;
      if (e.status === 404) {
        S.conversations = S.conversations.filter((c) => c.id !== id);
        S.current = null;
        composers.delete(id);
        switchComposer("");
        S.messages = [];
        setRoute("");
        renderSidebar();
        renderChat();
        renderTopbar();
        renderComposer();
      } else {
        S.loadError = id;
        toast("Could not open conversation", e.message, "error");
        renderChat();
      }
    }
  }

  function setRoute(id) {
    const path = id ? `/c/${encodeURIComponent(id)}` : "/";
    if (location.pathname !== path) window.history.pushState({ id }, "", path);
    S.route = path;
  }

  function upsertConversation(conv, render = true) {
    if (!conv || !conv.id) return;
    const i = S.conversations.findIndex((c) => c.id === conv.id);
    if (i >= 0) S.conversations[i] = { ...S.conversations[i], ...conv };
    else S.conversations.unshift(conv);
    S.conversations.sort((a, b) => (Number(b.pinned) - Number(a.pinned)) || ((b.lastMessageAt || b.updatedAt || 0) - (a.lastMessageAt || a.updatedAt || 0)));
    if (S.current && S.current.id === conv.id) S.current = { ...S.current, ...conv };
    if (render) { renderSidebar(); renderTopbar(); }
  }

  function normalizeMessage(m) {
    if (!m) return m;
    return {
      ...m,
      kind: m.kind === "assistant" ? "text" : m.kind,
      status: m.status === "completed" ? "complete" : m.status,
      text: m.text || "",
    };
  }

  /* ---------------- shell ---------------- */
  function renderShell() {
    const app = $("#app");
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
      </div>`;
    document.title = "NusaMation";
    const sc = $("#chat-scroll");
    let userScrollAt = 0;
    for (const type of ["wheel", "touchstart", "pointerdown", "keydown"]) {
      sc.addEventListener(type, () => { userScrollAt = Date.now(); }, { passive: true });
    }
    sc.addEventListener("scroll", () => {
      if (Date.now() - userScrollAt < 1000) S.scrollPinned = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 140;
    }, { passive: true });
    renderSidebar();
    renderTopbar();
    renderOffline();
    renderChat();
    renderComposer();
  }

  function renderOffline() {
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

  /* ---------------- sidebar ---------------- */
  function renderSidebar() {
    const el = $("#sidebar");
    if (!el) return;
    const convs = S.conversations.filter((c) => !c.archived);
    const pinned = convs.filter((c) => c.pinned);
    const rest = convs.filter((c) => !c.pinned);
    const groups = [];
    const today = new Date(); today.setHours(0, 0, 0, 0);
    const yesterday = new Date(today.getTime() - 864e5);
    const gToday = [], gYesterday = [], gEarlier = [];
    rest.forEach((c) => {
      const t = c.lastMessageAt || c.updatedAt || 0;
      if (t >= today.getTime()) gToday.push(c);
      else if (t >= yesterday.getTime()) gYesterday.push(c);
      else gEarlier.push(c);
    });
    if (pinned.length) groups.push(["Pinned", pinned]);
    if (gToday.length) groups.push(["Today", gToday]);
    if (gYesterday.length) groups.push(["Yesterday", gYesterday]);
    if (gEarlier.length) groups.push(["Earlier", gEarlier]);

    el.innerHTML = `
      <div class="sidebar-top">
        <div class="brand">
          <span class="brand-mark">N</span>
          <div><div class="brand-name">NusaMation</div><div class="brand-tag">agent mesh</div></div>
          <button class="icon-btn sm" style="margin-left:auto" data-action="open-settings" title="Settings">${icon("settings", "sm")}</button>
        </div>
        <button class="new-chat" data-action="new-chat">${icon("plus")} New conversation</button>
        <div class="sidebar-search">${icon("search")}<input id="conv-search" type="search" placeholder="Search conversations…" value="${esc(S.query)}" data-input="conv-search" autocomplete="off"></div>
      </div>
      <div class="sidebar-scroll" id="conv-list">
        ${groups.length ? groups.map(([label, list]) => `
          <div class="conv-section" data-group>
            <div class="conv-section-title">${esc(label)}</div>
            ${list.map(convItemHTML).join("")}
          </div>`).join("") : `<div class="sidebar-empty">${S.query ? "No conversations match your search." : "No conversations yet.<br>Start one and it will show up here."}</div>`}
      </div>
      <div class="sidebar-bottom">
        <div class="connection-pill" title="Live event stream">
          <span class="status-dot ${S.wsState === "online" ? "" : S.wsState === "connecting" ? "warn" : "offline"}"></span>
          <span>${S.wsState === "online" ? "Live" : S.wsState === "connecting" ? "Connecting…" : "Offline"}</span>
          <span class="grow"></span>
          ${S.approvals.length ? `<button class="badge-soft amber" data-action="open-approvals">${icon("shield", "sm")} ${S.approvals.length}</button>` : ""}
        </div>
        <div class="user-row" data-action="open-account">
          <div class="user-avatar">AD</div>
          <div class="grow"><div class="user-name">Administrator</div><div class="user-sub">Local owner</div></div>
          ${icon("settings", "sm")}
        </div>
      </div>`;
    filterSidebar();
  }

  function convItemHTML(c) {
    const agent = agentById(c.agentId);
    const links = c.links || [];
    const external = links.find((l) => l.channel !== "web");
    const channel = external ? external.channel : (c.originChannel && c.originChannel !== "web" ? c.originChannel : "");
    const unread = S.unread.get(c.id) || 0;
    const preview = c.lastMessage ? truncate(c.lastMessage.replace(/\s+/g, " "), 70) : "No messages yet";
    return `<div class="conv-item ${S.current && S.current.id === c.id ? "active" : ""}" data-action="select-conv" data-id="${esc(c.id)}" data-title="${esc((c.title || "").toLowerCase())}" data-preview="${esc((c.lastMessage || "").toLowerCase())}">
      <div class="conv-title-row">
        ${c.pinned ? icon("pin", "conv-pin") : ""}
        <span class="conv-title">${esc(c.title || "Untitled")}</span>
        ${unread ? `<span class="conv-badge">${unread > 9 ? "9+" : unread}</span>` : `<span class="conv-time">${timeAgo(c.lastMessageAt || c.updatedAt)}</span>`}
      </div>
      <div class="conv-meta">
        <span class="conv-preview">${esc(preview)}</span>
        ${channel ? `<span class="conv-channel" title="${esc(channel)}">${channel === "whatsapp" ? icon("message") : icon("globe")}</span>` : ""}
        <button class="icon-btn sm" data-action="conv-menu" data-id="${esc(c.id)}" title="Conversation menu">${icon("more", "sm")}</button>
      </div>
    </div>`;
  }

  function filterSidebar() {
    const q = S.query.trim().toLowerCase();
    $$(".conv-section", $("#conv-list")).forEach((group) => {
      let visible = 0;
      $$(".conv-item", group).forEach((item) => {
        const hit = !q || (item.dataset.title || "").includes(q) || (item.dataset.preview || "").includes(q);
        item.classList.toggle("hidden", !hit);
        if (hit) visible++;
      });
      group.classList.toggle("hidden", !visible);
    });
    const list = $("#conv-list");
    if (list && !$(".conv-section:not(.hidden)", list) && q && !$(".sidebar-empty", list)) {
      list.insertAdjacentHTML("beforeend", `<div class="sidebar-empty">No conversations match “${esc(q)}”.</div>`);
    }
  }

  function openMobileSidebar() {
    $("#sidebar")?.classList.add("open");
    $("#scrim")?.classList.add("show");
  }
  function closeMobileSidebar() {
    $("#sidebar")?.classList.remove("open");
    $("#scrim")?.classList.remove("show");
  }

  /* ---------------- topbar ---------------- */
  function renderTopbar() {
    const el = $("#topbar");
    if (!el) return;
    const c = S.current;
    const agent = currentAgent();
    const model = modelLabel(c, agent);
    const pending = S.approvals.length;
    const links = (c && c.links || []).filter((l) => l.channel !== "web");
    const active = S.active && ["running", "waiting_approval", "cancelling", "queued"].includes(S.active.status);
    let sub = "Your ACP control center";
    if (c) {
      sub = `${esc(c.agentName || agentName(agent))}${active ? ` <span class="dot"></span> <span style="color:var(--accent)">${esc(activeLabel())}</span>` : ` <span class="dot"></span> ${esc(c.status || "idle")}`}`;
    }
    el.innerHTML = `
      <button class="icon-btn menu-btn" data-action="open-sidebar" aria-label="Open sidebar">${icon("menu")}</button>
      <div class="topbar-title-wrap">
        <div class="topbar-title">${esc(c ? (c.title || "Untitled") : "NusaMation")}</div>
        <div class="topbar-sub">${sub}</div>
      </div>
      <div class="topbar-actions">
        ${c ? `<button class="pill-btn agent-pill" data-action="agent-menu">${agentOrb(agent)}<span class="pill-label">${esc(agentName(agent))}</span>${icon("chevron-down", "sm chev")}</button>` : ""}
        ${c ? `<button class="pill-btn" data-action="config-menu" title="Session configuration">${icon("sparkles", "sm")}<span class="pill-label">${esc(model)}</span>${icon("chevron-down", "sm chev")}</button>` : ""}
        ${c && links.length ? `<button class="pill-btn accent" data-action="routing-menu" title="Channel routing">${icon("link", "sm")}<span class="pill-label">${esc(links[0].channel)}</span></button>` : ""}
        <button class="icon-btn approval-bell" data-action="open-approvals" title="Approvals">${icon("shield")}${pending ? `<span class="badge">${pending > 9 ? "9+" : pending}</span>` : ""}</button>
        ${c ? `<button class="icon-btn" data-action="more-menu" title="Conversation actions">${icon("more")}</button>` : `<button class="icon-btn" data-action="open-settings" title="Settings">${icon("settings")}</button>`}
      </div>`;
  }

  function modelLabel(c, agent) {
    const opt = S.configs.find((o) => (o.category || "").toLowerCase() === "model");
    if (opt) return configLabel(opt);
    return agent?.defaultModel || "Default model";
  }

  function activeLabel() {
    const s = S.active?.status || "";
    if (s === "waiting_approval") return "Waiting for approval";
    if (s === "queued") return "Queued";
    if (s === "cancelling") return "Stopping";
    return "Working";
  }
  /* ---------------- chat ---------------- */
  function renderChat(skeleton = false) {
    const chat = $("#chat");
    if (!chat) return;
    const sc = $("#chat-scroll");
    const wasPinned = S.scrollPinned || S.messages.length === 0;
    const focused = document.activeElement?.closest?.("[data-action][data-id]");
    const focusedAction = focused && chat.contains(focused) ? [focused.dataset.action, focused.dataset.id] : null;
    if (skeleton || (S.loadingConversation && !S.messages.length)) {
      chat.innerHTML = `<div class="loading-row"><span class="spinner"></span> Loading conversation…</div>`;
      return;
    }
    if (S.loadError && S.loadError === S.current?.id) {
      chat.innerHTML = `<div class="loading-row" role="alert">${icon("alert", "sm")}<span>Could not open this conversation.</span><button class="btn btn-secondary btn-xs" data-action="retry-load" data-id="${esc(S.loadError)}">${icon("refresh", "sm")} Retry</button></div>`;
      return;
    }
    if (!S.current) {
      chat.innerHTML = welcomeHTML();
      enhanceMarkdown(chat);
      return;
    }
    if (!S.messages.length) {
      chat.innerHTML = welcomeHTML(true);
      enhanceMarkdown(chat);
      return;
    }
    const html = [];
    const plan = planEntries(S.current.plan);
    if (plan.length) html.push(planCard(plan));
    let lastDay = "";
    const savedScroll = new Map($$(".tool-pre", chat).map((el) => [el.dataset.scrollKey, [el.scrollTop, el.scrollLeft]]));
    for (let i = 0; i < S.messages.length;) {
      const m = S.messages[i];
      const day = dayLabel(m.createdAt);
      if (day && day !== lastDay) {
        html.push(`<div class="chat-date">${esc(day)}</div>`);
        lastDay = day;
      }
      if (m.turnId && (m.kind === "thought" || m.kind === "tool")) {
        const group = [];
        while (i < S.messages.length && S.messages[i].turnId === m.turnId && (S.messages[i].kind === "thought" || S.messages[i].kind === "tool")) group.push(S.messages[i++]);
        if (group.filter((item) => item.kind === "tool").length > 1) html.push(activityHTML(group));
        else group.forEach((item) => html.push(messageHTML(item)));
      } else {
        html.push(messageHTML(m));
        i++;
      }
    }
    const pending = currentApprovals();
    if (pending.length) {
      pending.forEach((a) => html.push(approvalCard(a)));
    }
    if (S.active && S.active.running && !pending.length) html.push(typingHTML());
    chat.innerHTML = html.join("");
    enhanceMarkdown(chat);
    $$(".tool-pre", chat).forEach((el) => {
      const pos = savedScroll.get(el.dataset.scrollKey);
      if (pos) [el.scrollTop, el.scrollLeft] = pos;
    });
    if (focusedAction) {
      const next = $$("[data-action][data-id]", chat).find((el) => el.dataset.action === focusedAction[0] && el.dataset.id === focusedAction[1]);
      if (next) next.focus({ preventScroll: true });
    }
    if (wasPinned && sc) {
      sc.scrollTop = sc.scrollHeight;
      S.scrollPinned = true;
      requestAnimationFrame(() => {
        if (S.scrollPinned) {
          sc.scrollTop = sc.scrollHeight;
        }
      });
    }
  }

  function currentApprovals() {
    const id = S.current?.id;
    if (!id) return [];
    return Array.from(S.approvalsById.values()).filter((a) => a.conversationId === id && a.status === "pending");
  }

  function messageHTML(m) {
    const kind = m.kind || "text";
    if (kind === "thought") return thoughtHTML(m);
    if (kind === "tool") return toolHTML(m);
    if (kind === "error") return errorHTML(m);
    if (kind === "system" || kind === "notice" || m.role === "system") return systemHTML(m);
    if (m.role === "user") return userHTML(m);
    return assistantHTML(m);
  }

  function messageHead(m) {
    const name = m.role === "user" ? "You" : (S.current?.agentName || agentName(currentAgent()));
    return `<div class="msg-head"><span class="msg-author">${esc(name)}</span><span class="msg-time">${esc(clock(m.createdAt))}</span></div>`;
  }

  function userHTML(m) {
    return `<div class="msg user" data-msg-id="${esc(m.id)}">
      <div class="msg-body">
        ${messageHead(m)}
        <div class="user-bubble markdown">${renderMarkdown(m.text)}</div>
        ${attachmentsHTML(m.attachments)}
        ${queueStatusLabel(m) ? `<div class="queue-status">${esc(queueStatusLabel(m))}</div>` : ""}
      </div>
    </div>`;
  }

  function assistantHTML(m) {
    const agent = currentAgent();
    const streaming = m.status === "streaming";
    const text = m.text || "";
    return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
      <div class="msg-avatar">${agentOrb(agent, "agent-orb")}</div>
      <div class="msg-body">
        ${messageHead(m)}
        <div class="markdown">${renderMarkdown(text)}${streaming ? `<span class="streaming-caret"></span>` : ""}</div>
        ${attachmentsHTML(m.attachments)}
      </div>
    </div>`;
  }

  function thoughtHTML(m) {
    const open = S.openThoughts.has(m.id);
    const streaming = m.status === "streaming";
    const preview = truncate((m.text || "").replace(/\s+/g, " "), 110);
    return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
      <div class="msg-avatar" style="background:rgba(180,155,255,.1);border-color:rgba(180,155,255,.22);color:var(--purple)">${icon("brain")}</div>
      <div class="msg-body">
        <div class="thought ${open ? "open" : ""}" data-thought-id="${esc(m.id)}">
          <button class="thought-head" data-action="toggle-thought" data-id="${esc(m.id)}" aria-expanded="${open}">
            ${icon("brain")}<span class="label">${streaming ? "Thinking" : "Thought process"}</span>
            <span class="preview">${esc(preview || (streaming ? "…" : ""))}</span>
            ${icon("chevron-right", "chev")}
          </button>
          <div class="thought-body markdown">${renderMarkdown(m.text || "")}</div>
        </div>
      </div>
    </div>`;
  }

  function activityHTML(messages) {
    const id = messages[0].turnId + ":" + messages[0].id;
    const tools = messages.filter((m) => m.kind === "tool");
    // Follow the turn, not the messages: message status goes idle between tool calls and made the label flicker.
    const running = S.active?.turnId === messages[0].turnId && isBusyTurn();
    const failed = tools.some((m) => ["failed", "error"].includes(String(parseJSON(m.content, {})?.status || m.status)));
    // Collapsed by default. Only the user's toggle opens it, so appending tool calls never flips it.
    const open = S.activityExpanded.get(id) === true;
    const turn = S.turns[messages[0].turnId];
    const allTurnTools = S.messages.filter((m) => m.turnId === messages[0].turnId && m.kind === "tool").length;
    const seconds = tools.length === allTurnTools && turn?.startedAt && turn?.endedAt && turn.endedAt >= turn.startedAt ? Math.round((turn.endedAt - turn.startedAt) / 1000) : null;
    return `<div class="activity ${open ? "open" : ""}">
      <button class="activity-head" data-action="toggle-activity" data-id="${esc(id)}" aria-expanded="${open}">
        ${icon("chevron-right", "chev")}<span>${running ? "Working" : seconds !== null ? `Worked for ${seconds}s` : "Activity"}</span>
        <span class="activity-count">${tools.length} tool calls${failed ? " · failed" : ""}</span>
      </button><div class="activity-items">${messages.map(messageHTML).join("")}</div>
    </div>`;
  }

  function toolHTML(m) {
    const t = parseJSON(m.content, {}) || {};
    if (!t.toolCallId) t.toolCallId = m.toolCallId || m.id;
    const open = S.openTools.has(m.id);
    const kind = String(t.kind || "").toLowerCase();
    const status = String(t.status || m.status || "pending");
    const title = t.title || t.name || m.text || "Tool call";
    const iconName = toolIcon(kind);
    const iconClass = `kind-${TOOL_KINDS.has(kind) ? kind : "other"}`;
    const sections = toolSections(t, m.id);
    const preview = toolPreview(t);
    const exits = Object.values(t.terminals || {}).map((term) => term.exitStatus).filter(Boolean);
    const exitText = exits.map((exit) => exit.exitCode !== undefined && exit.exitCode !== null ? `Exit ${exit.exitCode}` : exit.signal ? `Signal ${exit.signal}` : "").filter(Boolean).join(", ");
    return `<div class="msg assistant" data-msg-id="${esc(m.id)}">
      <div class="msg-avatar" style="background:rgba(131,184,255,.08);border-color:rgba(131,184,255,.2);color:var(--blue)">${icon("zap")}</div>
      <div class="msg-body">
        <div class="tool-card ${open ? "open" : ""} ${status === "failed" ? "failed" : ""}" data-tool-id="${esc(t.toolCallId || m.toolCallId || m.id)}">
          <button class="tool-head" data-action="toggle-tool" data-id="${esc(m.id)}" aria-expanded="${open}">
            <span class="tool-icon ${iconClass}">${icon(iconName)}</span>
            <span class="tool-main">
              <span class="tool-title-row"><span class="tool-title">${esc(title)}</span></span>
              <span class="tool-sub">${esc(t.name || kind || "tool")}${t.locations && t.locations.length ? ` · ${t.locations.length} location${t.locations.length === 1 ? "" : "s"}` : ""}${exitText ? ` · ${esc(exitText)}` : ""}</span>
            </span>
            <span class="tool-status ${esc(status)}"><span class="dot"></span>${esc(statusLabel(status))}</span>
            ${icon("chevron-right", "chev")}
          </button>
          ${preview ? `<div class="tool-preview">${esc(preview)}</div>` : ""}
          <div class="tool-body">${sections || `<div class="tool-text">No tool details were provided.</div>`}</div>
        </div>
      </div>
    </div>`;
  }

  function toolIcon(kind) {
    if (kind === "execute") return "terminal";
    if (kind === "edit" || kind === "move") return "edit";
    if (kind === "delete") return "trash";
    if (kind === "read") return "file";
    if (kind === "search") return "search";
    if (kind === "fetch") return "globe";
    if (kind === "think") return "brain";
    return "zap";
  }

  function statusLabel(s) {
    s = String(s || "").toLowerCase();
    if (s === "in_progress") return "Running";
    if (s === "completed" || s === "complete") return "Done";
    if (s === "failed" || s === "error") return "Failed";
    if (s === "pending") return "Pending";
    return s || "Working";
  }

  function toolSections(t, messageId) {
    const out = [];
    const contentTexts = [];
    const diffs = [];
    const content = Array.isArray(t.content) ? t.content : t.content ? [t.content] : [];
    content.forEach((item) => {
      if (!item || typeof item !== "object") { contentTexts.push(pretty(item)); return; }
      if (item.type === "content" && item.content) {
        if (item.content.type === "text" && item.content.text) contentTexts.push(item.content.text);
        else if (item.content.type === "resource" && item.content.resource?.text) contentTexts.push(item.content.resource.text);
        else contentTexts.push(pretty(item));
      } else if (item.type === "diff") {
        diffs.push(item);
      } else if (item.type === "terminal" && t.terminals?.[item.terminalId]) {
        // Rendered from the persistent terminal snapshot below.
      } else if (item.type === "text" && item.text) {
        contentTexts.push(item.text);
      } else contentTexts.push(pretty(item));
    });
    const section = (label, value, language = "") => {
      const key = messageId + ":" + label;
      const wrap = !S.unwrappedTools.has(key);
      return `<div class="tool-section"><div class="tool-section-head"><div class="tool-label">${esc(label)}</div><div class="tool-actions">
        <button type="button" data-action="tool-wrap" title="Toggle line wrapping">${wrap ? "No wrap" : "Wrap"}</button>
        <button type="button" data-action="tool-start" title="Go to the start of this output">Start</button>
        <button type="button" data-action="tool-end" title="Go to the end of this output">End</button>
        <button type="button" data-action="copy-tool" title="Copy the complete output">Copy</button>
      </div><button type="button" class="tool-menu-button" data-action="tool-menu" aria-label="${esc(label)} options" title="${esc(label)} options">+</button></div><pre class="tool-pre ${wrap ? "" : "no-wrap"}" data-scroll-key="${esc(key)}"><code class="${language ? `language-${language}` : "nohighlight"}">${esc(value)}</code></pre></div>`;
    };
    if (t.rawInput !== undefined && t.rawInput !== null) {
      out.push(section("Input", typeof t.rawInput === "string" ? t.rawInput : pretty(t.rawInput), typeof t.rawInput === "object" ? "json" : ""));
    }
    if (contentTexts.length) {
      out.push(section("Output", contentTexts.join("\n")));
    }
    if (diffs.length) {
      diffs.forEach((d, i) => out.push(section(`Changes ${i + 1}`, String(d.path || "") + "\n" + diffText(d), "diff")));
    }
    Object.values(t.terminals || {}).forEach((term) => out.push(section(`Terminal ${term.id}`, term.output || "")));
    if (t.rawOutput !== undefined && t.rawOutput !== null) {
      out.push(section("Result", typeof t.rawOutput === "string" ? t.rawOutput : pretty(t.rawOutput), typeof t.rawOutput === "object" ? "json" : ""));
    }
    if (Array.isArray(t.locations) && t.locations.length) {
      out.push(`<div class="tool-section"><div class="tool-label">Locations</div><div class="tool-locs">${t.locations.map((l) => `<span class="tool-loc">${esc((l.path || "") + (l.line ? ":" + l.line : ""))}</span>`).join("")}</div></div>`);
    }
    return out.join("");
  }

  function toolPreview(t) {
    const content = Array.isArray(t.content) ? t.content : [];
    const texts = content.map((item) => item?.type === "content" ? item.content?.text : item?.type === "text" ? item.text : "").filter(Boolean);
    const terminalOutput = Object.values(t.terminals || {}).map((term) => term.output || "").filter(Boolean).join("\n");
    const text = terminalOutput || texts.join("\n") || (typeof t.rawOutput === "string" ? t.rawOutput : "");
    return text ? truncate(text.trim(), 280) : "";
  }

  function saveActivityState() {
    try {
      sessionStorage.setItem("nusamation:activity-state", JSON.stringify({
        thoughts: [...S.openThoughts], tools: [...S.openTools], groups: [...S.activityExpanded], unwrapped: [...S.unwrappedTools],
      }));
    } catch {}
  }

  function diffText(d) {
    const old = String(d.oldText || "").split("\n").map((l) => "- " + l);
    const neu = String(d.newText || "").split("\n").map((l) => "+ " + l);
    return old.concat(neu).join("\n");
  }

  function planEntries(plan) {
    let p = plan;
    if (typeof p === "string") p = parseJSON(p, p);
    if (!p) return [];
    if (Array.isArray(p)) return p;
    if (Array.isArray(p.entries)) return p.entries;
    if (Array.isArray(p.plan)) return p.plan;
    return [];
  }

  function planCard(entries) {
    return `<div class="plan-card">
      <div class="plan-head">${icon("zap")} Plan <span class="grow"></span><span class="muted" style="font-weight:400">${entries.filter((e) => e.status === "completed").length}/${entries.length}</span></div>
      <div class="plan-list">${entries.map((e) => {
        const st = String(e.status || "pending").toLowerCase();
        return `<div class="plan-step ${esc(st)}"><span class="box">${st === "completed" ? icon("check") : ""}</span><span>${esc(e.title || e.content || e.text || "")}</span></div>`;
      }).join("")}</div>
    </div>`;
  }

  function systemHTML(m) {
    return `<div class="msg system ${m.kind === "error" ? "error" : ""}" data-msg-id="${esc(m.id)}"><div class="msg-body">${esc(m.text)}</div></div>`;
  }

  function errorHTML(m) {
    return `<div class="msg system error" data-msg-id="${esc(m.id)}"><div class="msg-body"><div class="error-card" style="border:1px solid rgba(255,118,118,.22);background:var(--danger-dim);border-radius:10px;padding:9px 13px;text-align:left;display:flex;gap:8px;align-items:flex-start">${icon("alert", "sm")}<span>${esc(m.text)}</span></div>${S.current && !S.active ? `<button class="btn btn-ghost btn-xs" data-action="retry" style="margin-top:6px">${icon("refresh", "sm")} Retry</button>` : ""}</div></div>`;
  }

  function attachmentsHTML(atts) {
    if (!atts || !atts.length) return "";
    return `<div class="attach-grid">${atts.map((a) => {
      const url = `/api/uploads/${encodeURIComponent(a.id)}`;
      const isImg = String(a.mime || "").startsWith("image/");
      if (isImg) return `<a class="attach-thumb" href="${url}" target="_blank" rel="noopener"><img src="${url}" alt="${esc(a.name)}" loading="lazy"></a>`;
      return `<a class="attach-file" href="${url}" target="_blank" rel="noopener">${icon("file", "sm")}<span class="name">${esc(a.name)}</span><span class="size">${fmtBytes(a.size)}</span></a>`;
    }).join("")}</div>`;
  }

  function typingHTML() {
    const agent = currentAgent();
    return `<div class="msg assistant"><div class="msg-avatar">${agentOrb(agent, "agent-orb")}</div><div class="msg-body"><div class="typing"><span></span><span></span><span></span></div></div></div>`;
  }

  function approvalModeRow() {
    const opt = S.configs.find((o) => o && o.id && o.type === "select" && String(o.category || "").toLowerCase() === "mode");
    const choices = opt ? configChoices(opt) : [];
    if (!choices.length) return "";
    return `<label class="approval-mode"><span>${esc(opt.name || "Mode")}</span><select class="select config-select" data-change="set-config" data-id="${esc(opt.id)}">${choices.map((c) => `<option value="${esc(c.value)}" ${String(c.value) === String(opt.currentValue) ? "selected" : ""}>${esc(c.name || c.value)}</option>`).join("")}</select></label>`;
  }

  function refreshApprovalModal() {
    const a = S.approvalsById.get(S.modal.id);
    const body = $("#modal-root .modal-body");
    if (a && body && a.status === "pending") body.innerHTML = approvalCard(a);
  }

  function approvalCard(a) {
    const risk = String(a.risk || "medium").toLowerCase();
    const opts = a.options || [];
    const resolving = S.resolving.has(a.id);
    const details = parseJSON(a.details, {}) || {};
    const tool = details.toolCall || {};
    const detailText = tool.rawInput ? pretty(tool.rawInput) : pretty(details);
    const allow = opts.filter((o) => String(o.kind || "").toLowerCase().startsWith("allow"));
    const deny = opts.filter((o) => String(o.kind || "").toLowerCase().startsWith("reject"));
    const ordered = [...allow, ...deny, ...opts.filter((o) => !allow.includes(o) && !deny.includes(o))];
    return `<div class="approval-card" data-approval-id="${esc(a.id)}">
      <div class="approval-top">
        <span class="approval-icon">${icon("shield")}</span>
        <div class="grow"><div class="approval-title">${esc(a.title || "Tool approval")}<span class="risk ${esc(risk)}">${esc(risk)}</span></div>
        <div class="approval-sub">${esc(details.toolName || a.toolCallId || a.kind || "tool call")} · the agent is waiting for your decision</div></div>
      </div>
      <div class="approval-details">${esc(truncate(detailText, 1800))}</div>
      ${a.conversationId === S.current?.id ? approvalModeRow() : ""}
      <div class="approval-actions">
        ${ordered.map((o) => {
          const k = String(o.kind || "").toLowerCase();
          const cls = k.startsWith("allow_once") ? "btn-primary" : k.startsWith("allow") ? "btn-secondary" : "btn-danger";
          return `<button class="btn ${cls} btn-sm" data-action="resolve-approval" data-id="${esc(a.id)}" data-option="${esc(o.optionId)}" ${resolving ? "disabled" : ""}>${k.startsWith("allow") ? icon("check", "sm") : icon("x", "sm")}${esc(o.name || o.optionId)}</button>`;
        }).join("")}
        ${resolving ? `<span class="loading-row" style="padding:4px 6px"><span class="spinner"></span></span>` : ""}
      </div>
    </div>`;
  }

  function welcomeHTML(compact = false) {
    const agents = S.agents.filter((a) => a.enabled);
    const selected = S.current ? currentAgent() : (agentById(S.welcomeAgent) || agents[0]);
    if (!agents.length) {
      return `<div class="welcome"><div class="welcome-orb">N</div><h1>Add your first <em>agent.</em></h1><p>NusaMation connects to ACP agents such as Devin, Claude Code, Codex or Gemini CLI. Tell the control center how to launch yours.</p><button class="btn btn-primary" data-action="add-agent">${icon("plus")} Add an ACP agent</button></div>`;
    }
    return `<div class="welcome ${compact ? "compact" : ""}">
      <div class="welcome-orb">N</div>
      <div class="agent-strip">${agents.map((a) => `<button class="agent-chip ${selected && a.id === selected.id ? "active" : ""}" data-action="pick-agent" data-id="${esc(a.id)}">${agentOrb(a)}<span>${esc(a.name)}</span></button>`).join("")}</div>
      <h1>${compact ? "This thread is <em>ready.</em>" : "What should we <em>build</em> today?"}</h1>
      <p>${compact ? `Message ${esc(agentName(selected))} below. The web control center shows every thought, tool call and approval; chat channels only receive the final answer.` : "Your agent has hands on this workspace, full tool visibility in the web, and a concise presence on WhatsApp. Start with a sentence — or pick a thread below."}</p>
      <div class="suggestion-grid">
        ${[
          ["search", "Explore the workspace", "List files and explain the project layout."],
          ["terminal", "Run a careful check", "Check the workspace health and report findings."],
          ["edit", "Build something small", "Create a small tool or script and verify it."],
          ["globe", "Research something", "Look up a topic and summarize the useful parts."],
        ].map(([ic, title, prompt]) => `<button class="suggestion" data-action="suggestion" data-prompt="${esc(prompt)}">${icon(ic)}<span><b>${esc(title)}</b><span>${esc(prompt)}</span></span></button>`).join("")}
      </div>
    </div>`;
  }

  function scheduleChatRender() {
    if (S.renderTimer) return;
    S.renderTimer = requestAnimationFrame(() => {
      S.renderTimer = null;
      renderChat();
    });
  }

  function scrollToBottom(force = false) {
    const sc = $("#chat-scroll");
    if (!sc) return;
    if (force || S.scrollPinned) sc.scrollTop = sc.scrollHeight;
  }

  /* ---------------- composer ---------------- */
  function renderComposer() {
    const wrap = $("#composer-wrap");
    if (!wrap) return;
    const popover = $(".composer-popover");
    const oldAnchor = $("[data-popover-anchor]", wrap);
    const anchorAction = oldAnchor?.dataset.action;
    const anchorId = oldAnchor?.dataset.id;
    const menuOpen = !!$(".send-menu-popover");
    const menuFocus = document.activeElement?.dataset?.mode || "";
    if (menuOpen) closePopover();
    const oldTa = $("#prompt");
    const wasFocused = !!oldTa && oldTa === document.activeElement;
    const selStart = oldTa ? oldTa.selectionStart : 0;
    const selEnd = oldTa ? oldTa.selectionEnd : 0;
    const agent = currentAgent();
    const conv = S.current;
    const links = (conv && conv.links || []).filter((l) => l.channel !== "web");
    const active = S.active && ["running", "waiting_approval", "cancelling"].includes(S.active.status);
    const mobileSummary = mobileConfigSummary();
    const markup = `<div class="composer-inner">
      <div class="composer" id="composer">
        <div class="composer-attachments" id="pending-attachments"></div>
        <textarea id="prompt" rows="1" placeholder="Message ${esc(agentName(agent))}…" aria-label="Message">${esc(S.draft)}</textarea>
        <div class="composer-bar">
          <button class="icon-btn sm" data-action="attach" title="Attach files">${icon("paperclip", "sm")}</button>
          <button class="composer-choice agent-choice" data-action="agent-menu" title="Choose agent">${agentOrb(agent)}<span>${esc(agentName(agent))}</span>${icon("chevron-down", "sm")}</button>
          <div class="composer-configs">${composerConfigs()}</div>
          ${mobileSummary ? `<span class="composer-mobile-summary" title="${esc(mobileSummary)}">${esc(mobileSummary)}</span>` : ""}
          <button class="composer-mobile-settings" data-action="${S.configs.length ? "composer-mobile-settings" : "prepare-composer"}" aria-label="Agent options" title="Agent options">${S.preparing === (conv?.id || "draft") ? `<span class="spinner"></span>` : "+"}</button>
          ${S.elicitations.some((e) => e.conversationId === conv?.id) ? `<button class="composer-choice" data-action="show-agent-request">Respond</button>` : ""}
          ${links.length ? `<button class="reply-toggle ${S.replyToChannel ? "on" : ""}" data-action="reply-toggle" title="Also send the final answer to this chat">${icon("message", "sm")} ${S.replyToChannel ? "Reply to " + esc(links[0].channel) : "Reply to chat"}</button>` : ""}
          <span class="grow"></span>
          ${active ? `<button class="stop-btn" data-action="stop" aria-label="Stop" title="Stop">${icon("stop", "sm")}</button>` : ""}
          <div class="send-split">
            <button class="send-btn" id="send-btn" data-action="send" title="${sendTitle()}" aria-label="${sendTitle()}">${icon("send")}</button>
            ${isBusyTurn() ? `<button class="send-menu-btn" id="send-menu-btn" data-action="send-menu" aria-haspopup="menu" aria-expanded="false" aria-label="Send options" title="Send options"${S.sending ? " disabled" : ""}>${icon("chevron-down", "sm")}</button>` : ""}
          </div>
        </div>
      </div>
      <div class="composer-foot"><span>${icon("shield", "sm")} Approvals stay in your hands</span><span>${icon("brain", "sm")} Full thoughts on web</span></div>
      <input type="file" id="file-input" multiple hidden>
      <div class="drop-overlay" id="drop-overlay">Drop files to attach</div>
    </div>`;
    const next = document.createElement("div");
    next.innerHTML = markup;
    const existing = $(".composer-inner", wrap);
    if (existing && oldTa) {
      $("#pending-attachments", existing).replaceWith($("#pending-attachments", next));
      $(".composer-bar", existing).replaceWith($(".composer-bar", next));
      $(".composer-foot", existing).replaceWith($(".composer-foot", next));
      if (oldTa.dataset.composerKey !== composerKey) oldTa.value = S.draft;
    } else {
      wrap.innerHTML = markup;
    }
    const ta = $("#prompt");
    ta.dataset.composerKey = composerKey;
    if (!oldTa || !existing) {
      ta.addEventListener("input", () => { S.draft = ta.value; autoGrow(); });
      ta.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && e.shiftKey && !isMobile() && !e.isComposing) {
          e.preventDefault();
          sendMessage();
        }
      });
      ta.addEventListener("paste", handlePaste);
    }
    if (wasFocused && ta !== oldTa) {
      ta.focus();
      try { ta.setSelectionRange(selStart, selEnd); } catch {}
    }
    autoGrow();
    $("#file-input").onchange = (e) => uploadFiles(e.target.files);
    wrap.onpointerdown = (e) => {
      if (isMobile() && document.activeElement === ta && e.target.closest('[data-action="prepare-composer"]')) e.preventDefault();
    };
    wrap.ondragover = (e) => { e.preventDefault(); $("#drop-overlay")?.classList.add("show"); };
    wrap.ondragleave = (e) => { if (!wrap.contains(e.relatedTarget)) $("#drop-overlay")?.classList.remove("show"); };
    wrap.ondrop = (e) => {
      e.preventDefault();
      $("#drop-overlay")?.classList.remove("show");
      uploadFiles(e.dataTransfer.files);
    };
    renderPendingAttachments();
    updateComposerState();
    if (menuOpen) reopenSendMenu(menuFocus);
    else if (popover && oldAnchor) {
      const replacement = $$('[data-action]', wrap).find((el) => el.dataset.action === anchorAction && el.dataset.id === anchorId);
      if (replacement) popover._setAnchor(replacement);
      else closePopover();
    }
  }

  function composerConfigs() {
    if (S.preparing === (S.current?.id || "draft")) return `<span class="composer-loading"><span class="spinner"></span> Connecting…</span>`;
    const opts = S.configs.filter((o) => o && o.id && (o.type === "select" || o.type === "boolean"));
    if (!opts.length) return `<button class="composer-choice" data-action="prepare-composer">Load options</button>`;
    const modes = opts.filter((o) => String(o.category || "").toLowerCase() === "mode");
    const rest = opts.filter((o) => !modes.includes(o));
    const direct = modes.map((o) => `<button class="composer-choice" data-action="composer-config" data-id="${esc(o.id)}" title="${esc(o.name || o.id)}">${esc(configLabel(o))}${icon("chevron-down", "sm")}</button>`).join("");
    if (!rest.length) return direct;
    const model = rest.find((o) => String(o.category || "").toLowerCase() === "model");
    const effort = rest.find((o) => ["thought_level", "reasoning_effort"].includes(String(o.category || "").toLowerCase()));
    const label = model ? configLabel(model) + (effort ? " · " + configLabel(effort) : "") : "Options";
    return direct + `<button class="composer-choice composer-settings" data-action="composer-settings" title="Model and agent options">${esc(label)}${icon("chevron-down", "sm")}</button>`;
  }

  function mobileConfigSummary() {
    const model = S.configs.find((o) => String(o?.category || "").toLowerCase() === "model");
    const effort = S.configs.find((o) => ["thought_level", "reasoning_effort"].includes(String(o?.category || "").toLowerCase()));
    return model ? [configLabel(model), effort && configLabel(effort)].filter(Boolean).join(" · ") : "";
  }

  function configLabel(o) {
    const choice = configChoices(o).find((v) => String(v.value) === String(o.currentValue));
    return o.type === "boolean" ? (o.currentValue === true || o.currentValue === "true" ? "On" : "Off") : (choice?.name || o.currentValue || o.name || o.id);
  }

  function configChoices(o) {
    return (o.options || []).flatMap((v) => Array.isArray(v.options) ? v.options.map((c) => ({ ...c, groupName: v.name || v.group })) : [v]);
  }

  function autoGrow() {
    const ta = $("#prompt");
    if (!ta) return;
    ta.style.height = "auto";
    const viewportHeight = Math.min(window.innerHeight, window.visualViewport?.height || window.innerHeight);
    const limit = isShort() ? Math.max(44, Math.floor(viewportHeight * 0.25)) : isMobile() ? Math.min(220, Math.max(60, Math.floor(viewportHeight * 0.28))) : 220;
    ta.style.height = Math.min(limit, ta.scrollHeight) + "px";
    updateComposerState();
  }

  function updateComposerState() {
    const ta = $("#prompt");
    const btn = $("#send-btn");
    if (!ta || !btn) return;
    const has = ta.value.trim().length > 0 || S.attachments.length > 0;
    btn.disabled = !has || S.sending || (!S.current && S.preparing === "draft");
    btn.innerHTML = S.sending ? `<span class="spinner"></span>` : icon("send");
    btn.title = sendTitle();
    btn.setAttribute("aria-label", sendTitle());
    const menuBtn = $("#send-menu-btn");
    if (menuBtn) menuBtn.disabled = S.sending;
    const agent = currentAgent();
    ta.placeholder = isMobile() ? "Message…" : `Message ${agentName(agent)}…`;
  }

  const TURN_RANK = { queued: 0, running: 1, waiting_approval: 1, cancelling: 1 };
  // A stale queued snapshot never replaces a started or finished turn; later
  // terminal states replace earlier ones.
  function upsertTurn(turn) {
    if (!turn || !turn.id) return;
    const rank = (s) => (s in TURN_RANK ? TURN_RANK[s] : 2);
    const known = S.turns[turn.id];
    if (known && rank(turn.status) < rank(known.status)) return;
    S.turns[turn.id] = turn;
  }

  function isBusyTurn() {
    return !!S.active && !!S.active.running && ["running", "waiting_approval"].includes(S.active.status);
  }

  function sendTitle() {
    if (!isBusyTurn()) return "Send";
    return S.sendDelivery === "after_tools" ? "Send after tools" : "Add to queue";
  }

  function openSendMenu(anchor) {
    if (!isBusyTurn()) return;
    if ($(".send-menu-popover")) { closePopover(); return; }
    const choice = (mode, label, help) => `<button class="menu-item" role="menuitemradio" aria-checked="${S.sendDelivery === mode}" data-action="send-mode" data-mode="${mode}"><span class="popover-main"><b>${label}</b><small>${help}</small></span>${S.sendDelivery === mode ? icon("check", "sm") : ""}</button>`;
    openPopover(anchor, `<div class="popover-list send-menu" role="menu" aria-label="Send mode">${choice("queue", "Add to queue", "Runs after the current turn finishes")}${choice("after_tools", "Send after tools", "Finish current tools, cancel, then send")}</div>`, { width: 260, class: "composer-popover send-menu-popover" });
    anchor.setAttribute("aria-expanded", "true");
    const el = $(".send-menu-popover");
    el.addEventListener("keydown", (e) => sendMenuKeys(e, anchor, el));
    el.querySelector('[aria-checked="true"]')?.focus();
  }

  function reopenSendMenu(focusMode) {
    const anchor = $("#send-menu-btn");
    if (!anchor) return;
    openSendMenu(anchor);
    if (focusMode) $(`.send-menu-popover [data-mode="${focusMode}"]`)?.focus();
    else anchor.focus();
  }

  function sendMenuKeys(e, anchor, el) {
    const items = $$("[role=menuitemradio]", el);
    const i = items.indexOf(document.activeElement);
    const go = (n) => { e.preventDefault(); items[n]?.focus(); };
    if (e.key === "ArrowDown") go((i + 1) % items.length);
    else if (e.key === "ArrowUp") go((i - 1 + items.length) % items.length);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(items.length - 1);
    else if (e.key === "Escape") { e.preventDefault(); closePopover(); anchor.focus(); }
  }

  function setSendMode(mode) {
    S.sendDelivery = mode;
    closePopover();
    updateComposerState();
    $("#send-menu-btn")?.focus();
  }

  function queuedTurnFor(m) {
    return Object.values(S.turns || {}).find((t) => t.userMessageId === m.id);
  }

  function queueStatusLabel(m) {
    const turn = queuedTurnFor(m);
    if (!turn || turn.status !== "queued") return "";
    const target = (m.metadata || {}).interruptTurnId;
    if (target && S.active && S.active.turnId === target) {
      return S.active.status === "cancelling" ? "Waiting for agent to stop" : "Waiting for tools";
    }
    return "Queued";
  }

  function renderPendingAttachments() {
    const el = $("#pending-attachments");
    if (!el) return;
    el.innerHTML = S.attachments.map((a) => {
      const u = a.upload || a;
      const isImg = String(u.mime || "").startsWith("image/");
      return `<div class="pending-attach" title="${esc(u.name)}">
        ${isImg ? `<img src="/api/uploads/${encodeURIComponent(u.id)}" alt="">` : icon("file", "sm")}
        <span class="name">${esc(u.name)}</span>
        <button data-action="remove-attachment" data-id="${esc(u.id)}" aria-label="Remove">${icon("x", "sm")}</button>
      </div>`;
    }).join("");
    updateComposerState();
  }

  function focusComposer() {
    setTimeout(() => $("#prompt")?.focus(), 30);
  }

  function handlePaste(e) {
    const items = Array.from(e.clipboardData?.items || []);
    const files = items.filter((i) => i.kind === "file").map((i) => i.getAsFile()).filter(Boolean);
    if (files.length) {
      e.preventDefault();
      uploadFiles(files);
    }
  }

  async function uploadFiles(files) {
    if (!files || !files.length) return;
    const owner = composerFor(composerKey);
    const conversationID = S.current?.id;
    for (const file of Array.from(files).slice(0, 8)) {
      if (file.size > 25 << 20) { toast("File is too large", `${file.name} exceeds 25 MB.`, "error"); continue; }
      const fd = new FormData();
      fd.append("file", file);
      if (conversationID) fd.append("conversationId", conversationID);
      try {
        const d = await api("/uploads", { method: "POST", body: fd });
        owner.attachments.push({ upload: d.upload });
        if (composerFor(composerKey) === owner) renderPendingAttachments();
      } catch (e) {
        toast("Upload failed", e.message, "error");
      }
    }
  }

  async function sendMessage() {
    if (S.sending || (!S.current && S.preparing === "draft")) return;
    const ta = $("#prompt");
    if (!ta) return;
    const text = ta.value.trim();
    if (!text && !S.attachments.length) return;
    const owner = composerFor(composerKey);
    const sourceKey = composerKey;
    let conversation = S.current;
    const delivery = isBusyTurn() ? owner.sendDelivery : "queue";
    const attachments = owner.attachments.slice();
    owner.sending = true;
    updateComposerState();
    try {
      if (!conversation) {
        const ag = agentById(S.welcomeAgent) || S.agents.find((a) => a.enabled);
        if (!ag) { openSettings("agents"); throw new Error("Add an agent before sending a message."); }
        owner.pendingSelections = S.draftSelections.slice();
        const d = await api("/conversations", { method: "POST", body: { agentId: ag.id } });
        conversation = d.conversation;
        moveComposer(sourceKey, conversation.id);
        upsertConversation(d.conversation);
        if (composerFor(composerKey) === owner) {
          ++S.loadSeq;
          S.loadingConversation = false;
          S.current = conversation;
          S.messages = [];
          S.configs = [];
          S.currentAgent = ag;
          S.active = null;
          setRoute(conversation.id);
        }
      }
      if (owner.pendingSelections?.length) {
        await api(`/conversations/${encodeURIComponent(conversation.id)}/prepare`, { method: "POST", body: {} });
        for (const choice of owner.pendingSelections) {
          await api(`/conversations/${encodeURIComponent(conversation.id)}/config`, { method: "POST", body: { configId: choice.id, value: choice.value } });
        }
        owner.pendingSelections = [];
      }
      const links = (conversation.links || []).filter((l) => l.channel !== "web");
      const body = {
        text,
        attachments: attachments.map((a) => ({ uploadId: (a.upload || a).id })),
        replyToChannel: !!(owner.replyToChannel && links.length),
        replyChannel: links.length ? links[0].channel : "",
        delivery,
      };
      const d = await api(`/conversations/${encodeURIComponent(conversation.id)}/messages`, { method: "POST", body });
      owner.sending = false;
      if (owner.draft.trim() === text) { owner.draft = ""; if (composerFor(composerKey) === owner) $("#prompt").value = ""; }
      owner.attachments = owner.attachments.filter((a) => !attachments.includes(a));
      if (composerFor(composerKey) !== owner || S.current?.id !== conversation.id) return;
      renderPendingAttachments();
      upsertTurn(d.turn);
      upsertMessage(normalizeMessage({ ...d.message }));
      if (!S.active || !S.active.running) S.active = { status: "queued", running: false, turnId: d.turn.id };
      S.scrollPinned = true;
      renderChat();
      renderTopbar();
      renderComposer();
      focusComposer();
    } catch (e) {
      owner.sending = false;
      if (composerFor(composerKey) === owner) updateComposerState();
      toast("Could not send", e.message, "error");
    }
  }

  async function stopTurn() {
    if (!S.current) return;
    try {
      await api(`/conversations/${encodeURIComponent(S.current.id)}/cancel`, { method: "POST" });
      if (S.active) S.active.status = "cancelling";
      renderTopbar();
      renderComposer();
      toast("Stopping", "The agent is being asked to stop.", "info");
    } catch (e) {
      toast("Nothing to stop", e.message, "error");
    }
  }

  async function retryTurn() {
    if (!S.current) return;
    try {
      const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/retry`, { method: "POST" });
      S.active = { status: "queued", running: false, turnId: d.turn.id };
      renderChat(); renderTopbar(); renderComposer();
    } catch (e) {
      toast("Could not retry", e.message, "error");
    }
  }

  function upsertMessage(m) {
    if (!m || !m.id) return;
    if (!S.current || m.conversationId !== S.current.id) return;
    m = normalizeMessage(m);
    const i = S.messages.findIndex((x) => x.id === m.id);
    if (i >= 0) {
      S.messages[i] = { ...S.messages[i], ...m };
      if (!m.attachments) m.attachments = S.messages[i].attachments;
    } else {
      S.messages.push(m);
      S.messages.sort((a, b) => (a.seq || 0) - (b.seq || 0));
    }
    const conv = S.current;
    if (conv && m.text) {
      conv.lastMessage = m.text;
      conv.lastMessageAt = m.createdAt || now();
      upsertConversation(conv, false);
    }
  }

  function pretty(v) {
    if (v === undefined || v === null) return "";
    if (typeof v === "string") {
      const t = v.trim();
      if ((t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"))) {
        try { return JSON.stringify(JSON.parse(t), null, 2); } catch {}
      }
      return v;
    }
    try { return JSON.stringify(v, null, 2); } catch { return String(v); }
  }
  function parseJSON(v, fallback = null) {
    if (v == null) return fallback;
    if (typeof v === "object") return v;
    try { return JSON.parse(v); } catch { return fallback; }
  }
  function parseConfigs(raw) {
    const v = parseJSON(raw, []);
    return Array.isArray(v) ? v : [];
  }
  /* ---------------- live stream ---------------- */
  function connectWS() {
    if (S.ws && (S.ws.readyState === WebSocket.OPEN || S.ws.readyState === WebSocket.CONNECTING)) return;
    S.wsState = "connecting";
    renderOffline();
    renderSidebar();
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    let ws;
    try {
      ws = new WebSocket(`${proto}//${location.host}/api/events`);
    } catch (e) {
      scheduleReconnect();
      return;
    }
    S.ws = ws;
    ws.onopen = () => {
      const attempt = S.reconnectAttempt;
      S.wsState = "online";
      S.reconnectAttempt = 0;
      renderOffline();
      renderSidebar();
      refreshBootstrap().catch(() => {});
      if (attempt > 0 && S.current) loadConversation(S.current.id, false).catch(() => {});
    };
    ws.onmessage = (e) => {
      let ev;
      try { ev = JSON.parse(e.data); } catch { return; }
      try { handleEvent(ev); } catch (err) { console.warn("event handler failed", err, ev); }
    };
    ws.onclose = () => {
      if (S.ws !== ws) return;
      S.ws = null;
      S.wsState = "offline";
      renderOffline();
      renderSidebar();
      scheduleReconnect();
    };
    ws.onerror = () => {
      try { ws.close(); } catch {}
    };
  }

  function closeWS() {
    clearTimeout(S.reconnectTimer);
    S.reconnectTimer = null;
    if (S.ws) {
      const ws = S.ws;
      S.ws = null;
      try { ws.close(); } catch {}
    }
    S.wsState = "offline";
  }

  function scheduleReconnect() {
    if (S.reconnectTimer || !S.booted) return;
    S.reconnectAttempt = Math.min(S.reconnectAttempt + 1, 8);
    const delay = Math.min(15000, 700 * Math.pow(1.7, S.reconnectAttempt));
    S.reconnectTimer = setTimeout(() => {
      S.reconnectTimer = null;
      connectWS();
    }, delay);
  }

  function handleEvent(ev) {
    const type = ev.type;
    const data = ev.data || {};
    const convId = ev.conversationId || data.conversationId || data.conversation?.id;
    switch (type) {
      case "hello":
        S.wsState = "online";
        renderOffline();
        break;
      case "conversation.created":
      case "conversation.updated": {
        const conv = data.conversation || data;
        if (!conv || !conv.id) break;
        upsertConversation(conv);
        if (S.current && S.current.id === conv.id) {
          if (data.agent) S.currentAgent = data.agent;
          renderTopbar();
          renderComposer();
        }
        break;
      }
      case "conversation.deleted": {
        const id = data.id || convId;
        S.conversations = S.conversations.filter((c) => c.id !== id);
        composers.delete(id);
        if (S.current && S.current.id === id) {
          S.current = null; switchComposer(""); S.messages = []; S.active = null; setRoute("");
          renderChat(); renderTopbar(); renderComposer();
        }
        renderSidebar();
        break;
      }
      case "message.created": {
        const m = normalizeMessage(data.message || data);
        if (!m.id) break;
        touchConversationFromMessage(m);
        if (S.current && m.conversationId === S.current.id) {
          upsertMessage(m);
          scheduleChatRender();
        } else {
          S.unread.set(m.conversationId, (S.unread.get(m.conversationId) || 0) + 1);
          renderSidebar();
        }
        break;
      }
      case "message.delta": {
        if (!S.current || data.conversationId !== S.current.id) break;
        let m = S.messages.find((x) => x.id === data.messageId);
        if (!m) {
          m = normalizeMessage({
            id: data.messageId, conversationId: data.conversationId, role: "assistant",
            kind: data.kind === "thought" ? "thought" : "text", text: "", status: "streaming",
            seq: (S.messages[S.messages.length - 1]?.seq || 0) + 0.1,
          });
          S.messages.push(m);
        }
        m.text = (m.text || "") + (data.delta || "");
        m.status = "streaming";
        touchConversationFromMessage(m);
        scheduleChatRender();
        break;
      }
      case "message.updated": {
        const m = normalizeMessage(data.message || data);
        if (!m.id) break;
        touchConversationFromMessage(m);
        if (S.current && m.conversationId === S.current.id) {
          upsertMessage(m);
          scheduleChatRender();
        }
        break;
      }
      case "tool.created":
      case "tool.updated": {
        const m = normalizeMessage(data.message || data);
        if (!m.id) break;
        if (S.current && m.conversationId === S.current.id) { upsertMessage(m); scheduleChatRender(); }
        break;
      }
      case "agent.session": {
        if (S.current && convId === S.current.id) {
          if (data.configOptions) { S.configs = parseConfigs(data.configOptions); S.current.configOptions = data.configOptions; }
          renderTopbar(); renderComposer();
        }
        break;
      }
      case "config.updated": {
        if (!S.current || convId !== S.current.id) break;
        if (data.configOptions) { S.configs = parseConfigs(data.configOptions); S.current.configOptions = data.configOptions; }
        if (data.model) S.current.model = data.model;
        renderTopbar(); renderComposer();
        if (S.modal?.type === "settings" && S.settingsTab === "channels") { /* noop */ }
        if (S.modal?.type === "approval") refreshApprovalModal();
        if (currentApprovals().length) scheduleChatRender();
        break;
      }
      case "commands.updated": {
        if (S.current && convId === S.current.id) S.commands = data.commands || [];
        break;
      }
      case "plan.updated": {
        if (S.current && convId === S.current.id) { S.current.plan = data.plan || []; scheduleChatRender(); }
        break;
      }
      case "usage.updated": {
        if (S.current && convId === S.current.id) { S.current.usage = data.usage; }
        break;
      }
      case "turn.updated": {
        if (S.current && convId === S.current.id) { upsertTurn(data.turn || data); scheduleChatRender(); }
        break;
      }
      case "turn.started": {
        const turn = data.turn || data;
        if (S.current && convId === S.current.id) {
          if (turn.id) S.turns[turn.id] = turn;
          S.active = { ...turn, turnId: turn.id || turn.turnId, running: true, status: "running" };
          clearTimeout(S.slowTimer);
          S.slowTimer = setTimeout(() => {
            if (S.current?.id === convId && S.active?.turnId === turn.id && !S.modal && !S.messages.some((m) => m.turnId === turn.id && m.role === "assistant")) showSlowAgent(false);
          }, 45000);
          renderTopbar(); renderComposer(); scheduleChatRender();
        }
        break;
      }
      case "turn.status": {
        if (!S.current || convId !== S.current.id) break;
        if (!S.active) S.active = { turnId: data.turnId, running: true };
        S.active.status = data.status;
        S.active.running = data.status !== "queued";
        if (data.status === "waiting_approval") S.active.running = true;
        renderTopbar(); renderComposer(); scheduleChatRender();
        break;
      }
      case "turn.completed": {
        const turn = data.turn || {};
        if (S.current && convId === S.current.id) {
          if (turn.id) S.turns[turn.id] = turn;
          const st = data.status || turn.status;
          S.active = null;
          clearTimeout(S.slowTimer);
          if (S.modal?.type === "agent-slow" && S.modal.conversationId === convId && !S.modal.connecting) closeModal();
          if (st === "error") showAgentBlocker(new ApiError(data.error || "Check the conversation for details.", 0, "agent_error", { authMethods: data.authMethods || [] }));
          else if (st === "cancelled") toast("Turn cancelled", "The agent stopped.", "info");
          renderTopbar(); renderComposer(); scheduleChatRender();
        }
        const c = S.conversations.find((x) => x.id === convId);
        if (c) { c.status = "idle"; renderSidebar(); }
        break;
      }
      case "approval.created": {
        const a = data.approval;
        if (!a?.id) break;
        S.approvalsById.set(a.id, a);
        if (a.status === "pending" && !S.approvals.some((x) => x.id === a.id)) S.approvals.push(a);
        if (S.current && convId === S.current.id) scheduleChatRender();
        renderTopbar(); renderSidebar();
        if (!S.current || convId !== S.current.id) toast("Approval needed", a.title || "An agent needs your decision.", "info", 7000);
        else toast("Approval needed", a.title || "An agent needs your decision.", "info", 5000);
        if (S.modal?.type === "approvals") renderApprovalsModal();
        if (S.current && convId === S.current.id) showPendingRequest();
        break;
      }
      case "approval.auto": {
        const a = data.approval;
        if (a?.id) S.approvalsById.set(a.id, a);
        break;
      }
      case "approval.resolved": {
        const a = data.approval;
        if (!a?.id) break;
        S.approvalsById.set(a.id, a);
        S.approvals = S.approvals.filter((x) => x.id !== a.id || a.status === "pending");
        if (a.status !== "pending") S.resolving.delete(a.id);
        if (S.current && convId === S.current.id) scheduleChatRender();
        renderTopbar(); renderSidebar();
        if (S.modal?.type === "approvals") renderApprovalsModal();
        if (S.modal?.type === "approval" && S.modal.id === a.id && a.status !== "pending") {
          closeModal();
          showPendingRequest();
        }
        break;
      }
      case "elicitation.created": {
        if (!data.id) break;
        if (S.current?.id === data.conversationId) {
          S.elicitations = [...S.elicitations.filter((v) => v.id !== data.id), data];
          renderComposer();
          showPendingRequest();
        } else {
          toast("Agent needs input", "Open the conversation to respond.", "info", 7000);
        }
        break;
      }
      case "elicitation.resolved": {
        S.elicitations = S.elicitations.filter((v) => v.id !== data.id);
        renderComposer();
        if (S.modal?.type === "elicitation" && S.modal.id === data.id) { closeModal(false); showPendingRequest(); }
        break;
      }
      case "notice": {
        const sev = data.severity || "info";
        toast(data.title || "Agent notice", data.description || "", sev === "error" ? "error" : sev === "warning" ? "info" : "info", sev === "error" ? 7500 : 5000);
        break;
      }
      case "agent.status": {
        const ag = agentById(data.agentId);
        if (ag) {
          ag.status = data.status;
          ag.lastError = data.lastError || "";
          renderTopbar();
          if (S.modal?.type === "settings" && S.settingsTab === "agents") renderSettings();
        }
        break;
      }
      case "channel.status": {
        const ch = data;
        const i = S.channels.findIndex((c) => c.name === ch.name);
        if (i >= 0) S.channels[i] = ch; else S.channels.push(ch);
        if (ch.name === "whatsapp") {
          S.whatsapp = ch;
          if (ch.state === "connected" && ch.connected) { /* no toast to avoid noise */ }
          updateWhatsAppPanel();
        }
        break;
      }
      default:
        break;
    }
  }

  function touchConversationFromMessage(m) {
    if (!m.conversationId) return;
    const c = S.conversations.find((x) => x.id === m.conversationId);
    if (!c) return;
    if (!c.lastMessageAt || (m.createdAt || 0) >= (c.lastMessageAt || 0)) {
      c.lastMessage = m.text || c.lastMessage;
      c.lastMessageAt = m.createdAt || c.lastMessageAt || now();
      c.lastMessageRole = m.role;
    }
    scheduleSidebarRender();
  }

  let sidebarRenderTimer = null;
  function scheduleSidebarRender() {
    if (sidebarRenderTimer) return;
    sidebarRenderTimer = setTimeout(() => {
      sidebarRenderTimer = null;
      renderSidebar();
    }, 220);
  }

  /* ---------------- popovers ---------------- */
  function closePopover() {
    document.querySelectorAll(".popover").forEach((e) => {
      try { e._cleanup?.(); } catch {}
      e.remove();
    });
    document.querySelectorAll("[data-popover-anchor]").forEach((e) => e.removeAttribute("data-popover-anchor"));
    document.querySelectorAll('[aria-haspopup][aria-expanded="true"]').forEach((e) => e.setAttribute("aria-expanded", "false"));
  }

  function toolPreFor(target) {
    return target.closest(".tool-section")?.querySelector(".tool-pre") ||
      $$(".tool-pre", $("#chat")).find((pre) => pre.dataset.scrollKey === target.dataset.toolKey);
  }

  function openToolMenu(anchor) {
    const pre = toolPreFor(anchor);
    if (!pre) return;
    const key = esc(pre.dataset.scrollKey);
    const wrapped = !pre.classList.contains("no-wrap");
    const item = (action, label) => `<button class="menu-item" data-action="${action}" data-tool-key="${key}"><span>${label}</span></button>`;
    openPopover(anchor, `<div class="popover-list">${item("tool-wrap", wrapped ? "Disable line wrap" : "Enable line wrap")}${item("tool-start", "Go to start")}${item("tool-end", "Go to end")}${item("copy-tool", "Copy full text")}</div>`, { width: 205 });
  }

  function openPopover(anchor, html, opts = {}) {
    closePopover();
    if (!anchor) return;
    const el = document.createElement("div");
    el.className = "popover " + (opts.class || "");
    el.innerHTML = html;
    document.body.appendChild(el);
    anchor.setAttribute("data-popover-anchor", "1");
    el._setAnchor = (next) => {
      anchor.removeAttribute("data-popover-anchor");
      anchor = next;
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

  function menuItem(iconName, label, action, attrs = "", danger = false) {
    return `<button class="menu-item ${danger ? "danger" : ""}" data-action="${action}" ${attrs}>${icon(iconName, "sm")}<span>${esc(label)}</span></button>`;
  }

  function openAgentMenu(anchor) {
    const current = S.current ? currentAgent() : (agentById(S.welcomeAgent) || currentAgent());
    const agents = S.agents.filter((a) => a.enabled);
    openPopover(anchor, `<div class="popover-title">Agent</div>
      <div class="popover-list">
        ${agents.map((a) => `<button class="menu-item ${current && a.id === current.id ? "selected" : ""}" data-action="pick-agent" data-id="${esc(a.id)}">${agentOrb(a)}<span class="popover-main"><b>${esc(a.name)}</b><small>${esc(a.description || a.slug)}</small></span>${current && a.id === current.id ? icon("check", "sm") : ""}</button>`).join("") || `<div class="popover-empty">No enabled agents.</div>`}
      </div>
      <div class="popover-foot">${menuItem("settings", "Manage agents", "open-settings", 'data-tab="agents"')}</div>`, { width: 290 });
  }

  async function openConfigMenu(anchor) {
    if (!S.current) { toast("Start a conversation first", "Session options appear once a conversation exists.", "info"); return; }
    if (!S.configs.length) {
      try {
        const d = await api(`/conversations/${encodeURIComponent(S.current.id)}`);
        S.current = d.conversation;
        S.configs = d.configs || parseConfigs(d.conversation.configOptions);
        if (d.agent) S.currentAgent = d.agent;
      } catch {}
    }
    const opts = S.configs;
    const usage = S.current.usage || {};
    openPopover(anchor, `<div class="popover-title">Session configuration <span class="muted" style="font-weight:400;font-size:10px">live</span></div>
      ${opts.length ? `<div class="config-list">${opts.map((o) => configControl(o)).join("")}</div>` : `<div class="popover-empty">No session options yet.<br>Send a message once; the agent will publish its model and mode.</div>`}
      <div class="config-usage">${usage.totalTokens ? `${usage.totalTokens.toLocaleString()} tokens` : ""}${usage.inputTokens ? ` · ↑${usage.inputTokens.toLocaleString()}` : ""}${usage.outputTokens ? ` · ↓${usage.outputTokens.toLocaleString()}` : ""}${usage.costAmount ? ` · $${usage.costAmount}` : ""}</div>`, { width: 330 });
  }

  function configControl(o) {
    const id = esc(o.id);
    const label = esc(o.name || o.id);
    const current = o.currentValue;
    const type = String(o.type || "").toLowerCase();
    if (type === "boolean") {
      const on = current === true || current === "true";
      return `<div class="config-row"><div class="config-label">${label}<small>${esc(o.description || "")}</small></div><button class="switch ${on ? "on" : ""}" data-action="set-config-toggle" data-id="${id}" data-value="${on ? "false" : "true"}"></button></div>`;
    }
    return `<div class="config-row"><div class="config-label">${label}<small>${esc(o.description || "")}</small></div>
      <select class="select config-select" data-change="set-config" data-id="${id}">
        ${configChoices(o).map((c) => `<option value="${esc(c.value)}" ${String(c.value) === String(current) ? "selected" : ""}>${esc(c.groupName ? c.groupName + " · " + (c.name || c.value) : (c.name || c.value))}</option>`).join("") || `<option value="${esc(current)}">${esc(current)}</option>`}
      </select></div>`;
  }

  function openComposerSettings(anchor, includeMode = false) {
    const opts = S.configs.filter((o) => o && o.id && ["select", "boolean"].includes(o.type) && (includeMode || String(o.category || "").toLowerCase() !== "mode"));
    const links = (S.current?.links || []).filter((l) => l.channel !== "web");
    const compact = isShort() ? [
      links.length ? menuItem(S.replyToChannel ? "check" : "message", S.replyToChannel ? "Stop replying to chat" : "Reply to chat", "reply-toggle") : "",
      S.elicitations.some((e) => e.conversationId === S.current?.id) ? menuItem("info", "Respond", "show-agent-request") : "",
    ].join("") : "";
    openPopover(anchor, `<div class="popover-title">Agent options</div><div class="popover-list composer-options">${compact}${opts.map((o) => `<button class="menu-item" data-action="composer-subconfig" data-id="${esc(o.id)}"><span class="popover-main"><b>${esc(o.name || o.id)}</b>${o.description ? `<small>${esc(o.description)}</small>` : ""}</span><span class="composer-option-value">${esc(configLabel(o))}</span>${icon("chevron-right", "sm")}</button>`).join("")}</div>`, { width: 300, class: "composer-popover" });
  }

  function openComposerConfig(anchor, id, nested = false) {
    const opt = S.configs.find((o) => o.id === id);
    if (!opt) return;
    const options = opt.type === "boolean" ? [{ value: "true", name: "On" }, { value: "false", name: "Off" }] : configChoices(opt);
    openPopover(anchor, `${nested ? `<button class="menu-item" data-action="composer-settings-back">‹ &nbsp; Agent options</button>` : ""}<div class="popover-title">${esc(opt.name || opt.id)}</div>
      ${options.length > 8 ? `<input class="input composer-search" placeholder="Search ${esc(opt.name || "options")}" aria-label="Search options">` : ""}
      <div class="popover-list composer-options">${options.map((c) => `<button class="menu-item ${String(c.value) === String(opt.currentValue) ? "selected" : ""}" data-action="pick-config" data-id="${esc(opt.id)}" data-value="${esc(c.value)}" data-type="${esc(opt.type)}"><span class="popover-main"><b>${esc(c.name || c.value)}</b>${c.description || c.groupName ? `<small>${esc([c.groupName, c.description].filter(Boolean).join(" · "))}</small>` : ""}</span>${String(c.value) === String(opt.currentValue) ? icon("check", "sm") : ""}</button>`).join("") || `<div class="popover-empty">No choices published by this agent.</div>`}</div>`, { width: 300, class: "composer-popover" });
    const search = $(".composer-search");
    if (search) {
      search.addEventListener("input", () => $$(".composer-options .menu-item").forEach((item) => { item.hidden = !item.textContent.toLowerCase().includes(search.value.toLowerCase()); }));
      if (!isMobile()) search.focus();
    }
  }

  async function prepareComposer() {
    if (!S.current) {
      await previewDraft();
      return;
    }
    const id = S.current.id;
    const agentId = S.current.agentId;
    if (S.preparing === id) return;
    S.preparing = id;
    const controller = new AbortController();
    S.prepareController = controller;
    const slowTimer = setTimeout(() => { if (S.preparing === id && S.current?.id === id && !S.modal) showSlowAgent(true); }, 30000);
    renderComposer();
    try {
      const d = await api(`/conversations/${encodeURIComponent(id)}/prepare`, { method: "POST", body: {}, signal: controller.signal });
      if (S.current?.id !== id || S.current?.agentId !== agentId) return;
      S.current = d.conversation;
      S.configs = d.configs || [];
      upsertConversation(d.conversation, false);
    } catch (e) {
      if (!controller.signal.aborted && S.current?.id === id && S.current?.agentId === agentId) showAgentBlocker(e);
    } finally {
      clearTimeout(slowTimer);
      if (S.modal?.type === "agent-slow" && S.modal.conversationId === id && S.modal.connecting) closeModal();
      if (S.prepareController === controller) S.prepareController = null;
      if (S.preparing === id) S.preparing = "";
      if (S.current?.id === id) { renderTopbar(); renderComposer(); }
    }
  }

  async function previewDraft() {
    const agentId = S.welcomeAgent;
    if (!agentId) return false;
    const seq = ++S.draftPreviewSeq;
    S.preparing = "draft";
    renderComposer();
    try {
      const chosenIds = new Set(S.draftSelections.map((choice) => choice.id));
      const defaults = agentId === S.agentDefaults.agentId ? (S.agentDefaults.selections || []).filter((choice) => !chosenIds.has(choice.id)) : [];
      const d = await api("/settings/agent-defaults/preview", { method: "POST", body: { agentId, selections: [...defaults, ...S.draftSelections] } });
      if (seq !== S.draftPreviewSeq || S.current || S.welcomeAgent !== agentId) return false;
      S.configs = d.configs || [];
      return true;
    } catch (e) {
      if (seq === S.draftPreviewSeq && !S.current) showAgentBlocker(e);
      return false;
    } finally {
      if (seq === S.draftPreviewSeq) {
        S.preparing = "";
        if (!S.current) renderComposer();
      }
    }
  }

  function showSlowAgent(connecting) {
    S.modal = { type: "agent-slow", conversationId: S.current?.id, connecting };
    openModal(`${modalHead("Still waiting for the agent", agentName(currentAgent()))}<div class="modal-body" style="padding:18px"><p class="muted">${connecting ? "The agent has not finished connecting." : "The agent has not sent a response yet."} You can keep waiting, inspect its details, or stop.</p></div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(currentAgent()?.id || "")}">Agent details</button><button class="btn btn-secondary" data-action="${connecting ? "stop-preparing" : "stop-slow-turn"}">Stop</button><button class="btn btn-primary" data-action="close-modal">Keep waiting</button></div>`, { size: "md" });
  }

  let xtermLoading = null;
  function loadXterm() {
    if (window.Terminal) return Promise.resolve();
    if (!xtermLoading) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = "/static/vendor/xterm/xterm.css";
      document.head.appendChild(link);
      const load = (src) => new Promise((resolve, reject) => {
        const s = document.createElement("script");
        s.src = src;
        s.onload = resolve;
        s.onerror = () => reject(new Error(`Could not load ${src}`));
        document.head.appendChild(s);
      });
      xtermLoading = load("/static/vendor/xterm/xterm.js")
        .then(() => load("/static/vendor/xterm/addon-web-links.js"))
        .catch((e) => { xtermLoading = null; throw e; });
    }
    return xtermLoading;
  }

  function openAuthTerminal(agentId, methodId) {
    closeModal();
    const agent = agentById(agentId);
    const conversationId = S.current?.id;
    S.modal = { type: "auth-terminal", agentId };
    openModal(`${modalHead("Sign in to " + agentName(agent), "Follow the agent's login instructions below.")}<div class="modal-body" style="padding:16px"><div id="auth-terminal" class="auth-terminal"></div><div id="auth-terminal-status" class="auth-terminal-status">Connecting…</div></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button></div>`, { size: "md" });
    loadXterm().then(() => startAuthTerminal(agentId, methodId, conversationId), () => { const status = $("#auth-terminal-status"); if (status) status.textContent = "Terminal could not be loaded."; });
  }

  function startAuthTerminal(agentId, methodId, conversationId) {
    if (!$("#auth-terminal")) return;
    const term = new Terminal({ cursorBlink: true, fontSize: 13, rows: 20, cols: 70, theme: { background: "#0b0c0e", foreground: "#e8e8e8" } });
    if (typeof WebLinksAddon !== "undefined") term.loadAddon(new WebLinksAddon.WebLinksAddon());
    const container = $("#auth-terminal");
    term.open(container);
    const cols = Math.max(25, Math.floor((container.clientWidth - 20) / 8));
    term.resize(cols, 20);
    const url = new URL(`/api/agents/${encodeURIComponent(agentId)}/auth-terminal`, location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("methodId", methodId);
    const socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    S.authTerminal = { socket, terminal: term };
    socket.onopen = () => {
      $("#auth-terminal-status").textContent = "Login in progress";
      socket.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      term.focus();
    };
    term.onData((data) => { if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "input", data })); });
    socket.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) { term.write(new Uint8Array(event.data)); return; }
      let result;
      try { result = JSON.parse(event.data); } catch { term.write(event.data); return; }
      if (result.type !== "exit") return;
      $("#auth-terminal-status").textContent = result.message || (result.ok ? "Sign-in complete." : "Sign-in failed.");
      if (result.ok) {
        closeModal();
        toast("Sign-in complete", "The agent is reconnecting.", "success");
        if (conversationId && S.current?.id === conversationId) prepareComposer();
      }
    };
    socket.onerror = () => { const status = $("#auth-terminal-status"); if (status) status.textContent = "Could not start the login terminal."; };
    socket.onclose = () => { const status = $("#auth-terminal-status"); if (status && status.textContent === "Login in progress") status.textContent = "The login terminal closed."; };
  }

  function showAgentBlocker(error) {
    const agent = currentAgent();
    const methods = error.data?.authMethods?.length ? error.data.authMethods : (agent?.authMethods || []);
    S.modal = { type: "agent-blocker" };
    openModal(`${modalHead("Agent needs attention", agentName(agent))}<div class="modal-body" style="padding:18px"><p class="muted">${esc(error.message)}</p>${methods.length ? `<div class="settings-title">Choose how to continue</div><div class="blocker-actions">${methods.map((m) => `<button class="btn btn-secondary" data-action="blocker-auth" data-id="${esc(agent.id)}" data-method="${esc(m.id)}" data-auth-type="${esc(m.type || "agent")}">${esc(m.name || m.id)}</button>`).join("")}</div>` : ""}</div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(agent?.id || "")}">View agent details</button><button class="btn btn-primary" data-action="retry-prepare">Try again</button></div>`, { size: "md" });
  }

  async function setConfig(id, value) {
    if (!S.current) {
      const previous = S.draftSelections.slice();
      S.draftSelections = previous.filter((choice) => choice.id !== id).concat({ id, value });
      closePopover();
      const seq = S.draftPreviewSeq + 1;
      if (!await previewDraft() && seq === S.draftPreviewSeq && !S.current) S.draftSelections = previous;
      return;
    }
    try {
      const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/config`, { method: "POST", body: { configId: id, value } });
      S.current = d.conversation;
      S.configs = d.configs || parseConfigs(d.conversation.configOptions);
      upsertConversation(d.conversation, false);
      renderTopbar();
      renderComposer();
      closePopover();
    } catch (e) {
      toast("Could not change setting", e.message, "error");
    }
  }

  async function pickAgent(id) {
    const ag = agentById(id);
    if (!ag) return;
    if (S.current && S.preparing === S.current.id) { toast("Connecting", "Wait for the agent to finish connecting.", "info"); return; }
    S.welcomeAgent = id;
    if (!S.current) {
      closePopover();
      S.currentAgent = ag;
      S.configs = [];
      S.draftSelections = [];
      ++S.draftPreviewSeq;
      S.preparing = "";
      renderTopbar();
      renderChat();
      renderComposer();
      focusComposer();
      return;
    }
    if (S.current.agentId === id) { closePopover(); return; }
    try {
      const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/agent`, { method: "POST", body: { agentId: id } });
      S.current = d.conversation;
      S.currentAgent = ag;
      S.messages = [];
      S.active = null;
      closePopover();
      await loadConversation(d.conversation.id, false);
      prepareComposer();
      toast("Agent switched", `${ag.name} will receive a recap on the next message.`, "success");
    } catch (e) {
      toast("Could not switch agent", e.message, "error");
    }
  }

  function openRoutingMenu(anchor) {
    if (!S.current) return;
    const r = S.current.routing || { replyToOrigin: true, mirrorFinal: false, notifyApprovals: true };
    const links = (S.current.links || []).filter((l) => l.channel !== "web");
    openPopover(anchor, `<div class="popover-title">Channel routing</div>
      <div class="popover-note">The web always receives the full stream. External channels get a concise final answer.</div>
      <div class="settings-row"><div><div class="label">Reply to origin</div><div class="desc">Send the final answer back to the chat where the turn started.</div></div><button class="switch ${r.replyToOrigin ? "on" : ""}" data-action="toggle-routing" data-key="replyToOrigin"></button></div>
      <div class="settings-row"><div><div class="label">Mirror final answers</div><div class="desc">Copy the final answer to every linked chat.</div></div><button class="switch ${r.mirrorFinal ? "on" : ""}" data-action="toggle-routing" data-key="mirrorFinal"></button></div>
      <div class="settings-row"><div><div class="label">Approval notifications</div><div class="desc">Ping the origin chat when a tool needs a decision.</div></div><button class="switch ${r.notifyApprovals ? "on" : ""}" data-action="toggle-routing" data-key="notifyApprovals"></button></div>
      <div class="popover-note" style="margin-top:8px">${links.length ? "Linked: " + links.map((l) => esc(l.channel + (l.displayName ? " · " + l.displayName : ""))).join(", ") : "No external chats linked yet."}</div>`, { width: 340 });
  }

  async function toggleRouting(key) {
    if (!S.current) return;
    const r = { ...(S.current.routing || {}) };
    r[key] = !r[key];
    try {
      const d = await api(`/conversations/${encodeURIComponent(S.current.id)}/routing`, { method: "POST", body: r });
      S.current = d.conversation;
      const btn = document.querySelector(`[data-action="routing-menu"]`);
      if (btn) openRoutingMenu(btn);
      renderTopbar();
    } catch (e) {
      toast("Could not update routing", e.message, "error");
    }
  }

  function openMoreMenu(anchor) {
    if (!S.current) return;
    const c = S.current;
    openPopover(anchor, `<div class="popover-list">
      ${menuItem(c.pinned ? "pin" : "pin", c.pinned ? "Unpin conversation" : "Pin conversation", "toggle-pin", `data-id="${esc(c.id)}"`)}
      ${menuItem("edit", "Rename", "rename-conv", `data-id="${esc(c.id)}"`)}
      ${menuItem("download", "Export JSON", "export-conv", `data-id="${esc(c.id)}"`)}
      ${menuItem("file", c.archived ? "Unarchive" : "Archive", "archive-conv", `data-id="${esc(c.id)}"`)}
      <div class="popover-sep"></div>
      ${menuItem("trash", "Delete conversation", "delete-conv", `data-id="${esc(c.id)}"`, true)}
    </div>`, { width: 230 });
  }

  async function togglePin(id) {
    const c = S.conversations.find((x) => x.id === id);
    if (!c) return;
    try {
      const d = await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { pinned: !c.pinned } });
      upsertConversation(d.conversation);
      closePopover();
    } catch (e) { toast("Could not update pin", e.message, "error"); }
  }

  async function archiveConversation(id) {
    try {
      await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { archived: true } });
      S.conversations = S.conversations.filter((c) => c.id !== id);
      if (S.current && S.current.id === id) { S.current = null; switchComposer(""); S.messages = []; setRoute(""); renderChat(); renderTopbar(); renderComposer(); }
      closePopover(); renderSidebar();
    } catch (e) { toast("Could not archive", e.message, "error"); }
  }

  async function deleteConversation(id) {
    confirmModal("Delete conversation?", "This removes the transcript, approvals and channel links from NusaMation. It cannot be undone.", async () => {
      try {
        await api(`/conversations/${encodeURIComponent(id)}`, { method: "DELETE" });
        S.conversations = S.conversations.filter((c) => c.id !== id);
        composers.delete(id);
        if (S.current && S.current.id === id) { S.current = null; switchComposer(""); S.messages = []; setRoute(""); renderChat(); renderTopbar(); renderComposer(); }
        renderSidebar(); closeModal(); toast("Conversation deleted", "", "success");
      } catch (e) { toast("Could not delete", e.message, "error"); }
    }, "Delete");
  }

  function exportConversation(id) {
    window.open(`/api/conversations/${encodeURIComponent(id)}/export`, "_blank", "noopener");
    closePopover();
  }
  /* ---------------- modals ---------------- */
  function openModal(html, opts = {}) {
    const root = $("#modal-root");
    root.innerHTML = `<div class="modal-overlay" data-modal-overlay="1"><div class="modal ${opts.size || ""}" role="dialog" aria-modal="true">${html}</div></div>`;
    const overlay = root.firstElementChild;
    overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) closeModal(); });
    setTimeout(() => {
      const focus = root.querySelector("[autofocus]");
      if (focus) focus.focus();
    }, 30);
  }

  function closeModal(cancelRequest = true) {
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
    if (S.channelsRefreshTimer) { clearInterval(S.channelsRefreshTimer); S.channelsRefreshTimer = null; }
    S.modal = null;
    S.confirmAction = null;
    const root = $("#modal-root");
    if (root) root.innerHTML = "";
    if (previousType === "agent-slow") setTimeout(showPendingRequest, 0);
  }

  function modalHead(title, sub, close = true) {
    return `<div class="modal-head"><div><div class="modal-title">${esc(title)}</div>${sub ? `<div class="modal-sub">${esc(sub)}</div>` : ""}</div>${close ? `<button class="icon-btn modal-close" data-action="close-modal" aria-label="Close">${icon("x")}</button>` : ""}</div>`;
  }

  function confirmModal(title, message, onConfirm, confirmLabel = "Confirm") {
    S.confirmAction = onConfirm;
    openModal(`${modalHead(title, "")}<div class="modal-body" style="padding:18px"><p class="muted" style="margin:0;font-size:13px;line-height:1.6">${esc(message)}</p></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button><button class="btn btn-danger" data-action="confirm-action">${esc(confirmLabel)}</button></div>`, { size: "sm" });
  }

  function promptModal(title, label, value, onSave, placeholder = "") {
    S.promptAction = onSave;
    openModal(`${modalHead(title, "")}<div class="modal-body" style="padding:18px"><div class="field"><label>${esc(label)}</label><input class="input" id="prompt-modal-input" value="${esc(value || "")}" placeholder="${esc(placeholder)}" autofocus></div></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button><button class="btn btn-primary" data-action="prompt-save">Save</button></div>`, { size: "sm" });
  }

  function renameConversation(id) {
    const c = S.conversations.find((x) => x.id === id);
    if (!c) return;
    closePopover();
    promptModal("Rename conversation", "Title", c.title, async (value) => {
      if (!value.trim()) return;
      try {
        const d = await api(`/conversations/${encodeURIComponent(id)}`, { method: "PATCH", body: { title: value.trim() } });
        upsertConversation(d.conversation);
        closeModal();
      } catch (e) { toast("Could not rename", e.message, "error"); }
    });
  }

  function renderApprovalsModal() {
    S.modal = { type: "approvals" };
    const list = S.approvals.filter((a) => a.status === "pending");
    const body = list.length
      ? `<div class="approval-list">${list.map((a) => `<div class="approval-modal-item"><div class="muted" style="font-size:10.5px;margin-bottom:5px">${esc(a.conversationTitle || "Conversation")} · ${esc(a.agentName || "agent")}</div>${approvalCard(a)}</div>`).join("")}</div>`
      : `<div class="empty-settings">${icon("shield")}<div style="margin-top:8px">No pending approvals. Your agents are behaving.</div></div>`;
    openModal(`${modalHead("Approvals", `${list.length} pending`)}<div class="modal-body" style="padding:16px">${body}</div>`, { size: "md" });
  }

  function showPendingApproval() {
    if (S.modal) return;
    const approval = currentApprovals()[0];
    if (!approval) return;
    S.modal = { type: "approval", id: approval.id };
    openModal(`${modalHead("Approval needed", "The agent is waiting for your decision")}<div class="modal-body" style="padding:16px">${approvalCard(approval)}</div>`, { size: "md" });
  }

  function showPendingRequest() {
    if (S.modal) return;
    const request = S.elicitations.find((e) => e.conversationId === S.current?.id);
    if (request) return showElicitation(request);
    showPendingApproval();
  }

  function showElicitation(entry) {
    const req = entry.request || {};
    const agent = agentById(entry.agentId) || currentAgent();
    const schema = req.requestedSchema || {};
    const required = new Set(schema.required || []);
    const unsupported = Object.values(schema.properties || {}).some((spec) => !["string", "integer", "number", "boolean", "array"].includes(spec.type) || (spec.type === "array" && !spec.items?.enum && !spec.items?.anyOf));
    const fields = Object.entries(schema.properties || {}).map(([key, spec]) => {
      const title = esc(spec.title || key);
      const label = `<label>${title}${required.has(key) ? " *" : ""}</label>${spec.description ? `<div class="settings-desc">${esc(spec.description)}</div>` : ""}`;
      const attrs = `data-key="${esc(key)}" data-type="${esc(spec.type || "string")}" ${required.has(key) ? "required" : ""}`;
      const choices = spec.oneOf || (spec.enum || []).map((v) => ({ const: v, title: v }));
      let control = "";
      if (spec.type === "array") {
        const items = spec.items || {};
        const values = items.anyOf || (items.enum || []).map((v) => ({ const: v, title: v }));
        control = `<div class="elicitation-multi" data-key="${esc(key)}" data-min="${esc(spec.minItems ?? (required.has(key) ? 1 : 0))}" data-max="${esc(spec.maxItems ?? "")}">${values.map((v) => `<label><input type="checkbox" value="${esc(v.const)}" ${(spec.default || []).includes(v.const) ? "checked" : ""}> ${esc(v.title || v.const)}</label>`).join("")}</div>`;
      } else if (choices.length || spec.type === "boolean") {
        const options = spec.type === "boolean" ? [{ const: "true", title: "Yes" }, { const: "false", title: "No" }] : choices;
        control = `<select class="select" ${attrs}><option value="">Choose…</option>${options.map((v) => `<option value="${esc(v.const)}" ${String(spec.default) === String(v.const) ? "selected" : ""}>${esc(v.title || v.const)}</option>`).join("")}</select>`;
      } else {
        const inputType = spec.type === "number" || spec.type === "integer" ? "number" : spec.format === "email" ? "email" : spec.format === "date" ? "date" : "text";
        const bounds = `${spec.minimum != null ? `min="${esc(spec.minimum)}"` : ""} ${spec.maximum != null ? `max="${esc(spec.maximum)}"` : ""} ${spec.minLength != null ? `minlength="${esc(spec.minLength)}"` : ""} ${spec.maxLength != null ? `maxlength="${esc(spec.maxLength)}"` : ""}`;
        control = `<input class="input" type="${inputType}" ${attrs} ${bounds} ${spec.type === "number" ? 'step="any"' : ""} value="${esc(spec.default ?? "")}">`;
      }
      return `<div class="field">${label}${control}</div>`;
    }).join("");
    let body = `<p class="muted">${esc(req.message || "The agent needs your input.")}</p>`;
    if (req.mode === "url") {
      try {
        const url = new URL(req.url);
        body += `<div class="settings-row"><div><div class="label">${esc(url.host)}</div><div class="desc">${esc(url.href)}</div></div></div>`;
      } catch { body += `<p>Invalid URL from agent.</p>`; }
    } else if (unsupported) {
      body += `<p>This request contains field types that cannot be entered here.</p><pre class="tool-pre">${esc(JSON.stringify(schema, null, 2))}</pre>`;
    } else {
      body += fields;
    }
    S.modal = { type: "elicitation", id: entry.id, conversationId: entry.conversationId };
    openModal(`<form data-form="elicitation" id="elicitation-form">${modalHead("Agent needs input", agentName(agent))}<div class="modal-body" style="padding:18px">${body}</div><div class="modal-foot"><button type="button" class="btn btn-secondary" data-action="elicitation-decline">Decline</button><button type="submit" class="btn btn-primary" ${unsupported ? "disabled" : ""}>${req.mode === "url" ? "Open link" : "Continue"}</button></div></form>`, { size: "md" });
  }

  async function respondElicitation(conversationId, id, response) {
    try {
      await api(`/conversations/${encodeURIComponent(conversationId)}/elicitations/${encodeURIComponent(id)}`, { method: "POST", body: response });
      S.elicitations = S.elicitations.filter((e) => e.id !== id);
      if (S.modal?.type === "elicitation" && S.modal.id === id) closeModal(false);
      showPendingRequest();
    } catch (e) { toast("Could not answer agent", e.message, "error"); }
  }

  function submitElicitation(form) {
    const entry = S.elicitations.find((e) => e.id === S.modal?.id);
    if (!entry) return;
    const req = entry.request || {};
    const content = {};
    if (req.mode === "url") {
      try { const url = new URL(req.url); if (!["http:", "https:"].includes(url.protocol)) throw new Error("invalid protocol"); window.open(url.href, "_blank", "noopener"); } catch { toast("Invalid link", "The agent provided an invalid URL.", "error"); return; }
    } else {
      for (const input of $$('[data-key][data-type]', form)) {
        if (input.value === "") continue;
        content[input.dataset.key] = input.dataset.type === "boolean" ? input.value === "true" : ["number", "integer"].includes(input.dataset.type) ? Number(input.value) : input.value;
      }
      for (const group of $$(".elicitation-multi", form)) {
        const selected = $$("input:checked", group).map((input) => input.value);
        if (selected.length < Number(group.dataset.min || 0) || (group.dataset.max && selected.length > Number(group.dataset.max))) { toast("Check your selection", "Choose the requested number of options.", "error"); return; }
        if (selected.length) content[group.dataset.key] = selected;
      }
    }
    respondElicitation(entry.conversationId, entry.id, req.mode === "url" ? { action: "accept" } : { action: "accept", content });
  }

  async function resolveApproval(id, optionId, card) {
    if (S.resolving.has(id)) return;
    S.resolving.add(id);
    if (card) card.innerHTML = `<div class="loading-row"><span class="spinner"></span> Sending decision…</div>`;
    try {
      const d = await api(`/approvals/${encodeURIComponent(id)}`, { method: "POST", body: { optionId } });
      S.approvalsById.set(d.approval.id, d.approval);
      S.approvals = S.approvals.filter((a) => a.id !== id);
      renderTopbar(); renderSidebar();
      if (S.modal?.type === "approvals") renderApprovalsModal();
      scheduleChatRender();
      toast("Decision sent", d.approval.status === "approved" ? "The agent can continue." : "The tool call was not allowed.", "success");
    } catch (e) {
      toast("Could not answer approval", e.message, "error");
      scheduleChatRender();
    } finally {
      S.resolving.delete(id);
    }
  }

  /* ---------------- settings ---------------- */
  function openSettings(tab = "") {
    closePopover();
    if (tab) S.settingsTab = tab;
    S.agentForm = null;
    S.mcpForm = null;
    S.modal = { type: "settings" };
    renderSettings();
  }

  function renderSettings() {
    const tabs = [
      ["agents", "sparkles", "Agents"],
      ["defaults", "star", "Defaults"],
      ["automations", "clock", "Automations"],
      ["mcp", "zap", "MCP servers"],
      ["channels", "message", "Channels"],
      ["routing", "link", "Routing"],
      ["account", "shield", "Account"],
    ];
    let body = "";
    if (S.settingsTab === "agents") body = agentsSettings();
    else if (S.settingsTab === "defaults") body = agentDefaultsSettings();
    else if (S.settingsTab === "automations") body = automationsSettings();
    else if (S.settingsTab === "mcp") body = mcpSettings();
    else if (S.settingsTab === "channels") body = channelsSettings();
    else if (S.settingsTab === "routing") body = routingSettings();
    else body = accountSettings();
    const content = `<div class="settings-layout">
      <nav class="settings-tabs">${tabs.map(([id, ic, label]) => `<button class="settings-tab ${S.settingsTab === id ? "active" : ""}" data-action="settings-tab" data-tab="${id}">${icon(ic)}<span>${label}</span></button>`).join("")}</nav>
      <div class="settings-content" id="settings-content">${body}</div>
    </div>`;
    const layout = $("#modal-root .settings-modal .settings-layout");
    if (layout) {
      $$(".settings-tab", layout).forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === S.settingsTab));
      $("#settings-content", layout).innerHTML = body;
    } else openModal(`${modalHead("Control center", "Agents, channels, routing and account")}${content}`, { size: "settings-modal" });
    if (S.settingsTab === "channels") startQRTimer();
  }

  function settingsHead(title, desc, action = "") {
    return `<div class="section-head"><div><h3>${title}</h3><div class="settings-desc" style="margin:3px 0 0">${desc}</div></div>${action}</div>`;
  }

  function automationDate(ms, zone) {
    if (!ms) return "";
    try {
      const parts = Object.fromEntries(new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(new Date(ms)).map((p) => [p.type, p.value]));
      return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
    } catch { return ""; }
  }

  function displayAutomationTime(ms, zone) {
    try { return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: zone === "Local" ? S.automationServerZone : zone }).format(new Date(ms)); }
    catch { return new Date(ms).toLocaleString(); }
  }

  function automationTimestamp(value, zone) {
    if (!value) return 0;
    const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
    if (!match) throw new Error("Enter a valid date and time.");
    const wanted = Date.UTC(...[Number(match[1]), Number(match[2]) - 1, Number(match[3]), Number(match[4]), Number(match[5])]);
    let result = wanted;
    for (let i = 0; i < 3; i++) {
      const actual = automationDate(result, zone);
      if (!actual) throw new Error("Enter a valid IANA timezone, for example Asia/Jakarta.");
      const p = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(actual);
      result += wanted - Date.UTC(Number(p[1]), Number(p[2]) - 1, Number(p[3]), Number(p[4]), Number(p[5]));
    }
    if (automationDate(result, zone) !== value) throw new Error("This local time does not exist in the selected timezone.");
    return result;
  }

  function automationsSettings() {
    if (S.automationLoading) return `${settingsHead("Automations", "Scheduled ACP jobs.")}<div class="settings-desc"><span class="spinner"></span> Loading…</div>`;
    if (S.automationError) return `${settingsHead("Automations", "Scheduled ACP jobs.")}<div role="alert">${esc(S.automationError)}</div><button class="btn btn-secondary" data-action="automations-list">Retry</button>`;
    if (S.automationView === "form") return automationFormHTML();
    if (S.automationView === "history") {
      const job = S.automationJob;
      return `${settingsHead(esc(job.name), "Run history", `<button class="btn btn-secondary btn-sm" data-action="automations-list">Back</button>`)}
        <div class="settings-desc">${esc(job.prompt)}</div>
        <div class="settings-section">${S.automationRuns.map((r) => `<div class="settings-row"><div><div class="label">${esc(r.status)} · ${esc(displayAutomationTime(r.startedAt, job.timezone))}</div><div class="desc">Scheduled ${esc(displayAutomationTime(r.scheduledAt, job.timezone))} (${esc(job.timezone)})${r.error ? " · " + esc(r.error) : ""}</div></div>${r.conversationId ? `<button class="btn btn-secondary btn-sm" data-action="automation-room" data-id="${esc(r.conversationId)}">Open chat</button>` : ""}</div>`).join("") || `<div class="empty-settings">No runs yet.</div>`}</div>`;
    }
    return `${settingsHead("Automations", "Run prompts on a schedule.", `<button class="btn btn-primary btn-sm" data-action="automation-add">${icon("plus", "sm")} Add automation</button>`)}
      <div class="agent-list">${S.automations.map((a) => {
        const state = !a.enabled ? "Paused" : a.expiresAt && a.expiresAt < Date.now() ? "Expired" : !a.nextRunAt ? "Dispatched" : "Active";
        const next = a.nextRunAt && state === "Active" ? ` · Next ${displayAutomationTime(a.nextRunAt, a.timezone)}` : "";
        return `<div class="agent-card"><div class="agent-card-main"><div class="agent-card-name">${esc(a.name)} <span class="agent-status">${state}</span></div><div class="agent-card-sub">${esc(agentName(agentById(a.agentId)))} · ${a.schedule === "cron" ? esc(a.cron) + " · " + esc(a.timezone) : "Once"}${esc(next)}</div><div class="agent-card-sub">${a.sessionMode === "same" ? "Dedicated chat" : "New chat each run"}</div></div><div class="agent-card-actions"><button class="btn btn-secondary btn-sm" data-action="automation-history" data-id="${esc(a.id)}">History</button><button class="btn btn-secondary btn-sm" data-action="automation-edit" data-id="${esc(a.id)}">Edit</button><button class="btn btn-secondary btn-sm" data-action="automation-toggle" data-id="${esc(a.id)}">${a.enabled ? "Pause" : "Resume"}</button><button class="icon-btn sm" data-action="automation-delete" data-id="${esc(a.id)}" title="Delete" aria-label="Delete automation">${icon("trash", "sm")}</button></div></div>`;
      }).join("") || `<div class="empty-settings">No automations yet.</div>`}</div>`;
  }

  function automationFormHTML() {
    const a = S.automationJob || {};
    const zone = a.timezone && a.timezone !== "Local" ? a.timezone : (S.automationServerZone && S.automationServerZone !== "Local" ? S.automationServerZone : (Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"));
    const agent = a.agentId || S.agentDefaults.agentId || S.agents.find((x) => x.enabled)?.id || "";
    return `${settingsHead(a.id ? "Edit automation" : "New automation", "Times use the selected timezone.", `<button class="btn btn-secondary btn-sm" data-action="automations-list">Cancel</button>`)}
      <form data-form="automation" data-id="${esc(a.id || "")}"><div class="form-grid">
        <div class="field full"><label>Name</label><input class="input" name="name" value="${esc(a.name || "")}" required></div>
        <div class="field full"><label>Prompt</label><textarea class="textarea" name="prompt" rows="4" required>${esc(a.prompt || "")}</textarea></div>
        <div class="field"><label>ACP agent</label><select class="select" name="agentId" required>${S.agents.filter((x) => x.enabled).map((x) => `<option value="${esc(x.id)}" ${x.id === agent ? "selected" : ""}>${esc(x.name)}</option>`).join("")}</select></div>
        <div class="field"><label>Session</label><select class="select" name="sessionMode"><option value="isolated" ${a.sessionMode !== "same" ? "selected" : ""}>New chat each run</option><option value="same" ${a.sessionMode === "same" ? "selected" : ""}>Reuse dedicated chat</option></select></div>
        <div class="field"><label>Schedule</label><select class="select" name="schedule" data-change="automation-schedule"><option value="once" ${a.schedule !== "cron" ? "selected" : ""}>Run once</option><option value="cron" ${a.schedule === "cron" ? "selected" : ""}>Repeat (cron)</option></select></div>
        <div class="field"><label>Timezone</label><input class="input" name="timezone" value="${esc(zone)}" required></div>
        <div class="field full" data-automation-once ${a.schedule === "cron" ? "hidden" : ""}><label>Run at</label><input class="input" type="datetime-local" name="runAt" value="${esc(automationDate(a.runAt, zone))}"></div>
        <div class="field full" data-automation-cron ${a.schedule === "cron" ? "" : "hidden"}><label>Schedule builder</label><select class="select" data-change="automation-preset"><option value="">Custom expression</option><option value="minutes">Every N minutes</option><option value="hours">Every N hours</option><option value="daily">Every day</option><option value="weekly">Every week</option><option value="monthly">Every month</option></select>
          <div class="automation-builder" hidden><label data-automation-interval>Interval <input class="input" type="number" min="1" max="59" value="5" data-automation-build="interval"></label><label data-automation-time>Time <input class="input" type="time" value="09:00" data-automation-build="time"></label><label data-automation-weekday>Day <select class="select" data-automation-build="weekday"><option value="1">Monday</option><option value="2">Tuesday</option><option value="3">Wednesday</option><option value="4">Thursday</option><option value="5">Friday</option><option value="6">Saturday</option><option value="0">Sunday</option></select></label><label data-automation-monthday>Day of month <input class="input" type="number" min="1" max="31" value="1" data-automation-build="monthday"></label></div>
          <label style="margin-top:10px">Cron expression</label><input class="input mono" name="cron" value="${esc(a.cron || "0 9 * * *")}" placeholder="0 9 * * *" data-input="automation-cron"><small class="muted">minute · hour · day of month · month · day of week</small></div>
        <div class="field full"><label>Expires at <span class="muted">(optional)</span></label><input class="input" type="datetime-local" name="expiresAt" value="${esc(automationDate(a.expiresAt, zone))}"></div>
      </div><div class="settings-desc" style="margin-top:12px">Missed schedules run once when the server comes back online. No run starts after expiry.</div><div class="form-actions automation-actions"><button class="btn btn-secondary" type="button" data-action="automations-list">Cancel</button><button class="btn btn-primary" type="submit">Save automation</button></div></form>`;
  }

  async function loadAutomations() {
    S.automationView = "list"; S.automationLoading = true; S.automationError = ""; renderSettings();
    try { const d = await api("/automations"); S.automations = d.automations || []; S.automationServerZone = d.timezone || ""; }
    catch (e) { S.automationError = e.message; }
    finally { S.automationLoading = false; if (S.modal?.type === "settings" && S.settingsTab === "automations") renderSettings(); }
  }

  function updateAutomationBuilder(form) {
    const preset = $('[data-change="automation-preset"]', form).value;
    const box = $(".automation-builder", form); box.hidden = !preset;
    const visible = (selector, show) => { $(selector, box).hidden = !show; };
    visible("[data-automation-interval]", ["minutes", "hours"].includes(preset));
    visible("[data-automation-time]", ["daily", "weekly", "monthly"].includes(preset));
    visible("[data-automation-weekday]", preset === "weekly"); visible("[data-automation-monthday]", preset === "monthly");
    if (!preset) return;
    const interval = $('[data-automation-build="interval"]', box); interval.max = preset === "hours" ? "23" : "59";
    const n = Math.max(1, Math.min(Number(interval.max), Number(interval.value) || 1)); interval.value = String(n);
    const [hour, minute] = ($('[data-automation-build="time"]', box).value || "09:00").split(":").map(Number);
    const weekday = $('[data-automation-build="weekday"]', box).value;
    const dayInput = $('[data-automation-build="monthday"]', box); const day = Math.max(1, Math.min(31, Number(dayInput.value) || 1)); dayInput.value = String(day);
    const expressions = { minutes: `*/${n} * * * *`, hours: `0 */${n} * * *`, daily: `${minute} ${hour} * * *`, weekly: `${minute} ${hour} * * ${weekday}`, monthly: `${minute} ${hour} ${day} * *` };
    $('[name="cron"]', form).value = expressions[preset];
  }

  async function saveAutomation(form) {
    const fd = new FormData(form); const id = form.dataset.id; const zone = String(fd.get("timezone") || "").trim();
    try {
      const body = { name: String(fd.get("name") || ""), prompt: String(fd.get("prompt") || ""), agentId: String(fd.get("agentId") || ""), schedule: String(fd.get("schedule") || "once"), cron: String(fd.get("cron") || ""), runAt: 0, timezone: zone, sessionMode: String(fd.get("sessionMode") || "isolated"), expiresAt: automationTimestamp(String(fd.get("expiresAt") || ""), zone), enabled: id ? !!S.automationJob.enabled : true, updatedAt: id ? S.automationJob.updatedAt : 0 };
      if (body.schedule === "once") body.runAt = automationTimestamp(String(fd.get("runAt") || ""), zone);
      const btn = form.querySelector('[type="submit"]'); btn.disabled = true;
      try { await api(id ? `/automations/${encodeURIComponent(id)}` : "/automations", { method: id ? "PUT" : "POST", body }); }
      finally { btn.disabled = false; }
      toast("Automation saved", "The scheduler will pick up the change.", "success");
      if (S.modal?.type === "settings" && S.settingsTab === "automations") await loadAutomations();
    } catch (e) { toast("Could not save automation", e.message, "error", 8000); }
  }

  function agentDefaultsSettings() {
    const draft = S.defaultDraft || { agentId: "", selections: [] };
    const selected = new Set(draft.selections.map((s) => s.id));
    const opts = S.defaultConfigs.filter((o) => o && o.id && !selected.has(o.id) && ["select", "boolean"].includes(o.type));
    const rows = opts.map((o) => {
      const choices = o.type === "boolean" ? [{ value: "true", name: "On" }, { value: "false", name: "Off" }] : configChoices(o);
      return `<div class="field"><label>${esc(o.name || o.id)}</label><select class="select" data-change="default-choice" data-id="${esc(o.id)}" data-type="${esc(o.type)}" ${S.defaultBusy ? "disabled" : ""}><option value="">Use agent default</option>${choices.map((c) => `<option value="${esc(c.value)}">${esc(c.groupName ? c.groupName + " · " + (c.name || c.value) : (c.name || c.value))}</option>`).join("")}</select></div>`;
    }).join("");
    return `${settingsHead("Default ACP", "Used for new chats, including new WhatsApp conversations. Choose options in order; each choice refreshes the available options.")}
      <div class="field"><label>Agent</label><select class="select" data-change="default-agent" ${S.defaultBusy ? "disabled" : ""}><option value="">Choose an agent</option>${S.agents.filter((a) => a.enabled).map((a) => `<option value="${esc(a.id)}" ${draft.agentId === a.id ? "selected" : ""}>${esc(a.name)}</option>`).join("")}</select></div>
      ${S.defaultBusy ? `<div class="settings-desc"><span class="spinner"></span> Loading agent options…</div>` : ""}
      ${S.defaultError ? `<div class="settings-desc" role="alert">${esc(S.defaultError)} <button class="btn btn-secondary btn-sm" data-action="default-retry">Retry</button></div>` : ""}
      ${draft.selections.length ? `<div class="settings-section"><div class="settings-title">Selected in order</div>${draft.selections.map((s) => { const opt = S.defaultConfigs.find((o) => o.id === s.id); const label = opt && configChoices(opt).find((c) => String(c.value) === String(s.value))?.name; return `<div class="settings-row"><span>${esc(s.name || opt?.name || s.id)}: ${esc(s.label ?? label ?? s.value)}</span></div>`; }).join("")}<button class="btn btn-secondary btn-sm" data-action="default-undo" ${S.defaultBusy ? "disabled" : ""}>Undo last choice</button></div>` : ""}
      ${draft.agentId && !S.defaultError ? `<div class="form-grid" style="margin-top:16px">${rows || (!S.defaultBusy ? `<div class="settings-desc">No more configurable options.</div>` : "")}</div>` : ""}
      <div class="form-actions"><button class="btn btn-primary" data-action="save-agent-defaults" ${!draft.agentId || S.defaultBusy || S.defaultError ? "disabled" : ""}>Save defaults</button></div>`;
  }

  async function previewAgentDefaults() {
    const draft = S.defaultDraft;
    const seq = ++S.defaultSeq;
    S.defaultError = "";
    S.defaultConfigs = [];
    if (!draft.agentId) { renderSettings(); return; }
    S.defaultBusy = true; renderSettings();
    try {
      const d = await api("/settings/agent-defaults/preview", { method: "POST", body: { agentId: draft.agentId, selections: draft.selections.map(({ id, value }) => ({ id, value })) } });
      if (seq !== S.defaultSeq) return;
      S.defaultConfigs = d.configs || [];
    } catch (e) { if (seq === S.defaultSeq) S.defaultError = e.message; }
    finally { if (seq === S.defaultSeq) { S.defaultBusy = false; if (S.settingsTab === "defaults") renderSettings(); } }
  }

  async function saveAgentDefaults() {
    if (S.defaultBusy || !S.defaultDraft?.agentId) return;
    S.defaultBusy = true; renderSettings();
    try {
      const d = await api("/settings/agent-defaults", { method: "PUT", body: { agentId: S.defaultDraft.agentId, selections: S.defaultDraft.selections.map(({ id, value }) => ({ id, value })) } });
      S.agentDefaults = d.defaults;
      S.welcomeAgent = d.defaults.agentId;
      if (!S.current) { S.currentAgent = agentById(d.defaults.agentId); renderTopbar(); renderChat(); renderComposer(); }
      toast("Defaults saved", "New conversations will use these settings.", "success");
    } catch (e) { S.defaultError = e.message; }
    finally { S.defaultBusy = false; if (S.settingsTab === "defaults") renderSettings(); }
  }

  function agentsSettings() {
    if (S.agentForm) return agentFormHTML();
    return `${settingsHead("Agents", "ACP CLIs are the brain. Launch commands, workspaces, policies and secrets.", `<button class="btn btn-primary btn-sm" data-action="add-agent">${icon("plus", "sm")} Add agent</button>`)}
      <div class="agent-list">${S.agents.map((a) => `<div class="agent-card">
        ${agentOrb(a)}
        <div class="agent-card-main">
          <div class="agent-card-name">${esc(a.name)} <span class="agent-status"><span class="status-dot ${a.status === "ready" ? "" : a.status === "error" ? "offline" : "warn"}"></span>${esc(a.status || "unknown")}</span></div>
          <div class="agent-card-sub">${esc(a.command)} ${esc((a.args || []).join(" "))} · ${esc(a.cwd)}</div>
          <div class="agent-card-sub">${esc(a.description || "")}${a.defaultModel ? " · default " + esc(a.defaultModel) : ""}</div>
        </div>
        <div class="agent-card-actions">
          <button class="icon-btn sm" data-action="probe-agent" data-id="${esc(a.id)}" title="Probe ACP">${icon("refresh", "sm")}</button>
          <button class="icon-btn sm" data-action="agent-logs" data-id="${esc(a.id)}" title="Runtime logs">${icon("terminal", "sm")}</button>
          <button class="icon-btn sm" data-action="edit-agent" data-id="${esc(a.id)}" title="Edit">${icon("edit", "sm")}</button>
          <button class="icon-btn sm" data-action="delete-agent" data-id="${esc(a.id)}" title="Delete">${icon("trash", "sm")}</button>
        </div>
      </div>`).join("") || `<div class="empty-settings">No agents configured yet.<br>Add any ACP-compatible CLI to get started.</div>`}</div>
      <div class="settings-section" style="margin-top:22px">${settingsHead("Approval policy", "Local policy is separate from the agent's own permission mode. Ask is the safest default.")}<div class="settings-row"><div><div class="label">Default workspace</div><div class="desc">${esc((S.bootstrap?.app && S.bootstrap.app.dataDir) || "the NusaMation data directory")}/workspace</div></div><code class="mono" style="font-size:10.5px;color:var(--muted)">AGENTS.md seeded</code></div></div>`;
  }

  function envRowsHTML(list, prefix) {
    return (list || []).map((e, i) => `<div class="secret-row" data-env-row="${prefix}">
      <input class="input" name="env-name" value="${esc(e.name || "")}" placeholder="NAME">
      <input class="input" name="env-value" type="password" value="" placeholder="${e.hasValue ? "•••••••• (saved)" : "value"}" ${e.hasValue ? 'data-has-value="1"' : ""}>
      <button class="icon-btn sm" data-action="remove-env" data-prefix="${prefix}" title="Remove">${icon("x", "sm")}</button>
    </div>`).join("");
  }

  function agentFormHTML() {
    const f = S.agentForm;
    const a = f.agent || {};
    return `<div class="section-head"><div><h3>${f.id ? "Edit agent" : "Add ACP agent"}</h3><div class="settings-desc" style="margin:3px 0 0">A command that speaks the Agent Client Protocol over stdio.</div></div><button class="btn btn-ghost btn-sm" data-action="cancel-agent">Cancel</button></div>
      <form data-form="agent" data-id="${esc(f.id || "")}">
        <div class="form-grid">
          <div class="field"><label>Name</label><input class="input" name="name" value="${esc(a.name || "")}" placeholder="Devin" required></div>
          <div class="field"><label>Slug</label><input class="input" name="slug" value="${esc(a.slug || "")}" placeholder="devin" required></div>
          <div class="field full"><label>Command</label><input class="input" name="command" value="${esc(a.command || "")}" placeholder="/usr/local/bin/devin" required></div>
          <div class="field full"><label>Arguments (one per line)</label><textarea class="textarea" name="args" rows="3" placeholder="acp">${esc((a.args || []).join("\n"))}</textarea></div>
          <div class="field full"><label>Working directory</label><input class="input" name="cwd" value="${esc(a.cwd || ((S.bootstrap?.app && S.bootstrap.app.dataDir) || "") + "/workspace")}" placeholder="/home/me/.nusamation/workspace" required></div>
          <div class="field full"><label>Description</label><input class="input" name="description" value="${esc(a.description || "")}" placeholder="What this agent is good at"></div>
          <div class="field"><label>Icon</label><select class="select" name="icon">${["sparkles", "zap", "brain", "terminal", "globe", "star"].map((i) => `<option value="${i}" ${a.icon === i ? "selected" : ""}>${i}</option>`).join("")}</select></div>
          <div class="field"><label>Color</label><select class="select" name="color">${Object.keys(AGENT_COLORS).map((c) => `<option value="${c}" ${a.color === c ? "selected" : ""}>${c}</option>`).join("")}</select></div>
          <div class="field"><label>Approval policy</label><select class="select" name="approvalPolicy">${["ask", "auto_safe", "auto_all"].map((p) => `<option value="${p}" ${a.approvalPolicy === p ? "selected" : ""}>${p.replace("_", " ")}</option>`).join("")}</select></div>
          <div class="field full"><label class="checkbox"><input type="checkbox" name="enabled" ${a.enabled !== false ? "checked" : ""}> Enabled</label></div>
        </div>
        <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Environment secrets <span class="badge-soft gray">AES-GCM at rest</span><span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="agent">+ Add variable</button></div>
          <div id="agent-env-list">${envRowsHTML(a.env || [], "agent")}</div>
        </div>
        <div class="form-actions"><button class="btn btn-secondary" type="button" data-action="cancel-agent">Cancel</button><button class="btn btn-primary" type="submit">${f.id ? "Save agent" : "Add agent"}</button></div>
      </form>`;
  }

  function mcpSettings() {
    if (S.mcpForm) return mcpFormHTML();
    const agents = S.agents;
    const groups = agents.map((a) => {
      const list = S.mcp[a.id] || [];
      return `<div class="settings-section">
        <div class="section-head"><div><h3>${esc(a.name)}</h3><div class="settings-desc" style="margin:2px 0 0">${list.length} server${list.length === 1 ? "" : "s"}</div></div><button class="btn btn-ghost btn-sm" data-action="add-mcp" data-agent="${esc(a.id)}">${icon("plus", "sm")} Add</button></div>
        ${list.length ? list.map((m) => `<div class="mcp-card"><span class="tool-icon kind-execute">${icon(m.transport === "stdio" ? "terminal" : "globe")}</span><div class="grow" style="min-width:0"><div class="mcp-name">${esc(m.name)} <span class="status-dot ${m.enabled ? "" : "warn"}"></span></div><div class="mcp-sub">${esc(m.transport)} · ${esc(m.transport === "stdio" ? m.command : m.url)} ${esc((m.args || []).join(" "))}</div></div><div class="mcp-actions"><button class="icon-btn sm" data-action="edit-mcp" data-id="${esc(m.id)}" title="Edit">${icon("edit", "sm")}</button><button class="icon-btn sm" data-action="delete-mcp" data-id="${esc(m.id)}" title="Delete">${icon("trash", "sm")}</button></div></div>`).join("") : `<div class="empty-settings" style="padding:16px">No MCP servers. Add stdio, HTTP or SSE tools.</div>`}
      </div>`;
    }).join("");
    return `${settingsHead("MCP servers", "Tools are attached per agent. Changes restart the agent runtime on the next turn.")}${groups || `<div class="empty-settings">Add an agent first.</div>`}`;
  }

  function mcpFormHTML() {
    const f = S.mcpForm;
    const m = f.server || {};
    return `<div class="section-head"><div><h3>${f.id ? "Edit MCP server" : "Add MCP server"}</h3><div class="settings-desc" style="margin:3px 0 0">Stdio runs a local command; HTTP/SSE connects to a URL.</div></div><button class="btn btn-ghost btn-sm" data-action="cancel-mcp">Cancel</button></div>
      <form data-form="mcp" data-id="${esc(f.id || "")}">
        <div class="form-grid">
          <div class="field full"><label>Agent</label><select class="select" name="agentId">${S.agents.map((a) => `<option value="${esc(a.id)}" ${((m.agentId || f.agentId) === a.id) ? "selected" : ""}>${esc(a.name)}</option>`).join("")}</select></div>
          <div class="field"><label>Name</label><input class="input" name="name" value="${esc(m.name || "")}" placeholder="filesystem" required></div>
          <div class="field"><label>Transport</label><select class="select" name="transport"><option value="stdio" ${m.transport !== "http" && m.transport !== "sse" ? "selected" : ""}>stdio</option><option value="http" ${m.transport === "http" ? "selected" : ""}>HTTP</option><option value="sse" ${m.transport === "sse" ? "selected" : ""}>SSE</option></select></div>
          <div class="field full"><label>Command <span class="muted">(stdio)</span></label><input class="input" name="command" value="${esc(m.command || "")}" placeholder="npx"></div>
          <div class="field full"><label>Arguments (one per line)</label><textarea class="textarea" name="args" rows="2">${esc((m.args || []).join("\n"))}</textarea></div>
          <div class="field full"><label>URL <span class="muted">(HTTP/SSE)</span></label><input class="input" name="url" value="${esc(m.url || "")}" placeholder="https://mcp.example.com/mcp"></div>
          <div class="field full"><label class="checkbox"><input type="checkbox" name="enabled" ${m.enabled !== false ? "checked" : ""}> Enabled</label></div>
        </div>
        <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Environment <span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="mcp">+ Add variable</button></div><div id="mcp-env-list">${envRowsHTML(m.env || [], "mcp")}</div></div>
        <div class="settings-section"><div class="settings-title" style="display:flex;align-items:center;gap:8px">Headers <span class="grow"></span><button class="btn btn-ghost btn-xs" type="button" data-action="add-env" data-prefix="mcp-header">+ Add header</button></div><div id="mcp-header-list">${envRowsHTML(m.headers || [], "mcp-header")}</div></div>
        <div class="form-actions"><button class="btn btn-secondary" type="button" data-action="cancel-mcp">Cancel</button><button class="btn btn-primary" type="submit">${f.id ? "Save server" : "Add server"}</button></div>
      </form>`;
  }

  function channelsSettings() {
    return `${settingsHead("Channels", "Web always sees everything. External channels receive a concise final answer and approval pings.")}
      <div class="agent-list" id="channel-cards">${S.channels.map(channelCard).join("")}</div>
      <div id="wa-panel" style="margin-top:20px">${whatsappPanel()}</div>`;
  }

  function channelCard(ch) {
    const statusClass = ch.loggedIn ? "" : ch.state === "error" ? "offline" : "warn";
    return `<div class="agent-card"><span class="tool-icon kind-execute" style="width:34px;height:34px">${ch.name === "whatsapp" ? icon("message") : icon("globe")}</span><div class="grow"><div class="agent-card-name">${esc(ch.label || ch.name)} <span class="status-dot ${statusClass}"></span></div><div class="agent-card-sub">${esc(ch.state || "unknown")}${ch.detail ? " · " + esc(ch.detail) : ""}</div></div>${ch.selfID ? `<span class="badge-soft green">${esc(ch.selfID)}</span>` : ""}</div>`;
  }

  function whatsappPanel() {
    const ch = S.whatsapp || S.channels.find((c) => c.name === "whatsapp");
    if (!ch) {
      return `<div class="empty-settings">WhatsApp is disabled for this process.<br>Remove <code class="mono">NUSAMATION_NO_WHATSAPP</code> and restart to enable it.</div>`;
    }
    const settings = S.whatsappSettings || { enabled: ch.enabled, autoStart: true, groupPolicy: "mention", readReceipts: true, sendTyping: true, streamEdits: true, allowedNumbers: [] };
    const paired = ch.stats && ch.stats.paired;
    const pairing = ch.state === "pairing";
    const connecting = ch.state === "connecting";
    const qr = pairing || connecting;
    const statusBadge = ch.loggedIn ? `<span class="badge-soft green">connected</span>` : pairing ? `<span class="badge-soft amber">pairing</span>` : connecting ? `<span class="badge-soft amber">connecting</span>` : ch.state === "error" ? `<span class="badge-soft red">error</span>` : `<span class="badge-soft gray">${esc(ch.state || "offline")}</span>`;
    return `<div class="settings-title" style="display:flex;align-items:center;gap:8px">WhatsApp ${statusBadge}<span class="grow"></span>
      ${pairing || connecting ? `<button class="btn btn-ghost btn-xs" data-action="refresh-channels">${icon("refresh", "sm")} Refresh</button>` : ""}
      <button class="btn btn-secondary btn-xs" data-action="wa-connect" ${ch.connected ? "disabled" : ""}>Connect</button>
      <button class="btn btn-ghost btn-xs" data-action="wa-disconnect" ${!ch.connected && ch.state !== "pairing" ? "disabled" : ""}>Disconnect</button>
      <button class="btn btn-danger btn-xs" data-action="wa-logout" ${!paired ? "disabled" : ""}>Unlink</button></div>
      <div class="settings-desc" style="margin:6px 0 14px">${esc(ch.detail || (paired ? "Linked and ready." : "Link this device to let NusaMation answer WhatsApp messages."))}</div>
      ${qr ? (pairing ? `<div class="qr-box"><img class="qr-img" src="/api/channels/whatsapp/qr?t=${Date.now()}" alt="WhatsApp pairing QR"><div class="qr-info"><h4>Scan this code</h4><p>Open WhatsApp on your phone → Linked devices → Link a device. The QR refreshes automatically.</p><div class="muted" style="font-size:10.5px">The web control center always receives the full stream. WhatsApp receives only the final digest.</div></div></div>` : `<div class="qr-box"><div class="qr-placeholder"><span class="spinner" style="margin-bottom:8px"></span>Preparing a fresh QR code…</div><div class="qr-info"><h4>Connecting</h4><p>NusaMation is opening a secure WhatsApp web session. A QR code will appear in a moment.</p></div></div>`) : (ch.loggedIn || paired) ? `<div class="qr-box"><div class="tool-icon kind-execute" style="width:44px;height:44px">${icon("message")}</div><div class="grow"><h4 style="margin:0 0 4px">${ch.selfID ? esc(ch.selfID) : "WhatsApp linked"}</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Inbound messages are gatewayed through the routing layer. The agent's full thoughts stay on web.</p></div></div>` : `<div class="qr-box"><div class="tool-icon" style="width:44px;height:44px">${icon("message")}</div><div class="grow"><h4 style="margin:0 0 4px">Not linked</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Press Connect to generate a pairing QR. Already linked? Press Connect to resume.</p></div></div>`}
      <div class="settings-section" style="margin-top:20px">
        <div class="settings-title">Behaviour</div>
        <div class="settings-row"><div><div class="label">Enabled</div><div class="desc">Accept inbound messages and send replies.</div></div><button class="switch ${settings.enabled ? "on" : ""}" data-action="wa-toggle" data-key="enabled"></button></div>
        <div class="settings-row"><div><div class="label">Auto-connect on boot</div><div class="desc">Only when a device is already linked.</div></div><button class="switch ${settings.autoStart ? "on" : ""}" data-action="wa-toggle" data-key="autoStart"></button></div>
        <div class="settings-row"><div><div class="label">Read receipts</div><div class="desc">Mark allowed messages as read.</div></div><button class="switch ${settings.readReceipts ? "on" : ""}" data-action="wa-toggle" data-key="readReceipts"></button></div>
        <div class="settings-row"><div><div class="label">Typing indicator</div><div class="desc">Show composing while the agent works.</div></div><button class="switch ${settings.sendTyping ? "on" : ""}" data-action="wa-toggle" data-key="sendTyping"></button></div>
        <div class="settings-row"><div><div class="label">Progressive edits</div><div class="desc">Edit the “On it…” message as the final answer streams. Disable for strict clients.</div></div><button class="switch ${settings.streamEdits ? "on" : ""}" data-action="wa-toggle" data-key="streamEdits"></button></div>
        <div class="settings-row"><div><div class="label">Group policy</div><div class="desc">Mention requires a direct @mention or a reply to the bot.</div></div><select class="select" style="width:150px" data-change="wa-group"><option value="mention" ${settings.groupPolicy === "mention" ? "selected" : ""}>mention only</option><option value="all" ${settings.groupPolicy === "all" ? "selected" : ""}>all messages</option><option value="disabled" ${settings.groupPolicy === "disabled" ? "selected" : ""}>disabled</option></select></div>
      </div>
      <div class="settings-section">
        <div class="settings-title">Allowlist</div>
        <div class="settings-desc">One phone number or JID per line. Empty means anyone who can message this account. Groups need the group JID or a participant number.</div>
        <textarea class="textarea allowlist" id="wa-allowlist" placeholder="+628123456789&#10;1203630...@g.us">${esc((settings.allowedNumbers || []).join("\n"))}</textarea>
        <div class="form-actions" style="justify-content:flex-start"><button class="btn btn-secondary btn-sm" data-action="wa-save-allowlist">Save allowlist</button></div>
      </div>`;
  }

  function routingSettings() {
    const r = S.routingDefaults || (S.bootstrap && S.bootstrap.routingDefaults) || { replyToOrigin: true, mirrorFinal: false, notifyApprovals: true };
    return `${settingsHead("Routing defaults", "These apply to new conversations. Each conversation can override them from the top bar.")}
      <div class="settings-row"><div><div class="label">Reply to origin</div><div class="desc">Send the final digest back to the chat where the prompt came from.</div></div><button class="switch ${r.replyToOrigin ? "on" : ""}" data-action="default-routing" data-key="replyToOrigin"></button></div>
      <div class="settings-row"><div><div class="label">Mirror final answers</div><div class="desc">Copy final answers to every linked channel. Off by default to avoid noise.</div></div><button class="switch ${r.mirrorFinal ? "on" : ""}" data-action="default-routing" data-key="mirrorFinal"></button></div>
      <div class="settings-row"><div><div class="label">Notify approvals</div><div class="desc">Ping the origin chat when a tool needs a decision.</div></div><button class="switch ${r.notifyApprovals ? "on" : ""}" data-action="default-routing" data-key="notifyApprovals"></button></div>
      <div class="settings-section" style="margin-top:26px"><div class="settings-title">How it works</div><div class="settings-card" style="border:1px solid var(--border);border-radius:11px;padding:12px 14px;color:var(--muted);font-size:12px;line-height:1.7">
        <div><b style="color:var(--text-soft)">Web</b> receives every message, thought, tool call, plan, approval and usage update in real time.</div>
        <div><b style="color:var(--text-soft)">External channels</b> receive an “On it…” message and a final answer. Intermediate reasoning and raw tool output stay private.</div>
        <div><b style="color:var(--text-soft)">Approvals</b> are always required in web unless the agent policy or a remembered rule allows the call.</div>
      </div></div>`;
  }

  function accountSettings() {
    const app = (S.bootstrap && S.bootstrap.app) || {};
    return `${settingsHead("Account", "Single-admin local control center.")}
      <div class="about-grid">
        <div class="about-item"><div class="k">Version</div><div class="v">${esc(app.version || "0.1.0")}</div></div>
        <div class="about-item"><div class="k">Mode</div><div class="v">${app.dev ? "Development" : "Local-first"}</div></div>
        <div class="about-item"><div class="k">Public URL</div><div class="v">${esc(app.publicUrl || "not configured")}</div></div>
        <div class="about-item"><div class="k">WhatsApp</div><div class="v">${app.whatsappDisabled ? "disabled" : "available"}</div></div>
      </div>
      <div class="settings-section" style="margin-top:24px">
        <div class="settings-title">Change password</div>
        <form data-form="password" style="max-width:420px">
          <div class="field"><label>Current password</label><input class="input" type="password" name="current" autocomplete="current-password" required></div>
          <div class="field"><label>New password</label><input class="input" type="password" name="new" autocomplete="new-password" minlength="8" required></div>
          <button class="btn btn-secondary" type="submit">Update password</button>
        </form>
      </div>
      <div class="settings-section">
        <div class="settings-title">Session</div>
        <div class="settings-row"><div><div class="label">Sign out</div><div class="desc">End this browser session. Agents keep running.</div></div><button class="btn btn-secondary btn-sm" data-action="logout">Sign out</button></div>
      </div>
      <div class="settings-section">
        <div class="danger-zone"><div class="settings-title" style="color:var(--danger)">Danger zone</div><div class="settings-desc">Erase every conversation, message, approval and channel link. Agents, MCP servers and channel pairings are kept.</div><button class="btn btn-danger btn-sm" data-action="delete-all-conversations">Erase conversations</button></div>
      </div>`;
  }

  function startQRTimer() {
    if (S.qrTimer) clearInterval(S.qrTimer);
    const ch = S.whatsapp || S.channels.find((c) => c.name === "whatsapp");
    if (!ch || !["pairing", "connecting"].includes(ch.state)) return;
    S.qrTimer = setInterval(async () => {
      try {
        const d = await api("/channels");
        S.channels = d.channels || [];
        const wa = S.channels.find((c) => c.name === "whatsapp");
        if (wa) S.whatsapp = wa;
        updateWhatsAppPanel();
        if (!wa || !["pairing", "connecting"].includes(wa.state)) {
          clearInterval(S.qrTimer); S.qrTimer = null;
          if (wa?.connected) toast("WhatsApp connected", "The channel is live.", "success");
        }
      } catch {}
    }, 2600);
  }

  function updateWhatsAppPanel() {
    if (S.modal?.type !== "settings" || S.settingsTab !== "channels") return;
    const cards = $("#channel-cards");
    if (cards) cards.innerHTML = S.channels.map(channelCard).join("");
    const el = $("#wa-panel");
    if (!el) return;
    const active = document.activeElement;
    if (active && (active.id === "wa-allowlist")) return;
    el.innerHTML = whatsappPanel();
  }

  async function saveWaSettings(patch) {
    const current = S.whatsappSettings || { enabled: true, autoStart: true, groupPolicy: "mention", readReceipts: true, sendTyping: true, streamEdits: true, allowedNumbers: [] };
    const next = { ...current, ...patch };
    try {
      const d = await api("/channels/whatsapp/settings", { method: "PUT", body: next });
      S.whatsappSettings = d.settings;
      if (d.channel) {
        const i = S.channels.findIndex((c) => c.name === "whatsapp");
        if (i >= 0) S.channels[i] = d.channel; else S.channels.push(d.channel);
        S.whatsapp = d.channel;
      }
      updateWhatsAppPanel();
    } catch (e) {
      toast("Could not save WhatsApp settings", e.message, "error");
      renderSettings();
    }
  }

  async function waAction(action) {
    try {
      if (action === "connect") {
        await api("/channels/whatsapp/connect", { method: "POST" });
        toast("Connecting WhatsApp", "A QR code will appear in a moment if pairing is needed.", "info");
        S.whatsapp = { ...(S.whatsapp || {}), state: "connecting" };
        renderSettings();
        startQRTimer();
      } else if (action === "disconnect") {
        await api("/channels/whatsapp/disconnect", { method: "POST" });
        await refreshChannels();
        renderSettings();
      } else if (action === "logout") {
        confirmModal("Unlink WhatsApp?", "This removes the linked device from NusaMation. Your WhatsApp account is not deleted.", async () => {
          await api("/channels/whatsapp/logout", { method: "POST" });
          await refreshChannels();
          closeModal();
          openSettings("channels");
          toast("WhatsApp unlinked", "", "success");
        }, "Unlink");
      }
    } catch (e) {
      toast("WhatsApp action failed", e.message, "error");
      await refreshChannels();
    }
  }

  async function refreshChannels() {
    try {
      const d = await api("/channels");
      S.channels = d.channels || [];
      const wa = S.channels.find((c) => c.name === "whatsapp");
      if (wa) S.whatsapp = wa;
      renderSidebar();
      updateWhatsAppPanel();
      if (S.modal?.type === "settings" && S.settingsTab === "channels") {
        const cards = $("#channel-cards");
        if (cards) cards.innerHTML = S.channels.map(channelCard).join("");
      }
    } catch (e) { console.warn(e); }
  }

  async function openAgentLogs(id, fromSettings = false) {
    try {
      const d = await api(`/agents/${encodeURIComponent(id)}/logs`);
      const a = d.agent;
      const rt = d.runtime || {};
      S.modal = { type: "agentlogs", id };
      const methods = (a.authMethods || []).map((m) => `<button class="btn btn-secondary btn-sm" data-action="auth-agent" data-id="${esc(a.id)}" data-method="${esc(m.id)}" data-auth-type="${esc(m.type || "agent")}">${esc(m.name || m.id)}</button>`).join("");
      openModal(`${modalHead(`Runtime · ${a.name}`, "stderr ring buffer and live process state")}<div class="modal-body" style="padding:16px">
        <div class="about-grid" style="margin-bottom:14px"><div class="about-item"><div class="k">Status</div><div class="v">${esc(rt.status || a.status || "unknown")}</div></div><div class="about-item"><div class="k">PID</div><div class="v">${rt.pid || "—"}</div></div><div class="about-item"><div class="k">Uptime</div><div class="v">${rt.uptimeSeconds ? rt.uptimeSeconds + "s" : "—"}</div></div><div class="about-item"><div class="k">Last probe</div><div class="v">${a.lastProbeAt ? timeAgo(a.lastProbeAt) + " ago" : "never"}</div></div></div>
        ${methods ? `<div class="settings-section"><div class="settings-title">Authentication</div><div style="display:flex;gap:7px;flex-wrap:wrap">${methods}</div><div class="settings-desc">Some agents open a browser or terminal login flow. Finish it, then probe again.</div></div>` : ""}
        <div class="tool-label">stderr</div><pre class="tool-pre" style="max-height:360px">${esc((rt.logs || []).join("\n") || "No stderr captured yet.")}</pre>
      </div><div class="modal-foot"><button class="btn btn-secondary" data-action="agent-logs" data-id="${esc(a.id)}">${icon("refresh", "sm")} Refresh</button><button class="btn btn-primary" data-action="close-modal">Close</button></div>`, { size: "md" });
    } catch (e) {
      toast("Could not load logs", e.message, "error");
    }
  }

  async function probeAgent(id) {
    toast("Probing agent", "Starting a temporary ACP session…", "info");
    try {
      await api(`/agents/${encodeURIComponent(id)}/probe`, { method: "POST" });
      const d = await api("/agents");
      S.agents = d.agents || S.agents;
      const m = await api("/mcp");
      S.mcp = m.servers || S.mcp;
      toast("Agent is ready", "Capabilities and model list refreshed.", "success");
      if (S.modal?.type === "settings") renderSettings();
      renderTopbar();
    } catch (e) {
      toast("Probe failed", e.message, "error");
    }
  }
  /* ---------------- actions ---------------- */
  async function newConversation() {
    ++S.loadSeq;
    ++S.draftPreviewSeq;
    S.prepareController?.abort();
    S.preparing = "";
    S.current = null;
    S.currentAgent = null;
    S.messages = [];
    S.turns = {};
    S.active = null;
    S.configs = [];
    S.draftSelections = [];
    S.welcomeAgent = (agentById(S.agentDefaults.agentId)?.enabled ? S.agentDefaults.agentId : "") || S.agents.find((a) => a.enabled)?.id || "";
    switchComposer("");
    S.scrollPinned = true;
    S.loadingConversation = false;
    setRoute("");
    renderSidebar(); renderTopbar(); renderChat(); renderComposer();
    closeMobileSidebar();
    focusComposer();
  }

  function openConversationMenu(anchor, id) {
    const c = S.conversations.find((x) => x.id === id);
    if (!c) return;
    openPopover(anchor, `<div class="popover-list">
      ${menuItem("pin", c.pinned ? "Unpin conversation" : "Pin conversation", "toggle-pin", `data-id="${esc(c.id)}"`)}
      ${menuItem("edit", "Rename", "rename-conv", `data-id="${esc(c.id)}"`)}
      ${menuItem("download", "Export JSON", "export-conv", `data-id="${esc(c.id)}"`)}
      ${menuItem("file", "Archive", "archive-conv", `data-id="${esc(c.id)}"`)}
      <div class="popover-sep"></div>
      ${menuItem("trash", "Delete", "delete-conv", `data-id="${esc(c.id)}"`, true)}
    </div>`, { width: 220 });
  }

  async function saveAgent(form) {
    const fd = new FormData(form);
    const id = form.dataset.id;
    const env = [];
    $$("#agent-env-list .secret-row", form).forEach((row) => {
      const name = row.querySelector("[name=env-name]").value.trim();
      const valueInput = row.querySelector("[name=env-value]");
      const value = valueInput.value;
      if (!name) return;
      if (value) env.push({ name, value });
      else if (valueInput.dataset.hasValue) env.push({ name, hasValue: true });
    });
    const body = {
      name: String(fd.get("name") || "").trim(),
      slug: String(fd.get("slug") || "").trim(),
      command: String(fd.get("command") || "").trim(),
      args: String(fd.get("args") || "").split("\n").map((s) => s.trim()).filter(Boolean),
      cwd: String(fd.get("cwd") || "").trim(),
      description: String(fd.get("description") || "").trim(),
      icon: String(fd.get("icon") || "sparkles"),
      color: String(fd.get("color") || "amber"),
      approvalPolicy: String(fd.get("approvalPolicy") || "ask"),
      defaultModel: S.agentForm?.agent?.defaultModel || "",
      enabled: !!fd.get("enabled"),
      env,
    };
    try {
      const d = await api(id ? `/agents/${encodeURIComponent(id)}` : "/agents", { method: id ? "PATCH" : "POST", body });
      const list = await api("/agents");
      S.agents = list.agents || S.agents;
      S.agentForm = null;
      renderSettings();
      renderTopbar();
      toast(id ? "Agent updated" : "Agent added", d.agent.name + " is ready.", "success");
    } catch (e) {
      toast("Could not save agent", e.message, "error", 8000);
    }
  }

  async function saveMCP(form) {
    const fd = new FormData(form);
    const id = form.dataset.id;
    const collect = (sel) => {
      const out = [];
      $$(sel + " .secret-row", form).forEach((row) => {
        const name = row.querySelector("[name=env-name]").value.trim();
        const valueInput = row.querySelector("[name=env-value]");
        if (!name) return;
        if (valueInput.value) out.push({ name, value: valueInput.value });
        else if (valueInput.dataset.hasValue) out.push({ name, hasValue: true });
      });
      return out;
    };
    const body = {
      agentId: String(fd.get("agentId") || ""),
      name: String(fd.get("name") || "").trim(),
      transport: String(fd.get("transport") || "stdio"),
      command: String(fd.get("command") || "").trim(),
      args: String(fd.get("args") || "").split("\n").map((s) => s.trim()).filter(Boolean),
      url: String(fd.get("url") || "").trim(),
      enabled: !!fd.get("enabled"),
      env: collect("#mcp-env-list"),
      headers: collect("#mcp-header-list"),
    };
    try {
      await api(id ? `/mcp/${encodeURIComponent(id)}` : "/mcp", { method: id ? "PATCH" : "POST", body });
      const m = await api("/mcp");
      S.mcp = m.servers || S.mcp;
      S.mcpForm = null;
      renderSettings();
      toast(id ? "MCP server saved" : "MCP server added", "", "success");
    } catch (e) {
      toast("Could not save MCP server", e.message, "error", 8000);
    }
  }

  async function savePassword(form) {
    const fd = new FormData(form);
    try {
      const d = await api("/auth/password", { method: "POST", body: { current: fd.get("current"), new: fd.get("new") } });
      S.csrf = d.csrf;
      toast("Password updated", "Other sessions were signed out.", "success");
      form.reset();
    } catch (e) {
      toast("Could not change password", e.message, "error");
    }
  }

  async function deleteAgent(id) {
    const a = agentById(id);
    confirmModal("Delete agent?", `Remove ${a?.name || "this agent"} from NusaMation. Its conversations must be deleted first.`, async () => {
      try {
        await api(`/agents/${encodeURIComponent(id)}`, { method: "DELETE" });
        const d = await api("/agents");
        S.agents = d.agents || [];
        if (S.agentForm?.id === id) S.agentForm = null;
        renderSettings();
        toast("Agent removed", "", "success");
      } catch (e) {
        toast("Could not remove agent", e.message, "error");
      }
    }, "Delete");
  }

  async function deleteMCP(id) {
    confirmModal("Delete MCP server?", "The agent will no longer see this tool after the next runtime restart.", async () => {
      try {
        await api(`/mcp/${encodeURIComponent(id)}`, { method: "DELETE" });
        const d = await api("/mcp");
        S.mcp = d.servers || {};
        renderSettings();
        toast("MCP server removed", "", "success");
      } catch (e) { toast("Could not remove server", e.message, "error"); }
    }, "Delete");
  }

  /* ---------------- command palette ---------------- */
  function openPalette() {
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

  function renderPalette() {
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

  function paletteKeys(e) {
    const items = $$(".palette-item", $("#palette-list"));
    if (e.key === "ArrowDown") { e.preventDefault(); S.paletteIndex = Math.min(items.length - 1, S.paletteIndex + 1); renderPalette(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); S.paletteIndex = Math.max(0, S.paletteIndex - 1); renderPalette(); }
    else if (e.key === "Enter") {
      e.preventDefault();
      const item = $$(".palette-item", $("#palette-list"))[S.paletteIndex];
      if (item) runPalette(item.dataset.kind, item.dataset.id);
    } else if (e.key === "Escape") closePalette();
  }

  function closePalette() { $("#modal-root").innerHTML = ""; }

  async function runPalette(kind, id) {
    closePalette();
    if (kind === "conversation") return selectConversation(id);
    if (id === "new") return newConversation();
    if (id === "settings") return openSettings("agents");
    if (id === "approvals") return renderApprovalsModal();
    if (id === "channels") return openSettings("channels");
  }

  /* ---------------- event delegation ---------------- */
  document.addEventListener("submit", (e) => {
    const form = e.target.closest("form[data-form]");
    if (!form) return;
    e.preventDefault();
    if (form.dataset.form === "login") doLogin(form);
    else if (form.dataset.form === "agent") saveAgent(form);
    else if (form.dataset.form === "automation") saveAutomation(form);
    else if (form.dataset.form === "mcp") saveMCP(form);
    else if (form.dataset.form === "password") savePassword(form);
    else if (form.dataset.form === "elicitation") submitElicitation(form);
  });

  document.addEventListener("input", (e) => {
    if (e.target.matches('[data-input="automation-cron"]')) {
      const form = e.target.closest("form"); $('[data-change="automation-preset"]', form).value = ""; $(".automation-builder", form).hidden = true;
    }
    if (e.target.matches("[data-input=conv-search]")) {
      S.query = e.target.value;
      filterSidebar();
    }
  });

  document.addEventListener("change", (e) => {
    if (e.target.matches("[data-automation-build]")) { updateAutomationBuilder(e.target.closest("form")); return; }
    const sel = e.target.closest("[data-change]");
    if (!sel) return;
    if (sel.dataset.change === "set-config") setConfig(sel.dataset.id, sel.value);
    if (sel.dataset.change === "wa-group") saveWaSettings({ groupPolicy: sel.value });
    if (sel.dataset.change === "automation-schedule") {
      const form = sel.closest("form");
      $("[data-automation-once]", form).hidden = sel.value !== "once";
      $("[data-automation-cron]", form).hidden = sel.value !== "cron";
    }
    if (sel.dataset.change === "automation-preset") updateAutomationBuilder(sel.closest("form"));
    if (sel.dataset.change === "default-agent") {
      S.defaultDraft = { agentId: sel.value, selections: [] };
      previewAgentDefaults();
    }
    if (sel.dataset.change === "default-choice" && sel.value) {
      const opt = S.defaultConfigs.find((o) => o.id === sel.dataset.id);
      S.defaultDraft.selections.push({ id: sel.dataset.id, value: sel.dataset.type === "boolean" ? sel.value === "true" : sel.value, name: opt?.name, label: sel.selectedOptions[0]?.textContent });
      previewAgentDefaults();
    }
  });

  document.addEventListener("click", async (e) => {
    const target = e.target.closest("[data-action]");
    if (!target) {
      if (!e.target.closest(".popover")) closePopover();
      return;
    }
    const action = target.dataset.action;
    const id = target.dataset.id || "";
    if (action === "select-conv" && !e.target.closest("[data-action=conv-menu]")) {
      e.preventDefault();
      await selectConversation(id);
      return;
    }
    if (action !== "conv-menu") closePopover();
    switch (action) {
      case "toast-close": target.closest(".toast")?.remove(); break;
      case "toggle-password": {
        const input = target.closest(".password-wrap")?.querySelector("input");
        if (input) { input.type = input.type === "password" ? "text" : "password"; target.classList.toggle("active"); }
        break;
      }
      case "new-chat": e.preventDefault(); newConversation(); break;
      case "open-sidebar": openMobileSidebar(); break;
      case "close-sidebar": closeMobileSidebar(); break;
      case "open-settings": openSettings(target.dataset.tab || ""); break;
      case "open-account": openSettings("account"); break;
      case "settings-tab":
        S.settingsTab = target.dataset.tab; S.agentForm = null; S.mcpForm = null;
        if (S.settingsTab === "automations") loadAutomations();
        else if (S.settingsTab === "defaults") {
          S.defaultDraft = { agentId: S.agentDefaults.agentId || "", selections: (S.agentDefaults.selections || []).map((s) => ({ ...s })) };
          previewAgentDefaults();
        } else renderSettings();
        break;
      case "default-undo": S.defaultDraft.selections.pop(); previewAgentDefaults(); break;
      case "default-retry": previewAgentDefaults(); break;
      case "save-agent-defaults": await saveAgentDefaults(); break;
      case "automations-list": await loadAutomations(); break;
      case "automation-add": S.automationJob = null; S.automationView = "form"; renderSettings(); break;
      case "automation-edit": S.automationJob = S.automations.find((a) => a.id === id); S.automationView = "form"; renderSettings(); break;
      case "automation-history": {
        S.automationLoading = true; renderSettings();
        try { const d = await api(`/automations/${encodeURIComponent(id)}`); S.automationJob = d.automation; S.automationRuns = d.runs || []; S.automationView = "history"; }
        catch (err) { S.automationError = err.message; }
        finally { S.automationLoading = false; if (S.modal?.type === "settings" && S.settingsTab === "automations") renderSettings(); }
        break;
      }
      case "automation-room": closeModal(); await selectConversation(id); break;
      case "automation-toggle": {
        const job = S.automations.find((a) => a.id === id);
        try { await api(`/automations/${encodeURIComponent(id)}/enabled`, { method: "PATCH", body: { enabled: !job.enabled } }); await loadAutomations(); }
        catch (err) { toast("Could not update automation", err.message, "error"); }
        break;
      }
      case "automation-delete": confirmModal("Delete automation?", "Its chat transcripts remain available.", async () => {
        try { await api(`/automations/${encodeURIComponent(id)}`, { method: "DELETE" }); openSettings("automations"); await loadAutomations(); }
        catch (err) { toast("Could not delete automation", err.message, "error"); }
      }, "Delete"); break;
      case "open-approvals": renderApprovalsModal(); break;
      case "close-modal": closeModal(); break;
      case "confirm-action": {
        const fn = S.confirmAction;
        if (fn) { try { await fn(); } finally { S.confirmAction = null; } }
        break;
      }
      case "prompt-save": {
        const val = $("#prompt-modal-input")?.value || "";
        const fn = S.promptAction;
        if (fn) { await fn(val); S.promptAction = null; }
        break;
      }
      case "conv-menu": e.preventDefault(); openConversationMenu(target, id); break;
      case "agent-menu": openAgentMenu(target); break;
      case "config-menu": openConfigMenu(target); break;
      case "prepare-composer": await prepareComposer(); break;
      case "show-agent-request": showPendingRequest(); break;
      case "composer-config": openComposerConfig(target, id); break;
      case "composer-settings": openComposerSettings(target); break;
      case "composer-mobile-settings": openComposerSettings(target, true); break;
      case "composer-subconfig": openComposerConfig(isMobile() ? $(".composer-mobile-settings") : $(".composer-settings"), id, true); break;
      case "composer-settings-back": openComposerSettings(isMobile() ? $(".composer-mobile-settings") : $(".composer-settings"), isMobile()); break;
      case "pick-config": await setConfig(id, target.dataset.type === "boolean" ? target.dataset.value === "true" : target.dataset.value); break;
      case "retry-prepare": closeModal(); await prepareComposer(); break;
      case "stop-preparing": S.prepareController?.abort(); closeModal(); break;
      case "stop-slow-turn": closeModal(); await stopTurn(); break;
      case "elicitation-decline": {
        if (S.modal?.type === "elicitation") respondElicitation(S.modal.conversationId, S.modal.id, { action: "decline" });
        break;
      }
      case "blocker-auth": {
        if (target.dataset.authType === "terminal") { openAuthTerminal(id, target.dataset.method); break; }
        try {
          await api(`/agents/${encodeURIComponent(id)}/authenticate`, { method: "POST", body: { methodId: target.dataset.method } });
          closeModal();
          await prepareComposer();
        } catch (err) { showAgentBlocker(err); }
        break;
      }
      case "routing-menu": openRoutingMenu(target); break;
      case "more-menu": openMoreMenu(target); break;
      case "toggle-pin": await togglePin(id); closePopover(); break;
      case "rename-conv": renameConversation(id); break;
      case "archive-conv": await archiveConversation(id); break;
      case "delete-conv": await deleteConversation(id); break;
      case "export-conv": exportConversation(id); break;
      case "pick-agent": await pickAgent(id); break;
      case "set-config-toggle": await setConfig(target.dataset.id, target.dataset.value === "true"); break;
      case "toggle-routing": await toggleRouting(target.dataset.key); break;
      case "suggestion": {
        const ta = $("#prompt");
        if (ta) { ta.value = target.dataset.prompt || ""; autoGrow(); ta.focus(); }
        break;
      }
      case "send": await sendMessage(); break;
      case "send-menu": openSendMenu(target); break;
      case "send-mode": setSendMode(target.dataset.mode); break;
      case "stop": await stopTurn(); break;
      case "retry": await retryTurn(); break;
      case "retry-boot": startApp(); break;
      case "retry-load": await loadConversation(id, false); break;
      case "attach": $("#file-input")?.click(); break;
      case "reply-toggle": S.replyToChannel = !S.replyToChannel; renderComposer(); focusComposer(); break;
      case "remove-attachment": S.attachments = S.attachments.filter((a) => (a.upload || a).id !== id); renderPendingAttachments(); break;
      case "convert": break;
      case "resolve-approval": await resolveApproval(id, target.dataset.option, target.closest(".approval-card")); break;
      case "toggle-thought": {
        const mid = target.dataset.id;
        if (S.openThoughts.has(mid)) S.openThoughts.delete(mid); else S.openThoughts.add(mid);
        saveActivityState();
        S.scrollPinned = false;
        renderChat();
        break;
      }
      case "toggle-activity": {
        const expanded = target.getAttribute("aria-expanded") === "true";
        S.activityExpanded.set(target.dataset.id, !expanded);
        saveActivityState();
        S.scrollPinned = false;
        renderChat();
        break;
      }
      case "toggle-tool": {
        const mid = target.dataset.id;
        if (S.openTools.has(mid)) S.openTools.delete(mid); else S.openTools.add(mid);
        saveActivityState();
        S.scrollPinned = false;
        renderChat();
        break;
      }
      case "tool-menu": openToolMenu(target); break;
      case "tool-wrap": {
        const pre = toolPreFor(target);
        if (!pre) break;
        const unwrapped = pre.classList.toggle("no-wrap");
        if (unwrapped) S.unwrappedTools.add(pre.dataset.scrollKey); else S.unwrappedTools.delete(pre.dataset.scrollKey);
        const button = pre.closest(".tool-section")?.querySelector('.tool-actions [data-action="tool-wrap"]');
        if (button) button.textContent = unwrapped ? "Wrap" : "No wrap";
        saveActivityState();
        closePopover();
        break;
      }
      case "tool-start":
      case "tool-end": {
        const pre = toolPreFor(target);
        closePopover();
        if (pre) {
          const end = action === "tool-end";
          pre.scrollTop = end ? pre.scrollHeight : 0;
          const text = pre.querySelector("code")?.textContent || "";
          const firstNewline = text.indexOf("\n");
          const multipleLines = firstNewline >= 0 && firstNewline < text.length - 1;
          pre.scrollLeft = end && !multipleLines ? pre.scrollWidth : 0;
          pre.scrollIntoView({ block: "nearest", inline: "nearest" });
        }
        break;
      }
      case "copy-tool": {
        const code = toolPreFor(target)?.querySelector("code");
        closePopover();
        if (!code) break;
        try { await navigator.clipboard.writeText(code.textContent); toast("Copied", "", "success", 1500); } catch { toast("Copy failed", "", "error"); }
        break;
      }
      case "copy-code": {
        const pre = target.closest("pre");
        const code = pre?.querySelector("code")?.innerText || pre?.innerText || "";
        try { await navigator.clipboard.writeText(code.replace(/\nCopy$/, "")); toast("Copied", "", "success", 1500); } catch { toast("Copy failed", "", "error"); }
        break;
      }
      case "add-agent": S.agentForm = { agent: { enabled: true, approvalPolicy: "ask", icon: "sparkles", color: "amber" } }; S.mcpForm = null; S.settingsTab = "agents"; renderSettings(); break;
      case "edit-agent": {
        const a = agentById(id);
        if (a) { S.agentForm = { id: a.id, agent: a }; S.mcpForm = null; S.settingsTab = "agents"; renderSettings(); }
        break;
      }
      case "cancel-agent": S.agentForm = null; renderSettings(); break;
      case "delete-agent": deleteAgent(id); break;
      case "cancel-mcp": S.mcpForm = null; renderSettings(); break;
      case "add-mcp": S.mcpForm = { agentId: target.dataset.agent || (S.agents[0] || {}).id, server: { enabled: true, transport: "stdio" } }; S.agentForm = null; S.settingsTab = "mcp"; renderSettings(); break;
      case "edit-mcp": {
        const m = Object.values(S.mcp).flat().find((x) => x.id === id);
        if (m) { S.mcpForm = { id: m.id, agentId: m.agentId, server: m }; S.agentForm = null; S.settingsTab = "mcp"; renderSettings(); }
        break;
      }
      case "delete-mcp": deleteMCP(id); break;
      case "probe-agent": probeAgent(id); break;
      case "agent-logs": openAgentLogs(id); break;
      case "auth-agent": {
        if (target.dataset.authType === "terminal") { openAuthTerminal(id, target.dataset.method); break; }
        try {
          await api(`/agents/${encodeURIComponent(id)}/authenticate`, { method: "POST", body: { methodId: target.dataset.method } });
          toast("Authentication started", "Finish the browser or terminal flow, then probe again.", "info");
        } catch (err) { toast("Authentication failed", err.message, "error"); }
        break;
      }
      case "add-env": {
        const prefix = target.dataset.prefix || "agent";
        const host = prefix === "agent" ? $("#agent-env-list") : prefix === "mcp" ? $("#mcp-env-list") : $("#mcp-header-list");
        if (host) host.insertAdjacentHTML("beforeend", envRowsHTML([{ name: "", hasValue: false }], prefix));
        break;
      }
      case "remove-env": target.closest(".secret-row")?.remove(); break;
      case "default-routing": {
        const r = { ...(S.routingDefaults || S.bootstrap?.routingDefaults || {}) };
        r[target.dataset.key] = !r[target.dataset.key];
        S.routingDefaults = r;
        try { await api("/settings", { method: "PUT", body: { routing: r } }); } catch (err) { toast("Could not save routing", err.message, "error"); }
        renderSettings();
        break;
      }
      case "wa-connect": await waAction("connect"); break;
      case "wa-disconnect": await waAction("disconnect"); break;
      case "wa-logout": await waAction("logout"); break;
      case "wa-toggle": {
        const key = target.dataset.key;
        const cur = S.whatsappSettings || {};
        await saveWaSettings({ [key]: !cur[key] });
        break;
      }
      case "wa-save-allowlist": {
        const raw = $("#wa-allowlist")?.value || "";
        const allowedNumbers = raw.split(/\n|,/).map((s) => s.trim()).filter(Boolean);
        await saveWaSettings({ allowedNumbers });
        toast("Allowlist saved", allowedNumbers.length ? `${allowedNumbers.length} entries.` : "Empty allowlist — anyone can reach the agent.", "success");
        break;
      }
      case "refresh-channels": await refreshChannels(); break;
      case "logout": doLogout(); break;
      case "delete-all-conversations": {
        promptModal("Erase all conversations?", 'Type DELETE to confirm. This cannot be undone.', "", async (v) => {
          if (v !== "DELETE") { toast("Not erased", 'Type DELETE exactly.', "error"); return; }
          try {
            await api("/conversations", { method: "DELETE", body: { confirm: "DELETE" } });
            S.conversations = []; S.current = null; S.messages = []; S.approvals = []; S.approvalsById.clear();
            closeModal(); setRoute(""); renderSidebar(); renderChat(); renderTopbar(); renderComposer();
            toast("Conversations erased", "", "success");
          } catch (err) { toast("Could not erase", err.message, "error"); }
        }, "DELETE");
        break;
      }
      case "palette-run": await runPalette(target.dataset.kind, target.dataset.id); break;
      default: break;
    }
  });

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      if ($("#modal-root").innerHTML) closeModal();
      openPalette();
      return;
    }
    if (e.key === "Escape") {
      if ($("#modal-root").innerHTML) { closeModal(); return; }
      if ($(".popover")) { closePopover(); return; }
      closeMobileSidebar();
      return;
    }
    if (e.target?.id === "prompt-modal-input" && e.key === "Enter") {
      e.preventDefault();
      $("[data-action=prompt-save]")?.click();
    }
  });

  window.addEventListener("online", () => { closeWS(); S.reconnectAttempt = Math.max(1, S.reconnectAttempt); connectWS(); renderOffline(); });
  const syncAppHeight = () => {
    const vv = window.visualViewport;
    document.documentElement.style.setProperty("--app-h", Math.round(vv && vv.scale === 1 ? vv.height : window.innerHeight) + "px");
  };
  window.addEventListener("resize", () => {
    syncAppHeight();
    const searchPopover = isMobile() && document.activeElement?.matches(".composer-search") ? $(".composer-popover") : null;
    if (searchPopover) searchPopover._reposition?.(); else closePopover();
    autoGrow();
    const box = $("#auth-terminal");
    const live = S.authTerminal;
    if (box && live) {
      const cols = Math.max(25, Math.floor((box.clientWidth - 20) / 8));
      live.terminal.resize(cols, live.terminal.rows);
      if (live.socket.readyState === WebSocket.OPEN) live.socket.send(JSON.stringify({ type: "resize", cols, rows: live.terminal.rows }));
    }
  });
  window.visualViewport?.addEventListener("resize", () => {
    syncAppHeight();
    autoGrow();
    if (isMobile() && document.activeElement?.matches(".composer-search")) $(".composer-popover")?._reposition?.();
  });
  window.visualViewport?.addEventListener("scroll", syncAppHeight);
  window.addEventListener("offline", () => { S.wsState = "offline"; renderOffline(); renderSidebar(); });
  window.addEventListener("popstate", () => {
    const m = location.pathname.match(/^\/c\/([^/]+)/);
    if (m) selectConversation(decodeURIComponent(m[1]), false);
    else { ++S.loadSeq; S.loadingConversation = false; S.current = null; switchComposer(""); S.messages = []; S.turns = {}; renderTopbar(); renderChat(); renderComposer(); }
  });

  async function doLogout() {
    try { await api("/auth/logout", { method: "POST" }); } catch {}
    closeModal(); closeWS();
    S.csrf = ""; S.current = null; S.messages = []; S.conversations = []; S.approvals = []; S.approvalsById.clear();
    composers.clear(); switchComposer("");
    S.sendDelivery = "queue"; S.turns = {};
    renderLogin();
  }

  syncAppHeight();
  boot();
})();
