package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
)

const (
	// maxMediaBytes is the same 25 MB ceiling the chat layer applies to reply
	// attachments; the adapter does not raise it.
	maxMediaBytes = 25 << 20

	// photoLimitBytes is where sendPhoto stops and sendDocument takes over.
	photoLimitBytes = 10 << 20

	// downloadLimitBytes is the Bot API's getFile ceiling. A larger declared
	// file is reported instead of being requested at all.
	downloadLimitBytes = 20 << 20

	// inboundAttachmentLimit is what the chat layer stores per message; an
	// album never buffers more than this.
	inboundAttachmentLimit = 6
)

var _ channels.MediaSender = (*Channel)(nil)

// SendMedia uploads one file to Telegram and delivers it to the target chat:
// GIFs as animations, images up to 10 MB as photos, video as video, OGG audio
// as a voice note, other audio as audio, and everything else as a document.
func (c *Channel) SendMedia(ctx context.Context, target channels.Target, media channels.OutboundMedia) error {
	chatID, threadID, err := parseTarget(target)
	if err != nil {
		return err
	}
	if !c.settingsSnapshot().Enabled {
		return errors.New("Telegram channel is disabled")
	}
	data := media.Data
	if len(data) == 0 && strings.TrimSpace(media.Path) != "" {
		// Stat first so an oversized file is refused without being read.
		if info, err := os.Stat(media.Path); err == nil && info.Size() > maxMediaBytes {
			return fmt.Errorf("media exceeds the %d byte limit", maxMediaBytes)
		}
		data, err = os.ReadFile(media.Path)
		if err != nil {
			return fmt.Errorf("read media %q: %w", media.Path, err)
		}
	}
	if len(data) == 0 {
		return errors.New("media has no data or path")
	}
	if len(data) > maxMediaBytes {
		return fmt.Errorf("media exceeds the %d byte limit", maxMediaBytes)
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(media.MIME, ";")[0]))
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	name := strings.TrimSpace(media.Name)
	if name != "" {
		name = filepath.Base(name)
	} else if p := strings.TrimSpace(media.Path); p != "" {
		name = filepath.Base(p)
	}
	if name == "" {
		name = "attachment"
	}
	method, field := mediaMethod(mime, len(data))
	client := c.clientCopy()
	if client == nil {
		return errors.New("Telegram bot is not connected")
	}
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if threadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(threadID, 10)
	}
	caption := strings.TrimSpace(media.Caption)
	if caption != "" {
		fields["caption"] = truncateHTML(sanitizeHTML(caption), maxCaptionUnits)
		fields["parse_mode"] = "HTML"
	}
	_, err = client.sendMedia(ctx, method, field, name, mime, data, fields)
	if err != nil && c.waitRetryAfter(ctx, err) {
		_, err = client.sendMedia(ctx, method, field, name, mime, data, fields)
	}
	if err != nil && fields["parse_mode"] != "" && isParseError(err) {
		// A caption Telegram cannot parse must not cost the file. Retry the
		// upload with the caption as plain text.
		c.log.Warn("Telegram rejected the media caption's HTML; resending it unparsed", "error", err)
		delete(fields, "parse_mode")
		fields["caption"] = truncateUTF16(caption, maxCaptionUnits)
		_, err = client.sendMedia(ctx, method, field, name, mime, data, fields)
	}
	return err
}

// mediaMethod maps a MIME type and size to the Bot API send method and the
// multipart field that carries the bytes.
func mediaMethod(mime string, size int) (method, field string) {
	switch {
	case mime == "image/gif":
		return "sendAnimation", "animation"
	case strings.HasPrefix(mime, "image/"):
		if size <= photoLimitBytes {
			return "sendPhoto", "photo"
		}
		return "sendDocument", "document"
	case strings.HasPrefix(mime, "video/"):
		return "sendVideo", "video"
	case mime == "audio/ogg":
		return "sendVoice", "voice"
	case strings.HasPrefix(mime, "audio/"):
		return "sendAudio", "audio"
	default:
		return "sendDocument", "document"
	}
}

/* ---------------- inbound media ---------------- */

