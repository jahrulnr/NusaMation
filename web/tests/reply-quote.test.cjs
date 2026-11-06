const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'app.js'), 'utf8');
const start = app.indexOf('  function userHTML(');
const end = app.indexOf('\n  function assistantHTML(', start);
const userHTML = vm.runInNewContext(`(${app.slice(start, end).trim()})`, {
  esc: (s) => String(s).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
  renderMarkdown: (s) => `<p>${s || ''}</p>`,
  messageHead: () => '', attachmentsHTML: (a) => a?.length ? '<div class="attach-grid">attachment</div>' : '', queueStatusLabel: () => '',
});

test('quoted media and group attachment-only replies are styled together', () => {
  for (const label of ['an image', 'a video', 'a document', 'a voice message', 'a sticker', 'a video note']) {
    const html = userHTML({id: 'c', text: `Nama: [In reply to: ${label}]`, attachments: [{id: 'file'}]});
    assert.match(html, /class="reply-sender">Nama: /);
    assert.match(html, /class="reply-quote"/);
    assert.match(html, new RegExp(`class="reply-quote-text">${label}`));
    assert.match(html, /class="attach-grid">attachment<\/div><\/div>/);
    assert.doesNotMatch(html, /class="markdown"/);
  }
});

test('captioned media replies preserve the caption and attachment', () => {
  const html = userHTML({id: 'd', text: '[In reply to: an image]\nlihat ini', attachments: [{id: 'file'}]});
  assert.match(html, /class="reply-quote-text">an image/);
  assert.match(html, /<p>lihat ini<\/p>/);
  assert.match(html, /class="attach-grid">attachment/);
});

test('WhatsApp reply prefix is rendered as a quote with its own message', () => {
  const html = userHTML({ id: 'a', text: '[In reply to: hello [world] <test>]\ngaskeun' });
  assert.match(html, /class="reply-quote"/);
  assert.match(html, /hello \[world\] &lt;test&gt;/);
  assert.match(html, /<p>gaskeun<\/p>/);
  assert.doesNotMatch(html, /\[In reply to:/);
});

test('ordinary messages remain ordinary messages', () => {
  const html = userHTML({ id: 'b', text: 'hello there' });
  assert.doesNotMatch(html, /reply-quote/);
  assert.match(html, /<p>hello there<\/p>/);
});
