import { esc, icon } from "./util.js?v=43";
import { S, setSelectedProject } from "./state.js?v=43";
import { api } from "./api.js?v=43";
import { toast } from "./toast.js?v=43";
import { closeAllModals, closeModal, modalHead, onModalClose, openModal, stackModal } from "./modal.js?v=43";
import { menuItem, openPopover } from "./popover.js?v=43";
import { renderSidebar } from "./sidebar.js?v=43";
import { renderComposer } from "./composer.js?v=43";
import { renderTopbar } from "./topbar.js?v=43";
import {
  appendSource, baseName, buildSaveBody, makeSourcePrimary, projectSources, removeSourceAt,
  staleProjectSnapshot,
} from "./projectmodel.js?v=43";

let editor = null;
let sessionSeq = 0;
let fsSeq = 0;
const inflight = new Set();

async function exclusive(key, fn) {
  if (inflight.has(key)) return;
  inflight.add(key);
  try { await fn(); }
  finally { inflight.delete(key); }
}

let closeHookArmed = false;

// Deferred: an imported binding must not run at module top level.
function armEditorCloseHook() {
  if (closeHookArmed) return;
  closeHookArmed = true;
  onModalClose(() => {
    if (!document.getElementById("project-form")) editor = null;
  });
}

export function upsertProject(project) {
  if (!project?.id) return;
  if (!Array.isArray(S.projects)) S.projects = [];
  if (project.deleted) {
    S.projects = S.projects.filter((p) => p.id !== project.id);
    S.expandedProjects?.delete(project.id);
    for (const c of S.conversations || []) if (c.projectId === project.id) c.projectId = "";
    if (S.current?.projectId === project.id) { S.current.projectId = ""; renderTopbar(); }
    if (S.selectedProjectId === project.id) { setSelectedProject(""); renderComposer(); }
    return;
  }
  const i = S.projects.findIndex((p) => p.id === project.id);
  if (i >= 0 && staleProjectSnapshot(S.projects[i], project)) return;
  const renamed = i < 0 || S.projects[i].name !== project.name;
  const next = { ...project };
  if (i >= 0) S.projects[i] = next;
  else S.projects.push(next);
  // Only the name appears in the composer scope row and the topbar; a pin change does not.
  if (renamed && S.selectedProjectId === next.id) renderComposer();
  if (renamed && S.current?.projectId === next.id) renderTopbar();
}

function projectFromResponse(data) {
  if (!data || typeof data !== "object") return null;
  if (data.project?.id) return data.project;
  if (data.id && (Array.isArray(data.rootPaths) || Array.isArray(data.sources) || typeof data.name === "string")) return data;
  return null;
}

function editorOpen(snapshot) {
  const form = document.getElementById("project-form");
  return !!(
    editor && snapshot && form?.isConnected &&
    editor.session === snapshot.session &&
    (editor.id || null) === (snapshot.id || null)
  );
}

function sourcesHTML() {
  const sources = editor?.sources || [];
  if (!sources.length) return `<div class="project-empty-folders">No folders yet. Add the folder where chats should start.</div>`;
  const many = sources.length > 1;
  return sources.map((path, index) => {
    const name = baseName(path);
    const makePrimary = many && index > 0 ? `<button type="button" class="btn btn-ghost btn-xs" data-action="project-make-primary" data-index="${index}" title="Chats in this project run in this folder">Make primary</button>` : "";
    return `<div class="project-source">
      ${icon("folder", "sm")}
      <span class="project-source-text"><span class="project-source-name">${esc(name)}</span><span class="project-source-path" title="${esc(path)}">${esc(path)}</span></span>
      <span class="project-source-actions">${many && index === 0 ? `<span class="badge-soft amber">Primary</span>` : makePrimary}<button type="button" class="icon-btn sm" data-action="project-remove-source" data-index="${index}" aria-label="Remove ${esc(name)}" title="Remove folder">${icon("x", "sm")}</button></span>
    </div>`;
  }).join("");
}

