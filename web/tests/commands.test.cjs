const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const src = fs.readFileSync(path.join(__dirname, '../static/components/composer.js'), 'utf8');
const start = src.indexOf('export function matchingCommands(');
const end = src.indexOf('\nfunction commandChoices()', start);
const match = vm.runInNewContext(`(${src.slice(start, end).trim().replace(/^export /, '')})`);

test('filters agent commands by the slash prefix without consuming ordinary text', () => {
  const commands = [{ name: 'compact' }, { name: 'commit' }, { name: 'review' }];
  assert.deepEqual(Array.from(match('/com', commands), c => c.name), ['compact', 'commit']);
  assert.deepEqual(Array.from(match('/RE', commands), c => c.name), ['review']);
  assert.equal(match('/compact now', commands).length, 0);
  assert.equal(match('read /compact', commands).length, 0);
  assert.equal(match('/', null).length, 0);
});