// inboundCandidate is one attachment-shaped payload found on a message.
type inboundCandidate struct {
	kind       string // marker noun: Image, Document, Voice note, …
	stem       string // fallback file-name stem
	mime       string
	fileName   string
	fileID     string
	fileSize   int64
	defaultExt string
}

// mediaCandidates lists the media types this adapter accepts, in the order
// Telegram nests them.
func mediaCandidates(m *tgMessage) []inboundCandidate {
	var out []inboundCandidate
	seen := map[string]bool{}
	add := func(c inboundCandidate) {
		if c.fileID == "" || seen[c.fileID] {
			return
		}
		seen[c.fileID] = true
		out = append(out, c)
	}
	if len(m.Photo) > 0 {
		// Telegram orders photo renditions ascending; the last is largest.
		largest := m.Photo[len(m.Photo)-1]
		add(inboundCandidate{
			kind: "Image", stem: "photo", fileID: largest.FileID,
			fileSize: largest.FileSize, mime: "image/jpeg", defaultExt: ".jpg",
		})
	}
	addFile := func(kind, stem string, f *tgMedia, defExt string) {
		if f == nil {
			return
		}
		add(inboundCandidate{
			kind: kind, stem: stem, fileID: f.FileID, fileSize: f.FileSize,
			mime: f.MIME, fileName: strings.TrimSpace(f.FileName), defaultExt: defExt,
		})
	}
	addFile("Document", "document", m.Document, "")
	addFile("Video", "video", m.Video, ".mp4")
	addFile("Audio", "audio", m.Audio, ".mp3")
	addFile("Voice note", "voice", m.Voice, ".ogg")
	addFile("Video note", "video note", m.VideoNote, ".mp4")
	addFile("Animation", "animation", m.Animation, ".mp4")
	addFile("Sticker", "sticker", m.Sticker, ".webp")
	return out
}

// downloadInboundMedia fetches one candidate. The second return value is the
// text marker to append when the file did not arrive; the attachment is valid
// only when the marker is empty.
func (c *Channel) downloadInboundMedia(ctx context.Context, m *tgMessage, cand inboundCandidate) (channels.Attachment, string) {
	// getFile plus the transfer gets its own bound; the poll loop's context
	// lives for the whole connection.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tooLarge := "[File is larger than 20 MB and was not downloaded]"
	if cand.fileSize > downloadLimitBytes {
		return channels.Attachment{}, tooLarge
	}
	client := c.clientCopy()
	if client == nil {
		return channels.Attachment{}, "[" + cand.kind + " could not be downloaded]"
	}
	file, err := client.getFile(ctx, cand.fileID)
	if err != nil {
		c.log.Warn("failed to look up a Telegram file", "kind", cand.kind, "error", err)
		return channels.Attachment{}, "[" + cand.kind + " could not be downloaded]"
	}
	if file.FileSize > downloadLimitBytes {
		return channels.Attachment{}, tooLarge
	}
	data, err := client.downloadFile(ctx, file.FilePath, maxMediaBytes)
	if err != nil {
		c.log.Warn("failed to download a Telegram file", "kind", cand.kind, "error", err)
		return channels.Attachment{}, "[" + cand.kind + " could not be downloaded]"
	}
	mime := strings.TrimSpace(strings.Split(cand.mime, ";")[0])
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	name := cand.fileName
	if name == "" {
		name = cand.stem + "-" + strconv.FormatInt(m.MessageID, 10) + extensionForMIME(mime, cand.defaultExt)
	}
	return channels.Attachment{
		Name: filepath.Base(name), MIME: mime, Size: int64(len(data)), Data: data,
	}, ""
}

// extensionForMIME gives unnamed media a sane suffix instead of a bare id.
func extensionForMIME(mime, fallback string) string {
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
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "application/pdf":
		return ".pdf"
	}
	return fallback
}

// joinText appends one more block without duplicating identical text — used
// for captions and failure markers.
func joinText(text, extra string) string {
	text = strings.TrimSpace(text)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return text
	}
	if text == "" || text == extra {
		return extra
	}
	return text + "\n\n" + extra
}
