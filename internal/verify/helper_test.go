package verify

import (
	"testing"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/tag"
)

func mustPattern(t *testing.T, s string) tag.Pattern {
	t.Helper()
	p, err := tag.ParsePattern(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
