package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/store"
)

// descriptorExtractor is the Runtime.callFunctionOn payload run with `this`
// bound to the resolved node. It ports the Codex selector algorithm from the
// R&D spec: per level tag#id → tag.cls.cls (≤2 sane classes) → :nth-of-type,
// ≤4 ancestors, shadow boundaries joined with " >>> ", plus the §6.1 fields.
const descriptorExtractor = `function(){
const e=this;
if(!e||e.nodeType!==1)return{missed:true};
const cssv=s=>String(s).replace(/([^\w-])/g,'\\$1');
const seg=el=>{
	const tag=el.tagName.toLowerCase();
	if(el.id)return tag+'#'+cssv(el.id);
	const cls=[...el.classList].filter(c=>/^[A-Za-z_][\w-]*$/.test(c)).slice(0,2);
	if(cls.length)return tag+'.'+cls.map(cssv).join('.');
	const p=el.parentElement;
	if(p){const same=[...p.children].filter(c=>c.tagName===el.tagName);
		if(same.length>1)return tag+':nth-of-type('+(same.indexOf(el)+1)+')';}
	return tag;
};
const nth=el=>{
	const tag=el.tagName.toLowerCase();const p=el.parentElement;
	if(!p)return tag;
	const same=[...p.children].filter(c=>c.tagName===el.tagName);
	return same.length>1?tag+':nth-of-type('+(same.indexOf(el)+1)+')':tag;
};
let chains=[[]],node=e,d=0;
while(node&&node.nodeType===1&&d<4){
	chains[0].unshift(seg(node));d++;
	const root=node.getRootNode();
	if(root&&root.host){node=root.host;chains.unshift([]);continue;}
	node=node.parentElement;
}
const selector=chains.map(c=>c.join(' > ')).join(' >>> ');
let pchains=[[]],pn=e,pd=0;
while(pn&&pn.nodeType===1&&pd<24){
	pchains[0].unshift(nth(pn));pd++;
	const root=pn.getRootNode();
	if(root&&root.host){pn=root.host;pchains.unshift([]);continue;}
	pn=pn.parentElement;
}
const elementPath=pchains.map(c=>c.join(' > ')).join(' >>> ');
const framePath=[];let offX=0,offY=0;
// Accumulate frameElement rects so rect is top-viewport CSS px like the
// client's click point — the pin marker is painted in the main document.
try{let w=window;while(w.frameElement){const fr=w.frameElement.getBoundingClientRect();
	offX+=fr.left;offY+=fr.top;framePath.unshift(seg(w.frameElement));w=w.parent;}}catch(_){}
const roleMap={BUTTON:'button',SELECT:'combobox',TEXTAREA:'textbox',NAV:'navigation',MAIN:'main',
	ASIDE:'complementary',H1:'heading',H2:'heading',H3:'heading',H4:'heading',H5:'heading',
	H6:'heading',UL:'list',OL:'list',LI:'listitem',TABLE:'table',IMG:'img',DIALOG:'dialog',FORM:'form'};
let role=e.getAttribute('role')||'';
if(!role){
	if(e.tagName==='INPUT'){const t=(e.getAttribute('type')||'text').toLowerCase();
		role={checkbox:'checkbox',radio:'radio',range:'slider',number:'spinbutton',
			search:'searchbox',submit:'button',button:'button',reset:'button',image:'button'}[t]||'textbox';}
	else if(e.tagName==='A'){role=e.hasAttribute('href')?'link':'';}
	else role=roleMap[e.tagName]||'';
}
let name='';
const aria=e.getAttribute('aria-label');if(aria)name=aria.trim();
if(!name){const lb=e.getAttribute('aria-labelledby');
	if(lb){name=lb.split(/\s+/).map(id=>{const n=e.ownerDocument.getElementById(id);return n?n.textContent:''}).join(' ').trim();}}
if(!name){const al=e.getAttribute('alt')||e.getAttribute('title')||e.getAttribute('placeholder');if(al)name=al.trim();}
if(!name&&(e.tagName==='A'||e.tagName==='BUTTON'||role==='button'||role==='link'))name=(e.textContent||'').trim();
if(!name){try{const lab=e.labels&&e.labels[0];if(lab)name=(lab.textContent||'').trim();}catch(_){}}
let immediate=[...e.childNodes].filter(n=>n.nodeType===3).map(n=>n.textContent).join('').trim();
if(!immediate)immediate=(e.innerText||'').trim();
immediate=immediate.replace(/\s+/g,' ').slice(0,200);
let nearby='';
try{const p=e.parentElement;
	if(p){let t=(p.innerText||'').trim();
		const et=(e.innerText||'').trim();
		if(et&&t.includes(et))t=t.replace(et,'');
		nearby=t.replace(/\s+/g,' ').trim().slice(0,300);}}catch(_){}
let fixed=false;
for(let n=e;n&&n.nodeType===1;n=n.parentElement){const pos=getComputedStyle(n).position;
	if(pos==='fixed'||pos==='sticky'){fixed=true;break;}}
const r=e.getBoundingClientRect();
const de=document.documentElement;
const cs=getComputedStyle(e);
let html='';
try{const oh=e.outerHTML||'';const gi=oh.indexOf('>');
	html=(gi>=0?oh.slice(0,gi+1):oh).slice(0,400);}catch(_){}
return{missed:false,
	selector:selector,elementPath:elementPath,tagName:e.tagName.toLowerCase(),
	html:html,
	role:role,name:name.slice(0,200),immediateText:immediate,nearbyText:nearby,
	rect:{x:r.x+offX,y:r.y+offY,width:r.width,height:r.height},isFixed:fixed,
	framePath:framePath,pageUrl:location.href,title:document.title,
	viewport:{width:window.innerWidth||0,height:window.innerHeight||0,dpr:window.devicePixelRatio||1},
	computed:{fontSize:cs.fontSize,lineHeight:cs.lineHeight,fontFamily:cs.fontFamily,
		fontWeight:cs.fontWeight,letterSpacing:cs.letterSpacing,color:cs.color,
		backgroundColor:cs.backgroundColor,display:cs.display,position:cs.position},
	scrollX:window.scrollX||0,scrollY:window.scrollY||0,
	pageW:Math.max(de?de.scrollWidth:0,document.body?document.body.scrollWidth:0),
	pageH:Math.max(de?de.scrollHeight:0,document.body?document.body.scrollHeight:0)};
}`

