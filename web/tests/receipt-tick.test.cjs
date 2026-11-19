const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const start = src.indexOf('export function assistantHTML(');
const end = src.indexOf('\nexport function thoughtHTML(', start);
if (start < 0 || end < 0) throw new Error('assistantHTML not found');

function html(metadata, status) {
  const m = { id: 'm1', text: 'hello', status: status || 'complete', metadata: metadata ? JSON.stringify(metadata) : '' };
  const fn = vm.runInNewContext(
    `(function () { ${src.slice(start, end).trim().replace(/^export /, '')} return assistantHTML(m); })`,
    {
      parseJSON: (s, d) => { try { return JSON.parse(s); } catch { return d; } },
      esc: (s) => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;'),
      renderMarkdown: () => '<p>hello</p>',
      attachmentsHTML: () => '',
      messageHead: () => '',
      agentOrb: () => '',
      currentAgent: () => ({}),
      m,
    },
  );
  return fn();
}

test('a delivered tick renders on the bubble carrying channelReceipt', () => {
  const out = html({ channelMessageId: 'WA-1', channelReceipt: 'delivered' });
  assert.match(out, /msg-receipt/);
  assert.match(out, /✓</);
  assert.doesNotMatch(out, /✓✓/);
});

test('a read tick replaces the delivered one', () => {
  const out = html({ channelMessageId: 'WA-1', channelReceipt: 'read' });
  assert.match(out, /msg-receipt read/);
  assert.match(out, /✓✓/);
});

test('bubbles without metadata render no tick', () => {
  const out = html(null);
  assert.doesNotMatch(out, /msg-receipt/);
});
