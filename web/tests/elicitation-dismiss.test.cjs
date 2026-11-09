const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Dismissing the elicitation popup must not cancel an optional form: the agent
// receives an empty accepted answer ("Unanswered") and the turn keeps going.
// Forms with required fields and URL requests keep the cancellable behaviour.

const sliceExport = (src, startMarker, endMarker = '\nexport ') => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = src.indexOf(endMarker, start + startMarker.length);
  return src.slice(start, end < 0 ? undefined : end).trim().replace(/^export /, '');
};

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'requests.js'), 'utf8');
const raw = vm.runInNewContext(`(${sliceExport(src, 'export function dismissalResponse(')})`, {});
// Strip the VM realm so deepEqual can compare plain objects.
const dismissalResponse = (request) => JSON.parse(JSON.stringify(raw(request)));

const form = (extra = {}) => ({ mode: 'form', requestedSchema: { type: 'object', properties: { q0: { type: 'string' } }, ...extra } });

test('an optional form is answered empty instead of cancelled', () => {
  assert.deepEqual(dismissalResponse(form()), { action: 'accept', content: {} });
});

test('a form without a required list is treated as optional', () => {
  assert.deepEqual(dismissalResponse({ mode: 'form', requestedSchema: { properties: {} } }), { action: 'accept', content: {} });
});

test('a form with required fields still cancels', () => {
  assert.deepEqual(dismissalResponse(form({ required: ['q0'] })), { action: 'cancel' });
});

test('url requests still cancel', () => {
  assert.deepEqual(dismissalResponse({ mode: 'url', url: 'https://example.com' }), { action: 'cancel' });
});

test('a missing request cancels', () => {
  assert.deepEqual(dismissalResponse(undefined), { action: 'cancel' });
  assert.deepEqual(dismissalResponse(null), { action: 'cancel' });
});
