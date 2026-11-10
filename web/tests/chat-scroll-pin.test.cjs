const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Chat scroll-follow: a user scrolling UP must release the pinned tail-follow
// no matter how small the notch — the old distance-only rule (140px) kept a
// slow scroll "pinned", so every streamed chunk snapped the view back down.
// Re-pinning happens only when the user returns within ~40px of the tail.

const shellSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'shell.js'), 'utf8');

const slice = (src, startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, `missing end for ${startMarker}`);
  return src.slice(start, end).trim();
};

const blockSrc = slice(shellSrc, '// Scroll-follow state', 'export function renderShell()').replace(/\bexport /g, '');

function world(now = 10000) {
  const calls = { older: 0 };
  const sc = { scrollTop: 0, scrollHeight: 5000, clientHeight: 800 };
  const S = { scrollPinned: true, historyHasMore: false, historyError: '', loadingConversation: false };
  const ctx = {
    S,
    $: (sel) => (sel === '#chat-scroll' ? sc : null),
    Date: { now: () => now },
    loadOlderMessages() { calls.older++; },
  };
  vm.runInNewContext(`${blockSrc};this.setScrollAt = (v) => { userScrollAt = v; };`, ctx);
  return { sc, S, calls, ctx };
}

test('a small upward scroll during streaming releases the follow pin', () => {
  const { sc, S, ctx } = world();
  const obj = ctx;
  // Pinned at the bottom, user wheels up one notch (~53px) — the old rule
  // kept this "pinned" because 140 > 53, and the next chunk yanked it back.
  sc.scrollTop = 4200;
  obj.noteChatScrollGesture();
  obj.handleChatScroll(); // baseline: still at the tail
  assert.equal(S.scrollPinned, true);
  sc.scrollTop = 4147; // one notch up
  obj.handleChatScroll();
  assert.equal(S.scrollPinned, false);
});

test('programmatic bottom-snap while streaming does not unpin', () => {
  const { sc, S, ctx } = world();
  const obj = ctx;
  sc.scrollTop = 4100;
  obj.noteChatScrollGesture();
  // A render pulls to the tail — a downward move, not a user scroll-up.
  sc.scrollTop = 4200;
  obj.handleChatScroll();
  assert.equal(S.scrollPinned, true);
});

test('re-pinning needs the tail itself, not just a downward nudge', () => {
  const { sc, S, ctx } = world();
  const obj = ctx;
  S.scrollPinned = false;
  sc.scrollTop = 3000;
  obj.noteChatScrollGesture();
  sc.scrollTop = 4050; // down, but still 150px short of the tail
  obj.handleChatScroll();
  assert.equal(S.scrollPinned, false);
  sc.scrollTop = 4161; // within 40px of 4200 — re-pin
  obj.handleChatScroll();
  assert.equal(S.scrollPinned, true);
});

test('a stale gesture leaves the pin untouched', () => {
  const { sc, S, ctx } = world();
  const obj = ctx;
  obj.setScrollAt(0); // gesture 10s ago — programmatic scroll only
  sc.scrollTop = 100;
  obj.handleChatScroll();
  assert.equal(S.scrollPinned, true);
});

test('near-top scrolling still offers history loading', () => {
  const { sc, S, calls, ctx } = world();
  const obj = ctx;
  S.historyHasMore = true;
  sc.scrollTop = 300;
  obj.noteChatScrollGesture();
  sc.scrollTop = 120;
  obj.handleChatScroll();
  assert.equal(calls.older, 1);
  assert.equal(S.scrollPinned, false);
});

test('the wheel handler unpins on deltaY<0 even without a scroll event', () => {
  assert.match(shellSrc, /deltaY < 0\) S\.scrollPinned = false/);
});
