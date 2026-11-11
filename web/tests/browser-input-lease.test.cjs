const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Share-screen input lease: every attached pane receives frames, but only the
// controller drives input. A `control` stream message carries the lease state
// (owner = you | other | agent | none); canInput requires it; the Take over
// affordance sends a takeover claim on the live socket.

const browserSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const handleSrc = slice(browserSrc, 'function handleStreamMessage(', '\nfunction renderFrame(');
const canInputSrc = slice(browserSrc, 'function canInput()', '\nfunction sendCtl(');
const watchSrc = slice(browserSrc, 'function renderWatch()', '\nfunction closedLabel(').replace(/\bexport /g, '');
const toolbarSrc = slice(browserSrc, 'function updateToolbar()', '\n// First meta load');

function world() {
  const sent = [];
  const calls = { applyDeviceChoice: 0, stopResizeWatch: 0 };
  const watchEl = { innerHTML: '', classes: new Set() };
  watchEl.classList = { toggle(c, on) { on ? watchEl.classes.add(c) : watchEl.classes.delete(c); } };
  const els = {};
  const el = (id) => els[id] ??= { value: '', disabled: false, dataset: {} };
  const B = {
    open: true, annotate: false, status: 'live', gotFrame: true,
    inputOwner: false, agentLock: false, meta: null, sawMeta: true,
    viewportGuest: false, holdViewport: false, nav: {}, navigating: false,
    session: { id: 's' },
    socket: { readyState: 1, send(s) { sent.push(s); } }, pendingPointer: null,
  };
  const ctx = {
    B,
    WebSocket: { OPEN: 1 },
    document: { activeElement: null },
    $: (sel) => (sel === '#browser-watch' ? watchEl : sel.startsWith('#browser-') || sel.startsWith('[data-action') ? el(sel) : null),
    $$: () => [],
    esc: (s) => String(s),
    icon: () => '',
    applyMeta() {},
    hideSavedLogins() {},
    setStatus() {},
    placeAgentPointer() {},
    sendCtl(msg) { sent.push(JSON.stringify(msg)); },
    startResizeWatch() {},
    stopResizeWatch() { calls.stopResizeWatch++; },
    applyDeviceChoice() { calls.applyDeviceChoice++; },
    updateAnnotateUI() {},
    updateDeviceBar() {},
    clamp: (v, lo, hi) => Math.max(lo, Math.min(v, hi)),
  };
  ctx.handleStreamMessage = vm.runInNewContext(`(${handleSrc})`, ctx);
  ctx.canInput = vm.runInNewContext(`(${canInputSrc})`, ctx);
  vm.runInNewContext(`${watchSrc};({renderWatch, browserTakeover})`, ctx);
  ctx.updateToolbar = vm.runInNewContext(`(${toolbarSrc})`, ctx);
  return { B, sent, calls, watchEl, els, el, ctx };
}

test('control owner=you grants input; other/agent/none revoke it', () => {
  const { B, ctx } = world();
  ctx.handleStreamMessage({ type: 'control', owner: 'you' });
  assert.equal(B.inputOwner, true);
  assert.equal(ctx.canInput(), true);
  for (const owner of ['other', 'agent', 'none']) {
    ctx.handleStreamMessage({ type: 'control', owner });
    assert.equal(ctx.canInput(), false, `owner=${owner} must not input`);
  }
});

test('agent owner marks the lock state', () => {
  const { B, ctx } = world();
  ctx.handleStreamMessage({ type: 'control', owner: 'agent' });
  assert.equal(B.agentLock, true);
  assert.equal(B.inputOwner, false);
  ctx.handleStreamMessage({ type: 'control', owner: 'you' });
  assert.equal(B.agentLock, false);
});

test('watch bar appears only while watching without the lease', () => {
  const { B, watchEl, ctx } = world();
  ctx.handleStreamMessage({ type: 'control', owner: 'other' });
  assert.equal(watchEl.classes.has('show'), true);
  assert.match(watchEl.innerHTML, /Take over/);
  ctx.handleStreamMessage({ type: 'control', owner: 'you' });
  assert.equal(watchEl.classes.has('show'), false);
  B.status = 'closed';
  ctx.renderWatch();
  ctx.handleStreamMessage({ type: 'control', owner: 'other' });
  assert.equal(watchEl.classes.has('show'), false);
});

test('agent lock switches the watch bar copy', () => {
  const { watchEl, ctx } = world();
  ctx.handleStreamMessage({ type: 'control', owner: 'agent' });
  assert.match(watchEl.innerHTML, /agent is holding control/);
});

test('the takeover affordance sends a takeover claim, not a reload', () => {
  const browser = browserSrc;
  assert.match(browser, /data-action="browser-takeover"/);
  assert.match(browser, /sendCtl\(\{ type: "takeover" \}\)/);
});

test('the lease grant is the sizing push — only for non-guest owners', () => {
  const { calls, B, ctx } = world();
  ctx.handleStreamMessage({ type: 'control', owner: 'you' });
  assert.equal(calls.applyDeviceChoice, 1, 'first owner pushes pane sizing');
  calls.applyDeviceChoice = 0;
  B.inputOwner = false;
  B.viewportGuest = true;
  ctx.handleStreamMessage({ type: 'control', owner: 'you' });
  assert.equal(calls.applyDeviceChoice, 0, 'a guest winner keeps the held viewport');
});

test('losing the lease stops the resize watcher', () => {
  const { calls, B, ctx } = world();
  B.inputOwner = true;
  ctx.handleStreamMessage({ type: 'control', owner: 'other' });
  assert.equal(calls.stopResizeWatch, 1);
});

test('a watcher cannot drive: toolbar affordances are disabled', () => {
  const { B, el, ctx } = world();
  B.inputOwner = false;
  ctx.updateToolbar();
  for (const sel of [
    '[data-action="browser-back"]', '[data-action="browser-forward"]',
    '[data-action="browser-reload"]', '#browser-url',
    '[data-action="browser-annotate"]', '[data-action="browser-device"]',
    '#browser-profile', '[data-action="browser-devtools"]',
    '#browser-devtools-input', '[data-action="browser-devtools-run"]',
  ]) {
    assert.equal(el(sel).disabled, true, `${sel} must be disabled while watching`);
  }
  B.inputOwner = true;
  B.nav = { canGoBack: true, canGoForward: true };
  ctx.updateToolbar();
  for (const sel of [
    '[data-action="browser-back"]', '[data-action="browser-forward"]',
    '[data-action="browser-reload"]', '#browser-url',
    '[data-action="browser-annotate"]', '[data-action="browser-device"]',
    '#browser-profile', '[data-action="browser-devtools"]',
    '#browser-devtools-input', '[data-action="browser-devtools-run"]',
  ]) {
    assert.equal(el(sel).disabled, false, `${sel} must be enabled for the controller`);
  }
});
