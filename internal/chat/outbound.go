package chat

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
)

const maxOutboundMediaBytes = 25 << 20

// allowedOutboundRoots lists the directories an agent may attach files from.
// conv.Cwd and agent.Cwd are administrator-granted boundaries: an authenticated
// user can set a conversation's cwd via the API, so these roots express what
// the operator allowed the agent to touch, not an independent sandbox.
// Anything else — credentials, databases, arbitrary host paths — is refused.
func (s *Service) allowedOutboundRoots(a *activeTurn) []string {
	a.mu.Lock()
	roots := []string{a.conv.Cwd, a.agent.Cwd, s.workspace, s.uploadDir}
	meta, _ := s.metaMap(a.conv)
	a.mu.Unlock()
	if dirs, ok := meta["additionalDirectories"].([]any); ok {
		for _, d := range dirs {
			if str, ok := d.(string); ok {
				roots = append(roots, str)
			}
		}
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if abs == string(filepath.Separator) {
			continue
		}
		out = append(out, abs)
	}
	return out
}

func pathInRoots(p string, roots []string) bool {
	for _, r := range roots {
		rel, err := filepath.Rel(r, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveOutboundPath turns a file:// URI or absolute path into a validated
// local file path. It refuses URLs, relative paths, dangling symlinks,
// symlink escapes outside the allowed roots, non-regular files (devices,
// FIFOs), and empty or oversized files.
func (s *Service) resolveOutboundPath(raw string, roots []string) (string, bool) {
	raw = strings.TrimSpace(raw)
	var p string
	switch {
	case strings.HasPrefix(raw, "file://"):
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
			return "", false
		}
		dec, err := url.PathUnescape(u.Path)
		if err != nil {
			return "", false
		}
		if runtime.GOOS == "windows" {
			// file://C:/path and file:///C:/path both carry a Windows drive
			// letter that url.Parse does not fold into u.Path.
			if u.Host != "" && !strings.Contains(dec, ":") {
				dec = u.Host + dec
			}
			dec = strings.TrimPrefix(dec, "/")
		}
		p = dec
	case filepath.IsAbs(raw):
		p = raw
	default:
		return "", false
	}
	p = filepath.Clean(filepath.FromSlash(p))
	if !filepath.IsAbs(p) || p == string(filepath.Separator) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	if !pathInRoots(resolved, roots) {
		return "", false
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > maxOutboundMediaBytes {
		return "", false
	}
	return resolved, true
}

// collectOutbound converts a non-text agent content block into deliverable
// media so images, audio and file references reach the origin chat channel.
func (s *Service) collectOutbound(a *activeTurn, b acp.ContentBlock) {
	m, ok := s.outboundFromBlock(a, b)
	if !ok {
		s.log.Debug("ignoring unsupported agent content block", "type", b.Type, "conversation", a.conversationID)
		return
	}
	if !a.addOutbound(m) {
		s.log.Warn("outbound media dropped: per-turn limit reached", "conversation", a.conversationID)
	}
}

func (s *Service) outboundFromBlock(a *activeTurn, b acp.ContentBlock) (channels.OutboundMedia, bool) {
	switch b.Type {
	case "image", "audio":
		data, err := decodeLimitedB64(b.Data, maxOutboundMediaBytes)
		if err != nil {
			return channels.OutboundMedia{}, false
		}
		mime := b.MIMEType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = b.Type + "-" + a.turn.ID + fileExtFor(mime)
		}
		return channels.OutboundMedia{Name: name, MIME: mime, Data: data}, true
	case "resource_link":
		p, ok := s.resolveOutboundPath(b.URI, s.allowedOutboundRoots(a))
		if !ok {
			return channels.OutboundMedia{}, false
		}
		data, err := readRegular(p, maxOutboundMediaBytes)
		if err != nil {
			s.log.Warn("resource_link file unreadable", "path", p, "error", err)
			return channels.OutboundMedia{}, false
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = filepath.Base(p)
		}
		mime := strings.TrimSpace(b.MIMEType)
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		return channels.OutboundMedia{Name: name, MIME: mime, Path: p, Data: data}, true
	case "resource":
		r := b.Resource
		if r == nil {
			return channels.OutboundMedia{}, false
		}
		if strings.HasPrefix(strings.TrimSpace(r.URI), "file://") {
			p, ok := s.resolveOutboundPath(r.URI, s.allowedOutboundRoots(a))
			if !ok {
				return channels.OutboundMedia{}, false
			}
			data, err := readRegular(p, maxOutboundMediaBytes)
			if err != nil {
				s.log.Warn("resource file unreadable", "path", p, "error", err)
				return channels.OutboundMedia{}, false
			}
			mime := strings.TrimSpace(r.MIMEType)
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			return channels.OutboundMedia{
				Name: filepath.Base(p), MIME: mime, Path: p, Data: data,
			}, true
		}
		var data []byte
		var err error
		if r.Blob != "" {
			data, err = decodeLimitedB64(r.Blob, maxOutboundMediaBytes)
			if err != nil {
				return channels.OutboundMedia{}, false
			}
		} else if r.Text != "" {
			data = []byte(r.Text)
			if len(data) > maxOutboundMediaBytes {
				return channels.OutboundMedia{}, false
			}
		}
		if len(data) == 0 {
			return channels.OutboundMedia{}, false
		}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = filepath.Base(r.URI)
		}
		return channels.OutboundMedia{Name: name, MIME: strings.TrimSpace(r.MIMEType), Data: data}, true
	}
	return channels.OutboundMedia{}, false
}

// decodeLimitedB64 decodes base64 while refusing payloads larger than max.
func decodeLimitedB64(raw string, max int64) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errNoData
	}
	// base64 expands ~4/3: reject obviously oversized input before decoding.
	if int64(len(raw)) > (max/3)*4+8 {
		return nil, errTooLarge
	}
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(raw))
	data, err := io.ReadAll(io.LimitReader(dec, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max || len(data) == 0 {
		return nil, errTooLarge
	}
	return data, nil
}

var errNoData = errString("empty media")
var errTooLarge = errString("media too large")

type errString string

func (e errString) Error() string { return string(e) }

// fileLinkRe matches [label](/abs/path) and [label](file:///abs/path) markdown.
// fileURIRe matches bare file:///abs/path URIs written into the reply text.
var (
	fileLinkRe = regexp.MustCompile(`!?\[([^\]]{0,120})\]\(\s*<?((?:file://)?/[^\s>)]+)>?\s*\)`)
	fileURIRe  = regexp.MustCompile(`file:///[^\s)\]"'<>` + "`" + `]+`)
)

// collectOutboundFromText lets text-only agents attach files by writing
// file:// URIs or markdown links to absolute local paths in their reply.
// Only paths inside the allowed roots are accepted.
func (s *Service) collectOutboundFromText(a *activeTurn) {
	a.mu.Lock()
	text := a.assistantAll.String()
	a.mu.Unlock()
	if !strings.Contains(text, "file://") && !strings.Contains(text, "](") {
		return
	}
	roots := s.allowedOutboundRoots(a)
	seen := map[string]bool{}
	for _, m := range a.outboundSnapshot() {
		if m.Path != "" {
			seen[m.Path] = true
		}
	}
	add := func(ref, name string) {
		p, ok := s.resolveOutboundPath(ref, roots)
		if !ok || seen[p] {
			return
		}
		// Read eagerly so a file that disappears or becomes unreadable before
		// delivery leaves the link in the text instead of stripping it.
		data, err := readRegular(p, maxOutboundMediaBytes)
		if err != nil {
			s.log.Warn("outbound file unreadable; link left in text", "path", p, "error", err)
			return
		}
		m := channels.OutboundMedia{Name: name, Path: p, Data: data, MIME: http.DetectContentType(data)}
		if m.Name == "" {
			m.Name = filepath.Base(p)
		}
		if a.addOutbound(m) {
			seen[p] = true
			a.markOutboundRef(normalizeRef(ref))
		}
	}
	for _, mm := range fileLinkRe.FindAllStringSubmatch(text, 16) {
		add(mm[2], strings.TrimSpace(mm[1]))
	}
	for _, u := range fileURIRe.FindAllString(text, 16) {
		add(u, "")
	}
}

func normalizeRef(ref string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(ref), "<>"))
}

