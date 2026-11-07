import { composerFor, composerKey, onComposerSwitch } from "./state.js";
import { api } from "./api.js";

/* ---------------- per-conversation meta (file-backed on the server) --------
   Draft text + attachments + composer toggles persist to
   <data>/conversations/{id}/meta.json so a refresh or backend restart keeps
   the composer. Saved on input (debounced) and on blur; cleared on send. */

const draftTimers = new Map();
const metaWrites = new Map();   // key -> promise; serializes PUTs per conversation
const metaLoaded = new Set();   // keys whose draft restore already ran this session
const metaTouched = new Set();  // keys with a local edit — restore must not overwrite

// Writes chain per key so a blur/save can never land after the send's clear.
function patchMeta(key, patch) {
  const next = (metaWrites.get(key) || Promise.resolve()).then(async () => {
    try {
      await api(`/conversations/${encodeURIComponent(key)}/meta`, { method: "PUT", body: patch });
    } catch (e) {
      console.warn("conversation meta not saved", e);
    }
  });
  metaWrites.set(key, next);
  next.finally(() => { if (metaWrites.get(key) === next) metaWrites.delete(key); });
}

function draftPayload(key) {
  const owner = composerFor(key);
  return {
    text: owner.draft,
    attachments: owner.attachments.map((a) => (a.type === "annotation" ? { ...a, comment: a.comment || "" } : a)),
    sendDelivery: owner.sendDelivery,
    replyToChannel: owner.replyToChannel,
  };
}

// Debounced — coalesces rapid input/attachment churn into one write.
export function scheduleDraftSave(key = composerKey) {
  if (!key) return;
  metaTouched.add(key);
  clearTimeout(draftTimers.get(key));
  draftTimers.set(key, setTimeout(() => { draftTimers.delete(key); patchMeta(key, { draft: draftPayload(key) }); }, 600));
}

// Immediate — on blur or send, so the last state is never left in the timer.
export function saveDraftNow(key = composerKey) {
  if (!key) return;
  metaTouched.add(key);
  clearTimeout(draftTimers.get(key));
  draftTimers.delete(key);
  patchMeta(key, { draft: draftPayload(key) });
}

export function clearDraft(key) {
  if (!key) return;
  clearTimeout(draftTimers.get(key));
  draftTimers.delete(key);
  patchMeta(key, { draft: null });
}

export function saveLastURL(key, url) {
  if (!key || !url) return;
  patchMeta(key, { browser: { lastUrl: url } });
}

// Title mirror: keeps the file debugging-friendly (meta.json is where an
// operator looks first) — the conversation row remains the source of truth.
export function saveTitle(key, title) {
  if (!key) return;
  patchMeta(key, { title });
}

// Restores draft once per composer key — a composer the user already edited
// (metaTouched) or filled (draft/attachments) is never clobbered, so a draft
// cleared while the GET is in flight cannot resurrect.
async function restoreDraft(key) {
  if (!key || metaLoaded.has(key)) return;
  let meta;
  try {
    meta = (await api(`/conversations/${encodeURIComponent(key)}/meta`)).meta || {};
  } catch {
    return; // not marked loaded — a transient failure retries on next switch
  }
  metaLoaded.add(key);
  const owner = composerFor(key);
  const d = meta.draft;
  if (!d || metaTouched.has(key) || owner.draft || owner.attachments.length) return;
  owner.draft = d.text || "";
  owner.attachments = Array.isArray(d.attachments) ? d.attachments : [];
  if (d.sendDelivery) owner.sendDelivery = d.sendDelivery;
  owner.replyToChannel = !!d.replyToChannel;
  if (composerKey !== key) return;
  const ta = document.querySelector("#prompt");
  if (ta) ta.value = owner.draft;
  const { renderPendingAttachments, updateComposerState, autoGrow } = await import("./composer.js");
  renderPendingAttachments();
  updateComposerState();
  autoGrow();
}

// Logout/re-login starts a clean composer session — otherwise metaLoaded
// would silence draft restores and stale timers would fire doomed PUTs.
export function resetConvMeta() {
  for (const t of draftTimers.values()) clearTimeout(t);
  draftTimers.clear();
  metaWrites.clear();
  metaLoaded.clear();
  metaTouched.clear();
}

onComposerSwitch((key) => { restoreDraft(key); });
