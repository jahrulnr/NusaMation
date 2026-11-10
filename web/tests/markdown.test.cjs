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
const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'markdown.js'), 'utf8');
const relaxStart = app.indexOf('export function relaxListEndings(');
const relaxEnd = app.indexOf('\nexport function renderMarkdown(', relaxStart);
const relaxListEndings = vm.runInNewContext(`(${app.slice(relaxStart, relaxEnd).trim().replace(/^export /, '')})`, {});
function parse(text, scope) {
  const p = new context.marked.Marked({ gfm: true, breaks: true });
  p.use(context.NusaMarkdown.latex(), context.NusaMarkdown.math(),
    context.NusaMarkdown.markedFootnote({ prefixId: `footnote-${scope}-` }), context.NusaMarkdown.emoji());
  return p.parse(relaxListEndings(text));
}
const renderContext = vm.createContext({
  window: { marked: context.marked, NusaMarkdown: context.NusaMarkdown, DOMPurify: { sanitize: (html) => html } },
  esc: (s) => String(s).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
  console,
  mediaHTML: (raw, esc) => esc(raw),
  prepareResources: (html) => html,
});

test('WhatsApp multiline monospace keeps its first line and markup literal', () => {
  const block = renderContext.renderMarkdown('```baris *satu*\n  <dua>```', 'wa-block', 'whatsapp');
  assert.match(block, /<pre><code>baris \*satu\*\n  &lt;dua&gt;<\/code><\/pre>/);
  const blankStart = renderContext.renderMarkdown('```\nsatu\ndua```', 'wa-blank-start', 'whatsapp');
  assert.match(blankStart, /<pre><code>satu\ndua<\/code><\/pre>/);
  const inline = renderContext.renderMarkdown('Lihat ```satu\ndua``` ya', 'wa-inline', 'whatsapp');
  assert.match(inline, /<code class="whatsapp-monospace">satu\ndua<\/code>/);
  const fenced = renderContext.renderMarkdown('```sh\necho hi\n```', 'wa-fence', 'whatsapp');
  assert.match(fenced, /<code class="language-sh">echo hi/);
});
const renderStart = app.indexOf('export const markdownCache = new Map();');
const renderEnd = app.indexOf('\nexport const plainCache = new Map();', renderStart);
vm.runInContext(app.slice(renderStart, renderEnd).replace(/^export /gm, '') + '\nthis.renderMarkdown = renderMarkdown;', renderContext);

test('WhatsApp messages render bold, italic, strike, code and lists with WhatsApp semantics', () => {
  const text = '*tebal* _miring_ ~coret~ `inline` ```mono```\n- satu\n- dua';
  const html = renderContext.renderMarkdown(text, 'wa', 'whatsapp');
  assert.match(html, /<strong>tebal<\/strong>/);
  assert.match(html, /<em>miring<\/em>/);
  assert.match(html, /<del>coret<\/del>/);
  assert.match(html, /<code>inline<\/code>/);
  assert.match(html, /<code>mono<\/code>/);
  assert.match(html, /<li>satu<\/li>/);
  assert.match(html, /<li>dua<\/li>/);
});

test('Markdown emphasis is unchanged and cached renders respect the input format', () => {
  const text = '*miring* **tebal** `*code*`';
  const md = renderContext.renderMarkdown(text, 'same-id', 'markdown');
  const wa = renderContext.renderMarkdown(text, 'same-id', 'whatsapp');
  assert.match(md, /<em>miring<\/em>/);
  assert.match(wa, /<strong>miring<\/strong>/);
  assert.match(wa, /<strong>tebal<\/strong>/);
  assert.match(wa, /<code>\*code\*<\/code>/);
});
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

test('raw HTML in message text renders as literal text, not elements', () => {
  const html = renderContext.renderMarkdown('iframe id=frame srcdoc="Inner <button>Frame</button> content"', 'html-leak', 'markdown');
  assert.match(html, /&lt;button&gt;Frame&lt;\/button&gt;/);
  assert.equal(html.includes('<button>'), false);
  const block = renderContext.renderMarkdown('<div onclick="x()">hi</div>', 'html-block', 'markdown');
  assert.match(block, /&lt;div/);
  assert.equal(block.includes('<div onclick'), false);
});
