const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Pure helpers the browser pane's recording + region-annotate UI leans on:
// chip elapsed time, rubber-band rect normalization, kind-aware chip labels
// and the one-line send-to-agent summary.

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'util.js'), 'utf8');
const sliceExport = (marker, endMarker = '\nexport ') => {
  const start = src.indexOf(marker);
  assert.ok(start >= 0, `missing ${marker}`);
  const end = src.indexOf(endMarker, start + marker.length);
  return src.slice(start, end < 0 ? undefined : end).trim().replace(/^export /, '');
};

// \n before ) so a trailing comment in the slice can't eat the wrapper.
const formatElapsed = vm.runInNewContext(`(${sliceExport('export function formatElapsed(')}\n)`, {});
const normalizeRect = vm.runInNewContext(`(${sliceExport('export function normalizeRect(')}\n)`, {});
const annotationTag = vm.runInNewContext(`(${sliceExport('export function annotationTag(')}\n)`, {});
const recordingBrief = vm.runInNewContext(`(${sliceExport('export function recordingBrief(')}\n)`, { formatElapsed });

test('elapsed renders mm:ss with clamping', () => {
  assert.equal(formatElapsed(0), '00:00');
  assert.equal(formatElapsed(999), '00:00');
  assert.equal(formatElapsed(59999), '00:59');
  assert.equal(formatElapsed(61000), '01:01');
  assert.equal(formatElapsed(3600e3), '60:00');
  assert.equal(formatElapsed(-5000), '00:00');
  assert.equal(formatElapsed(NaN), '00:00');
});

test('normalizeRect anchors top-left from any drag direction', () => {
  // vm-sandbox objects have a foreign prototype — compare shapes via JSON.
  const rect = (a, b) => JSON.stringify(normalizeRect(a, b));
  assert.equal(rect({ x: 10, y: 20 }, { x: 110, y: 70 }), JSON.stringify({ x: 10, y: 20, width: 100, height: 50 }));
  assert.equal(rect({ x: 110, y: 70 }, { x: 10, y: 20 }), JSON.stringify({ x: 10, y: 20, width: 100, height: 50 }));
  assert.equal(rect({ x: 5, y: 5 }, { x: 5, y: 5 }), JSON.stringify({ x: 5, y: 5, width: 0, height: 0 }));
});

test('annotationTag names element, region and page picks', () => {
  assert.equal(annotationTag({ tagName: 'button' }), 'button');
  assert.equal(annotationTag({ kind: 'element', tagName: 'input' }), 'input');
  assert.equal(annotationTag({}), 'element');
  assert.equal(annotationTag({ kind: 'region', rect: { x: 1, y: 2, width: 200.4, height: 100.6 } }), 'Region 200×101');
  assert.equal(annotationTag({ kind: 'region', rect: { x: 0, y: 0, width: 0, height: 0 } }), 'Region');
  assert.equal(annotationTag({ kind: 'page' }), 'Page');
});

test('recordingBrief summarizes counts and duration', () => {
  const meta = { startedAt: 1000, endedAt: 61000, counts: { events: 12, annotations: 1, errors: 0 } };
  assert.equal(recordingBrief(meta), 'QA recording attached: 12 events · 1 annotation · 0 errors in 01:00');
  assert.equal(recordingBrief({ counts: { events: 1, annotations: 0, errors: 2 } }, 5000),
    'QA recording attached: 1 event · 0 annotations · 2 errors');
});

// updateRecButtons is the single place rec-bar buttons are gated; a
// "finalizing" recording must keep every action disabled — the old inline
// loop set disabled=true and updateRecButtons immediately re-enabled them.
const browserSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser.js'), 'utf8');
const sliceFn = (marker) => {
  const start = browserSrc.indexOf(marker);
  assert.ok(start >= 0, `missing ${marker}`);
  const end = browserSrc.indexOf('\nfunction ', start + marker.length);
  return browserSrc.slice(start, end < 0 ? undefined : end).trim();
};

test('updateRecButtons honors busy, dead session and finalizing', () => {
  const mkBtn = (action) => ({ dataset: { action }, disabled: false });
  const buttons = [
    mkBtn('browser-rec-pause'), mkBtn('browser-rec-stop'),
    mkBtn('browser-rec-attach'), mkBtn('browser-rec-dismiss'),
  ];
  const bar = { hidden: false };
  const B = { recording: { status: 'finalizing' }, recBusy: false, session: { id: 's' }, status: 'live' };
  // \n before ) so a trailing comment in the slice can't eat the wrapper.
  const updateRecButtons = vm.runInNewContext(`(${sliceFn('function updateRecButtons()')}\n)`, {
    $: () => bar, $$: () => buttons, B,
  });
  updateRecButtons();
  assert.deepEqual(buttons.map(b => b.disabled), [true, true, true, false]);

  // Live recording, nothing pending: only a dead session disables actions,
  // and send/dismiss stay usable.
  B.recording.status = 'recording';
  updateRecButtons();
  assert.deepEqual(buttons.map(b => b.disabled), [false, false, false, false]);
  B.status = 'closed';
  updateRecButtons();
  assert.deepEqual(buttons.map(b => b.disabled), [true, true, false, false]);
  B.status = 'live'; B.recBusy = true;
  updateRecButtons();
  assert.deepEqual(buttons.map(b => b.disabled), [true, true, true, false]);
});
