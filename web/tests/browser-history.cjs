// Isolated real-server UI check for history autofill. Run after make build;
// see web/tests/README.md.
//
// The seeded transcript reproduces a tool-heavy turn that fills the whole
// initial 150-message window: every message in the window is a tool row, so
// the renderer collapses them into one activity group, the transcript fits
// the viewport, and no scroll event can ever fire. The page must page in
// older messages by itself and end up scrollable, with the turn's user
// message present in the DOM.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { DatabaseSync } = require('node:sqlite');

async function main() {
  const data = fs.mkdtempSync(path.join(os.tmpdir(), 'nusamation-history-ui-'));
  const socket = net.createServer();
  await new Promise(resolve => socket.listen(0, '127.0.0.1', resolve));
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(path.resolve(process.env.NUSAMATION_BINARY || 'bin/nusamation'), ['--addr', `127.0.0.1:${port}`, '--data-dir', data, '--no-whatsapp'], { env: { ...process.env, NUSAMATION_PASSWORD: 'history-fixture-password' }, stdio: 'ignore' });
  let browser;
  try {
    for (let i = 0; i < 100; i++) {
      try { if ((await fetch(origin + '/api/auth/status')).ok) break; } catch {}
      if (child.exitCode !== null) throw Error('server exited');
      await new Promise(resolve => setTimeout(resolve, 100));
    }

    // Seed directly: the HTTP API cannot create tool messages, and the point
    // is the shape of the stored transcript, not how it was produced.
    const now = Date.now();
    const db = new DatabaseSync(path.join(data, 'nusamation.db'));
    db.exec('PRAGMA busy_timeout = 5000');
    // A live stand-in: opening a conversation prepares an ACP session, and a
    // command that exits immediately raises the agent-error modal mid-check.
    db.prepare('INSERT INTO agents (id,name,slug,command,args_json,cwd,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)')
      .run('ag_history', 'History Fixture', 'history-fixture', '/bin/sleep', '["3600"]', data, now, now);
    db.prepare('INSERT INTO conversations (id,title,agent_id,cwd,created_at,updated_at,last_message_at) VALUES (?,?,?,?,?,?,?)')
      .run('cv_history', 'Long tool turn', 'ag_history', data, now, now, now);
    const insert = db.prepare('INSERT INTO messages (id,conversation_id,turn_id,seq,role,kind,text,content_json,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)');
    // 250 older text messages: enough real height that the first prepended
    // page makes the transcript scrollable.
    for (let seq = 1; seq <= 250; seq++) {
      const role = seq % 5 === 0 ? 'user' : 'assistant';
      insert.run(`msg_h${seq}`, 'cv_history', '', seq, role, 'text',
        `History message ${seq}. This paragraph exists so the transcript has height after an older page is prepended.`,
        '{}', 'complete', now - (300 - seq) * 1000, now - (300 - seq) * 1000);
    }
    db.prepare('INSERT INTO turns (id,conversation_id,user_message_id,status,started_at,ended_at,created_at) VALUES (?,?,?,?,?,?,?)')
      .run('tr_history_final', 'cv_history', 'msg_h251', 'idle', now - 120000, now, now - 120000);
    // The turn's user message sits just outside the newest 150 messages.
    insert.run('msg_h251', 'cv_history', 'tr_history_final', 251, 'user', 'text', 'final prompt', '{}', 'complete', now - 60000, now - 60000);
    // 150 tool rows fill the window; consecutive rows collapse into one
    // "Activity · 150 tool calls" group, which is shorter than the viewport.
    for (let seq = 252; seq <= 401; seq++) {
      insert.run(`msg_h${seq}`, 'cv_history', 'tr_history_final', seq, 'assistant', 'tool',
        `Tool call ${seq}`,
        JSON.stringify({ toolCallId: `tc${seq}`, name: 'exec', kind: 'execute', status: 'completed', title: `cd /workspace (${seq})` }),
        'complete', now - 50000 + seq, now - 50000 + seq);
    }
    db.close();

    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME || '/usr/bin/google-chrome', args: ['--no-sandbox'] });
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    assert.equal((await context.request.post(origin + '/api/auth/login', { data: { password: 'history-fixture-password' } })).status(), 200);
    const page = await context.newPage();
    let olderRequests = 0;
    page.on('request', request => {
      if (request.url().includes('/api/conversations/cv_history/messages?before=')) olderRequests++;
    });
    const failures = [];
    page.on('pageerror', error => failures.push(String(error)));
    await page.goto(origin + '/c/cv_history');

    // The turn's user message is outside the initial window and can only
    // appear if the page fetched an older page without any user scroll.
    await page.getByText('final prompt', { exact: true }).waitFor({ state: 'attached', timeout: 15000 });
    assert.ok(olderRequests >= 1, `expected an automatic /messages?before= request, got ${olderRequests}`);
    const metrics = await page.evaluate(() => {
      const sc = document.querySelector('#chat-scroll');
      return { scrollHeight: sc.scrollHeight, clientHeight: sc.clientHeight };
    });
    assert.ok(metrics.scrollHeight > metrics.clientHeight + 8,
      `transcript should be scrollable after autofill: ${JSON.stringify(metrics)}`);
    assert.equal(await page.locator('[data-action="load-older"]').count(), 0, 'autofill must not leave the manual fallback');
    assert.deepEqual(failures, []);
    await page.screenshot({ path: '/tmp/nusamation-history-desktop.png', animations: 'disabled' });
    console.log(`PASS: autofilled ${olderRequests} older page(s); scrollHeight ${metrics.scrollHeight} > viewport ${metrics.clientHeight}`);
  } finally {
    await browser?.close();
    child.kill('SIGTERM');
    await new Promise(resolve => child.exitCode !== null ? resolve() : child.once('exit', resolve));
    fs.rmSync(data, { recursive: true, force: true });
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
