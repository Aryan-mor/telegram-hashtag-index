// Package verify checks that a stored link opens and finds out what the post holds.
//
// The check is done with a signed-in Telegram user account, one post at a time, at a slow
// steady pace. A post that exists is marked verified with its kind (video, audio, ...).
package verify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/link"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/store"
)

// ErrGone means the post, the channel or the name does not exist (any more).
var ErrGone = errors.New("post not found")

// Fetcher reads one post.
type Fetcher interface {
	Fetch(ctx context.Context, ref link.Ref) (store.Check, error)
}

// Worker checks pending links one by one.
type Worker struct {
	Store *store.Store
	Fetch Fetcher
	// PerMin is how many links are checked per minute.
	PerMin int
	// MaxTries is how many failed checks turn a link dead.
	MaxTries int
	Log      *slog.Logger
	Now      func() time.Time
}

// Run checks links until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	if w.Now == nil {
		w.Now = time.Now
	}
	per := w.PerMin
	if per <= 0 {
		per = 20
	}
	if w.MaxTries <= 0 {
		w.MaxTries = 10
	}
	gap := time.Minute / time.Duration(per)
	for ctx.Err() == nil {
		next, err := w.Store.Pending(ctx, 1)
		if err != nil || len(next) == 0 {
			sleep(ctx, 5*time.Second)
			continue
		}
		sleep(ctx, w.one(ctx, next[0], gap))
	}
}

// one checks a single link and returns how long to wait before the next one.
func (w *Worker) one(ctx context.Context, l store.Link, gap time.Duration) time.Duration {
	chk, err := w.Fetch.Fetch(ctx, l.Ref())
	now := w.Now()
	switch {
	case err == nil:
		err = w.Store.MarkVerified(ctx, l.ID, chk, now)
		w.Log.Info("verified", "id", l.ID, "kind", chk.Kind)
	case errors.Is(err, ErrGone):
		err = w.Store.MarkDead(ctx, l.ID, now)
		w.Log.Info("dead", "id", l.ID)
	default:
		if d, ok := tgerr.AsFloodWait(err); ok {
			w.Log.Warn("flood wait", "wait", d)
			return d + time.Second
		}
		w.Log.Warn("check failed", "id", l.ID, "err", err)
		err = w.Store.Retry(ctx, l.ID, w.MaxTries, now)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Error("store update failed", "id", l.ID, "err", err)
	}
	return gap
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// Telegram reads posts through a user account.
type Telegram struct {
	api *tg.Client

	mu    sync.Mutex
	known map[string]*tg.InputChannel
}

// NewTelegram builds a Fetcher on a signed-in client.
func NewTelegram(api *tg.Client) *Telegram {
	return &Telegram{api: api, known: map[string]*tg.InputChannel{}}
}

// Fetch reads the post. A public channel can be read without joining it.
func (t *Telegram) Fetch(ctx context.Context, ref link.Ref) (store.Check, error) {
	ch, err := t.channel(ctx, ref.Chan)
	if err != nil {
		return store.Check{}, err
	}
	res, err := t.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: ch,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(ref.Msg)}},
	})
	if err != nil {
		if tgerr.Is(err, "CHANNEL_INVALID", "CHANNEL_PRIVATE", "MSG_ID_INVALID") {
			return store.Check{}, ErrGone
		}
		return store.Check{}, err
	}
	mod, ok := res.AsModified()
	if !ok {
		return store.Check{}, errors.New("unexpected reply")
	}
	msgs := mod.GetMessages()
	if len(msgs) == 0 {
		return store.Check{}, ErrGone
	}
	return Classify(msgs[0])
}

// channel resolves a public name to a channel and remembers it, because name lookups are
// the calls Telegram limits most.
func (t *Telegram) channel(ctx context.Context, name string) (*tg.InputChannel, error) {
	t.mu.Lock()
	ch := t.known[name]
	t.mu.Unlock()
	if ch != nil {
		return ch, nil
	}
	res, err := t.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
	if err != nil {
		if tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID") {
			return nil, ErrGone
		}
		return nil, err
	}
	peer, ok := res.Peer.(*tg.PeerChannel)
	if !ok {
		// A user or a bot, not a channel.
		return nil, ErrGone
	}
	for _, c := range res.Chats {
		if c, ok := c.(*tg.Channel); ok && c.ID == peer.ChannelID {
			ch = &tg.InputChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
			break
		}
	}
	if ch == nil {
		return nil, ErrGone
	}
	t.mu.Lock()
	if len(t.known) > 5000 {
		t.known = map[string]*tg.InputChannel{}
	}
	t.known[name] = ch
	t.mu.Unlock()
	return ch, nil
}

// Classify tells what a message holds.
func Classify(m tg.MessageClass) (store.Check, error) {
	msg, ok := m.(*tg.Message)
	if !ok {
		// An empty slot or a service message such as "pinned".
		return store.Check{}, ErrGone
	}
	switch media := msg.Media.(type) {
	case nil, *tg.MessageMediaEmpty:
		if strings.TrimSpace(msg.Message) == "" {
			return store.Check{}, ErrGone
		}
		return store.Check{Kind: "text"}, nil
	case *tg.MessageMediaPhoto:
		return store.Check{Kind: "photo"}, nil
	case *tg.MessageMediaWebPage:
		return store.Check{Kind: "text"}, nil
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.AsNotEmpty()
		if !ok {
			return store.Check{}, ErrGone
		}
		c := store.Check{Kind: "document", Size: doc.Size, Mime: doc.MimeType}
		var video, animated bool
		for _, a := range doc.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeVideo:
				video = true
				c.Duration = int64(a.Duration)
			case *tg.DocumentAttributeAnimated:
				animated = true
			case *tg.DocumentAttributeSticker:
				c.Kind = "sticker"
				return c, nil
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					c.Kind = "voice"
				} else {
					c.Kind = "audio"
				}
				c.Duration = int64(a.Duration)
			}
		}
		switch {
		case animated:
			c.Kind = "animation"
		case video:
			c.Kind = "video"
		}
		return c, nil
	default:
		return store.Check{Kind: "other"}, nil
	}
}
