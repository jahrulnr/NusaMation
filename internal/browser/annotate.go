package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/overlay"
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
const framePath=[];
try{let w=window;while(w.frameElement){framePath.unshift(seg(w.frameElement));w=w.parent;}}catch(_){}
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
return{missed:false,
	selector:selector,elementPath:elementPath,tagName:e.tagName.toLowerCase(),
	role:role,name:name.slice(0,200),immediateText:immediate,nearbyText:nearby,
	rect:{x:r.x,y:r.y,width:r.width,height:r.height},isFixed:fixed,
	framePath:framePath,pageUrl:location.href,title:document.title,
	scrollX:window.scrollX||0,scrollY:window.scrollY||0,
	pageW:Math.max(de?de.scrollWidth:0,document.body?document.body.scrollWidth:0),
	pageH:Math.max(de?de.scrollHeight:0,document.body?document.body.scrollHeight:0)};
}`

// Preview is the hover probe result for annotate mode: the element under a
// viewport point, without painting, cropping, or recording a pick — the client
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
	nRes, err := chromedp.Call(cctx, dom.GetNodeForLocation,
		dom.GetNodeForLocationParams{X: int64(math.Round(x)), Y: int64(math.Round(y))})
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	rRes, err := chromedp.Call(cctx, dom.ResolveNode,
		dom.ResolveNodeParams{BackendNodeID: nRes.BackendNodeID})
	if err != nil || rRes.Object == nil || rRes.Object.ObjectID == "" {
		return &Preview{Missed: true}, nil
	}
	cfRes, err := chromedp.Call(cctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		FunctionDeclaration: descriptorExtractor,
		ObjectID:            rRes.Object.ObjectID,
		ReturnByValue:       ptr(true),
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

var highlightConfig = &overlay.HighlightConfig{
	ShowInfo:     true,
	ContentColor: &cdp.RGBA{R: 66, G: 133, B: 244, A: 0.3},
	BorderColor:  &cdp.RGBA{R: 66, G: 133, B: 244, A: 1},
}

// Annotate runs the §3.7 pipeline: hit test → descriptor extraction → overlay
// highlight → clipped element crop → upload → pick record.
func (s *Session) Annotate(ctx context.Context, x, y float64) (*Pick, error) {
	if err := s.acquire(); err != nil {
		return nil, err
	}
	defer s.busy.Store(false)
	// CDP calls need the tab context; ctx only scopes the upload save.
	cctx, cancel := context.WithTimeout(s.tabCtx, 20*time.Second)
	defer cancel()
	fail := func(err error) (*Pick, error) { return nil, fmt.Errorf("annotate: %w", err) }
	nRes, err := chromedp.Call(cctx, dom.GetNodeForLocation,
		dom.GetNodeForLocationParams{X: int64(math.Round(x)), Y: int64(math.Round(y))})
	if err != nil {
		return fail(err)
	}
	rRes, err := chromedp.Call(cctx, dom.ResolveNode,
		dom.ResolveNodeParams{BackendNodeID: nRes.BackendNodeID})
	if err != nil {
		return fail(err)
	}
	if rRes.Object == nil || rRes.Object.ObjectID == "" {
		return nil, ErrPickMissed
	}
	cfRes, err := chromedp.Call(cctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
		FunctionDeclaration: descriptorExtractor,
		ObjectID:            rRes.Object.ObjectID,
		ReturnByValue:       ptr(true),
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
	s.mu.Lock()
	overlayReady := s.overlayReady
	s.mu.Unlock()
	if !overlayReady {
		if _, err := chromedp.Call(cctx, overlay.Enable, cdp.Empty{}); err == nil {
			s.mu.Lock()
			s.overlayReady = true
			s.mu.Unlock()
		}
	}
	_, _ = chromedp.Call(cctx, overlay.HighlightNode, overlay.HighlightNodeParams{
		BackendNodeID: nRes.BackendNodeID, HighlightConfig: highlightConfig,
	})
	// Full-viewport capture — a clipped captureScreenshot makes Chromium
	// transiently resize the visual viewport to the clip, which leaks into the
	// screencast stream as a wrongly-sized frame + meta. The element crop is
	// done on the PNG bytes instead (rect is already viewport CSS px).
	shot, err := chromedp.Call(cctx, page.CaptureScreenshot, page.CaptureScreenshotParams{
		Format: "png",
	})
	// The inspect-style highlight is only needed to mark the element in the
	// crop — it dims the rest of the page, so don't leave it painted; the
	// client draws its own persistent marker while the comment popover is open.
	// Fresh ctx: cctx's 20s budget may already be spent by the capture.
	hctx, hcancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	_, _ = chromedp.Call(hctx, overlay.HideHighlight, cdp.Empty{})
	hcancel()
	if err != nil {
		return fail(err)
	}
	crop, err := cropViewportPNG(shot.Data, desc.Rect)
	if err != nil {
		return fail(err)
	}
	if s.m.save == nil {
		return fail(errors.New("attachment saving is not configured"))
	}
	pickID := ids.New("pk")
	up, err := s.m.save(ctx, s.conversationID, "element-"+pickID+".png", "image/png", crop)
	if err != nil {
		return fail(err)
	}
	p := &Pick{
		ID: pickID, SessionID: s.id, ConversationID: s.conversationID,
		BackendNodeID: int64(nRes.BackendNodeID), Descriptor: desc, Upload: up,
		CreatedAt: nowMillis(),
	}
	s.m.addPick(p)
	return p, nil
}

// cropViewportPNG cuts the element rect out of a full-viewport PNG. The
// descriptor rect is already viewport CSS px and deviceScaleFactor is pinned
// to 1, so rect maps 1:1 onto image pixels; partially off-screen elements are
// clamped to the image bounds (≥1px kept, like the old page-coords clip).
func cropViewportPNG(data []byte, rect store.AnnotationRect) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	x0 := clampF(rect.X, 0, float64(b.Dx()-1))
	y0 := clampF(rect.Y, 0, float64(b.Dy()-1))
	x1 := clampF(rect.X+rect.Width, x0+1, float64(b.Dx()))
	y1 := clampF(rect.Y+rect.Height, y0+1, float64(b.Dy()))
	si, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, errors.New("screenshot image does not support cropping")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, si.SubImage(image.Rect(int(x0), int(y0), int(x1), int(y1)))); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func clampF(v, lo, hi float64) float64 {
	return min(max(v, lo), hi)
}

// ClearAnnotate clears the painted overlay highlight (§3.8); pick records and
// uploads are kept. Best-effort — the only error is a dead session.
func (s *Session) ClearAnnotate() {
	s.mu.Lock()
	ready := s.overlayReady
	s.mu.Unlock()
	if !ready {
		return
	}
	ctx, cancel := context.WithTimeout(s.tabCtx, 5*time.Second)
	_, _ = chromedp.Call(ctx, overlay.HideHighlight, cdp.Empty{})
	cancel()
}
func nowMillis() int64 { return time.Now().UnixMilli() }
