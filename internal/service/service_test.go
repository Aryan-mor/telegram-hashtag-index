package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/store"
)

const (
	admin  = int64(1)
	writer = int64(2)
	reader = int64(3)
	nobody = int64(4)
)

type rig struct {
	t   *testing.T
	st  *store.Store
	svc *Service
	now time.Time
}

func newRig(t *testing.T, mod func(*Config)) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, now: time.Unix(1_800_000_000, 0)}
	cfg := Config{
		Admins: map[int64]bool{admin: true}, MaxPerTag: 3, ItemsPerMin: 30, QueriesPerMin: 30,
		PageSize: 2, Now: func() time.Time { return r.now },
	}
	if mod != nil {
		mod(&cfg)
	}
	r.svc = New(cfg, st)
	ctx := context.Background()
	st.SetRole(ctx, writer, store.RoleWrite)
	st.SetRole(ctx, reader, store.RoleRead)
	return r
}

func (r *rig) say(id int64, isBot bool, text string) string {
	return r.svc.Handle(context.Background(), From{ID: id, IsBot: isBot}, text)
}

// verify marks every pending link verified with the given kind.
func (r *rig) verify(kind string) {
	ctx := context.Background()
	for {
		p, _ := r.st.Pending(ctx, 10)
		if len(p) == 0 {
			return
		}
		for _, l := range p {
			r.st.MarkVerified(ctx, l.ID, store.Check{Kind: kind}, r.now)
		}
	}
}

func TestStrangersAndPlainTextGetSilence(t *testing.T) {
	r := newRig(t, nil)
	for _, in := range []string{"/help", "#tm_m_603", "/add #tm_m_603 https://t.me/chan1/1", "/stats"} {
		if got := r.say(nobody, true, in); got != "" {
			t.Errorf("stranger got %q for %q", got, in)
		}
	}
	// A member sending ordinary text, such as an answer to one of our replies, gets nothing.
	for _, in := range []string{"added 1, duplicate 0", "ok", "none", "thanks"} {
		if got := r.say(writer, true, in); got != "" {
			t.Errorf("answered plain text %q with %q", in, got)
		}
	}
}

func TestReplyNeverLooksLikeACommand(t *testing.T) {
	r := newRig(t, nil)
	for _, got := range []string{
		r.say(writer, true, "/add #tm_m_603 https://t.me/chan1/1"),
		r.say(writer, true, "/add garbage"),
		r.say(reader, true, "#tm_m_603"),
		r.say(admin, false, "/stats"),
		r.say(admin, false, "/help"),
	} {
		if got == "" || strings.HasPrefix(got, "/") || strings.HasPrefix(got, "#") {
			t.Errorf("reply %q could be taken for a command", got)
		}
	}
}

func TestRoles(t *testing.T) {
	r := newRig(t, nil)
	if got := r.say(reader, true, "/add #tm_m_603 https://t.me/chan1/1"); !strings.Contains(got, "not allowed") {
		t.Errorf("reader could add: %q", got)
	}
	if got := r.say(writer, true, "#tm_m_603"); !strings.Contains(got, "not allowed") {
		t.Errorf("writer could search: %q", got)
	}
	for _, cmd := range []string{"/allow 9 both", "/deny 2", "/purge 2", "/del #tm_m_603 https://t.me/chan1/1", "/stats"} {
		if got := r.say(writer, false, cmd); got != "" {
			t.Errorf("writer ran %q: %q", cmd, got)
		}
	}
}

