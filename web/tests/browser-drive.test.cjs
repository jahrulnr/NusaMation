const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// browser.drive must only touch the pane of the room on screen (composerKey),
// and the pane may only attach a session owned by the driving conversation.
// S.current is not trusted as "on screen" — it can hold a stale detail object
// on the welcome screen or mid-load.

const sliceExport = (src, startMarker, endMarker = '\nexport ') => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  return src.slice(start, end < 0 ? undefined : end).trim().replace(/^export /, '');
};

const wsSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'ws.js'), 'utf8');
const handleEventSrc = sliceExport(wsSrc, 'export function handleEvent(');

function driveCalls({ composerKey, event }) {
  const calls = [];
  const handleEvent = vm.runInNewContext(`(${handleEventSrc})`, {
    composerKey,
    showAgentBrowser: (...args) => calls.push(args),
  });
  handleEvent(event);
  return calls;
}

const drive = (convId, extra = {}) => ({ type: 'browser.drive', conversationId: convId, data: { sessionId: 'bs_1', x: 10, y: 20, kind: 'click', op: 'click', ...extra } });

test('a drive for another room never reaches the pane', () => {
  const calls = driveCalls({ composerKey: 'cv_other', event: drive('cv_a') });
  assert.equal(calls.length, 0);
});

test('a drive for the room on screen forwards pointer and convId', () => {
  const calls = driveCalls({ composerKey: 'cv_a', event: drive('cv_a') });
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], 'bs_1');
  assert.equal(calls[0][1].x, 10);
  assert.equal(calls[0][1].y, 20);
  assert.equal(calls[0][1].kind, 'click');
  assert.equal(calls[0][2], 'click');
  assert.equal(calls[0][3], 'cv_a');
});

test('a drive on the new-conversation draft stays closed', () => {
  const calls = driveCalls({ composerKey: '', event: drive('cv_a') });
  assert.equal(calls.length, 0);
});

test('a drive without a conversation is ignored', () => {
  const calls = driveCalls({ composerKey: 'cv_a', event: { type: 'browser.drive', data: { sessionId: 'bs_1' } } });
  assert.equal(calls.length, 0);
});

test('older payloads that only carry tool still forward it', () => {
  const calls = driveCalls({ composerKey: 'cv_a', event: drive('cv_a', { op: undefined, tool: 'navigate' }) });
  assert.equal(calls.length, 1);
  assert.equal(calls[0][2], 'navigate');
});

const browserSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');
const showAgentBrowserSrc = sliceExport(browserSrc, 'export async function showAgentBrowser(', '\n/* ---------------- stream');

function paneWorld({ composerKey, conversationId, open, bootSkipKeys = [], apiSession, cachedMeta = null }) {
  const calls = { open: 0, attach: [], attachOrCreate: 0 };
  const B = { open, conversationId, session: null, deviceChoice: 'relative', pendingPointer: null, holdViewport: false };
  const showAgentBrowser = vm.runInNewContext(`(${showAgentBrowserSrc})`, {
    composerKey,
    B,
    bootSkip: new Set(bootSkipKeys),
    updateDeviceBar() {},
    stopResizeWatch() {},
    applyDeviceChoice() {},
    openBrowserPane(_url, _opts) { calls.open++; B.open = true; B.conversationId = composerKey; },
    attachOrCreate() { calls.attachOrCreate++; },
    placeAgentPointer() {},
    attach(s) { calls.attach.push(s); },
    api: async () => ({ session: apiSession }),
    cachedBrowserMeta: () => cachedMeta,
  });
  return { B, calls, run: (sessionId, convId, tool) => showAgentBrowser(sessionId, null, tool || 'click', convId) };
}

test('the pane attaches the driving room session', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: false, apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.equal(w.calls.open, 1);
  assert.equal(w.calls.attachOrCreate, 0); // the drive attach is deferred to the fetch, never raced
  assert.deepEqual(w.calls.attach.map((s) => s.id), ['bs_1']);
});

test('a session owned by another room is never attached', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: false, apiSession: { id: 'bs_x', conversationId: 'cv_other' } });
  await w.run('bs_x', 'cv_a');
  assert.equal(w.calls.attach.length, 0);
});

test('a session with no owner is not attached by a drive either', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: false, apiSession: { id: 'bs_x' } });
  await w.run('bs_x', 'cv_a');
  assert.equal(w.calls.attach.length, 0);
});

