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
function parse(text, scope) {
  const p = new context.marked.Marked({ gfm: true, breaks: true });
  p.use(context.NusaMarkdown.latex(), context.NusaMarkdown.math(),
    context.NusaMarkdown.markedFootnote({ prefixId: `footnote-${scope}-` }), context.NusaMarkdown.emoji());
  return p.parse(text);
}
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
