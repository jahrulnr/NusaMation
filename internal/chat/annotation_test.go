package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func annotationMeta(t *testing.T, anns ...store.MessageAnnotation) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"annotations": anns})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAnnotationBlockUsesMarkerNumberAndNewFields(t *testing.T) {
	meta := annotationMeta(t,
		store.MessageAnnotation{
			PickID: "pk1", Marker: 2, Comment: "tipografi rusak",
			Descriptor: store.AnnotationDescriptor{
				TagName: "h1", Name: "Hero title", Selector: "h1.hero",
				PageURL: "https://example.test", NearbyText: "subheading",
				Rect:     store.AnnotationRect{X: 40, Y: 88, Width: 600, Height: 120},
				Viewport: store.AnnotationViewport{Width: 1280, Height: 800, DPR: 1},
				Computed: store.AnnotationComputed{
					FontSize: "100px", LineHeight: "105px", FontFamily: "Georgia, serif",
					FontWeight: "400", LetterSpacing: "-5.5px", Color: "rgb(17,17,17)",
					BackgroundColor: "rgb(255,255,255)", Display: "block", Position: "static",
				},
				ScrollY: 240,
			},
		},
	)
	out := annotationBlock(meta)
	for _, want := range []string{
		"[2] <h1>", "Comment: tipografi rusak", "Layout: 40,88 600×120 px · picked at 0,0 · viewport 1280×800 @1x · scroll 0,240",
		"Style: font 100px/105px · Georgia, serif · weight 400 · spacing -5.5px · rgb(17,17,17) on rgb(255,255,255) · block/static",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("annotation block missing %q:\n%s", want, out)
		}
	}
}

func TestAnnotationBlockFallsBackToIndexWithoutMarker(t *testing.T) {
	meta := annotationMeta(t,
		store.MessageAnnotation{PickID: "pk-old", Descriptor: store.AnnotationDescriptor{TagName: "div"}},
	)
	out := annotationBlock(meta)
	if !strings.Contains(out, "[1] <div>") {
		t.Fatalf("legacy annotation should number by position:\n%s", out)
	}
	if strings.Contains(out, "Layout:") || strings.Contains(out, "Style:") {
		t.Fatalf("legacy annotation should not emit Layout/Style lines:\n%s", out)
	}
}

func TestAnnotationBlockNotesOmittedScreenshot(t *testing.T) {
	meta := annotationMeta(t,
		store.MessageAnnotation{
			PickID: "pk1", Marker: 1, OmitScreenshot: true,
			Descriptor: store.AnnotationDescriptor{TagName: "button", Name: "Save"},
		},
	)
	out := annotationBlock(meta)
	if !strings.Contains(out, "(no screenshot)") {
		t.Fatalf("omitted screenshot should be noted:\n%s", out)
	}
}
