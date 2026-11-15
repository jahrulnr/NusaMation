const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Multi-browser-tab invariants: a tab switch swaps the whole B state (park +
// adopt), a tool tab shelves the browser runtime, re-activating a parked
// browser state re-adopts it, and disposing a tab deletes its backend session
// so it does not linger as an orphan target.

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');
const sliceExport = (startMarker, endMarker = '\nexport ') => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  return src.slice(start, end < 0 ? undefined : end).trim().replace(/^export /, '');
};

const activateSrc = sliceExport('export function activatePaneTab(');

function tabWorld() {
  const calls = { park: 0, adopt: 0, rendered: 0, synced: 0, fitted: 0, devtools: 0 };
  const states = {
    primary: { id: 's1', parked: false, session: { id: 'bs_a' } },
    tab2: { id: 's2', parked: true, session: { id: 'bs_b' } },
  };
  const primary = { id: 'browser', kind: 'browser', state: states.primary };
  const tab2 = { id: 'tab_2', kind: 'browser', state: states.tab2 };
  const term = { id: 'tab_3', kind: 'terminal', state: null, el: null };
  const st = { tabs: [tab2, term], active: 'browser', primary, browserTab: primary };
  const pane = { classList: { toggle() {}, contains: () => st.active !== 'browser' } };
  const ctx = vm.createContext({
    B: states.primary,
    P: { conversationId: 'cv_a', open: true },
    convTabs: () => st,
    parkBrowserTab() { calls.park++; ctx.B.parked = true; },
    adoptBrowserTab() { calls.adopt++; ctx.B.parked = false; },
    selectBrowserTab() { calls.devtools++; },
    initTabEl() {},
    fitTermTab() { calls.fitted++; },
    renderPaneTabs() { calls.rendered++; },
    syncTabEls() { calls.synced++; },
    $: (sel) => (sel === '#browser-pane' ? pane : null),
    TAB_KINDS: { browser: {}, terminal: {}, files: {} },
    freshBrowserState: () => ({ parked: false }),
  });
  const activatePaneTab = vm.runInContext(`(${activateSrc})`, ctx);
  return { calls, st, states, ctx, activatePaneTab, B: () => ctx.B };
}

test('activating another browser tab parks the live state and adopts the target', () => {
  const w = tabWorld();
  w.activatePaneTab('tab_2');
  assert.equal(w.calls.park, 1);
  assert.equal(w.calls.adopt, 1);
  assert.equal(w.st.browserTab, w.st.tabs[0]);
  assert.equal(w.st.active, 'tab_2');
  assert.equal(w.B(), w.states.tab2); // B rebound to the tab's own state
});

test('re-activating the SAME browser state only re-adopts when it was parked', () => {
  const w = tabWorld();
  w.states.primary.parked = false;
  w.activatePaneTab('browser'); // already live — nothing to do
  assert.equal(w.calls.park, 0);
  assert.equal(w.calls.adopt, 0);
  w.states.primary.parked = true; // runtime shelved while a tool tab owned the surface
  w.activatePaneTab('browser');
  assert.equal(w.calls.park, 0); // no re-park — same state
  assert.equal(w.calls.adopt, 1); // but the runtime is resumed
});

test('activating a tool tab shelves the browser runtime', () => {
  const w = tabWorld();
  w.activatePaneTab('tab_3');
  assert.equal(w.calls.park, 1);
  assert.equal(w.st.active, 'tab_3');
  // B still points at the browser state — the browser tab is remembered, not lost.
  assert.equal(w.B(), w.states.primary);
  assert.equal(w.st.browserTab, w.st.primary);
});

test('an unknown tab id falls back to the primary browser tab', () => {
  const w = tabWorld();
  w.activatePaneTab('tab_missing');
  assert.equal(w.st.active, 'browser');
});

const disposeSrc = (() => {
  const start = src.indexOf('function disposeBrowserState(');
  const end = src.indexOf('\nlet bootDone', start);
  return src.slice(start, end).trim();
})();

const disposeWorld = (calls) => ({
  api: (u, opts) => { calls.api.push([u, opts.method]); return Promise.resolve({}); },
  URL: { revokeObjectURL: (u) => calls.revoked.push(u) },
  clearTimeout() {}, clearInterval() {},
});

test('disposing a browser tab deletes its backend session and revokes frame URLs', async () => {
  const calls = { api: [], revoked: [] };
  const disposeBrowserState = vm.runInNewContext(`(${disposeSrc})`, disposeWorld(calls));
  await disposeBrowserState({
    session: { id: 'bs_dead' },
    socket: { close() {} },
    frameUrl: 'blob:f1', prevFrameUrl: 'blob:f2',
    recPollTimer: 1, hoverTimer: 2, moveTimer: 3, resizeTimer: 4, recTickTimer: 5,
  });
  assert.deepEqual(calls.api, [['/browser/sessions/bs_dead', 'DELETE']]);
  assert.deepEqual(calls.revoked, ['blob:f1', 'blob:f2']);
});

test('disposing a tab with no session issues no DELETE', async () => {
  const calls = { api: [], revoked: [] };
  const disposeBrowserState = vm.runInNewContext(`(${disposeSrc})`, disposeWorld(calls));
  await disposeBrowserState({ session: null, socket: null });
  assert.equal(calls.api.length, 0);
});
