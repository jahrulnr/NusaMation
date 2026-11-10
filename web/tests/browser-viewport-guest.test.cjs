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
    inputOwner: false, agentLock: false, nav: {}, navigating: false,
    status: 'live', gotFrame: true, open: true, annotate: false, session: null,
    socket: { readyState: 1 /* OPEN */ },
  };
  const ctx = {
    B,
    WebSocket: { OPEN: 1 },
    document: { activeElement: null },
    $: () => null,
    $$: () => [],
    viewBox: () => ({ w: 400, h: 300 }),
    applyMeta() {},
    setStatus() {},
    placeAgentPointer() {},
    updateDeviceBar() {},
    updateAnnotateUI() {},
    renderWatch() {},
    sendCtl(msg) { if (msg.type === 'emulate') calls.emulate++; if (msg.type === 'resize') calls.resize++; },
    startResizeWatch() {},
    stopResizeWatch() {},
    clamp: (v, lo, hi) => Math.max(lo, Math.min(v, hi)),
  };
  ctx.handleStreamMessage = vm.runInNewContext(`(${handleSrc})`, ctx);
  ctx.applyDeviceChoice = vm.runInNewContext(`(${choiceSrc})`, ctx);
  ctx.updateToolbar = () => {};
  return { B, calls, ctx };
}

const meta = (extra = {}) => ({ type: 'meta', deviceWidth: 800, deviceHeight: 600, ...extra });
const grant = { type: 'control', owner: 'you' };

test('the controller emulates the pane size once the lease lands', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta());
  assert.equal(calls.emulate, 0, 'meta alone does not size — the lease does');
  ctx.handleStreamMessage(grant);
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 1);
});

test('viewportHeld makes the attach a guest: no emulate is sent', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  ctx.handleStreamMessage(grant);
  assert.equal(B.viewportGuest, true);
  assert.equal(calls.emulate, 0);
});

test('only the first meta decides — later metas never emulate', () => {
  const { calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  ctx.handleStreamMessage(grant);
  ctx.handleStreamMessage(meta());
  assert.equal(calls.emulate, 0);
});

test('an explicit applyDeviceChoice retakes viewport control', () => {
  const { B, calls, ctx } = world();
  ctx.handleStreamMessage(meta({ viewportHeld: true }));
  ctx.handleStreamMessage(grant);
  ctx.applyDeviceChoice();
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 1);
});

test('an agent drive (holdViewport) blocks the grant emulate', () => {
  const { B, calls, ctx } = world();
  B.holdViewport = true;
  ctx.handleStreamMessage(meta());
  ctx.handleStreamMessage(grant);
  assert.equal(B.viewportGuest, false);
  assert.equal(calls.emulate, 0);
});
