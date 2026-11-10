package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jahrulnr/nusamation/internal/browser"
)

/* ---------------- [[env:NAME]] reference resolution ----------------
   Only the exact [[env: prefix is reserved; every other [[ sequence stays
   literal. A backslash directly before [[ escapes the opener and drops one
   slash, so \\[[env:X]] types [[env:X]] literally. Resolution is a single pass
   over the original text — a secret value that itself contains a tag is never
   re-scanned. */

var (
	errUnknownSecret     = errors.New("unknown_secret")
	errInvalidSecret     = errors.New("invalid_secret_reference")
	errSecretTarget      = errors.New("invalid_secret_target")
	errSecretUnavailable = errors.New("internal_error")
	envRefNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// hasReservedTag reports whether text contains an unescaped [[env: opener.
func hasReservedTag(text string) bool {
	for i := 0; i+6 <= len(text); i++ {
		if text[i:i+6] == "[[env:" && (i == 0 || text[i-1] != '\\') {
			return true
		}
	}
	return false
}

// resolveBrowserSecretText expands [[env:NAME]] references against values.
// bindings maps each resolved raw value back to its tag for session masking.
func resolveBrowserSecretText(text string, values map[string]string) (resolved string, bindings map[string]string, err error) {
	bindings = map[string]string{}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '\\' && i+2 < len(text) && text[i+1] == '[' && text[i+2] == '[' {
			out.WriteString("[[")
			i += 3
			continue
		}
		if strings.HasPrefix(text[i:], "[[env:") {
			rest := text[i+6:]
			end := strings.Index(rest, "]]")
			if end < 0 {
				return "", bindings, errInvalidSecret
			}
			name := rest[:end]
			if !envRefNamePattern.MatchString(name) {
				return "", bindings, errInvalidSecret
			}
			value, ok := values[name]
			if !ok || value == "" {
				return "", bindings, errUnknownSecret
			}
			out.WriteString(value)
			tag := "[[" + "env:" + name + "]]"
			if old, ok := bindings[value]; !ok || tag < old {
				bindings[value] = tag
			}
			i += 6 + end + 2
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String(), bindings, nil
}

// browserSecretValues loads the conversation agent's decrypted env names.
// It is only called when a request actually contains an unescaped [[env:
// opener, so routine drives never touch the sealed store.
func (s *Server) browserSecretValues(r *http.Request, convID string) (map[string]string, error) {
	if s.store == nil {
		return nil, fmt.Errorf("secret resolution unavailable")
	}
	conv, err := s.store.GetConversation(r.Context(), convID)
	if err != nil {
		return nil, err
	}
	ag, err := s.store.GetAgent(r.Context(), conv.AgentID)
	if err != nil {
		return nil, errSecretUnavailable
	}
	values := make(map[string]string, len(ag.Env))
	for _, e := range ag.Env {
		if e.Value != "" {
			values[e.Name] = e.Value
		}
	}
	return values, nil
}

// resolveSecretInput resolves one outbound text value: the agent env is read
// only when the text really contains an unescaped [[env: opener; escaped or
// plain text just gets its \\[[ literals normalized.
func (s *Server) resolveSecretInput(r *http.Request, convID, text string) (string, map[string]string, error) {
	var values map[string]string
	if hasReservedTag(text) {
		v, err := s.browserSecretValues(r, convID)
		if err != nil {
			return "", nil, errSecretUnavailable
		}
		values = v
	}
	resolved, bindings, err := resolveBrowserSecretText(text, values)
	return resolved, bindings, err
}

// fillBrowserFields preflights every field value, registers all resolved
// bindings before any browser side effect, then applies fields in order:
// consecutive plain values go through the existing native-setter batch, values
// that contained references go through the secret text-fill path.
func (s *Server) fillBrowserFields(r *http.Request, convID string, sess *browser.Session, fields []browser.FieldValue) (string, error) {
	var values map[string]string
	need := false
	for _, f := range fields {
		if hasReservedTag(f.Value) {
			need = true
			break
		}
	}
	if need {
		v, err := s.browserSecretValues(r, convID)
		if err != nil {
			return "", errSecretUnavailable
		}
		values = v
	}
	bindings := map[string]string{}
	secret := make([]bool, len(fields))
	resolved := make([]browser.FieldValue, len(fields))
	for i, f := range fields {
		if hasReservedTag(f.Value) && nonTextSecretTypes[strings.ToLower(f.Type)] {
			sess.RegisterSecrets(bindings)
			return "", errSecretTarget
		}
		res, b, err := resolveBrowserSecretText(f.Value, values)
		for raw, tag := range b {
			if old, ok := bindings[raw]; !ok || tag < old {
				bindings[raw] = tag
			}
		}
		if err != nil {
			sess.RegisterSecrets(bindings)
			return "", err
		}
		if len(b) > 0 {
			secret[i] = true
		}
		resolved[i] = f
		resolved[i].Value = res
	}
	sess.RegisterSecrets(bindings)

	var details []string
	var run []browser.FieldValue
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		out, err := sess.FillFields(r.Context(), run)
		run = nil
		if err != nil {
			return err
		}
		if out != "" {
			details = append(details, out)
		}
		return nil
	}
	for i, f := range resolved {
		if !secret[i] {
			run = append(run, f)
			continue
		}
		if err := flush(); err != nil {
			return "", err
		}
		if err := sess.InsertSecretText(r.Context(), f.Ref, f.Value, true); err != nil {
			return "", err
		}
		details = append(details, f.Ref+": filled")
	}
	if err := flush(); err != nil {
		return "", err
	}
	return strings.Join(details, "; "), nil
}

// secretError maps resolver sentinel errors onto client errors; raw secret
// values and tag contents are never echoed back.
func secretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnknownSecret):
		writeError(w, http.StatusBadRequest, "unknown_secret", "Unknown environment reference — save it on the agent first.")
	case errors.Is(err, errInvalidSecret):
		writeError(w, http.StatusBadRequest, "invalid_secret_reference", "Malformed environment reference.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not resolve environment references.")
	}
}

