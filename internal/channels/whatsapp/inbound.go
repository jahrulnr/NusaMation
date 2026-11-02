package whatsapp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/jahrulnr/nusamation/internal/channels"
)

const maxMediaBytes = 25 << 20

func (c *Channel) handleMessage(ctx context.Context, evt *events.Message) error {
	info := evt.Info
	if info.IsFromMe || info.Chat.IsEmpty() || info.Sender.IsEmpty() {
		return nil
	}
	switch info.Chat.Server {
	case "broadcast", "newsletter":
		return nil
	}
	if info.Chat.User == "status" {
		return nil
	}
	if evt.IsEdit {
		return nil
	}
	settings := c.settingsSnapshot()
	if !settings.Enabled {
		return nil
	}
	if info.IsGroup {
		switch settings.GroupPolicy {
		case "disabled":
			return nil
		case "mention":
			if !c.mentionsMe(info, evt.Message) {
				return nil
			}
		}
	}
	senderJIDs := []string{info.Sender.String(), info.Sender.User, info.SenderAlt.String(), info.SenderAlt.User}
	chatJIDs := []string{info.Chat.String(), info.Chat.User}
	if !settings.allows(senderJIDs, chatJIDs) {
		c.log.Debug("ignoring WhatsApp message from non-allowlisted sender", "sender", info.Sender.String())
		return nil
	}
	if !info.Timestamp.IsZero() && time.Since(info.Timestamp) > 2*time.Hour {
		return nil
	}

	text := waText(evt.Message)
	attachments := []channels.Attachment{}
	if img := evt.Message.GetImageMessage(); img != nil {
		text = joinCaption(text, img.GetCaption())
		if data, err := c.download(ctx, img, img.GetFileLength()); err == nil {
			mime := img.GetMimetype()
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			attachments = append(attachments, channels.Attachment{
				Name: "image-" + string(info.ID) + extensionForMIME(mime, ".jpg"), MIME: mime,
				Size: int64(len(data)), Data: data,
			})
		} else {
			c.log.Warn("failed to download WhatsApp image", "error", err)
			text = joinCaption(text, "[Image could not be downloaded]")
		}
	}
	if doc := evt.Message.GetDocumentMessage(); doc != nil {
		text = joinCaption(text, doc.GetCaption())
		if data, err := c.download(ctx, doc, doc.GetFileLength()); err == nil {
			mime := doc.GetMimetype()
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			name := strings.TrimSpace(doc.GetFileName())
			if name == "" {
				name = "document-" + string(info.ID)
			}
			attachments = append(attachments, channels.Attachment{Name: name, MIME: mime, Size: int64(len(data)), Data: data})
		} else {
			c.log.Warn("failed to download WhatsApp document", "error", err)
			text = joinCaption(text, "[Document could not be downloaded]")
		}
	}
	if vid := evt.Message.GetVideoMessage(); vid != nil {
		text = joinCaption(text, vid.GetCaption())
		text = joinCaption(text, "[Video attachment received; download it from the web UI or ask the sender for a file.]")
	}
	if aud := evt.Message.GetAudioMessage(); aud != nil {
		text = joinCaption(text, "[Voice note received. Ask the user to send it as a file if you need the audio.]")
	}
	if st := evt.Message.GetStickerMessage(); st != nil {
		text = joinCaption(text, "[Sticker received]")
	}
	text = strings.TrimSpace(text)
	if text == "" && len(attachments) == 0 {
		return nil
	}

	senderName := strings.TrimSpace(info.PushName)
	if senderName == "" {
		senderName = "+" + info.Sender.User
	}
	chatName := senderName
	if info.IsGroup {
		chatName = c.groupName(ctx, info.Chat)
	} else if info.Chat.User != "" {
		chatName = senderName
	}
	in := channels.Inbound{
		Channel: "whatsapp", ExternalID: info.Chat.String(), SenderID: info.Sender.String(),
		SenderName: senderName, ChatName: chatName, MessageID: string(info.ID), Text: text,
		Attachments: attachments, IsGroup: info.IsGroup, MentionsMe: c.mentionsMe(info, evt.Message),
		Timestamp: info.Timestamp.UnixMilli(),
	}
	if c.handler == nil {
		return fmt.Errorf("no inbound handler is configured")
	}
	if err := c.handler.HandleInbound(ctx, in); err != nil {
		return err
	}
	if settings.ReadReceipts {
		go func() {
			readCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := c.clientCopy()
			if client != nil && client.IsLoggedIn() {
				_ = client.MarkRead(readCtx, []types.MessageID{info.ID}, time.Now(), info.Chat, info.Sender)
			}
		}()
	}
	return nil
}