func TestAddAndQuery(t *testing.T) {
	r := newRig(t, nil)
	got := r.say(writer, true, "/add #tm_tv_5_1_2_en_fa https://t.me/Chan1/10\n#tm_tv_5_1_2_en https://t.me/chan1/11\n#tm_tv_5_1_2_en_fa https://t.me/chan1/10")
	if got != "added 2, duplicate 1, rejected 0, full 0" {
		t.Fatalf("add: %q", got)
	}
	// Links are not returned until the verifier has seen them.
	if got := r.say(reader, true, "#tm_tv_5_1_2_en_fa"); got != "none" {
		t.Fatalf("before verify: %q", got)
	}
	if got := r.say(reader, true, "/q #tm_tv_5_1_2_en_fa all"); !strings.Contains(got, "unverified") {
		t.Fatalf("all: %q", got)
	}
	r.verify("video")
	if got := r.say(reader, true, "#tm_tv_5_1_2_en_fa"); got != "https://t.me/chan1/10 video #tm_tv_5_1_2_en_fa" {
		t.Fatalf("exact: %q", got)
	}
	// A wildcard sees both tags, newest first.
	want := "https://t.me/chan1/11 video #tm_tv_5_1_2_en\nhttps://t.me/chan1/10 video #tm_tv_5_1_2_en_fa"
	if got := r.say(reader, true, "/q #tm_tv_5_1_2*"); got != want {
		t.Fatalf("wild: %q", got)
	}
	if got := r.say(reader, true, "/q #tm_tv_5_1_2_*_fa"); got != "https://t.me/chan1/10 video #tm_tv_5_1_2_en_fa" {
		t.Fatalf("middle wildcard: %q", got)
	}
	if got := r.say(reader, true, "/q #tm_tv_5_1_2* kind:audio"); got != "none" {
		t.Fatalf("kind filter: %q", got)
	}
	if got := r.say(reader, true, "/q #tm_tv_5_1_2* kind:audio,video"); !strings.Contains(got, "chan1/11") {
		t.Fatalf("kind list: %q", got)
	}
}

func TestPaging(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MaxPerTag = 10 })
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf("#tm_m_603 https://t.me/chan1/%d", i))
	}
	r.say(writer, true, "/add\n"+strings.Join(lines, "\n"))
	r.verify("video")

	var seen []string
	cmd := "/q #tm_m_603 kind:video"
	for page := 0; page < 5 && cmd != ""; page++ {
		got := r.say(reader, true, cmd)
		cmd = ""
		for _, ln := range strings.Split(got, "\n") {
			if strings.HasPrefix(ln, "next: ") {
				cmd = strings.TrimPrefix(ln, "next: ")
			} else {
				seen = append(seen, strings.Fields(ln)[0])
			}
		}
	}
	if len(seen) != 5 || seen[0] != "https://t.me/chan1/5" || seen[4] != "https://t.me/chan1/1" {
		t.Fatalf("pages gave %v", seen)
	}
}

func TestRejects(t *testing.T) {
	r := newRig(t, nil)
	got := r.say(writer, true, "/add\n#tm_m_603 https://t.me/c/123/4\n#tm_m_603 https://evil.com/chan1/1\ntm-m https://t.me/chan1/1\njust words\n#tm_m_603 https://t.me/chan1/1?start=x")
	if !strings.HasPrefix(got, "added 0, duplicate 0, rejected 5") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "private channel") {
		t.Errorf("no private link message: %q", got)
	}
	if n := len(strings.Split(got, "\n")); n > 6 {
		t.Errorf("reply lists %d lines", n)
	}
}

func TestTagCap(t *testing.T) {
	r := newRig(t, nil)
	got := r.say(writer, true, "/add\n#tm_m_603 https://t.me/chan1/1\n#tm_m_603 https://t.me/chan1/2\n#tm_m_603 https://t.me/chan1/3\n#tm_m_603 https://t.me/chan1/4")
	if !strings.HasPrefix(got, "added 3, duplicate 0, rejected 0, full 1") {
		t.Fatalf("got %q", got)
	}
}

