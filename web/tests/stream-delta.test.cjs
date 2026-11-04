const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'app.js'), 'utf8');
const start = app.indexOf('  function applyStreamDelta(');
const end = app.indexOf('\n  const pendingStreamMessages', start);
assert.ok(start >= 0 && end > start);
const apply = vm.runInNewContext(`(${app.slice(start, end).trim()})`, { streamEncoder: new TextEncoder(), streamDecoder: new TextDecoder() });
test('snapshot and deltas reconcile at UTF-8 byte offsets', () => {
  const msg = { text: 'hello 🙂', status: 'streaming' };
  assert.equal(apply(msg, { delta: '🙂', offsetBytes: 6 }), 'duplicate');
  assert.equal(apply(msg, { delta: ' é', offsetBytes: 10 }), 'changed');
  assert.equal(msg.text, 'hello 🙂 é');
  assert.equal(apply(msg, { delta: ' é!', offsetBytes: 10 }), 'changed');
  assert.equal(msg.text, 'hello 🙂 é!');
  assert.equal(apply(msg, { delta: 'missing', offsetBytes: 999 }), 'gap');
});
test('older backends can still emit deltas without offsets', () => {
  const msg = { text: 'a' };
  assert.equal(apply(msg, { delta: 'b' }), 'changed');
  assert.equal(msg.text, 'ab');
});
