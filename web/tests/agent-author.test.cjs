const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const start = src.indexOf('export function messageHead(');
const end = src.indexOf('\nexport function userHTML(', start);
if (start < 0 || end < 0) throw new Error('messageHead not found');

function head(m) {
  const fn = vm.runInNewContext(
    `(function () { ${src.slice(start, end).trim().replace(/^export /, '')} return messageHead(m); })`,
    {
      parseJSON: (v, d) => { try { return v == null || v === "" ? d : JSON.parse(v); } catch { return d; } },
      esc: (s) => String(s ?? ''),
      clock: () => '12:00',
      agentName: () => 'Devin',
      currentAgent: () => ({}),
      S: { current: {} },
      m,
    },
  );
  return fn(m);
}

test('a human message is authored as You', () => {
  const out = head({ role: 'user', channel: 'web', metadata: '' });
  assert.match(out, /msg-author">You\</);
});

test('a prompt sent by another conversation is authored as Agent, not You', () => {
  const out = head({ role: 'user', channel: 'agent', metadata: '' });
  assert.match(out, /msg-author">Agent\</);
  assert.doesNotMatch(out, /You/);
});

test('an orchestrated marker also authors as Agent even on web channel', () => {
  const out = head({ role: 'user', channel: 'web', metadata: JSON.stringify({ orchestration: { title: 'hunter' } }) });
  assert.match(out, /msg-author">Agent\</);
});

test('assistant bubbles keep the agent name', () => {
  const out = head({ role: 'assistant', channel: 'web', metadata: '' });
  assert.match(out, /msg-author">Devin\</);
});
