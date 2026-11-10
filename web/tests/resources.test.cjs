const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const ctx=vm.createContext({console,TextDecoder,Uint8Array});
vm.runInContext(fs.readFileSync(path.join(__dirname,'../static/components/resources.js'),'utf8').replace(/^export /gm,''),ctx);
const resolve=(raw,extra={})=>JSON.parse(JSON.stringify(ctx.resolveResource(raw,{conversationId:'room 1',...extra})));
for(const input of ['https://example.com/path','https://example.com:443','mailto:hello@example.com','#part','//cdn.example.com/img.png','/api/uploads/u1','/static/img.svg'])test(`preserve browser URL ${input}`,()=>{
 assert.equal(resolve(input).url,input);assert.equal(resolve(input).local,false);
});
for(const input of ['/tmp/file.png','./docs/readme.md','../notes.txt','file:///tmp/my%20file.png','C:\\Users\\Name\\image.png','~/report.pdf','app.go:12:3'])test(`route local resource ${input}`,()=>{
 const ref=resolve(input);assert.equal(ref.local,true);
 assert.equal(decodeURIComponent(ref.url.split('?path=')[1]),input);
 assert.match(ref.url,/^\/api\/conversations\/room%201\/files/);
});
test('encoded spaces, fragments, line numbers and percent signs survive once',()=>{
 assert.equal(resolve('/tmp/report%20%23%20%C3%BC.txt#L12').path,'/tmp/report # ü.txt');
 assert.equal(resolve('/tmp/report%20%23%20%C3%BC.txt#L12').line,12);
 assert.equal(resolve('/tmp/code.go:12:3').line,12);
 assert.equal(resolve('docs/readme.md#setup').anchor,'setup');
 assert.equal(resolve('docs/readme.md#setup').path,'docs/readme.md');
 assert.equal(resolve('/tmp/100%.txt').path,'/tmp/100%.txt');
});
test('links within preview documents resolve against the document directory',()=>{
 assert.equal(resolve('./image.png',{basePath:'/tmp/docs/readme.md'}).path,'/tmp/docs/./image.png');
 assert.equal(resolve('other.md',{basePath:'readme.md'}).path,'./other.md');
});
test('classify signed media URLs, MIME types and editor links',()=>{
 assert.equal(ctx.resourceKind('https://cdn/x.MP4?signature=1'),'video');
 assert.equal(ctx.resourceKind('id','audio/ogg; codecs=opus'),'audio');
 assert.equal(ctx.resourceKind('README.md#part'),'markdown');
 assert.equal(ctx.resourceKind('a.go:12:3'),'text');
 assert.equal(ctx.resourceKind('id','application/pdf'),'pdf');
 assert.equal(ctx.resourceKind('a.zip'),'');
});

test('decode UTF-8, UTF-16 BOM files and declared charsets without treating binary as text',()=>{
 assert.equal(ctx.decodeFileText(Buffer.from('hello')),'hello');
 assert.equal(ctx.decodeFileText(Buffer.from([255,254,104,0,105,0])),'hi');
 assert.equal(ctx.decodeFileText(Buffer.from([254,255,0,104,0,105])),'hi');
 assert.equal(ctx.decodeFileText(Buffer.from([233]),'text/plain;charset=windows-1252'),'é');
 assert.throws(()=>ctx.decodeFileText(Buffer.from([0,255,2])));
 assert.equal(resolve('sandbox:/tmp/chart.png').path,'/tmp/chart.png');
});
