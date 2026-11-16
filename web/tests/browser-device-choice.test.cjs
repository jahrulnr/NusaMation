const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The agent-set device choice ("") must survive leaving and returning to a
// room, and a re-attach must never push a pane-size emulate over it — the
// forced resize is what collapsed the agent's emulation mid-task.

const browserSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const choiceSrc = slice(browserSrc, 'function applyDeviceChoice()', '\nfunction updateDeviceBar()');
const switchSrc = slice(browserSrc, 'onComposerSwitch((key)', '\nexport function openBrowserPane');

function choiceWorld(deviceChoice, extra = {}) {
  const calls = { emulate: 0, resize: 0 };
  const B = {
    inputOwner: true, viewportGuest: false, deviceChoice,
    socket: { readyState: 1 /* OPEN */ },
    ...extra,
  };
  const ctx = {
    B,
    WebSocket: { OPEN: 1 },
    viewBox: () => ({ w: 400, h: 300 }),
    sendCtl(msg) { if (msg.type === 'emulate') calls.emulate++; if (msg.type === 'resize') calls.resize++; },
    startResizeWatch() { calls.watch = (calls.watch || 0) + 1; },
    stopResizeWatch() {},
  };
  ctx.applyDeviceChoice = vm.runInNewContext(`(${choiceSrc})`, ctx);
  return { B, calls, ctx };
}

// Per-room deviceChoice (and every other per-tab field) survives room switches
// structurally now: each room's tabs keep their own state objects, and the
// switch handler never writes another room's state.
function switchWorld() {
  const B = { recReady: null, recNote: '', nav: { url: '' } };
  const P = { open: true, conversationId: 'convA' };
  const paneTabsByConv = new Map();
  const browserTab = { id: 'tab_1', kind: 'browser', convId: 'convA', state: B };
  paneTabsByConv.set('convA', { tabs: [browserTab], active: 'tab_1', browserTab, idle: null });
  const calls = { close: 0, tabs: 0, api: [] };
  const ctx = {
    B,
    P,
    paneTabsByConv,
    updateDeviceBar() {},
    renderRecBar() {},
    renderPaneTabs() { calls.tabs++; },
    closeBrowserPane() { calls.close++; },
    saveBrowserOpen() {},
    saveLastURL() {},
    usableBrowserURL: (u) => u,
    onComposerSwitch(cb) { ctx.fire = cb; },
    api(path, opts) { calls.api.push({ path, opts }); return Promise.resolve({ ok: true }); },
  };
  vm.runInNewContext(switchSrc, ctx);
  return { B, P, calls, ctx, paneTabsByConv };
}

test('an agent-set viewport is never stomped by a (re)attach', () => {
  const { B, calls, ctx } = choiceWorld('', { viewportGuest: true });
  ctx.applyDeviceChoice();
  assert.equal(calls.emulate, 0);
  assert.equal(calls.resize, 0);
  assert.equal(B.viewportGuest, true, 'no implicit takeover without a chip click');
});

test('relative and presets still push their emulation', () => {
  const rel = choiceWorld('relative');
  rel.ctx.applyDeviceChoice();
  assert.equal(rel.calls.emulate, 1);
  const preset = choiceWorld('mobile');
  preset.ctx.applyDeviceChoice();
  assert.equal(preset.calls.emulate, 1);
});

test('a room switch hides the pane but keeps the room\'s tab states', () => {
  const { B, P, calls, ctx, paneTabsByConv } = switchWorld();
  ctx.fire('convB');
  assert.equal(calls.close, 1);
  assert.equal(P.conversationId, 'convB');
  assert.equal(paneTabsByConv.get('convA').browserTab.state, B, 'the state object survives the switch');
});

test('a draft pane carries its tabs into the adopting conversation', () => {
  const { P, ctx, paneTabsByConv } = switchWorld();
  P.conversationId = '';
  const browserTab = { id: 'tab_1', kind: 'browser', convId: '', state: {} };
  const toolTab = { id: 'tab_2', kind: 'terminal', convId: '' };
  const st = { tabs: [browserTab, toolTab], active: 'tab_1', browserTab, idle: null };
  paneTabsByConv.set('', st);
  ctx.fire('convB'); // convB has no tab set of its own — adoption applies
  assert.equal(paneTabsByConv.get('convB'), st, 'the whole tab set moves to the new room');
  assert.equal(browserTab.convId, 'convB');
  assert.equal(toolTab.convId, 'convB');
});

test('a draft pane does not clobber a room that already has tabs', () => {
  const { P, calls, ctx, paneTabsByConv } = switchWorld();
  P.conversationId = '';
  const browserTab = { id: 'tab_1', kind: 'browser', convId: '', state: {} };
  const st = { tabs: [browserTab], active: 'tab_1', browserTab, idle: null };
  paneTabsByConv.set('', st);
  const convATabs = paneTabsByConv.get('convA');
  ctx.fire('convA');
  assert.equal(paneTabsByConv.get('convA'), convATabs, 'convA keeps its own tab set');
  assert.equal(paneTabsByConv.get(''), st, 'the draft set stays keyed to the draft');
  assert.equal(calls.close, 1, 'the pane takes the normal hide path');
});

test('draft adoption claims draft-owned browser sessions for the new room', () => {
  const { P, calls, ctx, paneTabsByConv } = switchWorld();
  P.conversationId = '';
  const browserTab = {
    id: 'tab_1', kind: 'browser', convId: '',
    state: { session: { id: 'bs_draft', conversationId: '' } },
  };
  const st = { tabs: [browserTab], active: 'tab_1', browserTab, idle: null };
  paneTabsByConv.set('', st);
  ctx.fire('convB');
  assert.equal(browserTab.state.session.conversationId, 'convB');
  assert.equal(calls.api.length, 1);
  assert.equal(calls.api[0].path, '/browser/sessions/bs_draft/claim');
  assert.equal(calls.api[0].opts.method, 'POST');
  assert.equal(calls.api[0].opts.body.conversationId, 'convB');
});

test('already-owned sessions are not claimed again', () => {
  const { P, calls, ctx, paneTabsByConv } = switchWorld();
  P.conversationId = '';
  const browserTab = {
    id: 'tab_1', kind: 'browser', convId: '',
    state: { session: { id: 'bs_owned', conversationId: 'convOther' } },
  };
  const st = { tabs: [browserTab], active: 'tab_1', browserTab, idle: null };
  paneTabsByConv.set('', st);
  ctx.fire('convB');
  assert.equal(calls.api.length, 0);
});

test('draft adoption clears a ready recording for the new room', () => {
  const { B, P, ctx, paneTabsByConv } = switchWorld();
  P.conversationId = '';
  paneTabsByConv.set('', { tabs: [], active: 'home', browserTab: null, idle: null });
  B.recReady = { id: 'rec_1' };
  B.recNote = 'x';
  ctx.fire('convB');
  assert.equal(B.recReady, null);
  assert.equal(B.recNote, '');
});

test('a closed pane just re-binds the room without touching tabs', () => {
  const { P, calls, ctx } = switchWorld();
  P.open = false;
  ctx.fire('convB');
  assert.equal(calls.close, 0);
  assert.equal(P.conversationId, 'convB');
});
