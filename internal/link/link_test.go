package link

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	ok := map[string]Ref{
		"https://t.me/SomeChannel/123": {"somechannel", 123},
		"t.me/some_channel/5":          {"some_channel", 5},
		"https://t.me/chan1/77?single": {"chan1", 77},
		"https://t.me/forumchan/5/123": {"forumchan", 123},
		"  https://t.me/chan1/9  ":     {"chan1", 9},
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if got, _ := Parse("T.me/x"); got != (Ref{}) {
		t.Error("kept a bad link")
	}
	if r, _ := Parse("https://t.me/Chan1/9"); r.String() != "https://t.me/chan1/9" {
		t.Errorf("clean link %q", r.String())
	}
	if _, err := Parse("https://t.me/c/1234567/89"); !errors.Is(err, ErrPrivate) {
		t.Errorf("private link: %v", err)
	}
	bad := []string{
		"", "https://example.com/chan1/9", "http://t.me/chan1/9", "https://t.me.evil.com/chan1/9",
		"https://evil.com/t.me/chan1/9", "https://t.me/chan1", "https://t.me/chan1/abc",
		"https://t.me/chan1/0", "https://t.me/chan1/9?start=x", "https://t.me/chan1/9#frag",
		"https://t.me/chan1/9/10/11", "https://t.me/ab/9", "https://t.me/1chan/9",
		"https://user@t.me/chan1/9", "https://t.me/chan1/99999999999999", "javascript:alert(1)",
		"https://t.me/SomeBot?start=abc",
	}
	for _, in := range bad {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted", in)
		}
	}
}
