package botapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunRepliesAndSkipsSilence(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]any
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			polls++
			if polls == 1 {
				io.WriteString(w, `{"ok":true,"result":[
				 {"update_id":10,"message":{"from":{"id":5,"is_bot":true},"chat":{"id":99},"text":"#tm_m_603"}},
				 {"update_id":11,"message":{"from":{"id":6,"is_bot":false},"chat":{"id":98},"text":"quiet"}},
				 {"update_id":12,"edited_message":{}},
				 {"update_id":13,"message":{"chat":{"id":1},"text":"no sender"}}]}`)
				return
			}
			if req["offset"].(float64) != 14 {
				t.Errorf("offset %v", req["offset"])
			}
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent = append(sent, req)
			io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	defer srv.Close()

	c := New("123:SECRET")
	c.base = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		for {
			mu.Lock()
			n := polls
			mu.Unlock()
			if n >= 2 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	c.Run(ctx, func(_ context.Context, id int64, isBot bool, text string) string {
		if text == "quiet" {
			return ""
		}
		if !isBot || id != 5 {
			t.Errorf("sender %d bot=%v", id, isBot)
		}
		return "reply to " + text
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0]["chat_id"].(float64) != 99 || sent[0]["text"] != "reply to #tm_m_603" {
		t.Fatalf("sent %v", sent)
	}
	if sent[0]["link_preview_options"].(map[string]any)["is_disabled"] != true {
		t.Error("link previews left on")
	}
}

func TestRetryAfterAndTokenScrub(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"ok":false,"description":"slow down","parameters":{"retry_after":1}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	c := New("123:SECRET")
	c.base = srv.URL
	start := time.Now()
	if err := c.Send(context.Background(), 1, "x"); err != nil || calls != 2 || time.Since(start) < time.Second {
		t.Fatalf("retry: err=%v calls=%d", err, calls)
	}
	srv.Close()
	err := c.Send(context.Background(), 1, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the token: %v", err)
	}
}
