const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const src = fs.readFileSync(require('node:path').join(__dirname, '../static/components/chat.js'), 'utf8');
const code = src.slice(src.indexOf('export function orderMessages'), src.indexOf('\nexport function renderChat')).replace('export ', '');
const order = vm.runInNewContext(`(${code})`, { parseJSON: (x, fallback) => x || fallback });
const turns = { t: { id: 't', userMessageId: 'first' }, q: { id: 'q', userMessageId: 'queued' } };
for (const legacy of [false, true]) test(`steer stays inside running turn, legacy=${legacy}`, () => {
  const messages = [
    {id:'first', role:'user', seq:1}, {id:'queued', role:'user', seq:2},
    {id:'before', role:'assistant', turnId:'t', seq:3},
    {id:'steer', role:'user', ...(legacy ? {metadata:{delivery:'steer'}} : {turnId:'t'}), seq:4},
    {id:'after', role:'assistant', turnId:'t', seq:5},
    {id:'queued-answer', role:'assistant', turnId:'q', seq:6},
  ];
  assert.deepEqual(Array.from(order(messages, turns), m => m.id), ['first','before','steer','after','queued','queued-answer']);
});
test('partial page preserves steer between running turn messages', () => {
  const messages = [{id:'before',role:'assistant',turnId:'t',seq:3},{id:'steer',role:'user',turnId:'t',seq:4},{id:'after',role:'assistant',turnId:'t',seq:5}];
  assert.deepEqual(Array.from(order(messages, turns), m => m.id), ['before','steer','after']);
});
