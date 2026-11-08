// Package link parses Telegram post links.
//
// Only public posts are accepted: https://t.me/<channel>/<message>. The link is never
// fetched. It is cut into parts and a clean link is rebuilt from them.
package link

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrFormat  = errors.New("not a t.me post link")
	ErrPrivate = errors.New("private channel links (t.me/c/...) are not supported")
)

// Ref points at one post in a public channel.
type Ref struct {
	// Chan is the channel username in lower case.
	Chan string
	// Msg is the message id.
	Msg int64
}

// String is the clean link.
func (r Ref) String() string {
	return "https://t.me/" + r.Chan + "/" + strconv.FormatInt(r.Msg, 10)
}

var (
	userRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)
	numRe  = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// Parse accepts "https://t.me/name/123", "t.me/name/123", a forum topic form
// "t.me/name/5/123" (the last number is the message) and the "?single" suffix that
// Telegram adds to album posts. Any other query or fragment is rejected.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if len(s) > 200 {
		return Ref{}, ErrFormat
	}
	s = strings.TrimPrefix(s, "https://")
	rest, ok := strings.CutPrefix(s, "t.me/")
	if !ok {
		return Ref{}, ErrFormat
	}
	s = rest
	if i := strings.IndexByte(s, '?'); i >= 0 {
		if s[i:] != "?single" {
			return Ref{}, ErrFormat
		}
		s = s[:i]
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return Ref{}, ErrFormat
	}
	if parts[0] == "c" {
		return Ref{}, ErrPrivate
	}
	if !userRe.MatchString(parts[0]) {
		return Ref{}, ErrFormat
	}
	for _, p := range parts[1:] {
		if !numRe.MatchString(p) {
			return Ref{}, ErrFormat
		}
	}
	msg, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil || msg <= 0 {
		return Ref{}, ErrFormat
	}
	return Ref{Chan: strings.ToLower(parts[0]), Msg: msg}, nil
}
