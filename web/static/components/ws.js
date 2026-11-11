import { agentById, parseConfigs, parseJSON } from "./util.js";
import { S, composers, composerKey, switchComposer } from "./state.js";
import { ApiError } from "./api.js";
import { toast } from "./toast.js";
import { closeModal } from "./modal.js";
import { applyStreamDelta, currentApprovals, renderChat, scheduleChatRender, scheduleStreamRender, scheduleStreamResync } from "./chat.js";
import { refreshApprovalModal, renderApprovalsModal, showAgentBlocker, showPendingRequest, showSlowAgent } from "./requests.js";
import { renderCommands, renderComposer, upsertTurn } from "./composer.js";
import { loadConversation, normalizeMessage, setRoute, touchConversationFromMessage, upsertConversation, upsertMessage } from "./conversations.js";
import { renderOffline } from "./shell.js";
import { renderSidebar } from "./sidebar.js";
import { upsertProject } from "./projects.js";
import { renderTopbar } from "./topbar.js";
import { renderSettings } from "./settings.js";
import { updateWhatsAppPanel } from "./whatsapp.js";
import { refreshBootstrap } from "./boot.js";
import { showAgentBrowser } from "./browser.js";

/* ---------------- live stream ---------------- */
export function connectWS() {
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

export function closeWS() {
  clearTimeout(S.reconnectTimer);
  S.reconnectTimer = null;
  if (S.ws) {
    const ws = S.ws;
    S.ws = null;
    try { ws.close(); } catch {}
  }
  S.wsState = "offline";
}

export function scheduleReconnect() {
  if (S.reconnectTimer || !S.booted) return;
  S.reconnectAttempt = Math.min(S.reconnectAttempt + 1, 8);
  const delay = Math.min(15000, 700 * Math.pow(1.7, S.reconnectAttempt));
  S.reconnectTimer = setTimeout(() => {
    S.reconnectTimer = null;
    connectWS();
  }, delay);
}

export function handleEvent(ev) {
  const type = ev.type;
  const data = ev.data || {};
  const convId = ev.conversationId || data.conversationId || data.conversation?.id;
  switch (type) {
    case "hello":
      S.wsState = "online";
      renderOffline();
      // The server restarted into a new version (self-update): pull fresh
      // assets so the UI matches the binary.
      if (data.version && S.appVersion && data.version !== S.appVersion) { location.reload(); break; }
      S.appVersion = data.version || S.appVersion;
      break;
    case "update.status": {
      S.updateInfo = data;
      if (S.modal?.type === "settings" && S.settingsTab === "about") renderSettings();
      if (data.stage === "restarting") toast("Restarting to finish update", "This page reconnects and refreshes automatically.", "info", 15000);
      break;
    }
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
    case "project.updated": {
      const project = data.project || data;
      if (!project?.id) break;
      upsertProject(project);
      renderSidebar();
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
      if (S.loadingConversation) { if (data.conversationId === S.loadingId) S.loadingDeltas.push(data); break; }
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
      const result = applyStreamDelta(m, data);
      if (result === "gap") { scheduleStreamResync(data.conversationId); break; }
      if (result === "duplicate") break;
      touchConversationFromMessage(m);
      scheduleStreamRender(m.id);
      break;
    }
    case "message.updated": {
      const m = normalizeMessage(data.message || data);
      if (!m.id) break;
      touchConversationFromMessage(m);
      if (S.current && m.conversationId === S.current.id) {
        const previous = S.messages.find((item) => item.id === m.id);
        upsertMessage(m);
        if (previous && previous.kind === m.kind && previous.seq === m.seq && previous.turnId === m.turnId && m.role === "assistant" && ["text", "thought"].includes(m.kind) && !m.attachments) scheduleStreamRender(m.id);
        else scheduleChatRender();
      }
      break;
    }
    case "message.deleted": {
      const mid = data.id;
      if (!mid || !S.current || convId !== S.current.id) break;
      const i = S.messages.findIndex((x) => x.id === mid);
      if (i >= 0) { S.messages.splice(i, 1); scheduleChatRender(); }
      break;
    }
    case "tool.created":
    case "tool.updated": {
      const m = normalizeMessage(data.message || data);
      if (!m.id) break;
      if (S.current && m.conversationId === S.current.id) {
        const previous = S.messages.find((item) => item.id === m.id);
        upsertMessage(m);
        if (type === "tool.updated" && previous && previous.kind === "tool" && previous.seq === m.seq && previous.turnId === m.turnId) scheduleStreamRender(m.id);
        else scheduleChatRender();
      }
      break;
    }
    case "agent.session": {
      if (S.current && convId === S.current.id && (!data.agentId || S.current.agentId === data.agentId)) {
        S.current.acpSessionId = data.acpSessionId;
        if (data.configOptions) { S.configs = parseConfigs(data.configOptions); S.current.configOptions = data.configOptions; }
        renderTopbar(); renderComposer();
      }
      break;
    }
    case "commands.updated": {
      if (!S.current || composerKey !== convId || S.current.id !== convId || S.current.acpSessionId !== data.sessionId) break;
      const meta = parseJSON(S.current.metadata, {}) || {};
      S.current.metadata = { ...meta, commands: data.commands, commandsSessionId: data.sessionId };
      renderCommands();
      break;
    }
    case "config.updated": {
      if (!S.current || convId !== S.current.id) break;
      if (data.configOptions) { S.configs = parseConfigs(data.configOptions); S.current.configOptions = data.configOptions; }
      if (data.model) S.current.model = data.model;
      renderTopbar(); renderComposer();
      if (S.modal?.type === "approval") refreshApprovalModal();
      if (currentApprovals().length) scheduleChatRender();
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
      else if (data.turnId && S.active.turnId !== data.turnId) break;
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
        if (S.modal?.type === "settings" && S.settingsTab === "agents" && !S.agentForm && !S.mcpForm) renderSettings();
      }
      break;
    }
    case "browser.drive": {
      // composerKey is the room on screen; S.current can hold a stale detail
      // object (welcome screen, mid-load) that does not match what is shown.
      if (!convId || composerKey !== convId) break;
      const pointer = Number.isFinite(data.x) && Number.isFinite(data.y) ? { x: data.x, y: data.y, kind: data.kind || "move" } : null;
      showAgentBrowser(data.sessionId, pointer, data.op || data.tool, convId);
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
