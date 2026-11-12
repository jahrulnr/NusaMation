// Isolated real-server UI check. Run after make build; see web/tests/README.md.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

async function main() {
  const data = fs.mkdtempSync(path.join(os.tmpdir(), 'nusamation-update-ui-'));
  const socket = net.createServer();
  await new Promise(resolve => socket.listen(0, '127.0.0.1', resolve));
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(path.resolve(process.env.NUSAMATION_BINARY || 'bin/nusamation'), ['--addr', `127.0.0.1:${port}`, '--data-dir', data, '--no-whatsapp'], { env: { ...process.env, NUSAMATION_PASSWORD: 'update-fixture-password' }, stdio: 'ignore' });
  let browser;
  try {
    for (let i = 0; i < 100; i++) {
      try { if ((await fetch(origin + '/api/auth/status')).ok) break; } catch {}
      if (child.exitCode !== null) throw Error('server exited');
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME || '/usr/bin/google-chrome', args: ['--no-sandbox'] });
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    assert.equal((await context.request.post(origin + '/api/auth/login', { data: { password: 'update-fixture-password' } })).status(), 200);
    const page = await context.newPage();
    await page.route('**/api/update', route => route.fulfill({ json: { current: '1.0.0', latest: '2.0.0', available: true, canApply: true } }));
    await page.goto(origin);
    const button = page.getByRole('button', { name: 'NusaMation update available' });
    await button.waitFor();
    await page.screenshot({ path: '/tmp/nusamation-update-desktop.png', animations: 'disabled' });
    await button.click();
    await page.getByText('Software update', { exact: true }).waitFor();
    await page.locator('[data-action="close-modal"]').first().click();
    await page.setViewportSize({ width: 390, height: 844 });
    await button.waitFor();
    await page.screenshot({ path: '/tmp/nusamation-update-mobile.png', animations: 'disabled' });
    await page.evaluate(async () => (await import('/static/components/ws.js')).handleEvent({ type: 'update.status', data: { current: '2.0.0', available: false } }));
    assert.equal(await button.count(), 0);
    await page.evaluate(async () => (await import('/static/components/ws.js')).handleEvent({ type: 'update.status', data: { current: '2.0.0', latest: '3.0.0', available: true } }));
    await button.waitFor();
    console.log('PASS: update status on connection, shortcut navigation, mobile layout, and live availability changes');
  } finally {
    await browser?.close();
    child.kill('SIGTERM');
    await new Promise(resolve => child.exitCode !== null ? resolve() : child.once('exit', resolve));
    fs.rmSync(data, { recursive: true, force: true });
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
