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
const switchSrc = slice(browserSrc, 'const deviceChoiceByConv', '\nexport function openBrowserPane');

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

function switchWorld(deviceChoice = 'relative') {
  const B = { open: true, conversationId: 'convA', deviceChoice, nav: {} };
  const calls = { close: 0 };
  const ctx = {
    B,
    updateDeviceBar() {},
    closeBrowserPane() { calls.close++; },
    saveBrowserOpen() {},
    saveLastURL() {},
    usableBrowserURL: (u) => u,
    onComposerSwitch(cb) { ctx.fire = cb; },
  };
  vm.runInNewContext(switchSrc, ctx);
  return { B, calls, ctx };
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

test('device choice stashes and restores per conversation', () => {
  const { B, ctx } = switchWorld('');
  ctx.fire('convB');
  assert.equal(B.deviceChoice, 'relative', 'unvisited room gets the default');
  ctx.fire('convA');
  assert.equal(B.deviceChoice, '', 'the agent-set viewport is restored');
});

test('a user chip choice is per-room, not leaked', () => {
  const { B, ctx } = switchWorld('mobile');
  ctx.fire('convB');
  assert.equal(B.deviceChoice, 'relative');
  ctx.fire('convA');
  assert.equal(B.deviceChoice, 'mobile');
});

test('a draft pane carries its choice into the adopting conversation', () => {
  const { B, ctx } = switchWorld('mobile');
  B.conversationId = '';
  ctx.fire('convA');
  assert.equal(B.deviceChoice, 'mobile', 'adoption keeps the live choice');
  ctx.fire('convB');
  ctx.fire('convA');
  assert.equal(B.deviceChoice, 'mobile', 'and the carried choice is remembered');
});

test('a closed pane still keeps choices per room', () => {
  const { B, ctx } = switchWorld('');
  B.open = false;
  ctx.fire('convB');
  assert.equal(B.deviceChoice, 'relative');
  ctx.fire('convA');
  assert.equal(B.deviceChoice, '');
});
