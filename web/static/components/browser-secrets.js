import { $, agentById, agentName, currentAgent, esc, isMobile } from "./util.js";
import { S, composerFor, composerKey } from "./state.js";
import { closePopover, menuItem, openPopover } from "./popover.js";
import { autoGrow } from "./composer.js";
import { scheduleDraftSave } from "./convmeta.js";
import { toast } from "./toast.js";
import { reconcileReferences } from "./references.js";

/* ---------------- browser secret references ----------------
   Opaque [[env:NAME]] tags the human can paste into a prompt instead of a raw
   secret. The UI only ever sees saved env names + hasValue flags; saved values
   are not returned to this picker. Resolution happens server-side
   (browser-use slice). */

export const ENV_REF_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function isValidEnvRefName(name) {
  return ENV_REF_NAME_RE.test(String(name || ""));
}

export function envRefTag(name) {
  return `[[env:${name}]]`;
}

// eligibleEnvRefs lists the names the composer may offer: saved (hasValue),
// expressible by the reference grammar, deduped first-wins like mergeSecrets.
export function eligibleEnvRefs(env) {
  const seen = new Set();
  const out = [];
  for (const e of env || []) {
    const name = String(e?.name || "").trim();
    if (!name || seen.has(name) || !e?.hasValue || !isValidEnvRefName(name)) continue;
    seen.add(name);
    out.push(name);
  }
  return out;
}

// envRefCopyEnabled gates the agent-form Copy reference button: the typed name
// must still be a saved entry (hasValue) — renaming a saved row is unsaved.
export function envRefCopyEnabled(name, savedEnv) {
  const n = String(name || "").trim();
  if (!isValidEnvRefName(n)) return false;
  return (savedEnv || []).some((e) => e?.name === n && e?.hasValue);
}

// insertAtCaret splices text at [start,end] and returns the resulting value
// plus the caret offset just past the inserted text.
export function insertAtCaret(text, start, end, insert) {
  const value = String(text ?? "");
  const ins = String(insert ?? "");
  const s = Math.max(0, Math.min(Number.isFinite(start) ? start : value.length, value.length));
  const e = Math.max(s, Math.min(Number.isFinite(end) ? end : s, value.length));
  return { value: value.slice(0, s) + ins + value.slice(e), caret: s + ins.length };
}

// secretScopeActive rejects stale popover clicks after a room or agent switch.
export function secretScopeActive(scope, key, agentId) {
  return !!scope && scope.key === key && scope.agentId === agentId;
}

/* ---------------- agent env rows ---------------- */

// agentEnvSavedMatch reports whether name resolves to a saved entry (hasValue)
// in the agent form being edited. Renamed rows stop matching until saved.
function agentEnvSavedMatch(name) {
  const n = String(name || "").trim();
  return (S.agentForm?.agent?.env || []).some((e) => e?.name === n && e?.hasValue);
}

// refreshAgentEnvRow updates the live [[env:NAME]] preview, copy availability
// and the Saved/New value label in place — no row re-render, typed values kept.
export function refreshAgentEnvRow(row) {
  if (!row) return;
  const nameIn = row.querySelector("[name=env-name]");
  const valIn = row.querySelector("[name=env-value]");
  const tag = row.querySelector("[data-env-ref]");
  const copyBtn = row.querySelector('[data-action="copy-env-ref"]');
  const label = row.querySelector("[data-env-value-label]");
  const name = (nameIn?.value || "").trim();
  const saved = isValidEnvRefName(name) && agentEnvSavedMatch(name);
  if (tag) tag.textContent = name ? envRefTag(name) : "[[env:NAME]]";
  if (copyBtn) copyBtn.disabled = !saved;
  if (label) label.textContent = saved && valIn?.dataset.hasValue && !valIn?.value ? "Saved value" : "New value";
  if (valIn) valIn.placeholder = saved && valIn.dataset.hasValue ? "•••••••• (saved)" : "value";
}

