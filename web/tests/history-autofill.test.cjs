const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// A tool-heavy turn can fill the whole initial message window with collapsed
// tool/thought rows. Nothing scrolls, so the near-top trigger never fires and
// "scroll up" was a dead end. Autofill must page until the transcript can
// scroll, and stop at a bounded number of pages.

const convSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'conversations.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const blockSrc = slice(convSrc, 'const HISTORY_AUTOFILL_MAX', 'export async function loadConversation')
  .replace(/^export /gm, '');

function world({ scrollHeight = 400, clientHeight = 800, ...state } = {}) {
  const calls = { older: 0, control: 0 };
  const sc = { scrollTop: 0, scrollHeight, clientHeight };
  const S = {
    current: { id: 'c1' },
    historyHasMore: true,
    historyLoading: false,
    historyError: '',
    messages: [{ id: 'm1' }],
    historyAutoPages: 0,
    historyStuck: false,
    ...state,
  };
  const ctx = {
    S,
    $: (sel) => (sel === '#chat-scroll' ? sc : null),
    loadOlderMessages() { calls.older++; },
    renderHistoryControl() { calls.control++; },
  };
  vm.runInNewContext(`${blockSrc}\nthis.autoLoadOlderMessages = autoLoadOlderMessages;`, ctx);
  return { S, calls, ctx };
}

test('a transcript that fits the viewport pages in older messages', () => {
  const { S, calls, ctx } = world();
  assert.equal(ctx.autoLoadOlderMessages(), true);
  assert.equal(calls.older, 1);
  assert.equal(S.historyAutoPages, 1);
});

test('a scrollable transcript is left to the near-top trigger', () => {
  const { calls, ctx } = world({ scrollHeight: 2000 });
  assert.equal(ctx.autoLoadOlderMessages(), false);
  assert.equal(calls.older, 0);
});

test('no history, messages, or a live fetch means no autofill', () => {
  for (const state of [
    { historyHasMore: false },
    { messages: [] },
    { historyLoading: true },
    { historyError: 'boom' },
    { current: null },
  ]) {
    const { calls, ctx } = world(state);
    assert.equal(ctx.autoLoadOlderMessages(), false, JSON.stringify(state));
    assert.equal(calls.older, 0, JSON.stringify(state));
  }
});

test('the page cap stops autofill and offers the manual control', () => {
  const { S, calls, ctx } = world({ historyAutoPages: 8 });
  assert.equal(ctx.autoLoadOlderMessages(), false);
  assert.equal(calls.older, 0);
  assert.equal(S.historyStuck, true);
  assert.equal(calls.control, 1);
  // A second call must not re-render the control on every frame.
  ctx.autoLoadOlderMessages();
  assert.equal(calls.control, 1);
});

test('the cap does not mark a scrollable transcript stuck', () => {
  const { S, calls, ctx } = world({ historyAutoPages: 8, scrollHeight: 2000 });
  assert.equal(ctx.autoLoadOlderMessages(), false);
  assert.equal(S.historyStuck, false);
  assert.equal(calls.control, 0);
});
