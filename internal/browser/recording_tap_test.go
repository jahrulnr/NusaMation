package browser

// In-page tap integration: recordingTapJS must install on session creation,
// buffer L1 events even with no recorder running, batch inputs, keep
// consoleTapJS working, and deliver through the binding once a recording
// starts. NUSAMATION_BROWSER_TEST=1 to run.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func recTapSession(t *testing.T, html string) (*Manager, *Session) {
	t.Helper()
	m := NewManager(Options{RecordingsDir: t.TempDir()})
	sess, err := m.CreateSession(context.Background(), "cv_rec", htmlDataURL(html), 800, 600)
	if err != nil {
		m.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { sess.Close(); m.Close() })
	return m, sess
}

// recTapEval evaluates an expression and returns the raw result value.
func recTapEval(t *testing.T, sess *Session, expr string) json.RawMessage {
	t.Helper()
	res, err := chromedp.Call(sess.tabCtx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: expr, ReturnByValue: new(true),
	})
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return res.Result.Value
}

func TestRecordingTapInstalledAndBuffers(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1")
	}
	_, sess := recTapSession(t, `<html><body>
		<button id="b" style="position:absolute;left:10px;top:10px">Hit me</button>
		<input id="i" type="text">
		<input id="p" type="password">
		<select id="s"><option>a</option><option>b</option></select>
		</body></html>`)

	// The tap must be live: buffer + take/drain hooks exist before any
	// recording starts.
	var installed bool
	if err := json.Unmarshal(recTapEval(t, sess,
		`!!(window.__nusaRecBuf && window.__nusaRecTake && window.__nusaRecTap)`), &installed); err != nil || !installed {
		t.Fatalf("recording tap not installed: %v", err)
	}

	// Break the binding so flush() restores the batch — the buffered events
	// must survive for the drain to take. (With a working binding the 300ms
	// flush could legally empty the buffer before the take.)
	recTapEval(t, sess, `window.__nusaRecPush = function(){ throw new Error('no binding') }`)
	// Synthesize user-visible actions; the buffer must collect them with the
	// page tap even though nothing is recording.
	recTapEval(t, sess, `(() => {
		document.getElementById('b').click();
		const i = document.getElementById('i');
		i.focus(); i.value = 'hello';
		i.dispatchEvent(new Event('input', {bubbles:true}));
		i.value = 'hello world';
		i.dispatchEvent(new Event('input', {bubbles:true}));
		const p = document.getElementById('p');
		p.value = 'hunter2';
		p.dispatchEvent(new Event('input', {bubbles:true}));
		const sel = document.getElementById('s');
		sel.value = 'b';
		sel.dispatchEvent(new Event('change', {bubbles:true}));
		console.log('tap-log');
		document.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', bubbles:true}));
		return true;
	})()`)
	time.Sleep(100 * time.Millisecond)

	taken := recTapEval(t, sess, `JSON.stringify(window.__nusaRecTake())`)
	var batch string
	if err := json.Unmarshal(taken, &batch); err != nil {
		t.Fatalf("take not a string: %s", taken)
	}
	var evs []struct {
		T         string `json:"t"`
		Value     string `json:"value"`
		InputType string `json:"inputType"`
		Key       string `json:"key"`
		Level     string `json:"level"`
		Text      string `json:"text"`
		Desc      struct {
			Tag      string `json:"tag"`
			Selector string `json:"selector"`
		} `json:"descriptor"`
	}
	if err := json.Unmarshal([]byte(batch), &evs); err != nil {
		t.Fatalf("batch not JSON: %v — %s", err, batch)
	}
	var clicks, inputs, changes, consoles, keys int
	var pwdValue, pwdInputType, textValue string
	for _, ev := range evs {
		switch ev.T {
		case "click":
			clicks++
			if ev.Desc.Tag != "button" || !strings.HasSuffix(ev.Desc.Selector, "button#b") {
				t.Fatalf("click descriptor = %+v", ev.Desc)
			}
		case "input":
			inputs++
			if ev.InputType == "password" {
				pwdValue, pwdInputType = ev.Value, ev.InputType
			} else {
				textValue = ev.Value
			}
		case "change":
			changes++
		case "console":
			consoles++
			if ev.Level != "log" {
				t.Fatalf("console level = %q", ev.Level)
			}
		case "key":
			keys++
			if ev.Key != "Enter" {
				t.Fatalf("key = %q", ev.Key)
			}
		}
	}
	// Two batched text inputs coalesce into one entry per element — the
	// latest value wins — so 'hello world' (not 'hello') is what shipped.
	if clicks != 1 {
		t.Fatalf("clicks = %d", clicks)
	}
	if inputs < 2 || textValue != "hello world" {
		t.Fatalf("inputs = %d textValue=%q (batching should coalesce to latest)", inputs, textValue)
	}
	if pwdValue != "[password]" || pwdInputType != "password" {
		t.Fatalf("password value must mask in-page, got %q type %q", pwdValue, pwdInputType)
	}
	if changes != 1 || consoles != 1 || keys != 1 {
		t.Fatalf("change=%d console=%d key=%d", changes, consoles, keys)
	}
	// __nusaTake emptied the buffer; the consoleTapJS store is untouched by
	// the recording wrapper and still collects.
	var remaining int
	_ = json.Unmarshal(recTapEval(t, sess, `window.__nusaRecTake().length`), &remaining)
	if remaining != 0 {
		t.Fatalf("buffer not drained: %d", remaining)
	}
	var conCount int
	_ = json.Unmarshal(recTapEval(t, sess, `window.__nusaConsole.length`), &conCount)
	if conCount != 1 {
		t.Fatalf("consoleTapJS entries = %d, want 1 (recording must not eat it)", conCount)
	}
}

