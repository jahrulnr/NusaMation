package browser

// Region/page annotation tests run without a browser: finishPick is the
// CDP-free tail (upload → pick → recording event), so tests drive it
// directly with a canned PNG and a mock save func.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/store"
)

type savedUpload struct {
	name, mime string
	data       []byte
}

// testAnnotateSession is testRecSession plus the upload-save hook every pick
// commits through.
func testAnnotateSession(t *testing.T) (*Session, *Manager, *[]savedUpload) {
	t.Helper()
	dir := t.TempDir()
	var saved []savedUpload
	m := &Manager{
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		picksDir:      filepath.Join(dir, "picks"),
		recordingsDir: filepath.Join(dir, "recordings"),
		sessions:      map[string]*Session{},
		picks:         map[string]*Pick{},
		browsers:      map[string]*browserProc{},
		save: func(_ context.Context, conversationID, name, mime string, data []byte) (store.Upload, error) {
			saved = append(saved, savedUpload{name: name, mime: mime, data: data})
			return store.Upload{
				ID: ids.New("up"), ConversationID: conversationID,
				Name: name, MIME: mime, Size: int64(len(data)),
			}, nil
		},
	}
	return testRecSession(m, "cv1"), m, &saved
}

// A region pick keeps the drawn rect + page metadata, no element fields, and
// the recording's annotation event carries kind + rect.
func TestFinishPickRegion(t *testing.T) {
	sess, m, saved := testAnnotateSession(t)
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _, _ = sess.StopRecording() }()
	rect := store.AnnotationRect{X: 10, Y: 20, Width: 300, Height: 200}
	desc := store.AnnotationDescriptor{
		Kind: "region", Rect: rect,
		PageURL: "https://ex.test", Title: "Ex",
		Viewport: store.AnnotationViewport{Width: 1280, Height: 800, DPR: 1},
	}
	pick, err := sess.finishPick(context.Background(), []byte("png"), desc, 4,
		map[string]any{"kind": "region", "rect": rect})
	if err != nil {
		t.Fatalf("finishPick: %v", err)
	}
	if pick.Marker != 4 || pick.Descriptor.Kind != "region" || pick.Descriptor.Rect != rect {
		t.Fatalf("pick = %+v", pick)
	}
	if pick.Descriptor.TagName != "" || pick.Descriptor.Selector != "" || pick.Descriptor.Role != "" {
		t.Fatalf("region pick must carry no element fields: %+v", pick.Descriptor)
	}
	if len(*saved) != 1 {
		t.Fatalf("saved uploads = %+v", *saved)
	}
	if got := (*saved)[0]; !strings.HasPrefix(got.name, "region-") || !strings.HasSuffix(got.name, ".png") || got.mime != "image/png" {
		t.Fatalf("upload name/mime = %q %q", got.name, got.mime)
	}
	got, ok := m.Pick(pick.ID)
	if !ok || got.Descriptor.Kind != "region" {
		t.Fatalf("Pick() = %+v ok=%v", got, ok)
	}
	raw, err := os.ReadFile(filepath.Join(m.picksDir, pick.ID+".json"))
	if err != nil || !strings.Contains(string(raw), `"kind":"region"`) {
		t.Fatalf("pick file kind missing: %v %s", err, raw)
	}
	evs, _, _ := sess.Recording().Events(0, 0, []string{"annotation"})
	if len(evs) != 1 {
		t.Fatalf("annotation events = %d, want 1", len(evs))
	}
	var d struct {
		PickID string               `json:"pickId"`
		Kind   string               `json:"kind"`
		Marker int                  `json:"marker"`
		Rect   store.AnnotationRect `json:"rect"`
	}
	if err := json.Unmarshal(evs[0].Data, &d); err != nil {
		t.Fatalf("event data: %v", err)
	}
	if d.PickID != pick.ID || d.Kind != "region" || d.Marker != 4 || d.Rect != rect {
		t.Fatalf("annotation event = %s", evs[0].Data)
	}
}

// A page pick paints no target rect and records kind "page".
func TestFinishPickPage(t *testing.T) {
	sess, m, saved := testAnnotateSession(t)
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _, _ = sess.StopRecording() }()
	desc := store.AnnotationDescriptor{
		Kind: "page", PageURL: "https://ex.test/home", Title: "Home",
		Viewport: store.AnnotationViewport{Width: 1280, Height: 800, DPR: 1},
	}
	pick, err := sess.finishPick(context.Background(), []byte("png"), desc, 0, map[string]any{"kind": "page"})
	if err != nil {
		t.Fatalf("finishPick: %v", err)
	}
	if pick.Marker != 1 || pick.Descriptor.Kind != "page" {
		t.Fatalf("pick = %+v", pick)
	}
	if got := (*saved)[0]; !strings.HasPrefix(got.name, "page-") {
		t.Fatalf("upload name = %q", got.name)
	}
	var d struct {
		Kind string `json:"kind"`
	}
	evs, _, _ := sess.Recording().Events(0, 0, []string{"annotation"})
	if len(evs) != 1 {
		t.Fatalf("annotation events = %d, want 1", len(evs))
	}
	_ = json.Unmarshal(evs[0].Data, &d)
	if d.Kind != "page" {
		t.Fatalf("annotation kind = %q", d.Kind)
	}
	if got, ok := m.Pick(pick.ID); !ok || got.Descriptor.Kind != "page" {
		t.Fatalf("Pick() = %+v ok=%v", got, ok)
	}
}

