package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/link"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/tag"
)

// The guided /create flow asks for the hashtags, then for the link. A sender who gives
// nothing for convTTL starts again. At most maxConvs flows run at once.
const (
	convTTL  = 5 * time.Minute
	maxConvs = 2000
)

type convStep int

const (
	askTag convStep = iota + 1
	askLink
)

type conv struct {
	step convStep
	// tags holds the normalized hashtags, "#a #b".
	tags string
	at   time.Time
}

// startConv begins the guided flow for a sender.
func (s *Service) startConv(from From) string {
	if s.lim.take("c:"+strconv.FormatInt(from.ID, 10), 1, s.cfg.QueriesPerMin, s.cfg.Now()) == 0 {
		return "over the rate limit, try again in a minute"
	}
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.convs) >= maxConvs {
		for id, c := range s.convs {
			if now.Sub(c.at) > convTTL {
				delete(s.convs, id)
			}
		}
		if len(s.convs) >= maxConvs {
			return "busy, try again later"
		}
	}
	s.convs[from.ID] = &conv{step: askTag, at: now}
	return "send the hashtags, for example #tm_m_603 #tm_m (send /cancel to stop)"
}

// continueConv handles a message from a sender who is in the guided flow. It returns
// false when the message is not part of the flow, so the caller handles it as usual.
func (s *Service) continueConv(ctx context.Context, from From, text string) (string, bool) {
	now := s.cfg.Now()
	s.mu.Lock()
	c := s.convs[from.ID]
	if c != nil && now.Sub(c.at) > convTTL {
		delete(s.convs, from.ID)
		c = nil
	}
	s.mu.Unlock()
	if c == nil {
		return "", false
	}
	if strings.HasPrefix(text, "/") {
		s.mu.Lock()
		delete(s.convs, from.ID)
		s.mu.Unlock()
		if first, _, _ := strings.Cut(strings.ToLower(text), " "); strings.SplitN(first, "@", 2)[0] == "/cancel" {
			return "cancelled", true
		}
		return "", false
	}

	switch c.step {
	case askTag:
		var tags []string
		for _, f := range strings.Fields(text) {
			t, err := tag.Normalize(f)
			if err != nil {
				return "error: " + err.Error() + ". Send the hashtags again or /cancel", true
			}
			tags = append(tags, "#"+t)
		}
		if len(tags) > maxTagsPerLink {
			return fmt.Sprintf("error: at most %d hashtags per link. Send the hashtags again or /cancel", maxTagsPerLink), true
		}
		s.mu.Lock()
		c.tags, c.step, c.at = strings.Join(tags, " "), askLink, now
		s.mu.Unlock()
		return "now send the link, for example https://t.me/channel/123", true
	default:
		ref, err := link.Parse(text)
		if err != nil {
			return "error: " + err.Error() + ". Send the link again or /cancel", true
		}
		s.mu.Lock()
		delete(s.convs, from.ID)
		s.mu.Unlock()
		return s.addLines(ctx, from, []string{c.tags + " " + ref.String()}), true
	}
}
