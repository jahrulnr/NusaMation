const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '../static/components/browser.js'), 'utf8');
const section = (start, end) => src.slice(src.indexOf(start), src.indexOf(end, src.indexOf(start)));

test('touch keyboard forwards committed text once and waits for composition', () => {
  const sent = [];
  const ctx = { canInput: () => true, sendCtl: (value) => sent.push(value) };
  vm.runInNewContext(section('function onTouchInput(', '\nfunction flushMove('), ctx);
  const input = { value: '你好' };
  ctx.onTouchInput({ currentTarget: input, isComposing: true });
  assert.equal(sent.length, 0);
  ctx.onTouchInput({ currentTarget: input, isComposing: false });
  ctx.onTouchInput({ currentTarget: input, isComposing: false });
  assert.deepEqual(JSON.parse(JSON.stringify(sent)), [{ type: 'text', text: '你好' }]);
  assert.equal(input.value, '');
});

test('touch keyboard lets Android composition keys reach input events', () => {
  const sent = [];
  const ctx = { B: { annotate: false }, $: () => null, canInput: () => true, sendCtl: (value) => sent.push(value) };
  vm.runInNewContext(section('function onViewKey(', '\nfunction viewBox('), ctx);
  for (const key of ['Unidentified', 'Process', 'a']) {
    let prevented = false;
    ctx.onViewKey({ key, target: { id: 'browser-touch-input' }, stopPropagation() {}, preventDefault() { prevented = true; } });
    assert.equal(prevented, false, key);
  }
  assert.equal(sent.length, 0);
});
