const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// "Load earlier messages" is pagination state for S.messages — every path
// that abandons the transcript (new draft, delete, archive, popstate, ws,
// conversation.deleted) routes through switchComposer. Before the reset
// lived there, only loadConversation cleared the flags, so a stale
// historyHasMore ghosted the button onto fresh drafts.

const stateSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'state.js'), 'utf8');
const convSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'conversations.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const switchSrc = slice(stateSrc, 'export function switchComposer', '\nexport function moveComposer').replace(/^export /, '');
const controlSrc = slice(convSrc, 'export function renderHistoryControl', '\n}\n').replace(/^export /, '') + '\n}';

function switchWorld() {
  const fired = [];
  const S = { historyHasMore: true, historyLoading: true, historyError: 'x' };
  const ctx = {
    S,
    composerKey: '',
    composerSwitchListeners: new Set([fn => fired.push(fn)]),
  };
  ctx.switchComposer = vm.runInNewContext(`(${switchSrc})`, ctx);
  return { S, ctx, fired };
}

test('switchComposer clears stale history pagination state', () => {
  const { S, ctx, fired } = switchWorld();
  ctx.switchComposer('convB');
  assert.equal(ctx.composerKey, 'convB');
  assert.equal(S.historyHasMore, false);
  assert.equal(S.historyLoading, false);
  assert.equal(S.historyError, '');
  assert.equal(fired.length, 1);
});

test('the draft path cannot inherit the button from a long room', () => {
  const { ctx } = switchWorld();
  ctx.switchComposer('');
  assert.equal(ctx.composerKey, '');
});

function controlWorld(extra = {}) {
  const el = { innerHTML: 'sentinel' };
  const S = { historyHasMore: true, historyLoading: false, historyError: '', messages: [], ...extra };
  const ctx = { S, $: () => el };
  ctx.renderHistoryControl = vm.runInNewContext(`(${controlSrc})`, ctx);
  return { S, el, ctx };
}

test('the control never renders on an empty transcript', () => {
  const { el, ctx } = controlWorld({ messages: [] });
  ctx.renderHistoryControl();
  assert.equal(el.innerHTML, '', 'hasMore without messages shows nothing');
});

test('idle hasMore renders nothing — the scroll lazy-load covers it', () => {
  const { el, ctx } = controlWorld({ messages: [{ id: 'm1' }] });
  ctx.renderHistoryControl();
  assert.equal(el.innerHTML, '');
});

test('a failed fetch keeps a retry affordance the auto-load cannot give', () => {
  const { el, ctx } = controlWorld({ historyError: 'boom', messages: [{ id: 'm1' }] });
  ctx.renderHistoryControl();
  assert.match(el.innerHTML, /load-older/);
  assert.match(el.innerHTML, /Retry/);
});

test('an in-flight fetch shows the skeleton', () => {
  const { el, ctx } = controlWorld({ historyLoading: true, messages: [{ id: 'm1' }] });
  ctx.renderHistoryControl();
  assert.match(el.innerHTML, /history-skeleton/);
});
