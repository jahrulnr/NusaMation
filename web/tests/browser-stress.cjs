// Run with NUSAMATION_PLAYWRIGHT pointing to a local Playwright package.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const root = path.resolve(__dirname, '../static');
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2' };
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const relative = url.pathname.startsWith('/static/') ? url.pathname.slice(8) : 'index.html';
  const file = path.resolve(root, relative);
  if (!file.startsWith(root + path.sep)) { res.writeHead(404).end(); return; }
  fs.readFile(file, (err, data) => {
    if (err) { res.writeHead(404).end(); return; }
    res.writeHead(200, { 'Content-Type': mime[path.extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
    res.end(data);
  });
});
async function main() {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME || '/usr/bin/google-chrome', args: ['--no-sandbox'] });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    if (process.env.CPU_THROTTLE) {
      const session = await page.context().newCDPSession(page);
      await session.send('Emulation.setCPUThrottlingRate', { rate: Number(process.env.CPU_THROTTLE) });
    }
    await page.addInitScript(() => {
      class FakeSocket {
        constructor() { window.stressSocket = this; this.readyState = 1; setTimeout(() => this.onopen?.(), 0); }
        send() {} close() {}
      }
      FakeSocket.OPEN = 1;
      window.WebSocket = FakeSocket;
    });
    const agent = { id: 'a', name: 'Agent', enabled: true };
    const rooms = Array.from({ length: 10 }, (_, i) => ({ id: `r${i}`, title: `Room ${i}`, agentId: 'a' }));
    const messages = Array.from({ length: 2000 }, (_, i) => ({ id: `m${i}`, conversationId: 'r0', role: i % 2 ? 'assistant' : 'user', kind: 'text', text: `Message ${i} ` + '**content** example '.repeat(4), seq: i + 1, createdAt: 1760000000000 + i, status: 'complete' }));
    messages.push({ id: 'tool', conversationId: 'r0', turnId: 't', role: 'assistant', kind: 'tool', text: 'Tool', content: { toolCallId: 'tool', title: 'Tool', kind: 'execute', status: 'in_progress' }, seq: 2001, createdAt: 1760000002001, status: 'streaming' });
    await page.route('**/api/**', async route => {
      const pathname = new URL(route.request().url()).pathname;
      let data = {};
      if (pathname === '/api/auth/status') data = { initialized: true, authenticated: true, csrf: 'test' };
      else if (pathname === '/api/bootstrap') data = { agents: [agent], agentDefaults: { agentId: 'a' }, conversations: rooms, approvals: [], channels: [] };
      else if (pathname === '/api/conversations/r0') data = { conversation: rooms[0], messages, agent, turns: {}, active: { running: true, status: 'running', turnId: 't' }, configs: [] };
      await route.fulfill({ json: data });
    });
    const start = Date.now();
    await page.goto(`http://127.0.0.1:${server.address().port}/c/r0`);
    await page.waitForSelector('[data-msg-id="m1999"]', { timeout: 60000 });
    const loadMs = Date.now() - start;
    const duration = Number(process.env.STRESS_MS || 15000);
    const stats = await page.evaluate(async ({ duration, initialText }) => {
      const offsets = Array(10).fill(0);
      offsets[0] = new TextEncoder().encode(initialText).length;
      const begin = performance.now(), end = begin + duration;
      let last = begin, events = 0, visible = 0, toolUpdates = 0, createdTools = 0;
      const frames = [];
      await new Promise(resolve => {
        function frame(t) { frames.push(t - last); last = t; if (t < end) requestAnimationFrame(frame); else resolve(); }
        requestAnimationFrame(frame);
        const timer = setInterval(() => {
          for (let room = 0; room < 10; room++) for (let i = 0; i < 10; i++) {
            window.stressSocket.onmessage({ data: JSON.stringify({ type: 'message.delta', conversationId: `r${room}`, data: { conversationId: `r${room}`, messageId: room ? 'other' : 'm1999', delta: ' x', offsetBytes: offsets[room] } }) });
            offsets[room] += 2;
            events++; if (room === 0) visible++;
          }
          toolUpdates++;
          window.stressSocket.onmessage({ data: JSON.stringify({ type: 'tool.updated', conversationId: 'r0', data: { id: 'tool', conversationId: 'r0', turnId: 't', role: 'assistant', kind: 'tool', text: 'Tool', content: { toolCallId: 'tool', title: `Tool ${toolUpdates}`, kind: 'execute', status: 'in_progress' }, seq: 2001, createdAt: 1760000002001, status: 'streaming' } }) });
          if (toolUpdates % 30 === 0) {
            createdTools++;
            window.stressSocket.onmessage({ data: JSON.stringify({ type: 'tool.created', conversationId: 'r0', data: { id: `extra-${createdTools}`, conversationId: 'r0', turnId: 't', role: 'assistant', kind: 'tool', text: 'Extra tool', content: { toolCallId: `extra-${createdTools}`, title: 'Extra tool', kind: 'execute', status: 'completed' }, seq: 2001 + createdTools, createdAt: 1760000002001 + createdTools, status: 'complete' } }) });
          }
          if (performance.now() >= end) clearInterval(timer);
        }, 33);
      });
      await new Promise(resolve => setTimeout(resolve, 200));
      const text = document.querySelector('[data-msg-id="m1999"] .markdown').textContent;
      return { elapsedMs: Math.round(performance.now() - begin), events, visible, toolUpdates, createdTools, toolText: document.querySelector('[data-msg-id="tool"] .tool-title').textContent, rendered: (text.match(/ x/g) || []).length, frames: frames.length, longFrames: frames.filter(ms => ms > 50).length, maxFrameMs: Math.round(Math.max(...frames)), domNodes: document.querySelectorAll('*').length };
    }, { duration, initialText: messages[1999].text });
    assert.equal(stats.rendered, stats.visible, 'visible stream must contain every delta');
    assert.equal(stats.toolText, `Tool ${stats.toolUpdates}`, 'tool card must show its latest update');
    assert.ok(stats.createdTools > 0, 'the fixture must exercise structural tool events');
    assert.ok(stats.frames > duration / 100, 'rendering stalled');
    const mobile = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    const phone = await mobile.newPage();
    const diagramRoom = { id: 'diagram', title: 'Diagram', agentId: 'a' };
    await phone.route('**/api/**', async route => {
      const pathname = new URL(route.request().url()).pathname;
      let data = {};
      if (pathname === '/api/auth/status') data = { initialized: true, authenticated: true, csrf: 'test' };
      else if (pathname === '/api/bootstrap') data = { agents: [agent], agentDefaults: { agentId: 'a' }, conversations: [diagramRoom], approvals: [], channels: [] };
      else if (pathname === '/api/conversations/diagram') data = { conversation: diagramRoom, agent, messages: [{ id: 'chart', conversationId: 'diagram', role: 'assistant', kind: 'text', text: '```mermaid\nflowchart LR\nA[One] --> B[Two] --> C[Three]\n```', seq: 1, status: 'complete', createdAt: 1760000000000 }], turns: {}, active: { running: true, status: 'running', turnId: 't' } };
      await route.fulfill({ json: data });
    });
    await phone.goto(`http://127.0.0.1:${server.address().port}/c/diagram`);
    await phone.waitForSelector('.diagram-viewport svg');
    await phone.locator('.diagram-viewport').scrollIntoViewIfNeeded();
    const rect = await phone.locator('.diagram-viewport').boundingBox();
    const x = rect.x + rect.width * 0.6, y = rect.y + rect.height / 2;
    const cdp = await mobile.newCDPSession(phone);
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x - 30, y, id: 1 }] });
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x - 30, y, id: 1 }, { x: x + 30, y, id: 2 }] });
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x - 55, y, id: 1 }, { x: x + 55, y, id: 2 }] });
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    const pinchZoom = await phone.locator('.diagram-scale').innerText();
    const pinchScroll = await phone.locator('.diagram-viewport').evaluate(el => el.scrollLeft);
    assert.ok(parseInt(pinchZoom, 10) > 100 && pinchScroll > 0, 'pinch zoom must keep the selected area reachable');
    await mobile.close();
    console.log(JSON.stringify({ initialMessages: 2000, loadMs, ...stats, pinchZoom, pinchScroll }, null, 2));
  } finally { await browser.close(); }
}
main().catch(err => { console.error(err); process.exitCode = 1; }).finally(() => server.close());
