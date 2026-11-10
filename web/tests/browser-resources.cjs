// Real HTTP/backend + browser checks against an isolated data directory.
// Run make build first; NUSAMATION_PLAYWRIGHT and CHROME match browser-stress.cjs.
const { chromium } = require(process.env.NUSAMATION_PLAYWRIGHT || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const {spawn,execFileSync}=require('node:child_process');
const artifacts=process.env.RESOURCE_SCREENSHOTS || '/tmp/nusamation-resources';
const wait=ms=>new Promise(r=>setTimeout(r,ms));
async function main(){
 const data=fs.mkdtempSync(path.join(os.tmpdir(),'nusamation-media-'));
 const workspace=path.join(data,'artifacts');fs.mkdirSync(workspace);
 fs.mkdirSync(artifacts,{recursive:true});
 const svg=path.join(workspace,'plot # ü.svg');
 fs.writeFileSync(svg,'<svg xmlns="http://www.w3.org/2000/svg" width="460" height="160"><rect width="460" height="160" fill="#18212f"/><text x="30" y="88" font-size="28" fill="#f5b544">Agent media preview</text></svg>');
 const code=path.join(workspace,'sample.txt');fs.writeFileSync(code,'line 1\n<script>literal code</script>\nline 3');
 fs.writeFileSync(path.join(workspace,'readme.md'),'# Setup\n\nLocal document preview.\n\n[Text](sample.txt)\n\n![Diagram](<plot %23 ü.svg>)');
 fs.writeFileSync(path.join(workspace,'page.html'),'<h1>HTML preview</h1><p>Rendered document.</p>');
 fs.writeFileSync(path.join(workspace,'utf16.txt'),Buffer.concat([Buffer.from([255,254]),Buffer.from('Windows text preview','utf16le')]));
 fs.writeFileSync(path.join(workspace,'binary.zip'),Buffer.from([0,255,3,1]));
 // A small valid PDF with an xref table so the browser PDF viewer can load it.
 const pdfText='BT /F1 18 Tf 30 90 Td (PDF preview works) Tj ET';
 const objects=['<< /Type /Catalog /Pages 2 0 R >>','<< /Type /Pages /Kids [3 0 R] /Count 1 >>','<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 180] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>',`<< /Length ${pdfText.length} >>\nstream\n${pdfText}\nendstream`,'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>'];
 let pdf='%PDF-1.4\n',offsets=[0];objects.forEach((o,i)=>{offsets.push(Buffer.byteLength(pdf));pdf+=`${i+1} 0 obj\n${o}\nendobj\n`;});const xref=Buffer.byteLength(pdf);pdf+=`xref\n0 6\n0000000000 65535 f \n`+offsets.slice(1).map(o=>String(o).padStart(10,'0')+' 00000 n \n').join('')+`trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;fs.writeFileSync(path.join(workspace,'report.pdf'),pdf);
 execFileSync('ffmpeg',['-loglevel','error','-f','lavfi','-i','color=c=0x18212f:s=320x180:d=2','-c:v','libx264','-pix_fmt','yuv420p',path.join(workspace,'clip.mp4')]);
 execFileSync('ffmpeg',['-loglevel','error','-f','lavfi','-i','sine=frequency=440:duration=2',path.join(workspace,'sound.wav')]);
 const port=await new Promise(resolve=>{const s=net.createServer();s.listen(0,'127.0.0.1',()=>{const p=s.address().port;s.close(()=>resolve(p));});});
 const origin=`http://127.0.0.1:${port}`;
 const binary=path.resolve(process.env.NUSAMATION_BINARY||'bin/nusamation');
 const log=fs.openSync(path.join(artifacts,'backend.log'),'w');
 const child=spawn(binary,['--addr',`127.0.0.1:${port}`,'--data-dir',data,'--no-whatsapp'],{env:{...process.env,NUSAMATION_PASSWORD:'local-resource-fixture-password'},stdio:['ignore',log,log]});
 let browser;
 try {
  for(let i=0;i<100;i++){try{if((await fetch(origin+'/api/auth/status')).ok)break;}catch{} if(child.exitCode!=null)throw Error('Fixture server exited; see backend.log');await wait(100);}
  browser=await chromium.launch({headless:true,executablePath:process.env.CHROME||'/usr/bin/google-chrome',args:['--no-sandbox']});
  const context=await browser.newContext({viewport:{width:1280,height:850}});
  const login=await context.request.post(origin+'/api/auth/login',{data:{password:'local-resource-fixture-password'}});assert.equal(login.status(),200);const {csrf}=await login.json();
  const headers={'X-CSRF-Token':csrf,'Origin':origin};
  const a=await context.request.post(origin+'/api/agents',{headers,data:{name:'Resource fixture',slug:'resource-fixture',command:binary,cwd:workspace,enabled:true,autoStart:false}});assert.equal(a.status(),201,await a.text());const agent=(await a.json()).agent;
  const c=await context.request.post(origin+'/api/conversations',{headers,data:{agentId:agent.id,title:'Markdown, files and media',cwd:workspace}});assert.equal(c.status(),201,await c.text());const conv=(await c.json()).conversation;
  const up=await context.request.post(origin+'/api/uploads',{headers,multipart:{conversationId:conv.id,file:{name:'attachment.svg',mimeType:'image/svg+xml',buffer:fs.readFileSync(svg)}}});assert.equal(up.status(),201,await up.text());const upload=(await up.json()).upload;
  const page=await context.newPage(), errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/prepare',r=>r.fulfill({json:{conversation:conv,agent,configs:[]}}));
  await page.goto(origin+'/c/'+conv.id);
  await page.waitForFunction(async id=>(await import('/static/components/state.js')).S.current?.id===id,conv.id);
  const md=[
   '# Resource gallery','[Jump to examples](#examples)','[Web documentation](https://example.com/guide?x=a&y=b)','## Examples',
   `[Screenshot](<${svg.replace('#','%23')}>)`,
   `![Inline diagram](<${svg.replace('#','%23')}>)`,
   '<video controls poster="plot%20%23%20%C3%BC.svg"><source src="clip.mp4" type="video/mp4"></video>',
   'Listen: <audio controls><source src="sound.wav" type="audio/wav"></audio> after audio.',
   '[Code line](sample.txt#L2)','[File URI](<file://'+code+'>)','[Readme](readme.md#setup)','[HTML document](page.html)','[PDF document](report.pdf)','[UTF16](utf16.txt)','[Binary](binary.zip)','[Missing](missing.txt)',
   '<img src="missing.png" alt="Missing image">','```html\n<video src="sample.mp4"></video>\n```',
   '<a href="sample.txt">Raw link</a>',
   '![Video as Markdown](clip.mp4)',
   '<button data-action="delete-conv">literal button</button>'
  ].join('\n\n');
  const render=async(text)=>page.evaluate(async({text,conv,upload})=>{const {S}=await import('/static/components/state.js');const {renderChat}=await import('/static/components/chat.js');S.messages=[{id:'media-message',conversationId:conv.id,role:'assistant',kind:'text',text,seq:1,status:'complete',createdAt:Date.now(),attachments:[upload]}];S.turns={};renderChat();},{text,conv,upload});
  await render(md);
  await page.locator('.markdown video').first().evaluate(el=>new Promise((res,rej)=>{if(el.readyState>=1)return res();el.onloadedmetadata=res;el.onerror=()=>rej(Error('video failed'));}));
  await page.locator('.markdown audio').evaluate(el=>new Promise((res,rej)=>{if(el.readyState>=1)return res();el.onloadedmetadata=res;el.onerror=()=>rej(Error('audio failed'));}));
  assert.equal(await page.locator('.markdown video source').count(),1);
  assert.equal(await page.locator('.markdown audio source').count(),1);
  assert.equal(await page.locator('.markdown button[data-action="delete-conv"]').count(),0);
  assert.equal(await page.getByRole('link',{name:'Web documentation'}).getAttribute('target'),'_blank');
  const anchor=await page.getByRole('link',{name:'Jump to examples'}).getAttribute('href');assert.match(anchor,/^#heading-/);assert.equal(await page.locator(anchor).count(),1);
  await page.waitForFunction(()=>document.querySelector('.markdown img[alt="Inline diagram"]').naturalWidth>0);
  await page.waitForFunction(()=>document.querySelector('.media-load-error'));
  const click=async(name)=>page.getByRole('link',{name,exact:true}).click();
  const close=async()=>page.locator('.file-preview-dialog [data-action="close-modal"]').click();
  await click('Screenshot');await page.waitForFunction(()=>document.querySelector('.file-preview-content img')?.naturalWidth>0);
  await page.screenshot({path:path.join(artifacts,'image-desktop.png'),animations:'disabled'});const downloaded=page.waitForEvent('download');await page.getByRole('link',{name:'Download',exact:true}).click();assert.match((await downloaded).suggestedFilename(),/\.svg$/);await close();
  await click('Code line');await page.locator('.file-line-selected').waitFor();assert.match(await page.locator('.file-line-selected').innerText(),/literal code/);assert.equal(await page.locator('.file-preview-content script').count(),0);await close();
  await click('File URI');await page.locator('.file-preview-content pre').waitFor();await close();
  await click('Readme');await page.locator('.file-preview-content .markdown h1').waitFor();await page.getByRole('link',{name:'Text',exact:true}).click();await page.locator('.file-preview-content pre').waitFor();await close();
  await click('HTML document');await page.frameLocator('.file-preview-content iframe').getByRole('heading',{name:'HTML preview'}).waitFor();await close();
  await click('PDF document');const frame=page.locator('.file-preview-content iframe');await frame.waitFor();const blob=await frame.getAttribute('src');assert.match(blob,/^blob:/);assert(await page.evaluate(async u=>(await fetch(u)).ok,blob));await wait(750);await page.screenshot({path:path.join(artifacts,'pdf-desktop.png'),animations:'disabled'});await close();
  await page.waitForFunction(async u=>{try{await fetch(u);return false}catch{return true}},blob); // Object URL revoked on close.
  await click('UTF16');await page.getByText('Windows text preview',{exact:true}).waitFor();await close();
  await click('Raw link');await page.locator('.file-preview-content pre').waitFor();await close();
  await click('Binary');await page.getByText('Preview is unavailable for this file type.',{exact:false}).waitFor();await close();
  await click('Missing');await page.getByText('The linked file is missing or cannot be read.',{exact:true}).waitFor();await close();
  await page.locator('.attach-thumb').click();await page.waitForFunction(()=>document.querySelector('.file-preview-content img')?.naturalWidth>0);await close();
  // Preserve already-loaded players across streaming updates and full rerenders.
  await page.locator('.markdown video').first().evaluate(async el=>{el.muted=true;el.currentTime=0.5;await el.play();el.dataset.fixtureIdentity='same';});
  await page.evaluate(async()=>{const {S}=await import('/static/components/state.js');const {renderStreamMessage}=await import('/static/components/chat.js');S.messages[0].text+='\n\nMore streamed content';renderStreamMessage(S.messages[0]);});
  assert.equal(await page.locator('.markdown video').first().getAttribute('data-fixture-identity'),'same');assert.equal(await page.locator('.markdown video').first().evaluate(el=>el.paused),false);
  await render(md+'\n\nFinal update');assert.equal(await page.locator('.markdown video').first().getAttribute('data-fixture-identity'),'same');
  // A late response from an already-closed popup must not replace the new preview.
  await page.route('**/files?path=slow.txt',async route=>{await wait(300);try{await route.fulfill({contentType:'text/plain',body:'Stale contents'});}catch{}});
  await page.evaluate(async({id})=>{const {previewResource}=await import('/static/components/file-preview.js');previewResource(`/api/conversations/${id}/files?path=slow.txt`,'slow.txt');},{id:conv.id});
  await close();await click('Screenshot');await page.waitForFunction(()=>document.querySelector('.file-preview-content img')?.naturalWidth>0);await wait(350);assert.equal(await page.locator('.file-preview-content img').count(),1);await close();
  await page.route('https://files.example/notes.txt',r=>r.fulfill({headers:{'access-control-allow-origin':'*','content-type':'text/plain'},body:'External text preview'}));
  await page.evaluate(async()=>{const {previewResource}=await import('/static/components/file-preview.js');previewResource('https://files.example/notes.txt','notes.txt');});
  await page.getByText('External text preview',{exact:true}).waitFor();await close();
  // Remote resources render even without CORS; normal page links are not fetched.
  await page.route('https://media.example/plot.png',r=>r.fulfill({contentType:'image/svg+xml',body:fs.readFileSync(svg)}));
  await render(md+'\n\n![Remote](https://media.example/plot.png)');await page.waitForFunction(()=>document.querySelector('img[alt="Remote"]').naturalWidth>0);
  for(const [name,width,height] of [['gallery-desktop',1280,850],['gallery-mobile',390,844]]){await page.setViewportSize({width,height});await page.locator('#chat-scroll').evaluate(el=>{el.scrollTop=0});await page.screenshot({path:path.join(artifacts,name+'.png'),animations:'disabled'});}
  await click('Screenshot');await page.waitForFunction(()=>document.querySelector('.file-preview-content img')?.naturalWidth>0);
  await page.screenshot({path:path.join(artifacts,'image-mobile.png'),animations:'disabled'});
  const bounds=await page.locator('.file-preview-dialog').boundingBox();assert(bounds.x>=0&&bounds.x+bounds.width<=391);await close();
  assert.deepEqual(errors,[]);
  console.log('PASS: real authenticated local files/uploads, media metadata, links, anchors, previews, binary/missing errors, streaming identity, remote image, object URL cleanup. Screenshots: '+artifacts);
 } finally {await browser?.close();child.kill('SIGTERM');await new Promise(r=>{if(child.exitCode!=null)return r();child.once('exit',r);});fs.closeSync(log);fs.rmSync(data,{recursive:true,force:true});}
}
main().catch(e=>{console.error(e);process.exitCode=1});