func TestRateLimit(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ItemsPerMin = 30; c.MaxPerTag = 1000; c.QueriesPerMin = 2 })
	batch := func(from, n int) string {
		var b strings.Builder
		b.WriteString("/add")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "\n#tm_m_603 https://t.me/chan1/%d", from+i)
		}
		return b.String()
	}
	// 30 items in one minute are allowed, the 31st is not.
	if got := r.say(writer, true, batch(1, 30)); !strings.HasPrefix(got, "added 30,") {
		t.Fatalf("30 items: %q", got)
	}
	if got := r.say(writer, true, batch(100, 5)); !strings.Contains(got, "over the rate limit 5") || !strings.HasPrefix(got, "added 0,") {
		t.Fatalf("over the limit: %q", got)
	}
	r.now = r.now.Add(30 * time.Second)
	if got := r.say(writer, true, batch(200, 20)); !strings.Contains(got, "added 15,") || !strings.Contains(got, "over the rate limit 5") {
		t.Fatalf("after half a minute: %q", got)
	}
	// Another sender has its own budget.
	r.st.SetRole(context.Background(), 20, store.RoleWrite)
	if got := r.say(20, true, batch(300, 3)); !strings.HasPrefix(got, "added 3,") {
		t.Fatalf("second sender: %q", got)
	}
	// Searches have a budget too.
	r.say(reader, true, "#tm_m_603")
	r.say(reader, true, "#tm_m_603")
	if got := r.say(reader, true, "#tm_m_603"); !strings.Contains(got, "rate limit") {
		t.Fatalf("third search: %q", got)
	}
}

func TestQueryErrors(t *testing.T) {
	r := newRig(t, nil)
	for in, want := range map[string]string{
		"/q #*":               "6 literal",
		"/q #tm_tv*":          "6 literal",
		"/q #tm_m_603 kind:x": "unknown kind",
		"/q #tm_m_603 abc":    "unknown option",
		"/q":                  "usage",
		"/q #tm-m":            "a-z",
	} {
		if got := r.say(reader, true, in); !strings.Contains(got, want) {
			t.Errorf("%q gave %q, want %q", in, got, want)
		}
	}
}

func TestAdminCommands(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	if got := r.say(admin, false, "/allow 77 write"); got != "allowed" {
		t.Fatal(got)
	}
	if role, _ := r.st.Role(ctx, 77); role != store.RoleWrite {
		t.Errorf("role %d", role)
	}
	r.say(77, true, "/add #tm_m_603 https://t.me/chan1/1\n#tm_m_604 https://t.me/chan1/2")
	r.say(writer, true, "/add #tm_m_603 https://t.me/chan1/3")
	if got := r.say(admin, false, "/del #tm_m_603 https://t.me/chan1/3"); got != "deleted" {
		t.Errorf("del: %q", got)
	}
	if got := r.say(admin, false, "/del #tm_m_603 https://t.me/chan1/3"); got != "not found" {
		t.Errorf("del again: %q", got)
	}
	if got := r.say(admin, false, "/purge 77"); got != "removed, 2 links deleted" {
		t.Errorf("purge: %q", got)
	}
	if got := r.say(77, true, "/add #tm_m_603 https://t.me/chan1/9"); got != "" {
		t.Errorf("purged member still answered: %q", got)
	}
	if got := r.say(admin, false, "/stats"); got != "verified 0, pending 0, dead 0" {
		t.Errorf("stats: %q", got)
	}
}

func TestBotNameSuffixAndLength(t *testing.T) {
	r := newRig(t, func(c *Config) { c.PageSize = 200; c.MaxPerTag = 1000 })
	if got := r.say(writer, false, "/add@MyIndexBot #tm_m_603 https://t.me/chan1/1"); !strings.HasPrefix(got, "added 1") {
		t.Errorf("suffix: %q", got)
	}
	var b strings.Builder
	b.WriteString("/add")
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "\n#tm_m_603 https://t.me/averyveryverylongchannelname/%d", i)
	}
	r.svc.cfg.ItemsPerMin = 0
	r.say(writer, false, b.String())
	r.verify("document")
	got := r.say(reader, false, "#tm_m_603")
	if len(got) > maxReply+200 || !strings.Contains(got, "\nnext: /q #tm_m_603 ") {
		t.Errorf("long reply: %d bytes, tail %q", len(got), got[len(got)-60:])
	}
}