// pickElement resolves the topmost element at a client-viewport point. Unlike
// DOM.getNodeForLocation it is defined in the same coordinate space the
// screencast shows — click coords stay correct on a scrolled page. Same-origin
// iframes are entered recursively so pins land on inner elements; a
// cross-origin frame yields the <iframe> element itself.
const pickElement = `function(x,y){
var doc=document,el=null;
for(var d=0;d<8;d++){
	el=doc.elementFromPoint(x,y);
	if(!el)return null;
	var t=el.tagName;
	if(t!=='IFRAME'&&t!=='FRAME')return el;
	var inner=null;
	try{inner=el.contentDocument;}catch(e){}
	if(!inner)return el;
	var r=el.getBoundingClientRect();
	x-=r.left;y-=r.top;doc=inner;
}
return el;}`

// Preview is the hover probe result for annotate mode: the element under a
// viewport point, without painting, shooting, or recording a pick — the client
// draws the transient marker itself.
type Preview struct {
	Missed   bool                  `json:"missed"`
	Skipped  bool                  `json:"skipped,omitempty"`
	Rect     *store.AnnotationRect `json:"rect,omitempty"`
	TagName  string                `json:"tagName,omitempty"`
	Name     string                `json:"name,omitempty"`
	Selector string                `json:"selector,omitempty"`
}