function editorFormHTML() {
  const creating = !editor?.id;
  const fallbackName = editor?.sources?.[0] ? baseName(editor.sources[0]) : "";
  return `<form id="project-form" class="project-form" data-form="project">
    ${modalHead(creating ? "New project" : "Edit project", "Chats run in the first folder. Other folders are added as extra directories.")}
    <div class="modal-body">
      <div class="field">
        <label for="project-name">Name</label>
        <input class="input" id="project-name" value="${esc(editor?.name || "")}" placeholder="${esc(fallbackName || "Project name")}" data-input="project-name" autocomplete="off" autofocus>
      </div>
      <div id="project-error" class="project-error" role="alert" hidden></div>
      <div class="field">
        <label>Source folders</label>
        <div id="project-sources" class="project-sources">${sourcesHTML()}</div>
        <button type="button" class="btn btn-secondary btn-sm project-add" data-action="project-add-folder">${icon("plus", "sm")} Add folder</button>
      </div>
      ${creating ? "" : `<div class="project-danger">
        <div><div class="project-danger-title">Remove project</div><div class="project-danger-desc">Folders stay on disk. Its chats move to Chats.</div></div>
        <button type="button" class="btn btn-danger btn-sm" data-action="project-delete" data-id="${esc(editor.id)}">Remove</button>
      </div>`}
    </div>
    <div class="modal-foot">
      <button type="button" class="btn btn-secondary" data-action="close-modal">Cancel</button>
      <button type="submit" class="btn btn-primary" id="project-save" ${(editor?.sources || []).length && !editor?.saving ? "" : "disabled"}>${creating ? "Create project" : "Save"}</button>
    </div>
  </form>`;
}

function showError(message) {
  const el = document.getElementById("project-error");
  if (!el) return;
  if (!message) {
    el.hidden = true;
    el.textContent = "";
    return;
  }
  el.hidden = false;
  el.textContent = message;
}

function syncSaveDisabled() {
  const btn = document.getElementById("project-save");
  if (!btn || !editor) return;
  btn.disabled = !!(editor.saving || editor.sources.length === 0);
  btn.textContent = editor.saving ? "Saving…" : editor.id ? "Save" : "Create project";
}

function renderSourceList() {
  const host = document.getElementById("project-sources");
  if (host) host.innerHTML = sourcesHTML();
  const name = document.getElementById("project-name");
  if (name) name.placeholder = editor?.sources?.[0] ? baseName(editor.sources[0]) : "Project name";
  syncSaveDisabled();
}

export function openProjectEditor(project) {
  armEditorCloseHook();
  editor = {
    session: ++sessionSeq,
    id: project?.id || null,
    updatedAt: project?.updatedAt,
    name: project?.name || "",
    sources: projectSources(project),
    rev: 0,
    saving: false,
    pendingSnapshot: null,
  };
  openModal(editorFormHTML(), { size: "md", fit: true, dialogClass: "project-dialog" });
}

export function noteProjectName(value) {
  if (!editor || !document.getElementById("project-form")?.isConnected) return;
  editor.name = value;
  editor.rev++;
}

export function makeDraftPrimary(index) {
  if (!editor || !document.getElementById("project-form")?.isConnected) return;
  const next = makeSourcePrimary(editor.sources, index);
  if (next.length === editor.sources.length && next.every((path, i) => path === editor.sources[i])) return;
  editor.sources = next;
  editor.rev++;
  renderSourceList();
}

export function removeDraftSource(index) {
  if (!editor || !document.getElementById("project-form")?.isConnected) return;
  const next = removeSourceAt(editor.sources, index);
  if (next.length === editor.sources.length) return;
  editor.sources = next;
  editor.rev++;
  renderSourceList();
}

export function saveProjectEditor() {
  if (!editor || editor.saving || !document.getElementById("project-form")?.isConnected) return;
  if (document.querySelectorAll("#modal-root .modal-overlay").length > 1) return;
  const typed = document.getElementById("project-name")?.value ?? editor.name;
  editor.name = typed;
  editor.pendingSnapshot = null;
  const snapshot = {
    session: editor.session,
    id: editor.id,
    name: typed,
    sources: editor.sources.slice(),
    updatedAt: editor.updatedAt,
    rev: editor.rev,
    foldersConfirmed: false,
  };
  return saveSnapshot(snapshot);
}

