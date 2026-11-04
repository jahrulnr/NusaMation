package whatsapp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

const (
	maxMediaBytes  = 25 << 20
	pollCacheLimit = 512
)

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
			// Poll creations, votes and added options cannot carry an
			// @mention by design — requiring one would make them permanently
			// invisible. They pass as passive context; the allowlist still
			// gates which chats reach the agent at all.
			if !isPollTraffic(evt.Message) && !c.mentionsMe(info, evt.Message) {
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
	// PtvMessage is a video note (the round bubble); it shares the
	// VideoMessage payload, so download it the same way.
	attachVideo := func(vid *waE2E.VideoMessage, prefix string) {
		if vid == nil {
			return
		}
		text = joinCaption(text, vid.GetCaption())
		if data, err := c.download(ctx, vid, vid.GetFileLength()); err == nil {
			mime := vid.GetMimetype()
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			attachments = append(attachments, channels.Attachment{
				Name: prefix + string(info.ID) + extensionForMIME(mime, ".mp4"), MIME: mime,
				Size: int64(len(data)), Data: data,
			})
		} else {
			c.log.Warn("failed to download WhatsApp video", "error", err)
			text = joinCaption(text, "[Video could not be downloaded]")
		}
	}
	attachVideo(evt.Message.GetVideoMessage(), "video-")
	attachVideo(evt.Message.GetPtvMessage(), "videonote-")
	if aud := evt.Message.GetAudioMessage(); aud != nil {
		if data, err := c.download(ctx, aud, aud.GetFileLength()); err == nil {
			mime := aud.GetMimetype()
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			attachments = append(attachments, channels.Attachment{
				Name: "audio-" + string(info.ID) + extensionForMIME(mime, ".ogg"), MIME: mime,
				Size: int64(len(data)), Data: data,
			})
		} else {
			c.log.Warn("failed to download WhatsApp audio", "error", err)
			text = joinCaption(text, "[Voice note could not be downloaded]")
		}
	}
	if st := evt.Message.GetStickerMessage(); st != nil {
		if data, err := c.download(ctx, st, st.GetFileLength()); err == nil {
			mime := st.GetMimetype()
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			attachments = append(attachments, channels.Attachment{
				Name: "sticker-" + string(info.ID) + extensionForMIME(mime, ".webp"), MIME: mime,
				Size: int64(len(data)), Data: data,
			})
		} else {
			c.log.Warn("failed to download WhatsApp sticker", "error", err)
			text = joinCaption(text, "[Sticker could not be downloaded]")
		}
	}
	if poll := pollCreation(evt.Message); poll != nil {
		text = joinCaption(text, pollText(poll))
		c.rememberPoll(info, poll)
	}
	var vote *channels.PollVote
	if upd := evt.Message.GetPollUpdateMessage(); upd != nil {
		if mk := upd.GetPollCreationMessageKey(); mk.GetFromMe() {
			// A vote on a poll this channel sent (agent question/approval) —
			// carry the selection structurally so the chat service can
			// resolve the pending question instead of prompting the agent.
			_, picked := c.decodeVote(ctx, evt, mk)
			vote = &channels.PollVote{PollMessageID: mk.GetID(), Selected: picked}
		} else {
			text = joinCaption(text, c.pollVoteText(ctx, evt, upd))
		}
	}
	if add := evt.Message.GetPollAddOptionMessage(); add != nil {
		text = joinCaption(text, c.pollOptionText(add))
	}
	// Not rendered yet: PollResultSnapshotMessage(+V3) tallies and
	// SecretEncryptedMessage poll edits/additions (DecryptSecretEncryptedMessage).
	text = strings.TrimSpace(text)
	if text == "" && len(attachments) == 0 && vote == nil {
		return nil
	}
	if q := quoteContext(evt.Message); q != "" {
		text = q + "\n" + text
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
		PollVote:  vote,
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
			if own[NormalizeNumber(jid)] {
				return true
			}
		}
		if p := ci.GetParticipant(); p != "" && own[NormalizeNumber(p)] {
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
		out[NormalizeNumber(c.client.Store.ID.User)] = true
		out[NormalizeNumber(c.client.Store.ID.ToNonAD().String())] = true
	}
	if !c.client.Store.LID.IsEmpty() {
		out[NormalizeNumber(c.client.Store.LID.User)] = true
		out[NormalizeNumber(c.client.Store.LID.ToNonAD().String())] = true
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
	if x := msg.GetStickerMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetPtvMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetEventMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetLiveLocationMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetGroupInviteMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetContactsArrayMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetInteractiveResponseMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetButtonsResponseMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetListResponseMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetContactMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if x := msg.GetLocationMessage(); x != nil {
		out = append(out, x.GetContextInfo())
	}
	if p := pollCreation(msg); p != nil {
		out = append(out, p.GetContextInfo())
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
	if x := msg.GetLiveLocationMessage(); x != nil {
		return fmt.Sprintf("[Live location: %.6f, %.6f]", x.GetDegreesLatitude(), x.GetDegreesLongitude())
	}
	if x := msg.GetContactMessage(); x != nil {
		return "[Contact card: " + x.GetDisplayName() + "]"
	}
	if x := msg.GetContactsArrayMessage(); x != nil {
		names := make([]string, 0, len(x.GetContacts()))
		for _, c := range x.GetContacts() {
			names = append(names, c.GetDisplayName())
		}
		return "[Contact cards: " + strings.Join(names, ", ") + "]"
	}
	if x := msg.GetEventMessage(); x != nil {
		return eventText(x)
	}
	if x := msg.GetGroupInviteMessage(); x != nil {
		name := x.GetGroupName()
		if name == "" {
			name = x.GetGroupJID()
		}
		return "[Group invite: " + name + "]"
	}
	if x := msg.GetInteractiveResponseMessage(); x != nil {
		if nf := x.GetNativeFlowResponseMessage(); nf != nil {
			return "[Form response: " + nf.GetParamsJSON() + "]"
		}
		if b := strings.TrimSpace(x.GetBody().GetText()); b != "" {
			return b
		}
		return "[Form response]"
	}
	if x := msg.GetOrderMessage(); x != nil {
		return fmt.Sprintf("[Order: %s — %d item(s)]", x.GetOrderTitle(), x.GetItemCount())
	}
	if x := msg.GetProductMessage(); x != nil {
		body := x.GetBody()
		if body == "" {
			body = "product"
		}
		return "[Product: " + body + "]"
	}
	return ""
}

func eventText(e *waE2E.EventMessage) string {
	var b strings.Builder
	b.WriteString("[Event: " + e.GetName())
	if t := e.GetStartTime(); t > 0 {
		b.WriteString(" — " + time.Unix(t, 0).Format("2006-01-02 15:04"))
	}
	if loc := e.GetLocation(); loc != nil && (loc.GetDegreesLatitude() != 0 || loc.GetDegreesLongitude() != 0) {
		fmt.Fprintf(&b, " — %.6f, %.6f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
	}
	if e.GetIsCanceled() {
		b.WriteString(" — cancelled")
	}
	b.WriteString("]")
	if desc := strings.TrimSpace(e.GetDescription()); desc != "" {
		b.WriteString("\n" + desc)
	}
	return b.String()
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

// pollCreation finds the poll embedded in any of the poll message versions
// WhatsApp clients send (v1/v2/v3/v5/v6, plus future-proof wrapped v4).
func pollCreation(msg *waE2E.Message) *waE2E.PollCreationMessage {
	if msg == nil {
		return nil
	}
	for _, p := range []*waE2E.PollCreationMessage{
		msg.GetPollCreationMessage(), msg.GetPollCreationMessageV2(), msg.GetPollCreationMessageV3(),
		msg.GetPollCreationMessageV5(), msg.GetPollCreationMessageV6(),
	} {
		if p != nil {
			return p
		}
	}
	for _, fp := range []*waE2E.FutureProofMessage{msg.GetPollCreationMessageV4(), msg.GetPollCreationOptionImageMessage()} {
		if p := pollCreation(fp.GetMessage()); p != nil {
			return p
		}
	}
	return nil
}

func pollText(p *waE2E.PollCreationMessage) string {
	var b strings.Builder
	b.WriteString("[Poll] " + p.GetName())
	for _, o := range p.GetOptions() {
		if name := strings.TrimSpace(o.GetOptionName()); name != "" {
			b.WriteString("\n- " + name)
		}
	}
	switch n := p.GetSelectableOptionsCount(); {
	case n == 0:
		b.WriteString("\n(multiple answers allowed)")
	case n > 1:
		fmt.Fprintf(&b, "\n(choose up to %d)", n)
	}
	return b.String()
}

// isPollTraffic reports whether the message is passive poll context — a
// creation, a vote, or an added option. None of them can carry an @mention.
func isPollTraffic(msg *waE2E.Message) bool {
	return pollCreation(msg) != nil ||
		msg.GetPollUpdateMessage() != nil ||
		msg.GetPollAddOptionMessage() != nil
}

// pollKey is the cache key linking a poll creation (stored under the incoming
// message's chat JID + ID) to the MessageKey embedded in later votes and
// add-option messages — both sides must produce the same string.
func pollKey(chatJID, msgID string) string {
	return chatJID + ":" + msgID
}

// pollKeys returns the lookup keys for a poll: the verbatim chat JID plus its
// device-stripped form, so votes whose embedded JID varies only by device
// number still resolve. Server transitions (pn↔lid) can't be bridged this
// way — those votes fall back to a count-only label.
func pollKeys(chatJID, msgID string) []string {
	keys := []string{pollKey(chatJID, msgID)}
	if jid, err := types.ParseJID(chatJID); err == nil {
		if alt := jid.ToNonAD().String(); alt != chatJID {
			keys = append(keys, pollKey(alt, msgID))
		}
	}
	return keys
}

// rememberPoll caches a poll's option names so later encrypted poll votes can
// be mapped back to readable choices.
func (c *Channel) rememberPoll(info types.MessageInfo, p *waE2E.PollCreationMessage) {
	names := make([]string, 0, len(p.GetOptions()))
	for _, o := range p.GetOptions() {
		if name := strings.TrimSpace(o.GetOptionName()); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	c.cachePollOptions(info.Chat.String(), string(info.ID), names)
}

// cachePollOptions stores a poll's option names under every JID variant so a
// later vote resolves regardless of device-suffix differences.
func (c *Channel) cachePollOptions(chatJID, msgID string, names []string) {
	if len(names) == 0 {
		return
	}
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	for _, k := range pollKeys(chatJID, msgID) {
		if len(c.polls) >= pollCacheLimit {
			for old := range c.polls {
				delete(c.polls, old)
				break
			}
		}
		c.polls[k] = names
	}
}

// pollOptions returns the cached option names for a poll message key.
func (c *Channel) pollOptions(mk *waCommon.MessageKey) []string {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	for _, k := range pollKeys(mk.GetRemoteJID(), mk.GetID()) {
		if opts := c.polls[k]; len(opts) > 0 {
			return opts
		}
	}
	return nil
}

// decodeVote decrypts a poll vote and maps the selection back to option
// names using the cached poll metadata.
func (c *Channel) decodeVote(ctx context.Context, evt *events.Message, mk *waCommon.MessageKey) (*waE2E.PollVoteMessage, []string) {
	client := c.clientCopy()
	if client == nil {
		return nil, nil
	}
	vote, err := client.DecryptPollVote(ctx, evt)
	if err != nil {
		c.log.Debug("could not decrypt WhatsApp poll vote", "error", err)
		return nil, nil
	}
	return vote, voteOptionNames(vote, c.pollOptions(mk))
}

// rememberPollOption appends a user-added option to a cached poll so a later
// vote for it still resolves to a name.
func (c *Channel) rememberPollOption(mk *waCommon.MessageKey, name string) {
	if name == "" {
		return
	}
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	for _, k := range pollKeys(mk.GetRemoteJID(), mk.GetID()) {
		c.polls[k] = append(c.polls[k], name)
	}
}

func (c *Channel) pollVoteText(ctx context.Context, evt *events.Message, upd *waE2E.PollUpdateMessage) string {
	const fallback = "[Poll vote]"
	vote, picked := c.decodeVote(ctx, evt, upd.GetPollCreationMessageKey())
	if len(picked) > 0 {
		return "[Poll vote: " + strings.Join(picked, ", ") + "]"
	}
	if n := len(vote.GetSelectedOptions()); n > 0 {
		return fmt.Sprintf("[Poll vote: %d option(s) selected]", n)
	}
	return fallback
}

func (c *Channel) pollOptionText(add *waE2E.PollAddOptionMessage) string {
	name := strings.TrimSpace(add.GetAddOption().GetOptionName())
	c.rememberPollOption(add.GetPollCreationMessageKey(), name)
	if name == "" {
		return "[Poll option added]"
	}
	return "[Poll option added: " + name + "]"
}

// voteOptionNames maps a decrypted vote's option-name hashes back to the
// option names advertised when the poll was created.
func voteOptionNames(vote *waE2E.PollVoteMessage, options []string) []string {
	if len(options) == 0 {
		return nil
	}
	hashes := whatsmeow.HashPollOptions(options)
	var out []string
	for _, sel := range vote.GetSelectedOptions() {
		for i, h := range hashes {
			if bytes.Equal(h, sel) {
				out = append(out, options[i])
				break
			}
		}
	}
	return out
}

// quoteContext renders the message a reply is quoting, so agents can see what
// a short answer like "yes" refers to.
func quoteContext(msg *waE2E.Message) string {
	for _, ci := range contextInfos(msg) {
		if ci == nil {
			continue
		}
		q := unwrapQuoted(ci.GetQuotedMessage())
		if q == nil {
			continue
		}
		text := waText(q)
		if text == "" {
			text = quotedKindLabel(q)
		}
		return "[In reply to: " + textfmt.OneLine(text, 120) + "]"
	}
	return ""
}

// unwrapQuoted peels view-once/ephemeral wrappers off a quoted message —
// quoted payloads arrive raw, unlike incoming events which whatsmeow has
// already unwrapped.
func unwrapQuoted(m *waE2E.Message) *waE2E.Message {
	for m != nil {
		inner := m.GetViewOnceMessage().GetMessage()
		if inner == nil {
			inner = m.GetViewOnceMessageV2().GetMessage()
		}
		if inner == nil {
			inner = m.GetViewOnceMessageV2Extension().GetMessage()
		}
		if inner == nil {
			inner = m.GetEphemeralMessage().GetMessage()
		}
		if inner == nil {
			return m
		}
		m = inner
	}
	return nil
}

func quotedKindLabel(m *waE2E.Message) string {
	switch {
	case m.GetImageMessage() != nil:
		return "an image"
	case m.GetVideoMessage() != nil:
		return "a video"
	case m.GetAudioMessage() != nil:
		return "a voice message"
	case m.GetStickerMessage() != nil:
		return "a sticker"
	case m.GetDocumentMessage() != nil:
		return "a document"
	case pollCreation(m) != nil:
		return "a poll"
	case m.GetPollUpdateMessage() != nil:
		return "a poll vote"
	case m.GetPollAddOptionMessage() != nil:
		return "a poll option"
	case m.GetContactMessage() != nil, m.GetContactsArrayMessage() != nil:
		return "a contact"
	case m.GetEventMessage() != nil:
		return "an event"
	case m.GetPtvMessage() != nil:
		return "a video note"
	case m.GetLocationMessage() != nil, m.GetLiveLocationMessage() != nil:
		return "a location"
	case m.GetGroupInviteMessage() != nil:
		return "a group invite"
	}
	return "a message"
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
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	case "video/3gpp":
		return ".3gp"
	case "audio/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/wav", "audio/x-wav":
		return ".wav"
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
