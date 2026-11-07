import { $, agentById, agentName } from "./util.js?v=33";
import { S } from "./state.js?v=33";
import { toast } from "./toast.js?v=33";
import { closeModal, modalHead, openModal } from "./modal.js?v=33";
import { prepareComposer } from "./composer.js?v=33";

export let xtermLoading = null;

export function loadXterm() {
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

export function openAuthTerminal(agentId, methodId) {
  closeModal();
  const agent = agentById(agentId);
  const conversationId = S.current?.id;
  S.modal = { type: "auth-terminal", agentId };
  openModal(`${modalHead("Sign in to " + agentName(agent), "Follow the agent's login instructions below.")}<div class="modal-body" style="padding:16px"><div id="auth-terminal" class="auth-terminal"></div><div id="auth-terminal-status" class="auth-terminal-status">Connecting…</div></div><div class="modal-foot"><button class="btn btn-secondary" data-action="close-modal">Cancel</button></div>`, { size: "md" });
  loadXterm().then(() => startAuthTerminal(agentId, methodId, conversationId), () => { const status = $("#auth-terminal-status"); if (status) status.textContent = "Terminal could not be loaded."; });
}

export function startAuthTerminal(agentId, methodId, conversationId) {
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
