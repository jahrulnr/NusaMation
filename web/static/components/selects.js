import SlimSelect from "../vendor/slimselect.es.js";

// Global select enhancement. Any native <select> the UI emits — including
// markup from code that never heard of SlimSelect — is upgraded in place by a
// document-level MutationObserver. The original element stays in the DOM
// (hidden) and keeps its value, so FormData submits and delegated
// [data-change] handlers work unchanged: SlimSelect dispatches a real `change`
// event on it, and its own observer re-syncs when options or the disabled
// attribute are mutated. More than five options gets a search field; five or
// fewer stays a plain list. Opt out with `data-select-raw` on the element.

const SEARCH_MIN = 6;
const registry = new Map(); // HTMLSelectElement -> SlimSelect

export const selectWantsSearch = (sel) => sel.querySelectorAll("option").length >= SEARCH_MIN;
export const shouldEnhance = (sel) => !!sel && sel.dataset.selectRaw === undefined && !registry.has(sel);

function destroySlim(sel, slim) {
  registry.delete(sel);
  try { slim.destroy(); } catch { /* detached mid-build */ }
}

export function enhanceSelect(sel) {
  if (!shouldEnhance(sel)) return;
  const slim = new SlimSelect({
    select: sel,
    settings: {
      showSearch: selectWantsSearch(sel),
      searchPlaceholder: "Search…",
      searchText: "No matches",
      searchHighlight: true,
    },
  });
  slim.__nusaSearch = selectWantsSearch(sel);
  registry.set(sel, slim);
}

function enhanceTree(root) {
  if (root.tagName === "SELECT") enhanceSelect(root);
  else if (root.querySelectorAll) root.querySelectorAll("select").forEach(enhanceSelect);
}

// Detached selects leak a SlimSelect (it holds document/window listeners), so
// every mutation batch sweeps the registry. An option count crossing the
// search threshold flips the widget too — rebuild is cheaper than patching.
function sweep() {
  for (const [sel, slim] of registry) {
    if (!sel.isConnected) { destroySlim(sel, slim); continue; }
    if (selectWantsSearch(sel) !== slim.__nusaSearch) { destroySlim(sel, slim); enhanceSelect(sel); }
  }
}

export function initSelects() {
  enhanceTree(document.documentElement);
  const pending = new Set();
  let scheduled = false;
  new MutationObserver((muts) => {
    for (const m of muts) for (const n of m.addedNodes) if (n.nodeType === 1) pending.add(n);
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      const roots = [...pending]; pending.clear();
      roots.forEach(enhanceTree);
      sweep();
    });
  }).observe(document.documentElement, { childList: true, subtree: true });
}
