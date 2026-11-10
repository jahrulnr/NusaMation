const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The plan card is a dock floating above the composer, not a chat row. It is
// collapsed to a one-line status strip by default and expands on click —
// and being outside the scroll list means a lazy history prepend can never
// scroll it away.

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const slice = (from, to) => {
  const s = src.indexOf(from), e = src.indexOf(to, s + from.length);
  assert.ok(s >= 0 && e > s, `slice ${from} not found`);
  return src.slice(s, e).replace(/^export /gm, '');
};

const renderBody = slice('export function renderChat', '\nexport function currentApprovals');
// planEntries + renderPlanDock + planCard live as one block before systemHTML.
const dockBody = slice('export function planEntries', '\nexport function systemHTML');

function makeEl() {
  const node = {
    dataset: {}, children: [], _parent: null, _html: '', _innerHTML: '',
    get firstElementChild() { return this.children[0] || null; },
    get nextElementSibling() {
      if (!this._parent) return null;
      return this._parent.children[this._parent.children.indexOf(this) + 1] || null;
    },
    set innerHTML(html) {
      this._innerHTML = html;
      const child = makeEl();
      child._html = html;
      child._parent = this;
      this.children = html ? [child] : [];
    },
    get innerHTML() { return this._innerHTML; },
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
  return node;
}

function world({ dockHtml = null } = {}) {
  const chat = makeEl();
  const dock = makeEl();
  const scroll = { scrollTop: 0, scrollHeight: 1000, clientHeight: 500 };
  const S = {
    scrollPinned: false, loadingConversation: false, loadError: '',
    messages: [{ id: 'm1', role: 'assistant', seq: 1, createdAt: 1 }],
    turns: {}, current: { id: 'c1', plan: [{ status: 'in_progress', title: 'do it' }] },
    active: null, approvalsById: new Map(), planExpanded: false,
  };
  const ctx = {
    S,
    document: { activeElement: null, createElement: () => makeEl() },
    $(sel) { return sel === '#chat' ? chat : sel === '#chat-scroll' ? scroll : sel === '#plan-dock' ? dock : null; },
    $$() { return []; },
    closeDetachedPopovers() {}, welcomeHTML: () => '', enhanceMarkdown() {},
    orderMessages: (msgs) => msgs.slice().sort((a, b) => a.seq - b.seq),
    dayLabel: () => 'TODAY', esc: (s) => String(s), icon: () => '',
    activityHTML: () => '<div></div>',
    messageHTML: (m) => `<div data-msg-id="${m.id}"></div>`,
    currentApprovals: () => [], approvalCard: () => '', typingHTML: () => '<div></div>',
    renderHistoryControl() {}, snapshotMedia: () => null, restoreMedia() {},
    requestAnimationFrame() {},
  };
  ctx.renderChat = vm.runInNewContext(`(${renderBody})`, ctx);
  vm.runInNewContext(dockBody, ctx); // binds planEntries, renderPlanDock, planCard
  return { chat, dock, S, ctx };
}

test('chat rows never carry the plan — it lives in the dock now', () => {
  const { chat, ctx } = world();
  ctx.renderChat();
  assert.equal(chat.children.some((el) => el.dataset.renderKey === 'plan'), false);
});

test('renderPlanDock paints a collapsed strip; expanded shows the checklist', () => {
  const { dock, S, ctx } = world();
  ctx.renderPlanDock();
  assert.match(dock.innerHTML, /data-action="toggle-plan"/);
  assert.match(dock.innerHTML, /0\/1/);
  assert.doesNotMatch(dock.innerHTML, /plan-list/, 'collapsed hides the steps');

  S.planExpanded = true;
  ctx.renderPlanDock();
  assert.match(dock.innerHTML, /plan-list/);
  assert.match(dock.innerHTML, /do it/);
  assert.match(dock.innerHTML, /aria-expanded="true"/);
});

test('an empty plan empties the dock', () => {
  const { dock, S, ctx } = world();
  S.current.plan = [];
  ctx.renderPlanDock();
  assert.equal(dock.innerHTML, '');
  assert.equal(dock.children.length, 0);
});

test('toggle-plan handler flips the flag and repaints the dock', () => {
  const events = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'events.js'), 'utf8');
  assert.match(events, /case "toggle-plan": S\.planExpanded = !S\.planExpanded; renderPlanDock\(\)/);
  const shell = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'shell.js'), 'utf8');
  assert.ok(shell.indexOf('id="plan-dock"') < shell.indexOf('id="composer-wrap"'), 'dock sits above the composer');
});