// The element pick path still resolves through finishPick with its element
// descriptor intact — same upload prefix and event shape as before.
func TestFinishPickElement(t *testing.T) {
	sess, _, saved := testAnnotateSession(t)
	if _, err := sess.StartRecording(""); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	defer func() { _, _ = sess.StopRecording() }()
	desc := store.AnnotationDescriptor{
		Kind: "element", TagName: "button", Role: "button", Name: "Save",
		Selector: "button.save",
	}
	if _, err := sess.finishPick(context.Background(), []byte("png"), desc, 2,
		map[string]any{"kind": "element", "descriptor": map[string]any{
			"tag": desc.TagName, "role": desc.Role, "name": desc.Name, "selector": desc.Selector,
		}}); err != nil {
		t.Fatalf("finishPick: %v", err)
	}
	if got := (*saved)[0]; !strings.HasPrefix(got.name, "element-") {
		t.Fatalf("upload name = %q", got.name)
	}
	evs, _, _ := sess.Recording().Events(0, 0, []string{"annotation"})
	var d struct {
		Kind       string `json:"kind"`
		Descriptor struct {
			Tag      string `json:"tag"`
			Selector string `json:"selector"`
		} `json:"descriptor"`
	}
	if len(evs) != 1 {
		t.Fatalf("annotation events = %d", len(evs))
	}
	_ = json.Unmarshal(evs[0].Data, &d)
	if d.Kind != "element" || d.Descriptor.Tag != "button" || d.Descriptor.Selector != "button.save" {
		t.Fatalf("annotation event = %s", evs[0].Data)
	}
}

// Region validation fails before touching the tab — no browser needed.
func TestAnnotateRegionValidation(t *testing.T) {
	sess, _, _ := testAnnotateSession(t)
	for _, rect := range []store.AnnotationRect{
		{X: 0, Y: 0, Width: 0, Height: 10},
		{X: 0, Y: 0, Width: 10, Height: -1},
	} {
		if _, err := sess.AnnotateRegion(context.Background(), rect, 1); !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("AnnotateRegion(%+v) err = %v, want ErrInvalidAction", rect, err)
		}
	}
}

// Region rects clamp into a sane bound rather than rejecting user drags.
func TestClampAnnotationRect(t *testing.T) {
	r := clampAnnotationRect(store.AnnotationRect{X: 99999, Y: -99999, Width: 99999, Height: 99999})
	if r.X != 16384 || r.Y != -16384 || r.Width != 16384 || r.Height != 16384 {
		t.Fatalf("clamped rect = %+v", r)
	}
	r = clampAnnotationRect(store.AnnotationRect{X: 10, Y: 20, Width: 30, Height: 40})
	if r != (store.AnnotationRect{X: 10, Y: 20, Width: 30, Height: 40}) {
		t.Fatalf("in-bounds rect changed: %+v", r)
	}
}

// Each kind picks its painter: element ring, dashed region box + badge, or
// the bare badge for page.
func TestAnnotationMarkerExprKinds(t *testing.T) {
	rect := store.AnnotationRect{X: 10, Y: 20, Width: 30, Height: 40}
	expr, err := annotationMarkerExpr(store.AnnotationDescriptor{Kind: "region", Rect: rect}, 3)
	if err != nil || !strings.Contains(expr, "dashed") || !strings.Contains(expr, `"width":30`) || !strings.HasSuffix(expr, ",3)") {
		t.Fatalf("region expr = %q err=%v", expr, err)
	}
	expr, err = annotationMarkerExpr(store.AnnotationDescriptor{Kind: "page"}, 1)
	if err != nil || !strings.Contains(expr, "(null,1)") {
		t.Fatalf("page expr = %q err=%v", expr, err)
	}
	expr, err = annotationMarkerExpr(store.AnnotationDescriptor{Rect: rect}, 2)
	if err != nil || !strings.Contains(expr, "solid") || strings.Contains(expr, "dashed") {
		t.Fatalf("element expr = %q err=%v", expr, err)
	}
}
