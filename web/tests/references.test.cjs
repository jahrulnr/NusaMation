const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const src = fs.readFileSync(path.join(__dirname, '../static/components/references.js'), 'utf8').replace(/^export /gm, '');
const { referenceTrigger, reconcileReferences, referencesForSend, referencesFromStored } = vm.runInNewContext(src + '\n;({ referenceTrigger, reconcileReferences, referencesForSend, referencesFromStored })', { TextEncoder, TextDecoder });
const json = x => JSON.parse(JSON.stringify(x));

test('all prefixes find the token at the caret, not email text', () => {
  for (const p of ['/', '@', '#']) assert.deepEqual(json(referenceTrigger(`look ${p}mcp`, 9)), { start: 5, end: 9, prefix: p, query: 'mcp' });
  assert.equal(referenceTrigger('me@example.com', 14), null);
  assert.deepEqual(json(referenceTrigger('look @mcp done', 7)), { start: 5, end: 9, prefix: '@', query: 'm' });
});

test('editing either duplicate keeps the other identity and shifts positions', () => {
  const refs = [{kind:'file',id:'/mcp',label:'@mcp',start:0,end:4},{kind:'mcp',id:'mcp',label:'@mcp',start:5,end:9}];
  const edited = reconcileReferences('@mcp @mcp', '@mcp @mxcp', refs, 7, 7, 'insertText');
  assert.deepEqual(json(edited), [refs[0]]);
  const shifted = reconcileReferences('@mcp @mcp', 'X@mcp @mcp', refs, 0, 0, 'insertText');
  assert.deepEqual(json(shifted).map(r => r.start), [1, 6]);
  const deleted = reconcileReferences('@mcp @mcp', '@mcp ', refs, 9, 9, 'deleteContentBackward');
  assert.deepEqual(json(deleted), [refs[0]]);
});

test('trimmed UTF-8 offsets round-trip non-ASCII and emoji', () => {
  const text = '  halo 🌻 @mcp  ';
  const start = text.indexOf('@');
  const input = [{kind:'mcp',id:'mcp',label:'@mcp',start,end:start+4}];
  const sent = referencesForSend(text, input);
  assert.equal(sent[0].start, new TextEncoder().encode('halo 🌻 ').length);
  assert.deepEqual(json(referencesFromStored(text.trim(), sent)), [{...input[0],start:start-2,end:start+2}]);
});