export function confirmAddedFolders() {
  if (!editor?.pendingSnapshot || editor.saving) return;
  const snapshot = editor.pendingSnapshot;
  editor.pendingSnapshot = null;
  closeModal();
  return saveSnapshot(snapshot);
}

async function saveSnapshot(snapshot) {
  if (!editorOpen(snapshot) || editor.saving) return;
  if (!snapshot.sources.length) {
    syncSaveDisabled();
    return;
  }
  editor.saving = true;
  syncSaveDisabled();
  showError("");
  const body = buildSaveBody(snapshot);
  const path = snapshot.id ? `/projects/${encodeURIComponent(snapshot.id)}` : "/projects";
  const method = snapshot.id ? "PATCH" : "POST";
  try {
    const data = await api(path, { method, body });
    onSaved(snapshot, data);
  } catch (err) {
    onSaveFailed(snapshot, err);
  }
}

function onSaved(snapshot, data) {
  const project = projectFromResponse(data);
  if (project) {
    upsertProject(project);
    S.expandedProjects?.add(project.id);
  }
  if (!editorOpen(snapshot)) {
    renderSidebar();
    return;
  }
  if (project?.id && !snapshot.id) editor.id = project.id;
  if (project && project.updatedAt != null) editor.updatedAt = project.updatedAt;
  editor.saving = false;
  if (editor.rev !== snapshot.rev) {
    syncSaveDisabled();
    renderSidebar();
    return;
  }
  closeAllModals();
  renderSidebar();
}

function onSaveFailed(snapshot, err) {
  if (!editorOpen(snapshot)) {
    toast("Could not save project", err?.message || "Request failed", "error");
    return;
  }
  editor.saving = false;
  syncSaveDisabled();
  if (err?.status === 409 && err.code === "confirmation_required") {
    openFolderConfirm(snapshot, err.data?.added);
    return;
  }
  showError(err?.message || "Could not save project");
  const form = document.getElementById("project-form");
  if (form && !form.contains(document.activeElement)) {
    const save = document.getElementById("project-save");
    (save && !save.disabled ? save : document.getElementById("project-name"))?.focus();
  }
}

function openFolderConfirm(snapshot, added) {
  if (!editorOpen(snapshot)) return;
  const name = String(snapshot.name || "").trim() || "this project";
  const paths = Array.isArray(added) ? added : [];
  editor.pendingSnapshot = {
    ...snapshot,
    sources: snapshot.sources.slice(),
    foldersConfirmed: true,
  };
  const items = paths.length
    ? paths.map((p) => `<li title="${esc(p)}">${esc(p)}</li>`).join("")
    : `<li>The server asked to confirm the new folders.</li>`;
  stackModal(`${modalHead(`Add folders to ${name}?`, "Every chat in this project gets them as extra directories.")}
    <div class="modal-body project-confirm-body"><ul class="project-add-list">${items}</ul></div>
    <div class="modal-foot">
      <button type="button" class="btn btn-secondary" data-action="close-modal">Cancel</button>
      <button type="button" class="btn btn-primary" data-action="project-confirm-folders" autofocus>Add folders</button>
    </div>`, { size: "sm", fit: true, dialogClass: "project-dialog" });
}

export function openFolderBrowser() {
  if (!editor || !document.getElementById("project-form")?.isConnected) return;
  stackModal(`${modalHead("Add folder", "Choose a folder for this project")}
    <div class="modal-body fs-browser" id="fs-browser" tabindex="-1" autofocus><div class="fs-status">Loading folders…</div></div>`, { size: "sm", fit: true, dialogClass: "project-dialog" });
  loadFolderBrowser(editor.sources[0] || "");
}

