package browser

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
)

// Observations feed the browser_use agent a "what happened since your last
// call" tail: the always-on __nusaRecPush binding stream lands in a small
// ring regardless of recording state, and every drive-tool response drains
// the ring past a per-session cursor. Synthesized entries join the drain —
// full-document navigations (the tap only sees SPA navs; its buffer resets
// on unload) and failed netLog requests — merged back into timestamp order.
// Scroll events are filtered out at push time as pure noise.

const (
	obsRingCap = 120
	obsTakeCap = 25
	obsTextCap = 160
)

// obsEvent mirrors the recordingTapJS event shape — only the fields
// observations read.
type obsEvent struct {
	T          string          `json:"t"`
	TS         int64           `json:"ts"`
	Level      string          `json:"level"`
	Text       string          `json:"text"`
	Count      int             `json:"count"`
	Key        string          `json:"key"`
	URL        string          `json:"url"`
	Value      json.RawMessage `json:"value"`
	X          float64         `json:"x"`
	Y          float64         `json:"y"`
	ToX        float64         `json:"toX"`
	ToY        float64         `json:"toY"`
	Descriptor json.RawMessage `json:"descriptor"`
}

type obsLine struct {
	seq  int64
	ts   int64
	line string
}

// watchObsBindings consumes __nusaRecPush batches for the tab's whole life.
// Each cdp.Events iterator is an independent subscription, so a recording's
// watchRecBindings receives the same batches beside this one.
func (s *Session) watchObsBindings() {
	for ev, err := range cdp.Events[runtime.EventBindingCalled](s.tabCtx, s.tab(), runtime.BindingCalled) {
		if err != nil {
			return
		}
		if ev.Name == "__nusaRecPush" {
			s.appendObsBatch([]byte(ev.Payload))
		}
	}
}

// appendObsBatch parses one pushed batch (a JSON array, or the same array
// inside a JSON string) and formats each entry into the obs ring.
func (s *Session) appendObsBatch(payload []byte) {
	var evs []json.RawMessage
	if json.Unmarshal(payload, &evs) != nil {
		var nested string
		if json.Unmarshal(payload, &nested) != nil || json.Unmarshal([]byte(nested), &evs) != nil {
			return
		}
	}
	for _, raw := range evs {
		var ev obsEvent
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		s.pushObs(&ev)
	}
}

// obsFormat renders one tap event as a compact agent-facing line; "" drops it.
func obsFormat(ev *obsEvent) string {
	desc := obsDescribe(ev.Descriptor)
	switch ev.T {
	case "navigate":
		return "nav " + obsClip(ev.URL)
	case "console":
		if ev.Level != "warn" && ev.Level != "error" {
			return ""
		}
		line := "console." + ev.Level + " " + obsClip(ev.Text)
		if ev.Count > 1 {
			line += fmt.Sprintf(" (×%d)", ev.Count)
		}
		return line
	case "click", "dblclick", "submit", "dragstart", "drop":
		if desc == "" {
			return ""
		}
		return ev.T + " " + desc
	case "drag":
		return fmt.Sprintf("drag %s (%d,%d)→(%d,%d)", desc, int(ev.X), int(ev.Y), int(ev.ToX), int(ev.ToY))
	case "input", "change":
		v := strings.Trim(string(ev.Value), `"`)
		return fmt.Sprintf("%s %s = %q", ev.T, desc, obsClip(v))
	case "key":
		if desc != "" {
			return "key " + ev.Key + " on " + desc
		}
		return "key " + ev.Key
	default:
		return ""
	}
}

// obsDescribe renders a tap descriptor as `role "name"` or a bare selector.
func obsDescribe(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var d struct {
		Tag      string `json:"tag"`
		Role     string `json:"role"`
		Name     string `json:"name"`
		Selector string `json:"selector"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return ""
	}
	kind := d.Role
	if kind == "" {
		kind = d.Tag
	}
	if d.Name != "" {
		return fmt.Sprintf("%s %q", kind, obsClip(d.Name))
	}
	if d.Selector != "" {
		return obsClip(d.Selector)
	}
	return kind
}

func obsClip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > obsTextCap {
		return s[:obsTextCap] + "…"
	}
	return s
}

// pushObs appends one formatted line; a navigate also updates obsURL so the
// synthesized full-nav line never repeats a SPA nav the tap already gave us.
func (s *Session) pushObs(ev *obsEvent) {
	line := obsFormat(ev)
	if line == "" {
		return
	}
	s.obsMu.Lock()
	s.obsSeq++
	s.obs = append(s.obs, obsLine{seq: s.obsSeq, ts: ev.TS, line: line})
	if len(s.obs) > obsRingCap {
		s.obs = s.obs[len(s.obs)-obsRingCap:]
	}
	if ev.T == "navigate" && ev.URL != "" {
		s.obsURL = ev.URL
	}
	s.obsMu.Unlock()
}

// Observations drains the ring past the cursor: tap lines, failed netLog
// requests, and a synthesized full-document nav, merged by timestamp.
func (s *Session) Observations() []string {
	var items []obsLine
	var skipped int64
	s.obsMu.Lock()
	for _, o := range s.obs {
		if o.seq <= s.obsCursor {
			continue
		}
		if s.obsCursor > 0 && o.seq > s.obsCursor+1 && skipped == 0 {
			skipped = o.seq - s.obsCursor - 1
		}
		items = append(items, o)
		s.obsCursor = o.seq
	}
	s.obsMu.Unlock()

	// Full-document navigations reset the in-page tap without an event; the
	// URL diff surfaces them here.
	if u := s.View(false).URL; u != "" {
		s.obsMu.Lock()
		if s.obsURL == "" {
			s.obsURL = u
		} else if u != s.obsURL {
			s.obsURL = u
			items = append(items, obsLine{ts: nowMillis(), line: "nav " + obsClip(u)})
		}
		s.obsMu.Unlock()
	}

	// Failed requests ride the always-on netLog ring past their own cursor.
	s.netMu.Lock()
	for _, e := range s.netLog {
		if e.Seq <= s.obsNetSeq {
			continue
		}
		s.obsNetSeq = e.Seq
		if e.Status >= 400 || e.Error != "" {
			line := "net "
			if e.Error != "" {
				line += "ERR " + e.Error + " "
			} else {
				line += fmt.Sprintf("%d ", e.Status)
			}
			items = append(items, obsLine{e.Seq, e.TS, line + e.Method + " " + obsClip(e.URL)})
		}
	}
	s.netMu.Unlock()

	slices.SortStableFunc(items, func(a, b obsLine) int { return int(a.ts - b.ts) })
	out := make([]string, 0, len(items)+2)
	if skipped > 0 {
		out = append(out, fmt.Sprintf("… %d earlier events", skipped))
	}
	keep := items
	if len(keep) > obsTakeCap {
		out = append(out, fmt.Sprintf("… %d more events", len(keep)-obsTakeCap))
		keep = keep[len(keep)-obsTakeCap:]
	}
	for _, it := range keep {
		out = append(out, it.line)
	}
	return out
}
