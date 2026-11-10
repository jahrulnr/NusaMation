const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Incremental activity append: a new tool/thought must land ONLY at the tail
// of its own turn's group. Three guards stop a wrong injection: the render-
// key turnId prefix, prev.turnId equality, and lastElementChild === prev.
// Anything ambiguous falls back to the full row diff (return false).

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const s0 = src.indexOf('function appendActivityItem');
const e0 = src.indexOf('export function scheduleChatRender', s0);
assert.ok(s0 > 0 && e0 > s0);
const appendSrc = src.slice(s0, e0).trim();

function item(id) {
  return {
    dataset: { msgId: id }, children: [],
    get lastElementChild() { return this.children[this.children.length - 1] || null; },
    get firstElementChild() { return this.children[0] || null; },
    appendChild(n) { this.children.push(n); },
    closest() { return null; },
  };
}

function world() {
  const calls = { md: 0 };
  const items1 = item('items'), items2 = item('items');
  const groups = {
    'activity:t1:': { querySelector: (s) => (s === '.activity-items' ? items1 : s === '.activity-count' ? { textContent: '' } : null) },
    'activity:t2:': { querySelector: (s) => (s === '.activity-items' ? items2 : s === '.activity-count' ? { textContent: '' } : null) },
  };
  const S = { messages: [], turns: {} };
  const ctx = {
    S,
    document: { createElement: () => { const w = item('w'); Object.defineProperty(w, 'innerHTML', { set(h) { w.children = [{ _h: h, dataset: {} }]; } }); return w; } },
    $$(sel, root) {
      const m = sel.match(/activity:(t\d+):/);
      return m && groups[`activity:${m[1]}:`] ? [groups[`activity:${m[1]}:`]] : [];
    },
    $(sel, root) { return root?.querySelector ? root.querySelector(sel) : null; },
    CSS: { escape: (s) => s },
    orderMessages: (msgs) => msgs.slice().sort((a, b) => a.seq - b.seq),
    messageHTML: (m) => `<div data-msg-id="${m.id}"></div>`,
    enhanceMarkdown() { calls.md++; },
    parseJSON: (s) => { try { return JSON.parse(s); } catch { return {}; } },
  };
  ctx.appendActivityItem = vm.runInNewContext(`(${appendSrc})`, ctx);
  return { S, ctx, items1, items2, calls };
}

const tool = (id, turnId, seq) => ({ id, turnId, kind: 'tool', role: 'assistant', seq, content: '{}' });

test('a tail tool appends into its own group', () => {
  const { S, ctx, items1, calls } = world();
  S.messages = [tool('a', 't1', 1), tool('b', 't1', 2)];
  items1.children = [{ dataset: { msgId: 'a' } }];
  assert.equal(ctx.appendActivityItem({}, S.messages[1]), true);
  assert.equal(items1.children.length, 2);
  assert.equal(calls.md, 1);
});

test('a new turn never injects into the previous turn\'s group', () => {
  const { S, ctx, items1, items2 } = world();
  S.messages = [tool('a', 't1', 1), tool('b', 't2', 2)];
  items1.children = [{ dataset: { msgId: 'a' } }];
  items2.children = [];
  // prev (a, t1) is tool-kind but turnId differs → hard bail before any group check
  assert.equal(ctx.appendActivityItem({}, S.messages[1]), false);
  assert.equal(items1.children.length, 1);
  assert.equal(items2.children.length, 0);
});

test('the second group in one turn gets the append, not the first', () => {
  const { S, ctx, items1 } = world();
  // One turn, second group's tail is 'y' — a tool following 'y' appends there.
  S.messages = [tool('x', 't1', 1), { id: 'txt', kind: 'text', role: 'assistant', seq: 2 }, tool('y', 't1', 3), tool('z', 't1', 4)];
  items1.children = [{ dataset: { msgId: 'y' } }];
  assert.equal(ctx.appendActivityItem({}, S.messages[3]), true);
  assert.equal(items1.children.length, 2);
});

test('a mid-sequence insert bails to the full diff', () => {
  const { S, ctx, items1 } = world();
  // 'late' sorts before 'a' but arrives after — the tail is 'a', not its prev.
  S.messages = [tool('a', 't1', 2), tool('late', 't1', 1)];
  items1.children = [{ dataset: { msgId: 'a' } }];
  assert.equal(ctx.appendActivityItem({}, S.messages[1]), false);
  assert.equal(items1.children.length, 1);
});

test('a tool following a text message starts a new group — no append', () => {
  const { S, ctx, items1 } = world();
  S.messages = [tool('a', 't1', 1), { id: 'txt', kind: 'text', role: 'assistant', seq: 2 }, tool('b', 't1', 3)];
  items1.children = [{ dataset: { msgId: 'a' } }];
  assert.equal(ctx.appendActivityItem({}, S.messages[2]), false);
  assert.equal(items1.children.length, 1);
});
