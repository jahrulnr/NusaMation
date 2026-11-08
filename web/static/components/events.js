import { $, agentById, isMobile, parseJSON } from "./util.js?v=45";
import { S, composerFor, composerKey, saveActivityState, setSelectedProject, switchComposer } from "./state.js?v=45";
import { api } from "./api.js?v=45";
import { toast } from "./toast.js?v=45";
import { closeModal, confirmModal, promptModal } from "./modal.js?v=45";
import { closePopover } from "./popover.js?v=45";
import { openToolMenu, previewAnnotation, renderChat, toolPreFor } from "./chat.js?v=45";
import { renderApprovalsModal, resolveApproval, respondElicitation, showAgentBlocker, showPendingRequest, submitElicitation } from "./requests.js?v=45";
import { autoGrow, editAnnotationComment, focusComposer, openAgentMenu, openComposerConfig, openComposerSettings, openConfigMenu, openSendMenu, pickAgent, prepareComposer, renderComposer, renderPendingAttachments, retryTurn, sendMessage, setConfig, setSendMode, stopTurn } from "./composer.js?v=45";
import { browserDeviceMode, browserEscape, browserHistory, browserReconnect, cancelAnnotation, closeBrowserPane, submitAnnotation, toggleAnnotate, toggleBrowserPane, toggleDeviceBar } from "./browser.js?v=45";
import { archiveConversation, deleteConversation, exportConversation, loadConversation, loadOlderMessages, newConversation, newProjectChat, openConversationMenu, openMoreMenu, openRoutingMenu, renameConversation, selectConversation, setRoute, togglePin, toggleRouting } from "./conversations.js?v=45";
import { renderOffline } from "./shell.js?v=45";
import { scheduleDraftSave } from "./convmeta.js?v=45";
import { closeMobileSidebar, filterSidebar, openMobileSidebar, renderSidebar, toggleProjectRow, toggleSidebarCollapsed } from "./sidebar.js?v=45";
import { addDraftSource, confirmAddedFolders, deleteProject, loadFolderBrowser, makeDraftPrimary, noteProjectName, openFolderBrowser, openProjectEditor, openProjectMenu, openProjectMenuAt, removeDraftSource, saveProjectEditor, toggleProjectPin } from "./projects.js?v=45";
import { renderTopbar } from "./topbar.js?v=45";
import { closeWS, connectWS } from "./ws.js?v=45";
import { openAuthTerminal } from "./terminal.js?v=45";
import { applyUpdate, checkUpdate, deleteAgent, deleteMCP, envRowsHTML, loadUpdateInfo, openAgentLogs, openSettings, previewAgentDefaults, probeAgent, renderSettings, saveAgent, saveAgentDefaults, saveMCP, savePassword, testNotification, toggleNotifications } from "./settings.js?v=45";
import { loadAutomations, saveAutomation, updateAutomationBuilder } from "./automations.js?v=45";
import { refreshChannels, saveWaSettings, waAction } from "./whatsapp.js?v=45";
import { openPalette, runPalette } from "./palette.js?v=45";
import { doLogin, doLogout } from "./auth.js?v=45";
import { startApp } from "./boot.js?v=45";

export const syncAppHeight = () => {
  const vv = window.visualViewport;
  document.documentElement.style.setProperty("--app-h", Math.round(vv && vv.scale === 1 ? vv.height : window.innerHeight) + "px");
};

const ROW_HOLD_MS = 500;
const ROW_HOLD_SLOP = 12;
let rowHold = null;
let rowMenuGuard = null;

function clearRowHold() {
  if (!rowHold) return;
  clearTimeout(rowHold.timer);
  rowHold.row.classList.remove("holding");
  rowHold = null;
}

function guardRowMenuClick(row) {
  rowMenuGuard = { row, until: performance.now() + 700 };
}

function openRowMenu(row) {
  if (!row?.dataset?.id || !row.isConnected) return;
  openConversationMenu(row, row.dataset.id);
  guardRowMenuClick(row);
}