// PreviewAnnotate is the lightweight hover companion of Annotate: hit test +
// descriptor fields only, and it must never queue behind a navigation or pick,
// so a busy session reports {skipped:true} instead of ErrBusy and the client
// drops that hover silently.
func (s *Session) PreviewAnnotate(x, y float64) (*Preview, error) {
	s.mu.Lock()
	err := s.aliveLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if s.busy.Load() {
		return &Preview{Skipped: true}, nil
	}
	cctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	defer cancel()
	objID, err := s.pickObjectID(cctx, x, y)
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	if objID == "" {
		return &Preview{Missed: true}, nil
	}
	cfRes, err := chromedp.Call(cctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		FunctionDeclaration: descriptorExtractor,
		ObjectID:            objID,
		ReturnByValue:       new(true),
	})
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	if cfRes.ExceptionDetails != nil || cfRes.Result == nil {
		return &Preview{Missed: true}, nil
	}
	var aux struct {
		Missed   bool                 `json:"missed"`
		Rect     store.AnnotationRect `json:"rect"`
		TagName  string               `json:"tagName"`
		Name     string               `json:"name"`
		Label    string               `json:"immediateText"`
		Selector string               `json:"selector"`
	}
	if err := json.Unmarshal([]byte(cfRes.Result.Value), &aux); err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	p := &Preview{Missed: aux.Missed, TagName: aux.TagName, Name: aux.Name, Selector: aux.Selector}
	if !aux.Missed {
		p.Rect = &aux.Rect
	}
	if p.Name == "" {
		p.Name = aux.Label
	}
	return p, nil
}

// markerPainter injects the pin ring + numbered badge as plain DOM so the
// screenshot shows exactly what the agent gets — including a badge number
// matching the [N] label in the prompt text. position:fixed aligns 1:1 with
// the descriptor's viewport-CSS-px rect, and z-index tops everything.
const markerPainter = `function(r,n){
var old=document.getElementById('__nusamation_marker');if(old)old.remove();
var host=document.createElement('div');
host.id='__nusamation_marker';
host.setAttribute('style','position:fixed !important;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none;');
var ring=document.createElement('div');
ring.setAttribute('style','position:fixed !important;left:'+r.x+'px;top:'+r.y+'px;width:'+Math.max(2,r.width)+'px;height:'+Math.max(2,r.height)+'px;border:2px solid #3b82f6 !important;background:rgba(59,130,246,.14) !important;border-radius:3px;box-sizing:border-box;');
var vw=window.innerWidth||1280,vh=window.innerHeight||800;
var bx=r.x+r.width/2,by=r.y;
bx=Math.max(14,Math.min(bx,vw-14));
if(by<14)by=r.y+r.height;
by=Math.max(14,Math.min(by,vh-14));
var badge=document.createElement('div');
badge.setAttribute('style','position:fixed !important;left:'+bx+'px;top:'+by+'px;transform:translate(-50%,-50%);min-width:20px;height:20px;padding:0 5px;border-radius:10px;background:#3b82f6 !important;color:#fff !important;font:600 12px/20px system-ui,-apple-system,"Segoe UI",sans-serif;text-align:center;box-shadow:0 0 0 2px #fff,0 1px 4px rgba(0,0,0,.35);box-sizing:content-box;');
badge.textContent=String(n);
host.appendChild(ring);host.appendChild(badge);
(document.documentElement||document.body).appendChild(host);
// Wait two frames so the pin is painted before the caller screenshots —
// with a timeout fallback: a throttled (backgrounded) tab may starve rAF.
return new Promise(function(res){var t=setTimeout(res,250);requestAnimationFrame(function(){requestAnimationFrame(function(){clearTimeout(t);res()})})});}`

const markerRemover = `(function(){var m=document.getElementById('__nusamation_marker');if(m)m.remove();return true})()`

func markerExpr(rect store.AnnotationRect, n int) (string, error) {
	r, err := json.Marshal(rect)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s)(%s,%d)", markerPainter, r, n), nil
}

// pickObjectID hit-tests a client-viewport point via elementFromPoint and
// returns the RemoteObjectID of the resolved element — empty when the point
// has no element. The object may live in an iframe's context; callFunctionOn
// on its id runs in that context, matching the extractor's expectations.
func (s *Session) pickObjectID(ctx context.Context, x, y float64) (runtime.RemoteObjectID, error) {
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: fmt.Sprintf("(%s)(%g,%g)", pickElement, x, y),
	})
	if err != nil {
		return "", err
	}
	if res.ExceptionDetails != nil {
		return "", errors.New(res.ExceptionDetails.Text)
	}
	if res.Result == nil {
		return "", errors.New("element pick returned no result")
	}
	return res.Result.ObjectID, nil
}