export async function copyEnvRef(btn) {
  const row = btn.closest(".secret-row");
  const name = (row?.querySelector("[name=env-name]")?.value || "").trim();
  if (!envRefCopyEnabled(name, S.agentForm?.agent?.env)) return;
  try {
    await navigator.clipboard.writeText(envRefTag(name));
    toast("Copied", "", "success", 1500);
  } catch {
    toast("Copy failed", "", "error");
  }
}

/* ---------------- composer popover ---------------- */

// Bound at open: insertion only lands while the same room+agent is on screen.
let secretScope = null;

export function openSecretRefPopover(anchor) {
  const agent = currentAgent();
  const agentId = agent?.id || "";
  const names = agentId ? eligibleEnvRefs(agentById(agentId)?.env) : [];
  const ta = $("#prompt");
  secretScope = { key: composerKey, agentId, ta: ta || null };
  const items = names.map((n) => `<button class="menu-item secret-ref-item" data-action="secret-ref-insert" data-name="${esc(n)}"><span class="popover-main"><b>${esc(n)}</b><code class="secret-ref-tag">${esc(envRefTag(n))}</code></span></button>`).join("");
  const search = names.length > 5
    ? `<input class="input composer-search" type="search" placeholder="Search references" aria-label="Search saved references">`
    : "";
  const list = names.length
    ? items + `<div class="popover-empty secret-none" hidden>No matches.</div>`
    : `<div class="popover-empty">No saved references yet.<br>Add an environment variable to this agent to use it in browser input.</div>`;
  openPopover(anchor, `<div class="popover-title">Browser secrets${agent ? ` <span class="secret-agent">${esc(agentName(agent))}</span>` : ""}</div>
    ${search}
    <div class="popover-list">${list}</div>
    <div class="popover-foot">${menuItem("settings", "Manage environment", "manage-agent-env", agentId ? `data-id="${esc(agentId)}"` : "")}</div>`,
    { width: 290, class: "composer-popover" });
  const box = $(".composer-search");
  if (box) {
    box.addEventListener("input", () => filterSecretPopover(box.value));
    if (!isMobile()) box.focus();
  }
}

export function filterSecretPopover(query) {
  const pop = $(".composer-popover");
  if (!pop) return;
  const q = String(query || "").trim().toLowerCase();
  let visible = 0;
  pop.querySelectorAll(".secret-ref-item").forEach((el) => {
    const show = !q || (el.dataset.name || "").toLowerCase().includes(q);
    el.hidden = !show;
    if (show) visible++;
  });
  const none = pop.querySelector(".secret-none");
  if (none) none.hidden = visible !== 0;
}

// insertSecretRef splices the tag into the live draft at the textarea's current
// caret (typing while the popover is open just moves the caret). It writes the
// existing draft forward — never replaces it — then refocuses the composer.
export function insertSecretRef(name) {
  const scope = secretScope;
  if (!isValidEnvRefName(name) || !secretScopeActive(scope, composerKey, currentAgent()?.id || "")) return false;
  if (!eligibleEnvRefs(agentById(scope.agentId)?.env).includes(name)) return false;
  let ta = scope.ta;
  if (!ta?.isConnected || ta.dataset.composerKey !== scope.key) {
    const cur = $("#prompt");
    ta = cur?.dataset.composerKey === scope.key ? cur : null;
  }
  if (!ta) return false;
  const r = insertAtCaret(ta.value, ta.selectionStart, ta.selectionEnd, envRefTag(name));
  const owner = composerFor(scope.key);
  owner.references = reconcileReferences(ta.value, r.value, owner.references, ta.selectionStart, ta.selectionEnd);
  ta.value = r.value;
  owner.draft = r.value;
  ta.focus();
  try { ta.setSelectionRange(r.caret, r.caret); } catch {}
  autoGrow();
  scheduleDraftSave(scope.key);
  closePopover();
  secretScope = null;
  return true;
}