function bindConversationGestures() {
  document.addEventListener("click", (e) => {
    const row = e.target.closest?.(".conv-item");
    if (!row || !rowMenuGuard || row !== rowMenuGuard.row || performance.now() > rowMenuGuard.until) return;
    e.preventDefault();
    e.stopPropagation();
  }, true);

  document.addEventListener("contextmenu", (e) => {
    const row = e.target.closest?.(".conv-item");
    if (!row) return;
    e.preventDefault();
    const opened = rowHold?.opened && rowHold.row === row;
    clearRowHold();
    if (opened) guardRowMenuClick(row);
    else openRowMenu(row);
  });

  document.addEventListener("pointerdown", (e) => {
    if (e.pointerType === "mouse" || e.button !== 0) return;
    const row = e.target.closest?.(".conv-item");
    if (!row) return;
    clearRowHold();
    row.classList.add("holding");
    const timer = setTimeout(() => {
      if (!rowHold || rowHold.row !== row || !row.isConnected) { clearRowHold(); return; }
      rowHold.opened = true;
      row.classList.remove("holding");
      openRowMenu(row);
    }, ROW_HOLD_MS);
    rowHold = { timer, row, x: e.clientX, y: e.clientY, pointerId: e.pointerId, opened: false };
  }, true);

  document.addEventListener("pointermove", (e) => {
    if (!rowHold || e.pointerId !== rowHold.pointerId || rowHold.opened) return;
    if (Math.hypot(e.clientX - rowHold.x, e.clientY - rowHold.y) > ROW_HOLD_SLOP) clearRowHold();
  }, true);

  const endHold = (e) => {
    if (!rowHold || e.pointerId !== rowHold.pointerId) return;
    if (rowHold.opened) {
      clearTimeout(rowHold.timer);
      rowHold.row.classList.remove("holding");
      rowHold = null;
      return;
    }
    clearRowHold();
  };
  document.addEventListener("pointerup", endHold, true);
  document.addEventListener("pointercancel", (e) => {
    if (!rowHold || e.pointerId !== rowHold.pointerId) return;
    clearRowHold();
  }, true);
}

let swallowClickUntil = 0;
let projectPress = null;

function cancelProjectPress() {
  if (!projectPress) return;
  clearTimeout(projectPress.timer);
  projectPress = null;
}

function bindProjectMenuGestures() {
  // A long press ends with a click on its row. Drop that click in the capture phase, before the
  // popover's outside-click listener closes the menu the press just opened. A click anywhere
  // else (e.g. the popover it opened) is real and must go through.
  window.addEventListener("click", (e) => {
    if (performance.now() > swallowClickUntil) return;
    swallowClickUntil = 0;
    if (!e.target.closest?.("#sidebar .project-head")) return;
    e.preventDefault();
    e.stopPropagation();
  }, true);
  document.addEventListener("contextmenu", (e) => {
    const block = e.target.closest?.("#sidebar [data-project-id]");
    if (!block || e.target.closest?.(".conv-item")) return;
    e.preventDefault();
    openProjectMenuAt(block.dataset.projectId, e.clientX, e.clientY);
  });
  document.addEventListener("pointerdown", (e) => {
    if (e.pointerType === "mouse" || e.button !== 0) return;
    const row = e.target.closest?.("#sidebar .project-head");
    if (!row || e.target.closest?.(".project-menu-btn")) return;
    cancelProjectPress();
    const press = { id: row.dataset.id, x: e.clientX, y: e.clientY, pointerId: e.pointerId, timer: 0, fired: false };
    press.timer = setTimeout(() => {
      if (projectPress !== press) return;
      press.fired = true;
      openProjectMenuAt(press.id, press.x, press.y);
    }, 550);
    projectPress = press;
  });
  document.addEventListener("pointermove", (e) => {
    if (!projectPress || e.pointerId !== projectPress.pointerId) return;
    if (Math.hypot(e.clientX - projectPress.x, e.clientY - projectPress.y) > 10) cancelProjectPress();
  });
  document.addEventListener("pointerup", (e) => {
    if (!projectPress || e.pointerId !== projectPress.pointerId) return;
    if (projectPress.fired) swallowClickUntil = performance.now() + 500;
    cancelProjectPress();
  });
  document.addEventListener("pointercancel", cancelProjectPress);
}