func (c *Channel) download(ctx context.Context, msg whatsmeow.DownloadableMessage, size uint64) ([]byte, error) {
	if size > maxMediaBytes {
		return nil, fmt.Errorf("attachment is too large (%d bytes)", size)
	}
	client := c.clientCopy()
	if client == nil {
		return nil, fmt.Errorf("WhatsApp is not connected")
	}
	dlCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	data, err := client.Download(dlCtx, msg)
	if err != nil {
		return nil, err
	}
	if len(data) > maxMediaBytes {
		return nil, fmt.Errorf("attachment is too large after download")
	}
	return data, nil
}

func (c *Channel) clientCopy() *whatsmeow.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

func (c *Channel) mentionsMe(info types.MessageInfo, msg *waE2E.Message) bool {
	if !info.IsGroup {
		return false
	}
	own := c.ownIDs()
	if len(own) == 0 {
		return false
	}
	for _, ci := range contextInfos(msg) {
		if ci == nil {
			continue
		}
		for _, jid := range ci.GetMentionedJID() {
			if own[normalizeNumber(jid)] {
				return true
			}
		}
		if p := ci.GetParticipant(); p != "" && own[normalizeNumber(p)] {
			return true
		}
	}
	text := strings.ToLower(waText(msg))
	for id := range own {
		if id != "" && strings.Contains(text, "@"+id) {
			return true
		}
	}
	return false
}

func (c *Channel) ownIDs() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]bool{}
	if c.client == nil {
		return out
	}
	if c.client.Store.ID != nil {
		out[normalizeNumber(c.client.Store.ID.User)] = true
		out[normalizeNumber(c.client.Store.ID.ToNonAD().String())] = true
	}
	if !c.client.Store.LID.IsEmpty() {
		out[normalizeNumber(c.client.Store.LID.User)] = true
		out[normalizeNumber(c.client.Store.LID.ToNonAD().String())] = true
	}
	return out
}

func contextInfos(msg *waE2E.Message) []*waE2E.ContextInfo {
	if msg == nil {
		return nil
	}
	var out []*waE2E.ContextInfo
	if x := msg.GetExtendedTextMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetImageMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetVideoMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetDocumentMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetAudioMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	return out
}

func waText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := strings.TrimSpace(msg.GetConversation()); text != "" {
		return text
	}
	if x := msg.GetExtendedTextMessage(); x != nil {
		if text := strings.TrimSpace(x.GetText()); text != "" {
			return text
		}
	}
	if x := msg.GetImageMessage(); x != nil {
		return strings.TrimSpace(x.GetCaption())
	}
	if x := msg.GetVideoMessage(); x != nil {
		return strings.TrimSpace(x.GetCaption())
	}
	if x := msg.GetDocumentMessage(); x != nil {
		return strings.TrimSpace(x.GetCaption())
	}
	if x := msg.GetButtonsResponseMessage(); x != nil {
		return strings.TrimSpace(x.GetSelectedDisplayText())
	}
	if x := msg.GetListResponseMessage(); x != nil {
		return strings.TrimSpace(x.GetTitle())
	}
	if x := msg.GetLocationMessage(); x != nil {
		return fmt.Sprintf("[Location: %.6f, %.6f]", x.GetDegreesLatitude(), x.GetDegreesLongitude())
	}
	if x := msg.GetContactMessage(); x != nil {
		return "[Contact card: " + x.GetDisplayName() + "]"
	}
	return ""
}

func joinCaption(text, caption string) string {
	text = strings.TrimSpace(text)
	caption = strings.TrimSpace(caption)
	if caption == "" {
		return text
	}
	if text == "" || text == caption {
		return caption
	}
	return text + "\n\n" + caption
}

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
	default:
		return fallback
	}
}

func (c *Channel) groupName(ctx context.Context, jid types.JID) string {
	c.groupMu.Lock()
	if e, ok := c.groupCache[jid.String()]; ok && time.Since(e.at) < 15*time.Minute {
		c.groupMu.Unlock()
		return e.name
	}
	c.groupMu.Unlock()
	client := c.clientCopy()
	name := ""
	if client != nil && client.IsLoggedIn() {
		infoCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		info, err := client.GetGroupInfo(infoCtx, jid)
		cancel()
		if err == nil && info != nil {
			name = strings.TrimSpace(info.GroupName.Name)
		}
	}
	if name == "" {
		name = "Group +" + jid.User
	}
	c.groupMu.Lock()
	c.groupCache[jid.String()] = groupEntry{name: name, at: time.Now()}
	c.groupMu.Unlock()
	return name
}