// Annotate runs the §3.7 pipeline: hit test → descriptor extraction → DOM
// pin marker → marked full-viewport shot → marker removed → upload → pick.
func (s *Session) Annotate(ctx context.Context, x, y float64, marker int) (*Pick, error) {
	if err := s.acquire(); err != nil {
		return nil, err
	}
	defer s.busy.Store(false)
	// CDP calls need the tab context; ctx only scopes the upload save.
	cctx, cancel := context.WithTimeout(s.tabCtx, 20*time.Second)
	defer cancel()
	fail := func(err error) (*Pick, error) { return nil, fmt.Errorf("annotate: %w", err) }
	objID, err := s.pickObjectID(cctx, x, y)
	if err != nil {
		return fail(err)
	}
	if objID == "" {
		return nil, ErrPickMissed
	}
	cfRes, err := chromedp.Call(cctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		FunctionDeclaration: descriptorExtractor,
		ObjectID:            objID,
		ReturnByValue:       new(true),
	})
	if err != nil {
		return fail(err)
	}
	if cfRes.ExceptionDetails != nil {
		return fail(errors.New(cfRes.ExceptionDetails.Text))
	}
	if cfRes.Result == nil {
		return fail(errors.New("descriptor extraction returned no result"))
	}
	var raw []byte = []byte(cfRes.Result.Value)
	var aux struct {
		Missed bool `json:"missed"`
	}
	var desc store.AnnotationDescriptor
	if err := json.Unmarshal(raw, &aux); err != nil {
		return fail(err)
	}
	if aux.Missed {
		return nil, ErrPickMissed
	}
	if err := json.Unmarshal(raw, &desc); err != nil {
		return fail(err)
	}
	desc.Point = store.AnnotationPoint{X: x, Y: y}
	if marker <= 0 {
		marker = 1
	}
	// Paint the numbered pin into the page and capture the marked viewport —
	// the badge inside the shot matches the [N] label in the prompt text.
	// Full-viewport capture only: a clipped captureScreenshot transiently
	// resizes the visual viewport and leaks a wrongly-sized frame into the
	// screencast stream.
	if expr, err := markerExpr(desc.Rect, marker); err == nil {
		if _, err := chromedp.Call(cctx, runtime.Evaluate, runtime.EvaluateParams{
			Expression: expr, AwaitPromise: new(true),
		}); err != nil {
			s.m.log.Warn("annotation pin paint failed; screenshot will be unmarked",
				"session", s.id, "error", err)
		}
	} else {
		s.m.log.Warn("annotation pin marker not built; screenshot will be unmarked",
			"session", s.id, "error", err)
	}
	marked, err := chromedp.Call(cctx, page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: "png",
	})
	// Fresh ctx: cctx's 20s budget may already be spent by the capture.
	hctx, hcancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	_, _ = chromedp.Call(hctx, runtime.Evaluate, runtime.EvaluateParams{Expression: markerRemover})
	hcancel()
	if err != nil {
		return fail(err)
	}
	if s.m.save == nil {
		return fail(errors.New("attachment saving is not configured"))
	}
	pickID := ids.New("pk")
	up, err := s.m.save(ctx, s.conversationID, "element-"+pickID+".png", "image/png", marked.Data)
	if err != nil {
		return fail(err)
	}
	p := &Pick{
		ID: pickID, SessionID: s.id, ConversationID: s.conversationID,
		Marker: marker, Descriptor: desc,
		Upload: up, CreatedAt: nowMillis(),
	}
	s.m.addPick(p)
	return p, nil
}

// ClearAnnotate removes the painted pin marker (§3.8); pick records and
// uploads are kept. Best-effort — the only error is a dead session. A pick
// in flight owns the marker lifecycle, so a clear landing mid-pick is a
// no-op rather than stripping the badge between paint and capture — the
// acquire() CAS serializes it with Annotate's own claim.
func (s *Session) ClearAnnotate() {
	if err := s.acquire(); err != nil {
		return
	}
	defer s.busy.Store(false)
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	_, _ = chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{Expression: markerRemover})
	cancel()
}
func nowMillis() int64 { return time.Now().UnixMilli() }
