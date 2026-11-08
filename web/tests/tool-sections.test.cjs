const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'chat.js'), 'utf8');
const start = app.indexOf('export function toolSections(');
const end = app.indexOf('\nexport function planEntries', start);
assert.ok(start >= 0 && end > start);
const slice = app.slice(start, end).trim().replace(/^export /gm, '');
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const { toolSections } = vm.runInNewContext(`(() => { ${slice}; return { toolSections }; })()`, {
  esc,
  pretty: (v) => JSON.stringify(v, null, 2),
  S: { unwrappedTools: new Set() },
});
const labels = (html) => [...html.matchAll(/class="tool-label">([^<]+)</g)].map((m) => m[1]);
const scrollKeys = (html) => [...html.matchAll(/data-scroll-key="([^"]+)"/g)].map((m) => m[1]);

test('every section gets a unique data-scroll-key, even when labels repeat', () => {
  const t = { content: [{ type: 'text', text: 'log line' }], terminals: { t1: { id: 't1', output: 'out' } } };
  const keys = scrollKeys(toolSections(t, 'm1'));
  assert.equal(labels(toolSections(t, 'm1')).join(','), 'Output,Output');
  assert.equal(new Set(keys).size, keys.length);
  assert.deepEqual(keys, ['m1:Output', 'm1:Output:1']);
});

test('command inputs render as $ command under a cwd-aware label', () => {
  const html = toolSections({ rawInput: { command: 'ls -la', cwd: '/repo', timeout: 5000 } }, 'm2');
  assert.ok(html.includes('Command in /repo'));
  assert.ok(html.includes('$ ls -la'));
  assert.ok(!html.includes('&quot;command&quot;'));
  // Fields beyond command/cwd still surface as an Input section.
  assert.ok(html.includes('Input'));
  assert.ok(html.includes('&quot;timeout&quot;: 5000'));
});

test('command-only input produces no Input section', () => {
  const html = toolSections({ rawInput: { command: 'pwd' } }, 'm3');
  assert.deepEqual(labels(html), ['Command']);
});

test('metadata-only result envelopes are suppressed, richer results kept', () => {
  const env = toolSections({ rawOutput: { metadata: { status: 'completed', exit: 0 } } }, 'm4');
  assert.deepEqual(labels(env), []);
  const rich = toolSections({ rawOutput: { metadata: { status: 'completed' }, output: 'data' } }, 'm4');
  assert.deepEqual(labels(rich), ['Result']);
});

test('command values are escaped before rendering', () => {
  const html = toolSections({ rawInput: { command: 'echo "<img>"' } }, 'm5');
  assert.ok(html.includes('$ echo &quot;&lt;img&gt;&quot;'));
  assert.ok(!html.includes('<img>'));
});