// cleanDeliveredText strips file references from the text delivered to
// channels — but only refs that were actually accepted as outbound media, so
// refused paths and ordinary links stay visible in the message.
func cleanDeliveredText(text string, sent map[string]bool) string {
	text = fileLinkRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := fileLinkRe.FindStringSubmatch(m)
		if len(sub) < 3 || !sent[normalizeRef(sub[2])] {
			return m
		}
		return sub[1]
	})
	return fileURIRe.ReplaceAllStringFunc(text, func(m string) string {
		if !sent[normalizeRef(m)] {
			return m
		}
		return ""
	})
}

// prepareOutbound collects media references from the final text before the
// turn's output flags are computed; delivery itself runs asynchronously.
func (s *Service) prepareOutbound(a *activeTurn) {
	s.collectOutboundFromText(a)
}

// deliverOutbound resolves paths, persists files to uploads so the web UI
// shows them, then sends them to the origin channel when it supports media.
// Media that never reaches the channel is surfaced as a notice — the file
// reference was already stripped from the delivered text at collection time.
func (s *Service) deliverOutbound(a *activeTurn) {
	media := a.outboundSnapshot()
	if len(media) == 0 {
		return
	}
	var undelivered []string
	loaded := make([]channels.OutboundMedia, 0, len(media))
	for _, m := range media {
		if len(m.Data) == 0 {
			if m.Path == "" {
				undelivered = append(undelivered, mediaLabel(m))
				continue
			}
			data, err := readRegular(m.Path, maxOutboundMediaBytes)
			if err != nil {
				s.log.Warn("skipping unreadable outbound media", "path", m.Path, "error", err)
				undelivered = append(undelivered, mediaLabel(m))
				continue
			}
			m.Data = data
		}
		if m.MIME == "" {
			m.MIME = http.DetectContentType(m.Data)
		}
		if m.Name == "" {
			m.Name = "attachment"
		}
		loaded = append(loaded, m)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msgID := s.outboundAnchorMessage(ctx, a)
	for _, m := range loaded {
		up, err := s.SaveInboundAttachment(ctx, a.conversationID, m.Name, m.MIME, m.Data)
		if err != nil {
			s.log.Warn("failed to store outbound media", "name", m.Name, "error", err)
			continue
		}
		if msgID != "" {
			if err := s.store.BindUploadsToMessage(ctx, []string{up.ID}, a.conversationID, msgID); err == nil {
				if m2, err := s.store.GetMessage(ctx, msgID); err == nil {
					s.emit("message.updated", m2.ConversationID, s.hydrateMessage(ctx, m2))
				}
			}
		}
	}

	var ch channels.Channel
	var target channels.Target
	deliver := false
	if s.deliveryChannel(a) != "web" {
		if c, t, ok := s.originChannel(a); ok {
			ch, target, deliver = c, t, true
		}
	}
	if deliver {
		if sender, ok := ch.(channels.MediaSender); ok {
			for _, m := range loaded {
				mctx, mcancel := context.WithTimeout(context.Background(), 60*time.Second)
				err := sender.SendMedia(mctx, target, m)
				mcancel()
				if err != nil {
					s.log.Warn("failed to send media", "channel", ch.Name(), "name", m.Name, "error", err)
					undelivered = append(undelivered, m.Name)
				}
			}
		} else {
			s.log.Warn("channel cannot deliver media", "channel", ch.Name(), "conversation", a.conversationID)
			for _, m := range loaded {
				undelivered = append(undelivered, m.Name)
			}
		}
	}
	if len(undelivered) == 0 {
		return
	}
	note := "Could not deliver attachment(s): " + strings.Join(undelivered, ", ")
	if ch != nil {
		nctx, ncancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := ch.Notify(nctx, target, note); err != nil {
			s.log.Warn("failed to notify attachment failure", "channel", ch.Name(), "error", err)
		}
		ncancel()
	}
	mctx, mcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer mcancel()
	sys := store.Message{
		ConversationID: a.conversationID, TurnID: a.turn.ID, Role: "system", Kind: "notice",
		Status: "complete", Text: note,
	}
	if err := s.store.AppendMessage(mctx, &sys); err == nil {
		s.emit("message.created", a.conversationID, sys)
	}
}

func mediaLabel(m channels.OutboundMedia) string {
	if m.Name != "" {
		return m.Name
	}
	if m.Path != "" {
		return filepath.Base(m.Path)
	}
	return "attachment"
}

// readRegular reads a path only when it is a regular file within size bounds.
func readRegular(path string, max int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errString("not a regular file")
	}
	if fi.Size() == 0 || fi.Size() > max {
		return nil, errTooLarge
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

// outboundAnchorMessage returns the assistant message uploads should attach
// to. It never falls back to the user's message; if the agent produced no
// text reply, a small assistant message is created to carry the files.
func (s *Service) outboundAnchorMessage(ctx context.Context, a *activeTurn) string {
	a.mu.Lock()
	id := a.assistantTextID
	a.mu.Unlock()
	if id != "" {
		return id
	}
	msgs, err := s.store.ListMessages(ctx, a.conversationID, 0, 0, 200)
	if err == nil {
		for _, m := range slices.Backward(msgs) {

			if m.TurnID == a.turn.ID && m.Role == "assistant" && m.Kind == "text" {
				return m.ID
			}
		}
	}
	msg := store.Message{
		ConversationID: a.conversationID, TurnID: a.turn.ID, Role: "assistant", Kind: "text",
		Status: "complete", Channel: "web", Text: "Attachments sent.",
	}
	if err := s.store.AppendMessage(ctx, &msg); err != nil {
		return ""
	}
	s.emit("message.created", a.conversationID, s.hydrateMessage(ctx, msg))
	return msg.ID
}

func fileExtFor(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}
