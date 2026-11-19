const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'composer.js'), 'utf8');
const start = src.indexOf('export function mentionChoices(');
const end = src.indexOf('\nfunction commandChoices(', start);
if (start < 0 || end < 0) throw new Error('mentionChoices function not found');

function choices(query, conversations, projects, currentId) {
  const fn = vm.runInNewContext(`(${`function mentionChoices(query, conversations, projects, currentId) { ${src.slice(start, end).trim().replace(/^export /, '')} return mentionChoices(query, conversations, projects, currentId); }`})`, {});
  return fn(query, conversations, projects, currentId);
}

test('rooms are offered by label, excluding the current and archived rooms', () => {
  const rooms = [
    { id: 'cv_current', label: 'cat', title: 'Me', archived: false },
    { id: 'cv_cat', label: 'cat', title: 'Bug hunter', archived: false },
    { id: 'cv_dog', label: 'dog', title: 'Bug fixer', archived: false },
    { id: 'cv_gone', label: 'wolf', title: 'Old', archived: true },
    { id: 'cv_none', label: '', title: 'No handle yet', archived: false },
  ];
  const got = choices('', rooms, [], 'cv_current');
  assert.deepEqual(got.map((g) => [g.kind, g.name, g.hint]), [
    ['conversation', 'cat', 'room'],
    ['conversation', 'dog', 'room'],
  ]);
  assert.equal(got[0].detail, 'Bug hunter');
  assert.equal(got[0].id, 'cv_cat');
});

test('the label prefix narrows rooms; a title match also surfaces', () => {
  const rooms = [
    { id: 'cv_cat', label: 'cat', title: 'Bug hunter', archived: false },
    { id: 'cv_dog', label: 'dog', title: 'Merah the fixer', archived: false },
    { id: 'cv_cow', label: 'cow', title: 'Other', archived: false },
  ];
  assert.deepEqual(choices('c', rooms, [], '').map((g) => g.name), ['cat', 'cow']);
  assert.deepEqual(choices('merah', rooms, [], '').map((g) => g.name), ['dog']);
  assert.deepEqual(choices('tiger', rooms, [], ''), []);
});

test('projects slugify their name so the mention stays one token', () => {
  const projects = [{ id: 'p1', name: 'NusaLLM' }, { id: 'p2', name: 'My Big Project' }];
  const got = choices('', [], projects, '');
  assert.deepEqual(got.map((g) => [g.kind, g.name, g.hint]), [
    ['project', 'nusallm', 'project'],
    ['project', 'my-big-project', 'project'],
  ]);
  assert.equal(got[1].detail, 'My Big Project');
  // Either the raw name or its slug matches the query.
  assert.equal(choices('my big', [], projects, '').length, 1);
  assert.equal(choices('my-big', [], projects, '').length, 1);
});

test('rooms come first and the lists are bounded', () => {
  const rooms = Array.from({ length: 12 }, (_, i) => ({ id: 'cv_' + i, label: 'room' + i, title: 'T' + i, archived: false }));
  const projects = Array.from({ length: 7 }, (_, i) => ({ id: 'p' + i, name: 'Proj ' + i }));
  const got = choices('', rooms, projects, '');
  assert.equal(got.filter((g) => g.kind === 'conversation').length, 8);
  assert.equal(got.filter((g) => g.kind === 'project').length, 4);
  assert.equal(got[0].kind, 'conversation');
});