export async function loadFolderBrowser(path) {
  const seq = ++fsSeq;
  const host = document.getElementById("fs-browser");
  if (!host) return;
  host.innerHTML = `<div class="fs-status">Loading folders…</div>`;
  try {
    const data = await api("/fs/list?path=" + encodeURIComponent(path ?? ""));
    if (seq !== fsSeq || !document.getElementById("fs-browser")) return;
    renderFs(data);
  } catch (err) {
    if (seq !== fsSeq || !document.getElementById("fs-browser")) return;
    const hostNow = document.getElementById("fs-browser");
    if (!hostNow) return;
    hostNow.innerHTML = `<div class="fs-status" role="alert">${esc(err?.message || "Could not list folders")}</div>
      <div class="fs-toolbar"><button type="button" class="btn btn-secondary btn-sm" data-action="fs-retry" data-path="${esc(path || "")}">Retry</button>${path ? `<button type="button" class="btn btn-ghost btn-sm" data-action="fs-retry" data-path="">Browse from home</button>` : ""}</div>`;
  }
}

function renderFs(data) {
  const host = document.getElementById("fs-browser");
  if (!host) return;
  const path = typeof data?.path === "string" ? data.path : "";
  const parent = typeof data?.parent === "string" ? data.parent : "";
  const dirs = Array.isArray(data?.dirs) ? data.dirs : [];
  const added = new Set(editor?.sources || []);
  const up = parent && parent !== path
    ? `<button type="button" class="btn btn-ghost btn-sm" data-action="fs-open" data-path="${esc(parent)}">${icon("chevron-left", "sm")} Up</button>`
    : "";
  const addCurrent = !path ? "" : added.has(path)
    ? `<span class="badge-soft gray">Already added</span>`
    : `<button type="button" class="btn btn-secondary btn-sm" data-action="fs-add" data-path="${esc(path)}">${icon("plus", "sm")} Add this folder</button>`;
  const list = dirs.length
    ? dirs.map((d) => `<div class="fs-row">
        <button type="button" class="fs-open" data-action="fs-open" data-path="${esc(d.path)}" title="${esc(d.path)}">${icon("folder", "sm")}<span>${esc(d.name || baseName(d.path))}</span>${icon("chevron-right", "sm fs-chev")}</button>
        ${added.has(d.path) ? `<span class="badge-soft gray">Added</span>` : `<button type="button" class="btn btn-secondary btn-xs" data-action="fs-add" data-path="${esc(d.path)}">Add</button>`}
      </div>`).join("")
    : `<div class="fs-status">No folders here</div>`;
  host.innerHTML = `<div class="fs-path" title="${esc(path)}">${esc(path || "Home")}</div>
    <div class="fs-toolbar">${up}${addCurrent}</div>
    <div class="fs-list">${list}</div>`;
}

export function addDraftSource(path) {
  if (!editor || !path || !document.getElementById("project-form")?.isConnected || editor.sources.includes(path)) return;
  editor.sources = appendSource(editor.sources, path);
  editor.rev++;
  renderSourceList();
  closeModal();
}

async function patchProject(id, body) {
  const data = await api(`/projects/${encodeURIComponent(id)}`, { method: "PATCH", body });
  const project = projectFromResponse(data);
  if (project) upsertProject(project);
  else {
    const current = (S.projects || []).find((p) => p.id === id);
    if (current) upsertProject({ ...current, ...body, id });
  }
  renderSidebar();
}

export async function toggleProjectPin(id) {
  const project = (S.projects || []).find((p) => p.id === id);
  if (!project) return;
  await exclusive("pin:" + id, async () => {
    try {
      await patchProject(id, { pinned: !project.pinned });
    } catch (err) {
      toast("Could not update pin", err.message, "error");
    }
  });
}

function detachedConversationIds(projectId, fromServer) {
  const ids = new Set(Array.isArray(fromServer) ? fromServer : []);
  for (const c of S.conversations || []) if (c?.projectId === projectId) ids.add(c.id);
  if (S.current?.projectId === projectId && S.current.id) ids.add(S.current.id);
  return [...ids];
}

