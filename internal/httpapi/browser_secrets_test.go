package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

/* ---------------- resolver ---------------- */

func TestResolveSecretTextWholeValue(t *testing.T) {
	res, b, err := resolveBrowserSecretText("[[env:TOKEN]]", map[string]string{"TOKEN": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if res != "s3cret" || b["s3cret"] != "[[env:TOKEN]]" {
		t.Fatalf("got %q %+v", res, b)
	}
}

func TestResolveSecretTextInlineAndRepeat(t *testing.T) {
	res, b, err := resolveBrowserSecretText("u=[[env:U]]&p=[[env:P]] x[[env:U]]", map[string]string{"U": "alice", "P": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if res != "u=alice&p=pw xalice" {
		t.Fatalf("got %q", res)
	}
	if len(b) != 2 || b["alice"] != "[[env:U]]" || b["pw"] != "[[env:P]]" {
		t.Fatalf("bindings %+v", b)
	}
}

func TestResolveSecretTextEscapedOpeners(t *testing.T) {
	// One backslash escapes the opener and is dropped.
	res, _, err := resolveBrowserSecretText(`\[[env:TOKEN]]`, map[string]string{"TOKEN": "s3cret"})
	if err != nil || res != "[[env:TOKEN]]" {
		t.Fatalf("got %q err %v", res, err)
	}
	// Two backslashes: first is literal, second escapes → one slash remains.
	res, _, err = resolveBrowserSecretText(`\\[[env:TOKEN]]`, map[string]string{"TOKEN": "s3cret"})
	if err != nil || res != `\[[env:TOKEN]]` {
		t.Fatalf("got %q err %v", res, err)
	}
	// A backslash that is not directly before [[ is untouched.
	res, _, err = resolveBrowserSecretText(`C:\Temp [[env:T]]`, map[string]string{"T": "v"})
	if err != nil || res != `C:\Temp v` {
		t.Fatalf("got %q err %v", res, err)
	}
}

func TestResolveSecretTextOtherBracketSyntaxUntouched(t *testing.T) {
	in := "[[link]] [[env]] [[envx:A]] x[[foo:bar]]"
	res, b, err := resolveBrowserSecretText(in, nil)
	if err != nil || res != in || len(b) != 0 {
		t.Fatalf("got %q %+v err %v", res, b, err)
	}
}

func TestResolveSecretTextUnknownAndEmpty(t *testing.T) {
	if _, _, err := resolveBrowserSecretText("[[env:NOPE]]", map[string]string{"A": "1"}); !errors.Is(err, errUnknownSecret) {
		t.Fatalf("want unknown, got %v", err)
	}
	if _, _, err := resolveBrowserSecretText("[[env:A]]", map[string]string{"A": ""}); !errors.Is(err, errUnknownSecret) {
		t.Fatalf("empty value should be unknown, got %v", err)
	}
}

func TestResolveSecretTextMalformed(t *testing.T) {
	for _, in := range []string{"[[env:]]", "[[env:9X]]", "[[env:A-B]]", "[[env:A", "[[env:A]", "[[env: ]]"} {
		if _, _, err := resolveBrowserSecretText(in, map[string]string{"A": "1"}); !errors.Is(err, errInvalidSecret) {
			t.Fatalf("%q: want invalid, got %v", in, err)
		}
	}
}

func TestResolveSecretTextNoRecursion(t *testing.T) {
	// A resolved value containing a tag is written literally, never re-scanned.
	res, _, err := resolveBrowserSecretText("[[env:A]]", map[string]string{"A": "[[env:B]]", "B": "deep"})
	if err != nil || res != "[[env:B]]" {
		t.Fatalf("got %q err %v", res, err)
	}
}

func TestResolveSecretTextNoTagsIsIdentity(t *testing.T) {
	res, b, err := resolveBrowserSecretText("plain text", nil)
	if err != nil || res != "plain text" || len(b) != 0 {
		t.Fatalf("got %q err %v", res, err)
	}
}

func TestHasReservedTagHonorsEscape(t *testing.T) {
	if !hasReservedTag("x[[env:A]]") {
		t.Fatal("unescaped opener not seen")
	}
	if hasReservedTag(`\[[env:A]]`) {
		t.Fatal("escaped opener should not count as reserved")
	}
}

/* ---------------- json masking ---------------- */

func TestMaskJSONValueMasksStringsKeysAndNested(t *testing.T) {
	sess := &browser.Session{}
	sess.RegisterSecrets(map[string]string{"s3cret-value": "[[env:TOKEN]]", "key-with-secret": "[[env:K]]"})
	mask := func(s string) string { return sess.MaskSecrets(s) }
	in := map[string]any{
		"title":           "the password is s3cret-value",
		"key-with-secret": map[string]any{"deep": "x s3cret-value y"},
		"result":          map[string]any{"image": "s3cret-value"}, // nested images DO mask
		"image":           "c2VjcmV0LWJ5dGVz",                      // top-level screencast kept
		"n":               3.0,
	}
	out := maskJSONValue(in, mask, true).(map[string]any)
	if out["title"] != "the password is [[env:TOKEN]]" {
		t.Fatalf("title %v", out["title"])
	}
	if out["[[env:K]]"] == nil {
		t.Fatalf("object key not masked: %v", out)
	}
	if out["result"].(map[string]any)["image"] != "[[env:TOKEN]]" {
		t.Fatal("nested result.image was skipped")
	}
	if out["image"] != "c2VjcmV0LWJ5dGVz" {
		t.Fatal("top-level image was masked")
	}
	if out["n"] != 3.0 {
		t.Fatal("unmatched number changed")
	}
}

func TestMaskJSONValueNumberAndBoolSpelling(t *testing.T) {
	sess := &browser.Session{}
	sess.RegisterSecrets(map[string]string{"42": "[[env:N]]", "true": "[[env:B]]"})
	mask := func(s string) string { return sess.MaskSecrets(s) }
	got := maskJSONValue(map[string]any{"a": json.Number("42"), "b": json.Number("-0"), "c": true}, mask, false).(map[string]any)
	if got["a"] != "[[env:N]]" {
		t.Fatalf("number spelling not masked: %v", got["a"])
	}
	if got["b"] != json.Number("-0") {
		t.Fatalf("unmatched -0 changed: %v", got["b"])
	}
	if got["c"] != "[[env:B]]" {
		t.Fatalf("bool spelling not masked: %v", got["c"])
	}
}

/* ---------------- masked writer ---------------- */

type fakeWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (f *fakeWriter) Header() http.Header         { return f.header }
func (f *fakeWriter) WriteHeader(s int)           { f.status = s }
func (f *fakeWriter) Write(p []byte) (int, error) { return f.body.Write(p) }

func TestSecretMaskWriterSuccessAndErrors(t *testing.T) {
	sess := &browser.Session{}
	sess.RegisterSecrets(map[string]string{"tok\nen\"": "[[env:T]]"})
	fw := &fakeWriter{header: http.Header{}}
	mw := newSecretMaskWriter(fw, sess)
	mw.Header().Set("Content-Type", "application/json; charset=utf-8")
	mw.Header().Set("Content-Length", "999")
	mw.WriteHeader(http.StatusBadGateway)
	_, _ = mw.Write([]byte(`{"error":"leaked tok\nen\" here","code":"x","image":"AA=="}`))
	mw.flush()
	if fw.status != http.StatusBadGateway {
		t.Fatalf("status %d", fw.status)
	}
	if fw.header.Get("Content-Length") != "" {
		t.Fatal("Content-Length not removed")
	}
	if !strings.Contains(fw.body.String(), "leaked [[env:T]] here") {
		t.Fatalf("escaped secret not masked: %s", fw.body.String())
	}
	if !strings.Contains(fw.body.String(), `"image":"AA=="`) {
		t.Fatalf("top-level image masked: %s", fw.body.String())
	}
}

func TestSecretMaskWriterNonJSONBecomesGenericError(t *testing.T) {
	sess := &browser.Session{}
	sess.RegisterSecrets(map[string]string{"s3cret": "[[env:T]]"})
	fw := &fakeWriter{header: http.Header{}}
	mw := newSecretMaskWriter(fw, sess)
	_, _ = mw.Write([]byte("not json s3cret"))
	mw.flush()
	if fw.status != http.StatusInternalServerError {
		t.Fatalf("status %d", fw.status)
	}
	if strings.Contains(fw.body.String(), "s3cret") {
		t.Fatalf("raw body leaked: %s", fw.body.String())
	}
}

func TestSecretMaskWriterEmptyRegistryPassesThrough(t *testing.T) {
	sess := &browser.Session{}
	fw := &fakeWriter{header: http.Header{}}
	mw := newSecretMaskWriter(fw, sess)
	mw.WriteHeader(http.StatusOK)
	_, _ = mw.Write([]byte(`{"ok":true,"n":-0}`))
	mw.flush()
	if !strings.Contains(fw.body.String(), `"ok":true`) || !strings.Contains(fw.body.String(), `"n":-0`) {
		t.Fatalf("body changed: %s", fw.body.String())
	}
}

func TestResolveAliasSmallestTagAndPartialBindings(t *testing.T) {
	// Two names sharing a value keep the smallest tag deterministically.
	res, b, err := resolveBrowserSecretText("[[env:Z]]x[[env:A]]", map[string]string{"Z": "v", "A": "v"})
	if err != nil || b["v"] != "[[env:A]]" {
		t.Fatalf("got %q %+v err %v", res, b, err)
	}
	// A failing later tag still returns the earlier resolved bindings.
	_, b2, err := resolveBrowserSecretText("[[env:A]] then [[env:NOPE]]", map[string]string{"A": "v1"})
	if !errors.Is(err, errUnknownSecret) || b2["v1"] != "[[env:A]]" {
		t.Fatalf("partial bindings lost: %+v err %v", b2, err)
	}
}

func TestMaskPreservesEnvelopeShape(t *testing.T) {
	sess := &browser.Session{}
	sess.RegisterSecrets(map[string]string{"t": "[[env:T]]", "1": "[[env:N]]"})
	mask := func(s string) string { return sess.MaskSecrets(s) }
	in := map[string]any{
		"sessionId": "bs_1", "url": "https://t/x", "title": "t title",
		"width": json.Number("1280"), "height": json.Number("800"),
		"mimeType": "image/jpeg", "result": map[string]any{"t": "v1"},
		"code": "not_actionable", "detail": "ref e1: filled",
	}
	out := maskJSONValue(in, mask, true).(map[string]any)
	if out["result"] == nil {
		t.Fatal("result key lost to masking")
	}
	if out["width"] != json.Number("1280") || out["height"] != json.Number("800") {
		t.Fatalf("dimensions corrupted: %v %v", out["width"], out["height"])
	}
	if out["sessionId"] != "bs_1" || out["code"] != "not_actionable" || out["mimeType"] != "image/jpeg" {
		t.Fatal("fixed envelope fields masked")
	}
	if out["title"] == "t title" {
		t.Fatal("dynamic title not masked")
	}
	inner := out["result"].(map[string]any)
	if inner["t"] != nil {
		t.Fatalf("nested result key must still mask: %v", inner)
	}
}

func TestFillBrowserFieldsAliasAcrossFields(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/nusamation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(dir + "/secret.key")
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	ctx := context.Background()
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "a", Command: "/bin/true", Cwd: dir,
		Env: store.SecretList{{Name: "Z", Value: "v"}, {Name: "A", Value: "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "c", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	sess := &browser.Session{}
	req := httptest.NewRequest(http.MethodPost, "/api/browser-use", strings.NewReader("{}"))
	// A then Z across two fields, then an unknown third field: the lex-min tag
	// must win over last-write ordering in the accumulated registry.
	fields := []browser.FieldValue{
		{Ref: "e1", Value: "[[env:A]]"},
		{Ref: "e2", Value: "[[env:Z]]"},
		{Ref: "e3", Value: "[[env:NOPE]]"},
	}
	_, err = s.fillBrowserFields(req, conv.ID, sess, fields)
	if !errors.Is(err, errUnknownSecret) {
		t.Fatalf("want unknown_secret, got %v", err)
	}
	if got := sess.MaskSecrets("v"); got != "[[env:A]]" {
		t.Fatalf("alias across fields: got %q", got)
	}
}