export function bindEvents() {
  bindProjectMenuGestures();
  bindConversationGestures();
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
    else if (form.dataset.form === "project") saveProjectEditor();
  });

  document.addEventListener("input", (e) => {
    if (e.target.matches('[data-input="automation-cron"]')) {
      const form = e.target.closest("form"); $('[data-change="automation-preset"]', form).value = ""; $(".automation-builder", form).hidden = true;
    }
    if (e.target.matches("[data-input=conv-search]")) {
      S.query = e.target.value;
      filterSidebar();
    }
    if (e.target.matches("[data-input=project-name]")) noteProjectName(e.target.value);
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
    if (action === "toggle-project") {
      e.preventDefault();
      toggleProjectRow(id);
      return;
    }
    if (action !== "conv-menu" && action !== "project-menu") closePopover();
    switch (action) {
      case "toast-close": target.closest(".toast")?.remove(); break;
      case "toggle-password": {
        const input = target.closest(".password-wrap")?.querySelector("input");
        if (input) { input.type = input.type === "password" ? "text" : "password"; target.classList.toggle("active"); }
        break;
      }
      case "new-chat": e.preventDefault(); newConversation(); break;
      case "project-new-chat": e.preventDefault(); newProjectChat(id); break;
      case "project-clear-scope": setSelectedProject(""); renderSidebar(); renderComposer(); break;
      case "new-project": openProjectEditor(null); break;
      case "project-menu": e.preventDefault(); openProjectMenu(target, id); break;
      case "project-settings": {
        const project = (S.projects || []).find((p) => p.id === id);
        if (project) openProjectEditor(project);
        break;
      }
      case "project-pin": await toggleProjectPin(id); break;
      case "project-delete": await deleteProject(id); break;
      case "project-add-folder": openFolderBrowser(); break;
      case "project-make-primary": makeDraftPrimary(target.dataset.index); break;
      case "project-remove-source": removeDraftSource(target.dataset.index); break;
      case "project-confirm-folders": await confirmAddedFolders(); break;
      case "fs-open":
      case "fs-retry": await loadFolderBrowser(target.getAttribute("data-path") || ""); break;
      case "fs-add": addDraftSource(target.getAttribute("data-path") || ""); break;
      case "open-sidebar": openMobileSidebar(); break;
      case "toggle-sidebar": toggleSidebarCollapsed(); break;
      case "close-sidebar": closeMobileSidebar(); break;
      case "open-settings": openSettings(target.dataset.tab || ""); break;
      case "open-account": openSettings("account"); break;
      case "settings-tab":
        S.settingsTab = target.dataset.tab; S.agentForm = null; S.mcpForm = null;
        if (S.qrTimer && S.settingsTab !== "channels") { clearInterval(S.qrTimer); S.qrTimer = null; }
        if (S.settingsTab === "automations") loadAutomations();
        else if (S.settingsTab === "defaults") {
          S.defaultDraft = { agentId: S.agentDefaults.agentId || "", selections: (S.agentDefaults.selections || []).map((s) => ({ ...s })) };
          previewAgentDefaults();
        } else {
          if (S.settingsTab === "account") loadUpdateInfo();
          renderSettings();
        }
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
      case "queue-cancel": {
        const convId = S.current?.id;
        if (!convId || !target.dataset.turn) break;
        confirmModal("Cancel this queued message?", "The agent will not receive it.", async () => {
          try {
            await api(`/conversations/${encodeURIComponent(convId)}/turns/${encodeURIComponent(target.dataset.turn)}/cancel`, { method: "POST" });
            toast("Queued message cancelled", "", "info");
          } catch (err) { toast("Could not cancel", err.message, "error"); }
        }, "Remove");
        break;
      }
      case "queue-edit": {
        const convId = S.current?.id;
        if (!convId || !target.dataset.turn) break;
        // Drop the queued copy first, then pull its text and attachments back
        // into the composer — the resend creates a fresh turn rather than
        // mutating the stored message.
        const m = S.messages.find((x) => x.id === id);
        try {
          await api(`/conversations/${encodeURIComponent(convId)}/turns/${encodeURIComponent(target.dataset.turn)}/cancel`, { method: "POST", body: { dropMessage: true } });
        } catch (err) { toast("Could not edit queued message", err.message, "error"); break; }
        if (m) {
          const meta = parseJSON(m.metadata, {}) || {};
          const anns = (Array.isArray(meta.annotations) ? meta.annotations : []).filter((a) => a && a.pickId);
          const pinUploadIds = new Set(anns.map((a) => a.uploadId).filter(Boolean));
          const owner = composerFor(composerKey);
          owner.draft = m.channel && m.channel !== "web"
            ? (m.text || "").replace(/^(?:[^\r\n]*?: )?\[In reply to: [^\r\n]*\](?:\r?\n|$)/, "")
            : (m.text || "");
          owner.attachments = (m.attachments || []).filter((a) => !pinUploadIds.has(a.id)).map((a) => ({ upload: a }))
            .concat(anns.map((a) => ({
              type: "annotation", id: a.pickId, pickId: a.pickId, comment: a.comment || "",
              marker: a.marker || 0, shot: !a.omitScreenshot,
              tagName: a.descriptor?.tagName || "", label: a.descriptor?.name || a.descriptor?.immediateText || "",
              selector: a.descriptor?.selector || "", upload: a.uploadId ? { id: a.uploadId } : null,
            })));
          const ta = $("#prompt");
          if (ta) { ta.value = owner.draft; autoGrow(); }
          renderPendingAttachments();
          focusComposer();
        }
        break;
      }
      case "retry": await retryTurn(); break;
      case "retry-boot": startApp(); break;
      case "retry-load": await loadConversation(id, false); break;
      case "load-older": await loadOlderMessages(); break;
      case "attach": $("#file-input")?.click(); break;
      case "reply-toggle": S.replyToChannel = !S.replyToChannel; renderComposer(); focusComposer(); scheduleDraftSave(); break;
      case "remove-attachment": S.attachments = S.attachments.filter((a) => (a.upload || a).id !== id); renderPendingAttachments(); scheduleDraftSave(); break;
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
      case "add-mcp": S.mcpForm = { server: { enabled: true, transport: "stdio" } }; S.agentForm = null; S.settingsTab = "mcp"; renderSettings(); break;
      case "edit-mcp": {
        const m = S.mcp.find((x) => x.id === id);
        if (m) { S.mcpForm = { id: m.id, server: m }; S.agentForm = null; S.settingsTab = "mcp"; renderSettings(); }
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
      case "update-check": await checkUpdate(); break;
      case "update-apply": applyUpdate(); break;
      case "notify-toggle": await toggleNotifications(); break;
      case "notify-test": await testNotification(); break;
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
      case "toggle-browser": toggleBrowserPane(); break;
      case "browser-back": await browserHistory("back"); break;
      case "browser-forward": await browserHistory("forward"); break;
      case "browser-reload": await browserHistory("reload"); break;
      case "browser-annotate": toggleAnnotate(); break;
      case "browser-device": toggleDeviceBar(); break;
      case "browser-device-mode": browserDeviceMode(target.dataset.device); break;
      case "browser-close": closeBrowserPane(); break;
      case "browser-reconnect": browserReconnect(); break;
      case "annotation-submit": submitAnnotation(); break;
      case "annotation-cancel": cancelAnnotation(); break;
      case "annotation-edit": editAnnotationComment(id); break;
      case "preview-annotation":
        e.preventDefault();
        previewAnnotation(target.dataset.upload, target.dataset.caption || "");
        break;
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
      if (browserEscape()) return;
      closeMobileSidebar();
      return;
    }
    if (e.target?.id === "prompt-modal-input" && e.key === "Enter") {
      e.preventDefault();
      $("[data-action=prompt-save]")?.click();
    }
    if ((e.key === "Enter" || e.key === " ") && e.target?.classList?.contains("conv-item")) {
      e.preventDefault();
      e.target.click();
    }
  });

  window.addEventListener("online", () => { closeWS(); S.reconnectAttempt = Math.max(1, S.reconnectAttempt); connectWS(); renderOffline(); });

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
}
