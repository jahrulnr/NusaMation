const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The plan card is a current-work artifact: it belongs at the tail next to
// approvals and the typing indicator. At the top of the transcript a lazy
// history prepend pushed it above the first message and out of the restored
// scroll anchor — the card looked like it flashed during load then vanished.

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const start = src.indexOf('export function renderChat');
const end = src.indexOf('\nexport function currentApprovals');
assert.ok(start >= 0 && end > start, 'renderChat slice not found');
const code = src.slice(start, end).replace('export ', '');

function makeEl() {
  const node = {
    dataset: {}, children: [], _parent: null, _html: '',
    get firstElementChild() { return this.children[0] || null; },
    get nextElementSibling() {
      if (!this._parent) return null;
      return this._parent.children[this._parent.children.indexOf(this) + 1] || null;
    },
    insertBefore(child, cursor) {
      child.remove();
      const i = cursor ? this.children.indexOf(cursor) : this.children.length;
      this.children.splice(i < 0 ? this.children.length : i, 0, child);
      child._parent = this;
    },
    remove() {
      if (!this._parent) return;
      const i = this._parent.children.indexOf(this);
      if (i >= 0) this._parent.children.splice(i, 1);
      this._parent = null;
    },
    contains() { return false; },
    matches() { return false; },
    focus() {},
  };
  Object.defineProperty(node, 'innerHTML', {
    set(html) {
      const child = makeEl();
      child._html = html;
      child._parent = this;
      this.children = [child];
    },
  });
  return node;
}

function world({ active = null, approvals = [] } = {}) {
  const chat = makeEl();
  const scroll = { scrollTop: 0, scrollHeight: 1000, clientHeight: 500 };
  const S = {
    scrollPinned: false,
    loadingConversation: false,
    loadError: '',
    messages: [
      { id: 'm2', role: 'assistant', seq: 2, createdAt: 2 },
      { id: 'm3', role: 'assistant', seq: 3, createdAt: 3 },
    ],
    turns: {},
    current: { id: 'c1', plan: [{ status: 'completed', title: 'step' }] },
    active,
    approvalsById: new Map(approvals.map((a) => [a.id, a])),
  };
  const ctx = {
    S,
    document: { activeElement: null, createElement: () => makeEl() },
    $(sel) { return sel === '#chat' ? chat : sel === '#chat-scroll' ? scroll : null; },
    $$() { return []; },
    closeDetachedPopovers() {},
    welcomeHTML: () => '',
    enhanceMarkdown() {},
    orderMessages: (msgs) => msgs.slice().sort((a, b) => a.seq - b.seq),
    dayLabel: () => 'TODAY',
    esc: (s) => String(s),
    icon: () => '',
    activityHTML: () => '<div></div>',
    messageHTML: (m) => `<div data-msg-id="${m.id}"></div>`,
    currentApprovals: () => approvals,
    approvalCard: (a) => `<div data-approval="${a.id}"></div>`,
    typingHTML: () => '<div></div>',
    planEntries: (p) => (Array.isArray(p) ? p : []),
    planCard: (entries) => `<div class="plan-card">${entries.length}</div>`,
    renderHistoryControl() {},
    snapshotMedia: () => null,
    restoreMedia() {},
    requestAnimationFrame() {},
  };
  ctx.renderChat = vm.runInNewContext(`(${code})`, ctx);
  return { chat, S, renderChat: ctx.renderChat };
}

const keys = (chat) => chat.children.map((el) => el.dataset.renderKey);

test('the plan row renders at the tail, after every message', () => {
  const { chat, renderChat } = world();
  renderChat();
  const order = keys(chat);
  const planIdx = order.indexOf('plan');
  assert.ok(planIdx > -1, 'plan row rendered');
  for (const [i, key] of order.entries()) {
    if (key.startsWith('message:') || key.startsWith('date:')) assert.ok(i < planIdx, `${key} rendered after plan`);
  }
});

test('prepending older history keeps the plan node at the tail', () => {
  const { chat, S, renderChat } = world();
  renderChat();
  const before = chat.children.find((el) => el.dataset.renderKey === 'plan');
  assert.ok(before, 'plan row rendered');
  S.messages = [{ id: 'm1', role: 'assistant', seq: 1, createdAt: 1 }, ...S.messages];
  renderChat();
  const order = keys(chat);
  assert.equal(order[order.length - 1], 'plan');
  const after = chat.children[chat.children.length - 1];
  assert.equal(after, before, 'plan row is reused, not rebuilt');
});

test('plan sits above approvals and the typing indicator', () => {
  const approvals = [{ id: 'a1', conversationId: 'c1', status: 'pending' }];
  const { chat, renderChat } = world({ approvals });
  renderChat();
  const order = keys(chat);
  assert.ok(order.indexOf('plan') < order.indexOf('approval:a1'), 'plan before approval card');

  const { chat: chat2, renderChat: renderChat2 } = world({ active: { running: true, turnId: 't1' } });
  renderChat2();
  const order2 = keys(chat2);
  assert.ok(order2.indexOf('plan') < order2.indexOf('typing'), 'plan before typing indicator');
});