// With a live recording, tap events leave through __nusaRecPush (or the
// drain tick) and land in events.jsonl as origin dom with Go-assigned seqs.
func TestRecordingTapDelivered(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1")
	}
	_, sess := recTapSession(t, `<html><body><button id="b">x</button></body></html>`)
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	var bindingType string
	_ = json.Unmarshal(recTapEval(t, sess, `typeof window.__nusaRecPush`), &bindingType)
	t.Logf("__nusaRecPush typeof = %q recCap=%v", bindingType, sess.recCap != nil)
	recTapEval(t, sess, `document.getElementById('b').click()`)
	deadline := time.Now().Add(4 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		evs, _, _ := sess.Recording().Events(0, 0, []string{"click"})
		if len(evs) > 0 {
			var d struct {
				Descriptor struct {
					Selector string `json:"selector"`
				} `json:"descriptor"`
			}
			if err := json.Unmarshal(evs[0].Data, &d); err == nil &&
				evs[0].Origin == "dom" && evs[0].Seq > 0 &&
				strings.HasSuffix(d.Descriptor.Selector, "button#b") {
				found = true
			}
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !found {
		var bufLen int
		_ = json.Unmarshal(recTapEval(t, sess, `(window.__nusaRecBuf||[]).length`), &bufLen)
		t.Fatalf("in-page click never reached events.jsonl (buf=%d)", bufLen)
	}
	if _, err := sess.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}

// SPA navigations and errors are captured by the same buffer.
func TestRecordingTapNavAndErrors(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1")
	}
	_, sess := recTapSession(t, `<html><body></body></html>`)
	// pushState rejects data: URLs (opaque origin); about:blank also proves
	// the new-document hook re-injected the tap.
	if _, err := chromedp.Call(sess.tabCtx, page.Navigate, page.NavigateParams{URL: "about:blank"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	var installed bool
	_ = json.Unmarshal(recTapEval(t, sess, `!!(window.__nusaRecBuf && window.__nusaRecTake)`), &installed)
	t.Logf("tap after about:blank = %v", installed)
	// Keep the buffer: a throwing binding makes flush() restore the batch,
	// so the take below sees every event the tap pushed.
	recTapEval(t, sess, `window.__nusaRecPush = function(){ throw new Error('no binding') }`)
	res := recTapEval(t, sess, `(() => {
		try {
			history.pushState({}, '', '#a');
			history.replaceState({}, '', '#b');
		} catch (e) { return 'pushState threw: ' + e.message; }
		console.error('boom');
		setTimeout(() => { Promise.reject(new Error('unhand')); }, 0);
		return 'ok';
	})()`)
	var resStr string
	_ = json.Unmarshal(res, &resStr)
	if resStr != "ok" {
		t.Fatalf("nav script: %s", resStr)
	}
	time.Sleep(300 * time.Millisecond)
	var batch string
	_ = json.Unmarshal(recTapEval(t, sess, `JSON.stringify(window.__nusaRecTake())`), &batch)
	var evs []struct {
		T       string `json:"t"`
		NavType string `json:"navType"`
		URL     string `json:"url"`
		Text    string `json:"text"`
		Level   string `json:"level"`
	}
	_ = json.Unmarshal([]byte(batch), &evs)
	var navs, errs int
	for _, ev := range evs {
		if ev.T == "navigate" && ev.NavType == "spa" {
			navs++
		}
		if ev.T == "console" && ev.Level == "error" {
			errs++
		}
	}
	if navs != 2 { // pushState then replaceState — each URL change emits
		t.Fatalf("spa nav events = %d, want 2", navs)
	}
	if errs < 1 {
		t.Fatalf("console errors = %d, want >= 1", errs)
	}
}
