package web

import (
	"context"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
)

// Channel is the always-present native web surface. Messages from the web UI
// are handled directly by the chat service; this channel exists so the control
// center can show the web surface alongside WhatsApp and future adapters.
type Channel struct{}

func New() *Channel { return &Channel{} }

func (c *Channel) Name() string  { return "web" }
func (c *Channel) Label() string { return "Web" }
func (c *Channel) Start(context.Context) error {
	return nil
}
func (c *Channel) Stop() error { return nil }

func (c *Channel) Status() channels.Status {
	return channels.Status{
		Name: "web", Kind: "native", Label: "Web", Enabled: true, Connected: true, LoggedIn: true,
		State: "connected", Detail: "Always available in this browser session.",
		Manageable: false, UpdatedAt: time.Now().UnixMilli(),
	}
}

func (c *Channel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return noopDelivery{}, nil
}

func (c *Channel) Notify(context.Context, channels.Target, string) error { return nil }

type noopDelivery struct{}

func (noopDelivery) ID() string { return "web" }
func (noopDelivery) Update(context.Context, string, bool) error {
	return nil
}
