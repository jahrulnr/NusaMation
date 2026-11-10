const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// A second client attaching to an in-use browser session must adopt the
// established viewport (meta.viewportHeld) instead of emulating its own pane
// size — the forced resize is what painted the "mirror" transitional frame.

const browserSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const handleSrc = slice(browserSrc, 'function handleStreamMessage(', '\nfunction renderFrame(');
const choiceSrc = slice(browserSrc, 'function applyDeviceChoice()', '\nfunction updateDeviceBar()');

function world() {
  const calls = { emulate: 0, resize: 0 };
  const B = {
    meta: null, sawMeta: false, viewportGuest: false, holdViewport: false,
    deviceChoice: 'relative', pendingPointer: null, observer: null,
    socket: { readyState: 1 /* OPEN */ }, session: null,
  };
  const ctx = {
    B,
    WebSocket: { OPEN: 1 },
    viewBox: () => ({ w: 400, h: 300 }),
    applyMeta() {},
    setStatus() {},
    placeAgentPointer() {},
    updateDeviceBar() {},
    sendCtl(msg) { if (msg.type === 'emulate') calls.emulate++; if (msg.type === 'resize') calls.resize++; },
    startResizeWatch() {},
    stopResizeWatch() {},
    clamp: (v, lo, hi) => Math.max(lo, Math.min(v, hi)),
  };
  ctx.handleStreamMessage = vm.runInNewContext(`(${handleSrc})`, ctx);
  ctx.applyDeviceChoice = vm.runInNewContext(`(${choiceSrc})`, ctx);
  return { B, calls, ctx };
}

const meta = (extra = {}) => ({ type: 'meta', deviceWidth: 800, deviceHeight: 600, ...extra });

test('a first-meta without viewportHeld emulates the pane size', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta());
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 1);
});

test('viewportHeld makes the attach a guest: no emulate is sent', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  assert.equal(B.viewportGuest, true);
  assert.equal(calls.emulate, 0);
});

test('only the first meta decides — later metas never emulate', () => {
  const { calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  ctx.handleStreamMessage(meta());
  assert.equal(calls.emulate, 0);
});

test('an explicit applyDeviceChoice retakes viewport control', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  ctx.applyDeviceChoice();
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 1);
});

test('an agent drive (holdViewport) blocks the attach emulate', () => {
  const { B, calls, ctx } = world();
  B.holdViewport = true;
  ctx.handleStreamMessage(meta());
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 0);
});