// nonTextSecretTypes cannot receive secret values in this MVP: they are not
// text inputs, so a reference there is rejected at preflight.
var nonTextSecretTypes = map[string]bool{"checkbox": true, "radio": true, "combobox": true, "select": true}

/* ---------------- response masking ---------------- */

// secretMaskWriter buffers a JSON response so registered session secrets can be
// swapped for their tags before bytes reach the agent. It captures status,
// headers and body and flushes once when the handler returns.
type secretMaskWriter struct {
	inner   http.ResponseWriter
	sess    *browser.Session
	status  int
	buf     bytes.Buffer
	flushed bool
}

func newSecretMaskWriter(w http.ResponseWriter, sess *browser.Session) *secretMaskWriter {
	return &secretMaskWriter{inner: w, sess: sess, status: http.StatusOK}
}

func (m *secretMaskWriter) Header() http.Header { return m.inner.Header() }

func (m *secretMaskWriter) WriteHeader(status int) { m.status = status }

func (m *secretMaskWriter) Write(p []byte) (int, error) { return m.buf.Write(p) }

// flush decodes the buffered JSON, masks every string (including object keys)
// except the top-level image payload, re-encodes it and writes the result.
// A body that is not valid JSON — or fails to re-encode — becomes a generic
// internal error rather than leaking unmasked bytes.
func (m *secretMaskWriter) flush() {
	if m.flushed {
		return
	}
	m.flushed = true
	var decoded any
	dec := json.NewDecoder(&m.buf)
	dec.UseNumber()
	if err := dec.Decode(&decoded); err == nil {
		decoded = maskJSONValue(decoded, m.sess.MaskSecrets, true)
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if err = enc.Encode(decoded); err == nil {
			m.inner.Header().Del("Content-Length")
			m.inner.WriteHeader(m.status)
			_, _ = m.inner.Write(out.Bytes())
			return
		}
	}
	m.inner.Header().Set("Content-Type", "application/json; charset=utf-8")
	m.inner.Header().Del("Content-Length")
	m.inner.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(m.inner).Encode(map[string]any{"error": "Could not encode the browser response.", "code": "internal_error"})
}

// browserUseFixedKeys are protocol constants and measurements, never
// page-derived data: their root keys and values are preserved verbatim so a
// short or numeric secret can never corrupt the envelope.
var browserUseFixedKeys = map[string]bool{
	"sessionId": true, "width": true, "height": true, "mimeType": true, "code": true,
}

// browserUseRootKeys is the response vocabulary whose root keys are part of
// the wire contract; their names are preserved even if a secret matches them.
var browserUseRootKeys = map[string]bool{
	"sessionId": true, "url": true, "title": true, "width": true, "height": true,
	"image": true, "mimeType": true, "result": true, "snapshot": true,
	"detail": true, "error": true, "code": true,
}

// maskJSONValue walks decoded JSON masking every string, including map keys.
// At the top level the fixed envelope above is preserved (keys AND values),
// "image" keeps its base64 bytes, and unknown root fields mask value and key;
// at any deeper level everything masks. json.Number and bool scalars become
// masked strings only when their spelling actually matched a registered value.
func maskJSONValue(v any, mask func(string) string, topLevel bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if topLevel && browserUseFixedKeys[k] {
				out[k] = val
				continue
			}
			if topLevel && k == "image" {
				out[k] = val
				continue
			}
			mk := k
			if !topLevel || !browserUseRootKeys[k] {
				mk = mask(k)
			}
			out[mk] = maskJSONValue(val, mask, false)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = maskJSONValue(val, mask, false)
		}
		return out
	case string:
		return mask(t)
	case json.Number:
		s := t.String()
		if masked := mask(s); masked != s {
			return masked
		}
		return t
	case bool:
		s := strconv.FormatBool(t)
		if masked := mask(s); masked != s {
			return masked
		}
		return t
	default:
		return v
	}
}
