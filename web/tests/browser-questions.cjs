// Isolated interaction checks; use NUSAMATION_PLAYWRIGHT and CHROME as in browser-stress.cjs.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const root = path.resolve(__dirname, '../static');
const artifacts = process.env.QUESTION_SCREENSHOTS || '/tmp/nusamation-questions';
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2' };
const server = http.createServer((req, res) => {
  const pathname = new URL(req.url, 'http://localhost').pathname;
  const file = path.resolve(root, pathname.startsWith('/static/') ? pathname.slice(8) : 'index.html');
  if (!file.startsWith(root + path.sep)) return res.writeHead(404).end();
  fs.readFile(file, (error, data) => {
    if (error) return res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream' });
    res.end(data);
  });
});
async function main() {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME || '/usr/bin/google-chrome', args: ['--no-sandbox'] });
    const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    const errors = [];
    page.on('pageerror', e => errors.push(e.message));
    const agent = { id: 'a', name: 'Test agent', enabled: true };
    const room = { id: 'fixture', title: 'Question preview', agentId: agent.id };
    let fail = false, release;
    const sent = [];
    await page.route('**/api/**', async route => {
      const pathname = new URL(route.request().url()).pathname;
      if (pathname.includes('/elicitations/')) {
        sent.push(route.request().postDataJSON());
        if (release) await new Promise(resolve => { release = resolve; });
        return route.fulfill({ status: fail ? 500 : 200, json: fail ? { error: 'Fixture failure' } : {} });
      }
      let data = {};
      if (pathname === '/api/auth/status') data = { initialized: true, authenticated: true, csrf: 'fixture' };
      else if (pathname === '/api/bootstrap') data = { agents: [agent], conversations: [room], channels: [], approvals: [] };
      else if (pathname.startsWith('/api/conversations/fixture')) data = { conversation: room, agent, messages: [], turns: {}, configs: [], active: { running: false } };
      await route.fulfill({ json: data });
    });
    await page.addInitScript(() => {
      window.WebSocket = class { static OPEN = 1; static CONNECTING = 0; readyState = 1; send() {} close() {} };
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/c/fixture`);
    await page.waitForFunction(async () => (await import('/static/components/state.js')).S.booted);
    const open = async (properties, required = [], extra = {}) => {
      await page.evaluate(async ({ properties, required, extra }) => {
        const { S } = await import('/static/components/state.js');
        const { showElicitation } = await import('/static/components/requests.js');
        const { closeModal } = await import('/static/components/modal.js');
        closeModal(false);
        const entry = { id: 'question-fixture', conversationId: 'fixture', agentId: 'a', request: { mode: 'form', message: 'Bantu agent melanjutkan tugas ini.', requestedSchema: { properties, required }, ...extra } };
        S.elicitations = [entry];
        showElicitation(entry);
      }, { properties, required, extra });
    };
    const tab = i => page.locator(`#question-tab-${i}`);
    const next = () => page.locator('#elicitation-form button[type="submit"]').click();
    const visiblePage = async i => assert.equal(await page.locator('.elicitation-page:visible').getAttribute('data-page'), String(i));
    const properties = {
      choice: { type: 'string', title: 'Apakah form pertanyaan ini tampil dengan benar?', oneOf: [{ const: 'yes', title: 'Ya, tampil' }, { const: 'no', title: 'Tidak tampil' }] },
      tags: { type: 'array', title: 'Pilih dua area untuk diperiksa', items: { enum: ['Frontend', 'Backend', 'CLI'] }, minItems: 2, maxItems: 2 },
      details: { type: 'string', title: 'Tambahkan detail', minLength: 2 },
      enabled: { type: 'boolean', title: 'Lanjutkan?', default: false },
      count: { type: 'integer', title: 'Jumlah percobaan', minimum: 1, maximum: 5, default: 2 },
    };
    await open(properties, ['choice', 'tags', 'details']);
    await next(); await visiblePage(0); // Required radio blocks Continue.
    await page.getByText('Ya, tampil', { exact: true }).click();
    await next(); await visiblePage(1);
    await next(); await visiblePage(1); // Multi-select bounds are enforced.
    await page.getByText('Frontend', { exact: true }).click();
    await page.getByText('Backend', { exact: true }).click();
    await page.getByText('CLI', { exact: true }).click();
    await next(); await visiblePage(1); // Maximum selection is enforced too.
    await page.getByText('CLI', { exact: true }).click();
    await next(); await visiblePage(2);
    await page.locator('[data-key="details"]').fill('Keep my answer');
    await tab(0).click();
    assert.equal(await page.locator('input[value="yes"]').isChecked(), true);
    await tab(1).click();
    assert.equal(await page.locator('.elicitation-multi input:checked').count(), 2);
    await tab(4).click(); await next(); // Final submission validates earlier pages too.
    await page.waitForFunction(() => !document.querySelector('#elicitation-form'));
    assert.deepEqual(sent.pop(), { action: 'accept', content: { choice: 'yes', tags: ['Frontend', 'Backend'], details: 'Keep my answer', enabled: false, count: 2 } });
    // Jumping straight to the last page must reveal the first unanswered field.
    await open(properties, ['choice', 'tags', 'details']);
    await tab(4).click(); await next(); await visiblePage(0);
    // Keyboard tabs and all ten questions fit without stacking the bodies.
    const ten = { ...properties, ...Object.fromEntries(Array.from({ length: 5 }, (_, i) => [`extra${i}`, { type: 'string', title: `Pertanyaan tambahan ${i + 1}` }])) };
    await open(ten);
    await tab(0).focus(); await page.keyboard.press('End'); await visiblePage(9);
    await page.keyboard.press('Home'); await visiblePage(0);
    fs.mkdirSync(artifacts, { recursive: true });
    for (const [name, width, height] of [['desktop', 1280, 800], ['mobile', 390, 844], ['short', 800, 360]]) {
      await page.setViewportSize({ width, height });
      await page.screenshot({ animations: 'disabled', path: path.join(artifacts, `${name}.png`) });
      const bounds = await page.locator('.elicitation-dialog').boundingBox();
      assert(bounds.y >= 0 && bounds.y + bounds.height <= height + 1, `${name}: dialog exceeds viewport`);
      const footer = await page.locator('.elicitation-foot').boundingBox();
      assert(footer.y + footer.height <= height + 1, `${name}: actions clipped`);
      assert.equal(await page.locator('.elicitation-page:visible').count(), 1);
    }
    await page.setViewportSize({ width: 390, height: 500 });
    await open({ long: { type: 'array', title: 'Many options', items: { enum: Array.from({ length: 30 }, (_, i) => `Option ${i}`) } } });
    const body = page.locator('.elicitation-body');
    const scroll = await body.evaluate(el => ({ height: el.clientHeight, scroll: el.scrollHeight }));
    assert(scroll.scroll > scroll.height, 'question body must own overflow');
    await body.hover(); await page.mouse.wheel(0, 2000);
    await page.waitForFunction(() => document.querySelector('.elicitation-body').scrollTop > 0);
    await page.screenshot({ animations: 'disabled', path: path.join(artifacts, 'long-options.png') });
    // Failure retains the answer; pending sends cannot be duplicated or dismissed.
    await open({ text: { type: 'string', title: 'Your answer' } });
    await page.locator('[data-key="text"]').fill('Retained after failure');
    fail = true; await next();
    await page.getByText('Fixture failure', { exact: true }).waitFor();
    assert.equal(await page.locator('[data-key="text"]').inputValue(), 'Retained after failure');
    fail = false; release = true;
    const count = sent.length;
    await next();
    await page.waitForFunction(() => document.querySelector('#elicitation-form button[type="submit"]').disabled);
    await page.keyboard.press('Escape');
    await page.evaluate(async () => { const { submitElicitation } = await import('/static/components/requests.js'); submitElicitation(document.querySelector('#elicitation-form')); });
    assert.equal(await page.locator('#elicitation-form').count(), 1);
    assert.equal(sent.length, count + 1);
    release(); release = null;
    await page.waitForFunction(() => !document.querySelector('#elicitation-form'));
    // Optional choices can be returned to unanswered, as with the old select.
    await open({ choice: { type: 'string', enum: ['One', 'Two'], default: 'One' } });
    await page.getByRole('button', { name: 'Clear answer' }).click();
    await next();
    await page.waitForFunction(() => !document.querySelector('#elicitation-form'));
    assert.deepEqual(sent.at(-1), { action: 'accept', content: {} });
    // Preserve optional dismissal and the non-form paths.
    await open({ text: { type: 'string' } });
    await page.keyboard.press('Escape');
    await page.waitForFunction(() => !document.querySelector('#elicitation-form'));
    assert.deepEqual(sent.at(-1), { action: 'accept', content: {} });
    await open({}, [], { mode: 'url', url: 'https://example.com' });
    assert.equal(await page.getByRole('button', { name: 'Open link' }).count(), 1);
    assert.equal(await page.locator('.elicitation-tabs').count(), 0);
    await open({ unsupported: { type: 'object' } });
    assert.equal(await page.locator('#elicitation-form button[type="submit"]').isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log(`PASS: pagination, retained answers, typed payload, validation, keyboard, scroll, retry, pending guard, dismissal. Screenshots: ${artifacts}`);
  } finally {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
  }
}
main().catch(e => { console.error(e); process.exitCode = 1; });
