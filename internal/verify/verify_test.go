package verify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/link"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/store"
)

func doc(mime string, size int64, attrs ...tg.DocumentAttributeClass) *tg.Message {
	return &tg.Message{Media: &tg.MessageMediaDocument{Document: &tg.Document{MimeType: mime, Size: size, Attributes: attrs}}}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		msg  tg.MessageClass
		want store.Check
	}{
		{"video", doc("video/mp4", 100, &tg.DocumentAttributeVideo{Duration: 61.5}), store.Check{Kind: "video", Size: 100, Duration: 61, Mime: "video/mp4"}},
		{"round video", doc("video/mp4", 1, &tg.DocumentAttributeVideo{RoundMessage: true, Duration: 5}), store.Check{Kind: "video", Size: 1, Duration: 5, Mime: "video/mp4"}},
		{"gif", doc("video/mp4", 2, &tg.DocumentAttributeVideo{Duration: 2}, &tg.DocumentAttributeAnimated{}), store.Check{Kind: "animation", Size: 2, Duration: 2, Mime: "video/mp4"}},
		{"audio", doc("audio/mpeg", 3, &tg.DocumentAttributeAudio{Duration: 200}), store.Check{Kind: "audio", Size: 3, Duration: 200, Mime: "audio/mpeg"}},
		{"voice", doc("audio/ogg", 4, &tg.DocumentAttributeAudio{Voice: true, Duration: 7}), store.Check{Kind: "voice", Size: 4, Duration: 7, Mime: "audio/ogg"}},
		{"sticker", doc("image/webp", 5, &tg.DocumentAttributeSticker{}), store.Check{Kind: "sticker", Size: 5, Mime: "image/webp"}},
		{"file", doc("application/zip", 6, &tg.DocumentAttributeFilename{FileName: "a.zip"}), store.Check{Kind: "document", Size: 6, Mime: "application/zip"}},
		{"mkv as a file", doc("video/x-matroska", 7, &tg.DocumentAttributeFilename{FileName: "Film.2026.mkv"}), store.Check{Kind: "video", Size: 7, Mime: "video/x-matroska"}},
		{"video by name only", doc("application/octet-stream", 8, &tg.DocumentAttributeFilename{FileName: "Episode.S01E02.AVI"}), store.Check{Kind: "video", Size: 8, Mime: "application/octet-stream"}},
		{"audio by type", doc("audio/flac", 9, &tg.DocumentAttributeFilename{FileName: "x"}), store.Check{Kind: "audio", Size: 9, Mime: "audio/flac"}},
		{"subtitle file stays a document", doc("application/x-subrip", 10, &tg.DocumentAttributeFilename{FileName: "a.srt"}), store.Check{Kind: "document", Size: 10, Mime: "application/x-subrip"}},
		{"photo", &tg.Message{Media: &tg.MessageMediaPhoto{}}, store.Check{Kind: "photo"}},
		{"text", &tg.Message{Message: "hello"}, store.Check{Kind: "text", Text: "hello"}},
		{"link preview", &tg.Message{Message: "see", Media: &tg.MessageMediaWebPage{}}, store.Check{Kind: "text", Text: "see"}},
		{"caption", &tg.Message{Message: "#tm_m_603", Media: &tg.MessageMediaPhoto{}}, store.Check{Kind: "photo", Text: "#tm_m_603"}},
		{"poll", &tg.Message{Media: &tg.MessageMediaPoll{}}, store.Check{Kind: "other"}},
	}
	for _, c := range cases {
		got, err := Classify(c.msg)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	for name, m := range map[string]tg.MessageClass{
		"empty":   &tg.MessageEmpty{},
		"service": &tg.MessageService{},
		"blank":   &tg.Message{Message: "  "},
	} {
		if _, err := Classify(m); !errors.Is(err, ErrGone) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type fake struct {
	res map[int64]error
	log []int64
}

func (f *fake) Fetch(_ context.Context, ref link.Ref) (store.Check, error) {
	f.log = append(f.log, ref.Msg)
	if err := f.res[ref.Msg]; err != nil {
		return store.Check{}, err
	}
	return store.Check{Kind: "video", Size: 9, Text: "Film\n#tm_m_603 #tm_m"}, nil
}

func TestWorkerNeedsTagOnPost(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(1_800_000_000, 0)
	ref := link.Ref{Chan: "chan1", Msg: 1}
	for _, name := range []string{"tm_m_603", "tm_m", "tm_m_60"} {
		st.Add(ctx, name, ref, 1, 10, 0, now)
	}
	w := &Worker{Store: st, Fetch: &fake{}, MaxTries: 2, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		l, _ := st.Pending(ctx, 1)
		w.one(ctx, l[0], 0)
	}
	counts, _ := st.Counts(ctx)
	if counts["verified"] != 2 || counts["dead"] != 1 {
		t.Fatalf("counts: %v", counts)
	}
	if got, _, _ := st.Find(ctx, store.Query{Pattern: mustPattern(t, "tm_m_60"), Pending: true, Limit: 10}); len(got) != 0 {
		t.Fatalf("tag missing from the post was kept: %+v", got)
	}
}

func TestWorker(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(1_800_000_000, 0)
	for i := int64(1); i <= 3; i++ {
		st.Add(ctx, "tm_m_603", link.Ref{Chan: "chan1", Msg: i}, 1, 10, 0, now)
	}
	f := &fake{res: map[int64]error{2: ErrGone, 3: errors.New("network down")}}
	w := &Worker{Store: st, Fetch: f, MaxTries: 2, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		l, _ := st.Pending(ctx, 1)
		w.one(ctx, l[0], 0)
	}
	counts, _ := st.Counts(ctx)
	if counts["verified"] != 1 || counts["dead"] != 1 || counts["pending"] != 1 {
		t.Fatalf("after one pass: %v", counts)
	}
	// The link with a network error goes to the back, and turns dead after MaxTries.
	l, _ := st.Pending(ctx, 1)
	w.one(ctx, l[0], 0)
	counts, _ = st.Counts(ctx)
	if counts["dead"] != 2 || counts["pending"] != 0 {
		t.Fatalf("after retries: %v", counts)
	}
	got, _, _ := st.Find(ctx, store.Query{Pattern: mustPattern(t, "tm_m_603"), Limit: 10})
	if len(got) != 1 || got[0].Kind != "video" || got[0].Size != 9 || got[0].Status != store.StatusVerified {
		t.Fatalf("stored: %+v", got)
	}
}
