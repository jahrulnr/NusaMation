import { composerFor, composerKey, onComposerSwitch } from "./state.js?v=43";
import { api } from "./api.js?v=43";

/* ---------------- per-conversation meta (file-backed on the server) --------
   Draft text + attachments + composer toggles persist to
   <data>/conversations/{id}/meta.json so a refresh or backend restart keeps
   the composer. Saved on input (debounced) and on blur; cleared on send. */

const draftTimers = new Map();
const metaWrites = new Map();   // key -> promise; serializes PUTs per conversation
const metaJobs = new Map();     // key -> in-flight GET; shared by draft restore and the browser pane
const metaLoaded = new Set();   // keys whose draft restore already ran this session
const metaTouched = new Set();  // keys with a local edit — restore must not overwrite
const browserCache = new Map(); // key -> {lastUrl, open} | null
let metaGen = 0;
let onBrowserMeta = null;

export function usableBrowserURL(url) {
  const u = String(url || "").trim();
  if (!u || u === "about:blank") return "";
  return u;
}

// The browser pane registers this. Called once, after a conversation's meta
// has loaded, so a refresh can reopen the pane without a static import cycle.
export function onBrowserMetaRestored(fn) { onBrowserMeta = fn; }

export function cachedBrowserMeta(key) {
  return browserCache.get(key) || null;
}

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
  const u = usableBrowserURL(url);
  if (!key || !u) return;
  const cur = browserCache.get(key);
  if (cur) cur.lastUrl = u;
  patchMeta(key, { browser: { lastUrl: u } });
}

export function saveBrowserOpen(key, open) {
  if (!key) return;
  const flag = !!open;
  const cur = browserCache.get(key);
  if (cur) cur.open = flag;
  else if (metaLoaded.has(key)) browserCache.set(key, { lastUrl: "", open: flag });
  patchMeta(key, { browser: { open: flag } });
}

export function whenBrowserMeta(key) {
  if (!key) return Promise.resolve(null);
  if (metaLoaded.has(key)) return Promise.resolve(browserCache.get(key) || null);
  return loadMeta(key).then((meta) => {
    if (!browserCache.has(key)) browserCache.set(key, meta.browser || null);
    return browserCache.get(key);
  }).catch(() => null);
}

function loadMeta(key) {
  const existing = metaJobs.get(key);
  if (existing) return existing;
  const job = (async () => {
    const res = await api(`/conversations/${encodeURIComponent(key)}/meta`);
    return res.meta || {};
  })();
  metaJobs.set(key, job);
  job.catch(() => { if (metaJobs.get(key) === job) metaJobs.delete(key); });
  return job;
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
  const gen = metaGen;
  let meta;
  try {
    meta = await loadMeta(key);
  } catch {
    return; // not marked loaded — a transient failure retries on next switch
  }
  if (gen !== metaGen || metaLoaded.has(key)) return;
  metaLoaded.add(key);
  browserCache.set(key, meta.browser || null);
  // Browser restore is independent of the draft: a typed composer must not
  // skip reopening the pane, and a draft must not wait on the pane.
  if (onBrowserMeta) onBrowserMeta(key, meta.browser || null);
  const owner = composerFor(key);
  const d = meta.draft;
  if (!d || metaTouched.has(key) || owner.draft || owner.attachments.length) return;
  owner.draft = d.text || "";
  owner.attachments = Array.isArray(d.attachments) ? d.attachments : [];
  if (d.sendDelivery) owner.sendDelivery = d.sendDelivery;
  owner.replyToChannel = !!d.replyToChannel;
  if (gen !== metaGen || composerKey !== key) return;
  const ta = document.querySelector("#prompt");
  if (ta) ta.value = owner.draft;
  const { renderPendingAttachments, updateComposerState, autoGrow } = await import("./composer.js?v=43");
  renderPendingAttachments();
  updateComposerState();
  autoGrow();
}

// Logout/re-login starts a clean composer session — otherwise metaLoaded
// would silence draft restores and stale timers would fire doomed PUTs.
export function resetConvMeta() {
  metaGen += 1;
  for (const t of draftTimers.values()) clearTimeout(t);
  draftTimers.clear();
  metaWrites.clear();
  metaJobs.clear();
  metaLoaded.clear();
  metaTouched.clear();
  browserCache.clear();
}

onComposerSwitch((key) => { restoreDraft(key); });