test('a pane bound to a different room than the drive is left alone', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_b', open: true, apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.equal(w.calls.attach.length, 0);
});

test('a drive for a room not on screen is ignored entirely', async () => {
  const w = paneWorld({ composerKey: 'cv_b', conversationId: 'cv_b', open: false, apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.equal(w.calls.open, 0);
  assert.equal(w.calls.attach.length, 0);
});

test('a pane the user closed is not reopened by later drives', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: false, bootSkipKeys: ['cv_a'], apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.equal(w.calls.open, 0);
  assert.equal(w.calls.attach.length, 0);
});

test('a persisted open:false also suppresses drive opens (post-refresh)', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: false, cachedMeta: { lastUrl: 'https://ex.test/', open: false }, apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.equal(w.calls.open, 0);
  assert.equal(w.calls.attach.length, 0);
});

test('the skip only blocks reopening — an open pane still attaches', async () => {
  const w = paneWorld({ composerKey: 'cv_a', conversationId: 'cv_a', open: true, bootSkipKeys: ['cv_a'], cachedMeta: { open: false }, apiSession: { id: 'bs_1', conversationId: 'cv_a' } });
  await w.run('bs_1', 'cv_a');
  assert.deepEqual(w.calls.attach.map((s) => s.id), ['bs_1']);
});

const openBrowserPaneSrc = sliceExport(browserSrc, 'export function openBrowserPane(', '\nexport function closeBrowserPane(');

function openWorld({ composerKey, bootSkipKeys = [] }) {
  const calls = { saveOpen: [], attachOrCreate: 0 };
  const B = { open: false, conversationId: 'stale', paneGen: 0, savedURL: 'x', restoreURL: 'y', nav: {} };
  const openBrowserPane = vm.runInNewContext(`(${openBrowserPaneSrc})`, {
    composerKey,
    B,
    bootSkip: new Set(bootSkipKeys),
    $: () => ({ dataset: {} }),
    usableBrowserURL: () => '',
    cachedBrowserMeta: () => null,
    saveBrowserOpen: (k, v) => calls.saveOpen.push([k, v]),
    buildPane() {},
    setPaneVisible() {},
    attachOrCreate() { calls.attachOrCreate++; },
  });
  return { B, calls, run: (url, opts) => openBrowserPane(url, opts) };
}

test('openBrowserPane binds the visible room, not a stale detail object', () => {
  const w = openWorld({ composerKey: 'cv_a', bootSkipKeys: ['cv_a'] });
  w.run();
  assert.equal(w.B.conversationId, 'cv_a');
  assert.deepEqual(w.calls.saveOpen, [['cv_a', true]]);
  assert.equal(w.calls.attachOrCreate, 1);
  assert.equal(w.B.open, true);
});

test('an explicit open lifts the close suppression for that room', () => {
  const w = openWorld({ composerKey: 'cv_a', bootSkipKeys: ['cv_a'] });
  // bootSkip lives in the vm sandbox above; recreate to inspect the same set.
  const bootSkip = new Set(['cv_a']);
  const B = { open: false, conversationId: '', paneGen: 0, savedURL: '', restoreURL: '', nav: {} };
  const openBrowserPane = vm.runInNewContext(`(${openBrowserPaneSrc})`, {
    composerKey: 'cv_a',
    B,
    bootSkip,
    $: () => ({ dataset: {} }),
    usableBrowserURL: () => '',
    cachedBrowserMeta: () => null,
    saveBrowserOpen() {},
    buildPane() {},
    setPaneVisible() {},
    attachOrCreate() {},
  });
  openBrowserPane();
  assert.equal(bootSkip.has('cv_a'), false);
  assert.equal(B.conversationId, 'cv_a');
});

test('deferAttach leaves the attach to the caller (drive path)', () => {
  const w = openWorld({ composerKey: 'cv_a' });
  w.run(undefined, { deferAttach: true });
  assert.equal(w.calls.attachOrCreate, 0);
});

const bootSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'boot.js'), 'utf8');
const applyBootstrapSrc = sliceExport(bootSrc, 'export function applyBootstrap(', '\nexport async function refreshBootstrap(');

test('bootstrap does not pretend the top conversation is on screen', () => {
  const S = { current: null, channels: [], agents: [] };
  const applyBootstrap = vm.runInNewContext(`(${applyBootstrapSrc})`, { S, setSelectedProject() {} });
  applyBootstrap({ conversations: [{ id: 'cv_a' }], agents: [] });
  assert.equal(S.current, null);
});
