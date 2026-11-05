package whatsapp

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jahrulnr/nusamation/internal/store"
)

// UserInfo uses the running connection so looking up a user never creates a
// second linked-device connection.
func (c *Channel) UserInfo(ctx context.Context, target string) (map[string]any, error) {
	jid, err := types.ParseJID(target)
	if err != nil || jid.IsEmpty() {
		return nil, fmt.Errorf("invalid user JID %q", target)
	}
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return nil, fmt.Errorf("user JID must be a WhatsApp user")
	}
	client := c.clientCopy()
	if client == nil || !client.IsLoggedIn() {
		return nil, fmt.Errorf("WhatsApp is not connected")
	}
	infos, err := client.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return nil, err
	}
	contact, err := client.Store.Contacts.GetContact(ctx, jid)
	if err != nil {
		return nil, err
	}
	info, found := infos[jid]
	return map[string]any{"jid": jid.String(), "found": found, "user": info, "contact": contact}, nil
}

// indexMessage is deliberately separate from routing: storing a message does
// not trigger an agent, download media, or apply the response allow list.
func (c *Channel) indexMessage(ctx context.Context, evt *events.Message, chatName string, live bool) error {
	if evt == nil || evt.Message == nil || evt.Info.Chat.IsEmpty() || evt.Info.ID == "" {
		return nil
	}
	msg := evt.Message
	id := string(evt.Info.ID)
	if edit := msg.GetProtocolMessage(); edit != nil && edit.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT && edit.GetEditedMessage() != nil {
		msg = edit.GetEditedMessage()
		if edit.GetKey().GetID() != "" {
			id = edit.GetKey().GetID()
		}
	}
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	text := waText(msg)
	if poll := pollCreation(msg); poll != nil {
		text = joinCaption(text, pollText(poll))
	}
	return c.store.SaveWhatsAppMessage(ctx, store.WhatsAppMessage{
		Chat: evt.Info.Chat.ToNonAD().String(), ID: id, Sender: evt.Info.Sender.ToNonAD().String(),
		SenderName: evt.Info.PushName, ChatName: chatName, Timestamp: evt.Info.Timestamp.UnixMilli(),
		FromMe: evt.Info.IsFromMe, IsGroup: evt.Info.IsGroup, Text: strings.TrimSpace(text), Raw: raw, Live: live,
	})
}

func (c *Channel) indexSent(ctx context.Context, chat types.JID, resp whatsmeow.SendResponse, msg *waE2E.Message, id types.MessageID) {
	if id == "" {
		id = resp.ID
	}
	if !resp.Chat.IsEmpty() {
		chat = resp.Chat
	}
	evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: resp.Sender, IsFromMe: true, IsGroup: chat.Server == types.GroupServer}, ID: id, Timestamp: resp.Timestamp}, Message: msg}
	if err := c.indexMessage(ctx, evt, "", true); err != nil {
		c.log.Warn("could not index sent WhatsApp message", "error", err)
	}
}

func (c *Channel) indexHistory(evt *events.HistorySync) {
	client := c.clientCopy()
	if client == nil || evt == nil || evt.Data == nil {
		return
	}
	for _, conv := range evt.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil {
			c.log.Warn("invalid WhatsApp history chat", "chat", conv.GetID(), "error", err)
			continue
		}
		for _, item := range conv.GetMessages() {
			if item.GetMessage() == nil {
				continue
			}
			msg, err := client.ParseWebMessage(chat, item.GetMessage())
			if err == nil {
				err = c.indexMessage(c.lifeCtx, msg, conv.GetDisplayName(), false)
			}
			if err != nil {
				c.log.Warn("could not index WhatsApp history message", "chat", chat.String(), "error", err)
			}
		}
	}
}