function clearProjectOnConversations(projectId, conversationIds) {
  const ids = new Set(conversationIds);
  for (const c of S.conversations || []) {
    if (c.projectId === projectId || ids.has(c.id)) c.projectId = "";
  }
  if (S.current && (S.current.projectId === projectId || ids.has(S.current.id))) S.current.projectId = "";
}

export async function deleteProject(id) {
  const existing = (S.projects || []).find((p) => p.id === id);
  if (!existing || !id) return;
  // Read the UI state now: the server's project.updated(deleted) event clears it before the response returns.
  const flags = { wasSelected: S.selectedProjectId === id, wasOpen: !!S.expandedProjects?.has(id) };
  await exclusive("delete:" + id, () => deleteProjectNow(id, existing, flags));
}

async function deleteProjectNow(id, existing, flags) {
  try {
    const data = await api(`/projects/${encodeURIComponent(id)}`, { method: "DELETE" });
    const project = { ...existing, ...(data?.project || {}) };
    const conversationIds = detachedConversationIds(id, data?.conversationIds);
    S.projects = (S.projects || []).filter((p) => p.id !== id);
    S.expandedProjects?.delete(id);
    clearProjectOnConversations(id, conversationIds);
    if (S.selectedProjectId === id) { setSelectedProject(""); renderComposer(); }
    if (editor?.id === id) closeAllModals();
    renderSidebar();
    renderTopbar();
    toast("Project removed", project.name || "", "info", 12000, {
      label: "Undo",
      run: () => restoreProject(project, conversationIds, flags),
    });
  } catch (err) {
    if (editor?.id === id && document.getElementById("project-form")?.isConnected) showError(err.message || "Could not remove project");
    else toast("Could not remove project", err.message, "error");
  }
}

async function restoreProject(project, conversationIds, flags) {
  try {
    const data = await api(`/projects/${encodeURIComponent(project.id)}/restore`, {
      method: "POST",
      body: { name: project.name || "", sources: projectSources(project), conversationIds, pinned: !!project.pinned },
    });
    const restored = projectFromResponse(data) || project;
    upsertProject({ ...project, ...restored, id: project.id });
    // The server skips members that already joined another project elsewhere.
    const ids = new Set(Array.isArray(data?.conversationIds) ? data.conversationIds : conversationIds);
    for (const c of S.conversations || []) if (ids.has(c.id)) c.projectId = project.id;
    if (S.current && ids.has(S.current.id)) S.current.projectId = project.id;
    if (flags.wasOpen) S.expandedProjects?.add(project.id);
    if (flags.wasSelected) setSelectedProject(project.id);
    renderSidebar();
    renderComposer();
    renderTopbar();
  } catch (err) {
    toast("Could not undo", err.message, "error");
  }
}

export function openProjectMenuAt(id, x, y) {
  const ghost = document.createElement("span");
  ghost.style.cssText = `position:fixed;left:${x}px;top:${y}px;width:1px;height:1px;pointer-events:none;`;
  document.body.appendChild(ghost);
  openProjectMenu(ghost, id);
  const pop = document.querySelector(".popover");
  if (!pop) { ghost.remove(); return; }
  const prev = pop._cleanup;
  pop._cleanup = () => { prev?.(); ghost.remove(); };
}

export function openProjectMenu(anchor, id) {
  const project = (S.projects || []).find((p) => p.id === id);
  if (!project) return;
  openPopover(anchor, `<div class="popover-list">
    ${menuItem("plus", "New chat", "project-new-chat", `data-id="${esc(id)}"`)}
    ${menuItem("pin", project.pinned ? "Unpin project" : "Pin project", "project-pin", `data-id="${esc(id)}"`)}
    ${menuItem("settings", "Project settings", "project-settings", `data-id="${esc(id)}"`)}
    <div class="popover-sep"></div>
    ${menuItem("trash", "Remove project", "project-delete", `data-id="${esc(id)}"`, true)}
  </div>`, { width: 240 });
}