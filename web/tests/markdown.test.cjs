const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const context = vm.createContext({ console });
context.window = context;
for (const name of ['marked.min.js', 'markdown-math.min.js']) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'static', 'vendor', name), 'utf8'), context);
}
const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'app.js'), 'utf8');
const relaxStart = app.indexOf('  function relaxListEndings(');
const relaxEnd = app.indexOf('\n  function renderMarkdown(', relaxStart);
const relaxListEndings = vm.runInNewContext(`(${app.slice(relaxStart, relaxEnd).trim()})`, {});
function parse(text, scope) {
  const p = new context.marked.Marked({ gfm: true, breaks: true });
  p.use(context.NusaMarkdown.latex(), context.NusaMarkdown.math(),
    context.NusaMarkdown.markedFootnote({ prefixId: `footnote-${scope}-` }), context.NusaMarkdown.emoji());
  return p.parse(relaxListEndings(text));
}
test('a top-level line after a list becomes its own paragraph', () => {
  const text = 'intro:\n- one --loop\n- two --loop\nclosing line';
  const html = parse(text, 'x');
  assert.match(html, /<ul>\s*<li>one --loop<\/li>\s*<li>two --loop<\/li>\s*<\/ul>\s*<p>closing line<\/p>/);
  assert.equal((html.match(/<li>/g) || []).length, 2);
  assert.equal(html.includes('closing line</p>'), true);
});
test('list endings keep code fences, nested items, continuations and quotes intact', () => {
  const fenced = '- a\n```\nplain line\n```\nstill plain';
  assert.equal((parse(fenced, 'x').match(/<pre>/g) || []).length, 1);
  const nested = '- a\n  - b\noutside';
  assert.match(parse(nested, 'x'), /<p>outside<\/p>/);
  const cont = '- a\n  continued\n- b\ndone';
  assert.match(parse(cont, 'x'), /continued/);
  assert.match(parse(cont, 'x'), /<p>done<\/p>/);
  const quoted = '> - a\n> text';
  assert.equal((parse(quoted, 'x').match(/<li>/g) || []).length, 1);
  assert.match(parse(quoted, 'x'), /<p>text<\/p>/);
});
test('math, emoji and footnotes render while code and currency stay literal', () => {
  const text = [String.raw`Inline $E=mc^2$ and \(x^2\).`, '',
    String.raw`$$\int_0^1 x^2 \, dx = \frac{1}{3}$$`, '',
    String.raw`\[y=\sqrt{x}\]`, '',
    'Price $20 and $30; code: `$x$ :rocket:`.', '',
    ':rocket: note[^1]', '', '[^1]: Footnote text.'].join('\n');
  const html = parse(text, 'one');
  assert.equal((html.match(/class="katex"/g) || []).length, 4);
  assert.match(html, /Price \$20 and \$30/);
  assert.match(html, /<code>\$x\$ :rocket:<\/code>/);
  assert.match(html, /🚀 note/);
  assert.match(html, /footnote-one-1/);
  assert.match(html, /Footnote text/);
});
test('footnote ids are scoped to each message', () => {
  assert.match(parse('a[^1]\n\n[^1]: first', 'one'), /footnote-one-1/);
  assert.match(parse('b[^1]\n\n[^1]: second', 'two'), /footnote-two-1/);
});
