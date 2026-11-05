package whatsapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/jahrulnr/nusamation/internal/channels"
)

// OutboundRequest describes one explicit WhatsApp operation. Times are Unix seconds.
type OutboundRequest struct {
	Target        string   `json:"target"`
	Type          string   `json:"type"`
	Text          string   `json:"text,omitempty"`
	MessageID     string   `json:"messageId,omitempty"`
	SenderID      string   `json:"senderId,omitempty"`
	QuotedText    string   `json:"quotedText,omitempty"`
	Data          string   `json:"data,omitempty"` // base64-encoded WebP
	Animated      bool     `json:"animated,omitempty"`
	Latitude      *float64 `json:"latitude,omitempty"`
	Longitude     *float64 `json:"longitude,omitempty"`
	Name          string   `json:"name,omitempty"`
	Address       string   `json:"address,omitempty"`
	VCard         string   `json:"vcard,omitempty"`
	Description   string   `json:"description,omitempty"`
	StartTime     int64    `json:"startTime,omitempty"`
	EndTime       int64    `json:"endTime,omitempty"`
	JoinLink      string   `json:"joinLink,omitempty"`
	Options       []string `json:"options,omitempty"`
	MaxSelectable int      `json:"maxSelectable,omitempty"`
}

func locationMessage(req OutboundRequest) (*waE2E.LocationMessage, error) {
	if req.Latitude == nil || req.Longitude == nil || *req.Latitude < -90 || *req.Latitude > 90 || *req.Longitude < -180 || *req.Longitude > 180 {
		return nil, errors.New("latitude and longitude are required and must be valid coordinates")
	}
	return &waE2E.LocationMessage{DegreesLatitude: req.Latitude, DegreesLongitude: req.Longitude, Name: proto.String(req.Name), Address: proto.String(req.Address)}, nil
}

// buildOutbound prepares non-media messages separately from network I/O.
func buildOutbound(req OutboundRequest) (*waE2E.Message, error) {
	switch req.Type {
	case "reply":
		if strings.TrimSpace(req.Text) == "" || req.MessageID == "" || req.SenderID == "" {
			return nil, errors.New("reply requires text, messageId and senderId")
		}
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(req.Text), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(req.MessageID), Participant: proto.String(req.SenderID), QuotedMessage: &waE2E.Message{Conversation: proto.String(req.QuotedText)}}}}, nil
	case "location":
		loc, err := locationMessage(req)
		if err != nil {
			return nil, err
		}
		return &waE2E.Message{LocationMessage: loc}, nil
	case "contact":
		if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.VCard) == "" {
			return nil, errors.New("contact requires name and vcard")
		}
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String(req.Name), Vcard: proto.String(req.VCard)}}, nil
	case "event":
		if strings.TrimSpace(req.Name) == "" || req.StartTime <= 0 || (req.EndTime != 0 && req.EndTime < req.StartTime) {
			return nil, errors.New("event requires a name and valid startTime/endTime")
		}
		ev := &waE2E.EventMessage{Name: proto.String(req.Name), Description: proto.String(req.Description), StartTime: proto.Int64(req.StartTime), JoinLink: proto.String(req.JoinLink)}
		if req.EndTime != 0 {
			ev.EndTime = proto.Int64(req.EndTime)
		}
		if req.Latitude != nil || req.Longitude != nil {
			loc, err := locationMessage(req)
			if err != nil {
				return nil, err
			}
			ev.Location = loc
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		return &waE2E.Message{EventMessage: ev, MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: secret}}, nil
	default:
		return nil, fmt.Errorf("unsupported outbound type %q", req.Type)
	}
}

// SendStructured sends an explicit operation; it does not start an ACP turn.
func (c *Channel) SendStructured(ctx context.Context, req OutboundRequest) (string, error) {
	target := channels.Target{ExternalID: req.Target}
	jid, err := c.parseTarget(target)
	if err != nil {
		return "", err
	}
	if !c.settingsSnapshot().Enabled {
		return "", errors.New("WhatsApp channel is disabled")
	}
	client := c.clientCopy()
	if client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	if req.Type == "poll" {
		if len(req.Options) < 2 || len(req.Options) > 12 || req.MaxSelectable < 0 || req.MaxSelectable > len(req.Options) || strings.TrimSpace(req.Text) == "" {
			return "", errors.New("poll requires a question, 2–12 options and a valid maxSelectable")
		}
		seen := make(map[string]bool, len(req.Options))
		for i, option := range req.Options {
			option = strings.TrimSpace(option)
			if option == "" || seen[option] {
				return "", errors.New("poll options must be nonempty and distinct")
			}
			seen[option] = true
			req.Options[i] = option
		}
		id, err := c.SendPoll(ctx, target, req.Text, req.Options, req.MaxSelectable)
		if err != nil {
			return "", err
		}
		if c.store != nil {
			if err := c.store.SetJSONSetting(ctx, generalPollKey(jid.ToNonAD().String(), id), req.Options); err != nil {
				return id, fmt.Errorf("poll sent, but could not save vote metadata: %w", err)
			}
		}
		return id, nil
	}
	var msg *waE2E.Message
	switch req.Type {
	case "reaction":
		if req.MessageID == "" || req.SenderID == "" {
			return "", errors.New("reaction requires messageId and senderId")
		}
		sender, err := types.ParseJID(req.SenderID)
		if err != nil {
			return "", fmt.Errorf("invalid senderId: %w", err)
		}
		msg = client.BuildReaction(jid, sender, req.MessageID, req.Text)
	case "sticker":
		data, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil || len(data) == 0 || len(data) > maxMediaBytes {
			return "", errors.New("sticker data must be base64 WebP within the media size limit")
		}
		if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return "", errors.New("sticker must be WebP")
		}
		up, err := client.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			return "", err
		}
		msg = &waE2E.Message{StickerMessage: &waE2E.StickerMessage{URL: &up.URL, DirectPath: &up.DirectPath, MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: &up.FileLength, Mimetype: proto.String("image/webp"), IsAnimated: proto.Bool(req.Animated)}}
	default:
		msg, err = buildOutbound(req)
		if err != nil {
			return "", err
		}
	}
	resp, err := client.SendMessage(ctx, jid, msg)
	if err != nil {
		return "", err
	}
	c.indexSent(ctx, jid, resp, msg, "")
	return resp.ID, nil
}

func generalPollKey(chat, id string) string { return "channel.whatsapp.poll." + chat + "." + id }

func (c *Channel) generalPollOptions(ctx context.Context, chat types.JID, id string) ([]string, bool) {
	if c.store == nil {
		return nil, false
	}
	var options []string
	if err := c.store.GetJSONSetting(ctx, generalPollKey(chat.ToNonAD().String(), id), &options); err != nil {
		return nil, false
	}
	return options, true
}
