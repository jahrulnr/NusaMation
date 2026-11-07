const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const start = app.indexOf('export function userHTML(');
const end = app.indexOf('\nexport function assistantHTML(', start);

function htmlFor(turn, label) {
  const userHTML = vm.runInNewContext(`(${app.slice(start, end).trim().replace(/^export /, '')})`, {
    esc: (s) => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
    renderMarkdown: (s) => `<p>${String(s || '')}</p>`,
    messageHead: () => '', attachmentsHTML: () => '', annotationsHTML: () => '',
    messageAnnotations: () => [], icon: (n) => `<i>${n}</i>`,
    queuedTurnFor: () => turn, queueStatusLabel: () => label,
  });
  return userHTML({ id: 'm1', text: 'hello', channel: 'web' });
}

test('a queued turn shows edit and cancel actions with its turn id', () => {
  const html = htmlFor({ id: 'turn_q', status: 'queued' }, 'Queued · waiting for the agent');
  assert.match(html, /data-action="queue-edit"[^>]*data-turn="turn_q"/);
  assert.match(html, /data-action="queue-cancel"[^>]*data-turn="turn_q"/);
  assert.match(html, /Queued · waiting for the agent/);
});

test('after_tools queued turns still expose the actions', () => {
  const html = htmlFor({ id: 'turn_q', status: 'queued' }, 'Waiting for tools');
  assert.match(html, /data-action="queue-cancel"/);
});

test('non-queued turns render no queue actions', () => {
  const html = htmlFor({ id: 'turn_r', status: 'running' }, '');
  assert.doesNotMatch(html, /queue-cancel|queue-edit/);
});

test('messages without a turn render no queue actions', () => {
  const html = htmlFor(null, '');
  assert.doesNotMatch(html, /queue-status/);
});
