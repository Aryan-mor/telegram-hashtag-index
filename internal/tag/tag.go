// Package tag normalizes hashtags and parses search patterns.
//
// A tag is ASCII only: a-z, 0-9 and underscore, at most 64 characters. Anything else is
// rejected instead of being folded, so look-alike letters from other scripts never become
// a second tag that reads the same.
//
// A pattern is a tag where "*" matches any run of characters. It must start with a literal
// prefix, so a pattern can never scan the whole table.
package tag

import (
	"errors"
	"strings"
)

const (
	// MaxLen is the longest tag or pattern.
	MaxLen = 64
	// MinPrefix is the shortest literal start a wildcard pattern must have.
	MinPrefix = 6
)

var (
	ErrEmpty    = errors.New("empty tag")
	ErrTooLong  = errors.New("tag longer than 64 characters")
	ErrChars    = errors.New("tag may only use a-z, 0-9 and _")
	ErrWildcard = errors.New("a wildcard needs at least 6 literal characters before the first *")
)

// Normalize turns "#Tm_TV_1_2" into "tm_tv_1_2". Wildcards are not allowed here.
func Normalize(s string) (string, error) {
	t, err := clean(s, false)
	if err != nil {
		return "", err
	}
	return t, nil
}

// Pattern is a search term: an exact tag or a tag with wildcards.
type Pattern struct {
	// Text is the normalized pattern without the leading "#".
	Text string
	// Prefix is the literal start of the pattern.
	Prefix string
	// Wild reports that the pattern holds at least one "*".
	Wild bool
}

// ParsePattern accepts "#tag" or "#tag*" forms. The leading "#" is optional.
func ParsePattern(s string) (Pattern, error) {
	t, err := clean(s, true)
	if err != nil {
		return Pattern{}, err
	}
	i := strings.IndexByte(t, '*')
	if i < 0 {
		return Pattern{Text: t, Prefix: t}, nil
	}
	if i < MinPrefix {
		return Pattern{}, ErrWildcard
	}
	return Pattern{Text: collapse(t), Prefix: t[:i], Wild: true}, nil
}

// UpperBound is the smallest string above every string that starts with Prefix, so a range
// scan "tag >= Prefix AND tag < UpperBound" uses the index.
func (p Pattern) UpperBound() string {
	b := []byte(p.Prefix)
	// Tags only hold ASCII, so adding one to the last byte never overflows.
	b[len(b)-1]++
	return string(b)
}

func clean(s string, wild bool) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if s == "" {
		return "", ErrEmpty
	}
	if len(s) > MaxLen {
		return "", ErrTooLong
	}
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		case c == '*' && wild:
		default:
			return "", ErrChars
		}
		b[i] = c
	}
	return string(b), nil
}

// collapse joins runs of "*" so "a**b" and "a*b" are the same pattern.
func collapse(s string) string {
	for strings.Contains(s, "**") {
		s = strings.ReplaceAll(s, "**", "*")
	}
	return s
}
