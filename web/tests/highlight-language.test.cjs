const test = require('node:test');
const assert = require('node:assert/strict');

let highlightLanguage;
test.before(async () => { ({ highlightLanguage } = await import('../static/components/resources.js')); });

test('extensions map to vendored hljs language ids', () => {
  assert.equal(highlightLanguage('types.go'), 'go');
  assert.equal(highlightLanguage('app.js'), 'javascript');
  assert.equal(highlightLanguage('view.tsx'), 'typescript');
  assert.equal(highlightLanguage('note.md'), 'markdown');
  assert.equal(highlightLanguage('fix.diff'), 'diff');
  assert.equal(highlightLanguage('/abs/path/main.py:42'), 'python');
  assert.equal(highlightLanguage('style.SCSS'), 'scss');
});

test('filenames without an extension still resolve', () => {
  assert.equal(highlightLanguage('Makefile'), 'makefile');
});

test('mime is the fallback for extensionless or unknown names', () => {
  assert.equal(highlightLanguage('blob', 'application/json; charset=utf-8'), 'json');
  assert.equal(highlightLanguage('data.xyz', 'text/yaml'), 'yaml');
});

test('languages outside the vendored build return empty', () => {
  assert.equal(highlightLanguage('run.ps1'), '');
  assert.equal(highlightLanguage('Dockerfile'), '');
  assert.equal(highlightLanguage('x.rs2', ''), '');
  assert.equal(highlightLanguage('', ''), '');
});
