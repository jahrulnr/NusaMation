package browser

// Repro: CreateSession must install selectShimJS on the pane document — both
// on the initial load (runtime.Evaluate) and on later navigations
// (addScriptToEvaluateOnNewDocument). NUSAMATION_BROWSER_TEST=1 to run.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestCreateSessionInjectsShim(t *testing.T) {
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1")
	}
	m := NewManager(Options{})
	defer m.Close()
	sess, err := m.CreateSession(context.Background(), "cv_test", "about:blank", 800, 600)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close()
	eval := func(expr string) bool {
		res, err := chromedp.Call(sess.tabCtx, runtime.Evaluate, runtime.EvaluateParams{
			Expression:    expr,
			ReturnByValue: new(bool),
		})
		if err != nil {
			return false
		}
		var v bool
		_ = json.Unmarshal([]byte(res.Result.Value), &v)
		return v
	}
	deadline := time.Now().Add(5 * time.Second)
	var tap, shim bool
	for {
		tap = eval(`!!window.__nusaConsole`)
		shim = eval(`!!window.__nusaSelectShim`)
		if tap && shim {
			break
		}
		if time.Now().After(deadline) {
			t.Logf("initial doc: tap=%v shim=%v", tap, shim)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Navigate to a real document; the new-document hook must re-inject both.
	if _, err := chromedp.Call(sess.tabCtx, page.Navigate, page.NavigateParams{URL: htmlDataURL(`<html><body><select><option>a</option></select></body></html>`)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	tap = eval(`!!window.__nusaConsole`)
	shim = eval(`!!window.__nusaSelectShim`)
	t.Logf("after navigate: tap=%v shim=%v", tap, shim)
	if !shim {
		t.Fatalf("shim missing after navigation (tap=%v)", tap)
	}
}
