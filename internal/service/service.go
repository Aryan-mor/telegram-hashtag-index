// Package service turns one incoming message into one reply. It knows nothing about the
// Bot API, so it can be tested without a network.
package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/link"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/store"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/tag"
)

// Config holds the limits.
type Config struct {
	// Admins may do everything. They are set by the operator, not by a command.
	Admins map[int64]bool
	// MaxPerTag is the most links one tag can hold.
	MaxPerTag int
	// ItemsPerMin is how many links one sender may submit per minute.
	ItemsPerMin int
	// QueriesPerMin is how many searches one sender may run per minute.
	QueriesPerMin int
	// PageSize is how many links one reply holds.
	PageSize int
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// From is the sender of a message.
type From struct {
	ID    int64
	IsBot bool
}

// Service answers messages.
type Service struct {
	cfg Config
	st  *store.Store
	lim *limiter
}

// New builds a Service.
func New(cfg Config, st *store.Store) *Service {
	if cfg.MaxPerTag <= 0 {
		cfg.MaxPerTag = 100
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 30
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, st: st, lim: newLimiter()}
}

// maxReply keeps a reply under Telegram's 4096 character limit.
const maxReply = 3800

// Handle answers one message. An empty reply means stay silent. Senders that are not
// members always get silence, so nobody can probe the bot and two bots cannot start a
// conversation with it.
func (s *Service) Handle(ctx context.Context, from From, text string) string {
	role, err := s.role(ctx, from.ID)
	if err != nil {
		return "error: try again later"
	}
	if role == 0 {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// A bare hashtag is a search.
	if strings.HasPrefix(text, "#") {
		text = "/q " + text
	}
	if !strings.HasPrefix(text, "/") {
		// Only commands get answers. This is what stops reply loops between bots.
		return ""
	}
	cmd, rest := text, ""
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		cmd, rest = text[:i], text[i+1:]
	}
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	cmd = strings.ToLower(cmd)
	if i := strings.IndexByte(rest, '\n'); i >= 0 && cmd != "/add" {
		rest = rest[:i]
	}
	admin := s.cfg.Admins[from.ID]

	switch cmd {
	case "/start", "/help":
		return helpText
	case "/add":
		if role&store.RoleWrite == 0 {
			return "not allowed to add links"
		}
		return s.add(ctx, from, rest)
	case "/q":
		if role&store.RoleRead == 0 {
			return "not allowed to search"
		}
		return s.query(ctx, from, rest)
	case "/del", "/allow", "/deny", "/purge", "/stats":
		if !admin {
			return ""
		}
		return s.admin(ctx, cmd, rest)
	}
	return ""
}

func (s *Service) role(ctx context.Context, id int64) (int, error) {
	if s.cfg.Admins[id] {
		return store.RoleWrite | store.RoleRead, nil
	}
	return s.st.Role(ctx, id)
}

const helpText = `commands:
#tag  or  /q #tag [kind:video,audio] [all] [cursor]
/add #tag https://t.me/channel/123   (one per line, several lines allowed)
tags use a-z 0-9 _ ; * is a wildcard after at least 6 characters
kinds: video audio voice document photo animation sticker text other`

func (s *Service) add(ctx context.Context, from From, body string) string {
	var lines []string
	for _, ln := range strings.Split(body, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) == 0 {
		return "usage: /add #tag https://t.me/channel/123"
	}
	now := s.cfg.Now()
	granted := s.lim.take("w:"+strconv.FormatInt(from.ID, 10), len(lines), s.cfg.ItemsPerMin, now)

	var added, dup, bad, full int
	var notes []string
	note := func(i int, msg string) {
		if len(notes) < 5 {
			notes = append(notes, fmt.Sprintf("line %d: %s", i+1, msg))
		}
	}
	for i, ln := range lines[:granted] {
		t, ref, err := parseItem(ln)
		if err != nil {
			bad++
			note(i, err.Error())
			continue
		}
		res, err := s.st.Add(ctx, t, ref, from.ID, s.cfg.MaxPerTag, now)
		if err != nil {
			bad++
			note(i, "storage error")
			continue
		}
		switch res {
		case store.Added:
			added++
		case store.Duplicate:
			dup++
		case store.Full:
			full++
			note(i, "tag is full")
		}
	}
	out := fmt.Sprintf("added %d, duplicate %d, rejected %d, full %d", added, dup, bad, full)
	if limited := len(lines) - granted; limited > 0 {
		out += fmt.Sprintf(", over the rate limit %d", limited)
	}
	if len(notes) > 0 {
		out += "\n" + strings.Join(notes, "\n")
	}
	return out
}

// parseItem reads "#tag link". The two parts may come in either order.
func parseItem(line string) (string, link.Ref, error) {
	f := strings.Fields(line)
	if len(f) != 2 {
		return "", link.Ref{}, fmt.Errorf("expected: #tag link")
	}
	a, b := f[0], f[1]
	if !strings.HasPrefix(a, "#") {
		a, b = b, a
	}
	t, err := tag.Normalize(a)
	if err != nil {
		return "", link.Ref{}, err
	}
	ref, err := link.Parse(b)
	if err != nil {
		return "", link.Ref{}, err
	}
	return t, ref, nil
}

func (s *Service) query(ctx context.Context, from From, args string) string {
	var q store.Query
	var havePattern bool
	var kindsText string
	for _, tok := range strings.Fields(args) {
		low := strings.ToLower(tok)
		switch {
		case strings.HasPrefix(tok, "#"):
			p, err := tag.ParsePattern(tok)
			if err != nil {
				return "error: " + err.Error()
			}
			q.Pattern, havePattern = p, true
		case strings.HasPrefix(low, "kind:"):
			kindsText = low[len("kind:"):]
			for _, k := range strings.Split(kindsText, ",") {
				if !store.ValidKind(k) {
					return "error: unknown kind " + k
				}
				q.Kinds = append(q.Kinds, k)
			}
		case low == "all":
			q.Pending = true
		default:
			n, err := strconv.ParseInt(tok, 10, 64)
			if err != nil || n <= 0 {
				return "error: unknown option " + tok
			}
			q.Before = n
		}
	}
	if !havePattern {
		return "usage: /q #tag [kind:video,audio] [all] [cursor]"
	}
	if s.lim.take("r:"+strconv.FormatInt(from.ID, 10), 1, s.cfg.QueriesPerMin, s.cfg.Now()) == 0 {
		return "over the rate limit, try again in a minute"
	}
	q.Limit = s.cfg.PageSize
	links, more, err := s.st.Find(ctx, q)
	if err != nil {
		return "error: try again later"
	}
	if len(links) == 0 {
		return "none"
	}
	var b strings.Builder
	last := links[0].ID
	for i, l := range links {
		line := l.Ref().String() + " " + kindOrDash(l.Kind) + " #" + l.Tag
		if l.Status == store.StatusPending {
			line += " unverified"
		}
		if b.Len()+len(line)+1 > maxReply {
			more = true
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		last = links[i].ID
	}
	if more {
		next := "next: /q #" + q.Pattern.Text
		if kindsText != "" {
			next += " kind:" + kindsText
		}
		if q.Pending {
			next += " all"
		}
		b.WriteString(next + " " + strconv.FormatInt(last, 10))
	}
	return strings.TrimRight(b.String(), "\n")
}

func kindOrDash(k string) string {
	if k == "" {
		return "-"
	}
	return k
}

func (s *Service) admin(ctx context.Context, cmd, args string) string {
	f := strings.Fields(args)
	switch cmd {
	case "/stats":
		c, err := s.st.Counts(ctx)
		if err != nil {
			return "error: try again later"
		}
		return fmt.Sprintf("verified %d, pending %d, dead %d", c[store.StatusVerified], c[store.StatusPending], c[store.StatusDead])
	case "/del":
		if len(f) != 2 {
			return "usage: /del #tag link"
		}
		t, ref, err := parseItem(f[0] + " " + f[1])
		if err != nil {
			return "error: " + err.Error()
		}
		ok, err := s.st.Delete(ctx, t, ref)
		if err != nil {
			return "error: try again later"
		}
		if !ok {
			return "not found"
		}
		return "deleted"
	case "/allow":
		if len(f) != 2 {
			return "usage: /allow <id> write|read|both"
		}
		id, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return "error: bad id"
		}
		role := map[string]int{"write": store.RoleWrite, "read": store.RoleRead, "both": store.RoleWrite | store.RoleRead}[strings.ToLower(f[1])]
		if role == 0 {
			return "usage: /allow <id> write|read|both"
		}
		if err := s.st.SetRole(ctx, id, role); err != nil {
			return "error: try again later"
		}
		return "allowed"
	case "/deny", "/purge":
		if len(f) != 1 {
			return "usage: " + cmd + " <id>"
		}
		id, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return "error: bad id"
		}
		if err := s.st.SetRole(ctx, id, 0); err != nil {
			return "error: try again later"
		}
		if cmd == "/deny" {
			return "removed"
		}
		n, err := s.st.DeleteBy(ctx, id)
		if err != nil {
			return "error: try again later"
		}
		return fmt.Sprintf("removed, %d links deleted", n)
	}
	return ""
}
