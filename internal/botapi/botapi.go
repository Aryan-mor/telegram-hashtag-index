// Package botapi is a small Bot API client that reads updates by long polling.
//
// Polling means the service opens no port and needs no domain or certificate.
package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Bot API.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// New builds a client for one bot token.
func New(token string) *Client {
	return &Client{
		token: token,
		base:  "https://api.telegram.org",
		// Longer than the 50 second poll, so a normal poll never times out.
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

// Update is one incoming event. Only plain messages are read.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a chat message.
type Message struct {
	From *User  `json:"from"`
	Chat Chat   `json:"chat"`
	Text string `json:"text"`
}

// User is the sender.
type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

// Chat is where the message was sent.
type Chat struct {
	ID int64 `json:"id"`
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts a JSON request. The token is cut out of every error text.
func (c *Client) call(ctx context.Context, method string, body any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(raw))
		if err != nil {
			return nil, c.scrub(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, c.scrub(err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.scrub(err)
		}
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, fmt.Errorf("%s: bad response (status %d)", method, resp.StatusCode)
		}
		if env.OK {
			return env.Result, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests && env.Parameters.RetryAfter > 0 && attempt == 0 {
			select {
			case <-time.After(time.Duration(env.Parameters.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("%s: %s", method, env.Description)
	}
}

func (c *Client) scrub(err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), c.token, "***"))
}

// Updates waits up to timeoutSec seconds for new messages.
func (c *Client) Updates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	res, err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	})
	if err != nil {
		return nil, err
	}
	var out []Update
	return out, json.Unmarshal(res, &out)
}

// Send sends a plain text message. Link previews are turned off.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	_, err := c.call(ctx, "sendMessage", map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	})
	return err
}

// Handler answers one message. An empty reply means stay silent.
type Handler func(ctx context.Context, userID int64, isBot bool, text string) string

// Run polls until ctx ends and sends each reply.
func (c *Client) Run(ctx context.Context, h Handler, log *slog.Logger) error {
	var offset int64
	wait := time.Second
	for ctx.Err() == nil {
		ups, err := c.Updates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Warn("poll failed", "err", err, "retry_in", wait)
			sleep(ctx, wait)
			if wait < 30*time.Second {
				wait *= 2
			}
			continue
		}
		wait = time.Second
		for _, u := range ups {
			offset = u.UpdateID + 1
			m := u.Message
			if m == nil || m.From == nil || m.Text == "" {
				continue
			}
			reply := h(ctx, m.From.ID, m.From.IsBot, m.Text)
			if reply == "" {
				continue
			}
			if err := c.Send(ctx, m.Chat.ID, reply); err != nil {
				log.Warn("send failed", "err", err)
			}
		}
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
