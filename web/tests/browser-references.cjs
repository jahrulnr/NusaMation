// NUSAMATION_PLAYWRIGHT may point to a local Playwright package.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const root = path.resolve(__dirname, '../static');
const types = { '.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml', '.woff2':'font/woff2' };
const server = http.createServer((req,res) => { const pathname = new URL(req.url, 'http://localhost').pathname; const file = path.resolve(root, pathname.startsWith('/static/') ? pathname.slice(8) : 'index.html'); if (!file.startsWith(root + path.sep)) return res.writeHead(404).end(); fs.readFile(file, (err,data) => err ? res.writeHead(404).end() : res.writeHead(200, {'Content-Type': types[path.extname(file)] || 'application/octet-stream'}).end(data)); });
async function main() {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless:true, executablePath:process.env.CHROME || '/usr/bin/google-chrome', args:['--no-sandbox'] });
  try {
    const page = await browser.newPage({ viewport:{width:1100,height:800} });
    const agent = {id:'a',name:'Agent',enabled:true};
    const conv = {id:'room',title:'References',agentId:'a',acpSessionId:'s',metadata:{commandsSessionId:'s',commands:[{name:'compact',description:'Compact the session'}]}};
    const items = [{kind:'folder',id:'/project/mcp',name:'mcp',detail:'project/mcp'},{kind:'mcp',id:'mcp',name:'mcp',detail:'MCP server'},{kind:'skill',id:'/skills/mcp',name:'mcp',detail:'/skills/mcp'},{kind:'file',id:'/project/mcp',name:'mcp',detail:'file mcp'}];
    const sent = []; let storedMeta = {};
    await page.addInitScript(() => { class FakeSocket { constructor(){this.readyState=1;setTimeout(()=>this.onopen?.(),0)} send(){} close(){} } FakeSocket.OPEN=1; window.WebSocket=FakeSocket; });
    await page.route('**/api/**', async route => {
      const request=route.request(), url=new URL(request.url()); let data={};
      if (url.pathname==='/api/auth/status') data={initialized:true,authenticated:true,csrf:'test'};
      else if (url.pathname==='/api/bootstrap') data={agents:[agent],agentDefaults:{agentId:'a'},conversations:[conv],approvals:[],channels:[]};
      else if (url.pathname==='/api/conversations/room' || url.pathname==='/api/conversations/room/prepare') data={conversation:conv,agent,messages:[],turns:{},configs:[]};
      else if (url.pathname==='/api/references') { const q=(url.searchParams.get('q') || '').toLowerCase(); data={items:items.filter(x=>x.name.includes(q))}; }
      else if (url.pathname==='/api/conversations/room/meta') { if(request.method()==='PUT') storedMeta={...storedMeta,...request.postDataJSON()}; data={meta:storedMeta}; }
      else if (url.pathname==='/api/conversations/room/messages' && request.method()==='POST') { const body=request.postDataJSON(); sent.push(body); data={message:{id:'m'+sent.length,conversationId:'room',role:'user',kind:'text',text:body.text,metadata:{references:body.references},seq:sent.length,status:'complete'},turn:{id:'t'+sent.length,status:'queued'}}; }
      await route.fulfill({json:data});
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/c/room`); await page.waitForSelector('#prompt');
    const prompt = page.locator('#prompt');
    await prompt.fill('@mcp'); await page.locator('.command-suggestion').filter({hasText:'MCP server'}).waitFor();
    await page.screenshot({path:'/tmp/nusamation-references-desktop.png'});
    await page.locator('.command-suggestion').filter({hasText:'MCP server'}).click();
    assert.equal(await prompt.inputValue(), '@mcp ');
    await page.waitForTimeout(800); assert.equal(storedMeta.draft.references[0].kind, 'mcp');
    await page.locator('#send-btn').click(); await page.waitForFunction(()=>document.querySelector('#prompt').value==='');
    assert.equal(sent[0].text, '@mcp'); assert.equal(sent[0].references[0].kind, 'mcp');
    await prompt.fill('#mcp'); await page.locator('.command-suggestion').first().waitFor();
    await page.locator('#send-btn').click(); await page.waitForFunction(()=>document.querySelector('#prompt').value==='');
    assert.equal(sent[1].text, '#mcp'); assert.deepEqual(sent[1].references, []);
    await prompt.fill('@compact'); await page.locator('.command-suggestion').filter({hasText:'Compact'}).waitFor(); await page.keyboard.press('Tab');
    await page.locator('#send-btn').click(); await page.waitForFunction(()=>document.querySelector('#prompt').value==='');
    assert.equal(sent[2].text, '@compact'); assert.equal(sent[2].references[0].kind, 'command');
    await prompt.fill('@mcp'); await page.locator('.command-suggestion').filter({hasText:'MCP server'}).waitFor(); await page.locator('.command-suggestion').filter({hasText:'MCP server'}).click();
    await prompt.press('ArrowLeft'); await prompt.press('ArrowLeft'); await page.keyboard.type('x'); await page.locator('#send-btn').click(); await page.waitForFunction(()=>document.querySelector('#prompt').value==='');
    assert.deepEqual(sent[3].references, []);
    await page.setViewportSize({width:390,height:740}); await prompt.fill('/mcp'); await page.locator('.command-suggestion').first().waitFor(); await page.screenshot({path:'/tmp/nusamation-references-mobile.png'});
    await page.locator('.command-suggestion').filter({hasText:'MCP server'}).click();
    assert.equal(await prompt.inputValue(), '/mcp ');
    console.log('PASS: explicit click/Tab, ordinary text, editable identity, draft persistence, desktop/mobile rendering');
  } finally { await browser.close(); server.close(); }
}
main().catch(err => { console.error(err); server.close(); process.exitCode=1; });
