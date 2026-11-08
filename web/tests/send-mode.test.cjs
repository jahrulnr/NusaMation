const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'composer.js'), 'utf8');
const start = src.indexOf('export function sendTitle(');
const end = src.indexOf('\nexport function openSendMenu(', start);

function titleFor(delivery, busy = true) {
  const sendTitle = vm.runInNewContext(`(${src.slice(start, end).trim().replace(/^export /, '')})`, {
    isBusyTurn: () => busy,
    S: { sendDelivery: delivery },
  });
  return sendTitle();
}

test('steer is the single fast path regardless of stored delivery', () => {
  assert.equal(titleFor('steer'), 'Steer');
  // after_tools stays a valid stored delivery (API/queue state), but the UI
  // presents it under the one steer concept.
  assert.equal(titleFor('after_tools'), 'Steer');
});

test('queue keeps its own title', () => {
  assert.equal(titleFor('queue'), 'Add to queue');
});

test('an idle composer is just Send', () => {
  assert.equal(titleFor('steer', false), 'Send');
});
