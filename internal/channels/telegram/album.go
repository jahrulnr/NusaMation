package telegram

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
)

// albumDebounce is how long a media group waits for its last item. Telegram
// sends every album item as a separate update, milliseconds apart.
const albumDebounce = 400 * time.Millisecond

// albumBatch accumulates one media group. gen invalidates a timer that fires
// just as a newer item arrives.
type albumBatch struct {
	gen         uint64
	first       channels.Inbound
	texts       []string
	attachments []channels.Attachment
	timer       *time.Timer
}

func albumKey(m *tgMessage) string {
	return strconv.FormatInt(m.Chat.ID, 10) + ":" +
		strconv.FormatInt(m.MessageThreadID, 10) + ":" + m.MediaGroupID
}

// bufferAlbum adds one item to its group and restarts the debounce. The
// merged inbound keeps the first item's id, so dedupe stays chat:message.
func (c *Channel) bufferAlbum(m *tgMessage, in channels.Inbound) {
	key := albumKey(m)
	c.albumMu.Lock()
	b := c.albums[key]
	if b == nil {
		b = &albumBatch{first: in}
		c.albums[key] = b
	}
	b.gen++
	gen := b.gen
	if room := inboundAttachmentLimit - len(b.attachments); room > 0 && len(in.Attachments) > 0 {
		add := in.Attachments
		if len(add) > room {
			add = add[:room]
		}
		b.attachments = append(b.attachments, add...)
	}
	if t := strings.TrimSpace(in.Text); t != "" {
		b.texts = append(b.texts, t)
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(albumDebounce, func() { c.flushAlbum(key, gen) })
	c.albumMu.Unlock()
}

// flushAlbum delivers one settled group, unless a newer item superseded the
// timer that scheduled this call.
func (c *Channel) flushAlbum(key string, gen uint64) {
	c.albumMu.Lock()
	b := c.albums[key]
	if b == nil || b.gen != gen {
		c.albumMu.Unlock()
		return
	}
	delete(c.albums, key)
	c.albumMu.Unlock()
	c.dispatchAlbum(b)
}

// flushAlbums delivers every buffered group. Disconnect calls it so an album
// that arrived just before the poll loop stopped is not lost.
func (c *Channel) flushAlbums() {
	c.albumMu.Lock()
	batches := make([]*albumBatch, 0, len(c.albums))
	for key, b := range c.albums {
		if b.timer != nil {
			b.timer.Stop()
		}
		delete(c.albums, key)
		batches = append(batches, b)
	}
	c.albumMu.Unlock()
	for _, b := range batches {
		c.dispatchAlbum(b)
	}
}

func (c *Channel) dispatchAlbum(b *albumBatch) {
	in := b.first
	in.Text = strings.TrimSpace(strings.Join(b.texts, "\n"))
	in.Attachments = b.attachments
	if in.Text == "" && len(in.Attachments) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c.dispatch(ctx, in)
}
