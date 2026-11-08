// Command hashtag-index is a Telegram bot that keeps links to public posts under hashtags.
//
//	hashtag-index login   sign the verifier's user account in once (needs a terminal)
//	hashtag-index serve   run the bot and the verifier (default)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Aryan-mor/telegram-hashtag-index/internal/botapi"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/service"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/store"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/userauth"
	"github.com/Aryan-mor/telegram-hashtag-index/internal/verify"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "login":
		err = login(ctx)
	case "say":
		err = say(ctx)
	case "serve":
		err = serve(ctx, log)
	default:
		err = fmt.Errorf("unknown command %q (use login or serve)", cmd)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

// say sends the arguments, after the bot name, to that bot from the verifier account and prints
// the answers. Stop the service first so two programs do not share one login.
func say(ctx context.Context) error {
	if len(os.Args) < 4 {
		return errors.New("usage: say <botusername> <text>...")
	}
	id, hash, err := telegramKeys()
	if err != nil {
		return err
	}
	return userauth.Say(ctx, id, hash, dataDir(), os.Args[2], os.Args[3:], os.Stdout)
}

func dataDir() string { return env("DATA_DIR", "data") }

func telegramKeys() (int, string, error) {
	id, err := strconv.Atoi(os.Getenv("TG_API_ID"))
	hash := os.Getenv("TG_API_HASH")
	if err != nil || hash == "" {
		return 0, "", errors.New("set TG_API_ID and TG_API_HASH (from my.telegram.org)")
	}
	return id, hash, nil
}

func login(ctx context.Context) error {
	id, hash, err := telegramKeys()
	if err != nil {
		return err
	}
	return userauth.Login(ctx, id, hash, dataDir(), os.Stdin, os.Stdout)
}

func serve(ctx context.Context, log *slog.Logger) error {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return errors.New("set BOT_TOKEN")
	}
	admins, err := parseIDs(os.Getenv("ADMIN_IDS"))
	if err != nil || len(admins) == 0 {
		return errors.New("set ADMIN_IDS to a comma separated list of Telegram user ids")
	}
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dir, "index.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	svc := service.New(service.Config{
		Admins:        admins,
		Open:          os.Getenv("OPEN") != "false",
		MaxPending:    envInt("MAX_PENDING", 5000),
		MaxPerTag:     envInt("MAX_PER_TAG", 100),
		ItemsPerMin:   envInt("ITEMS_PER_MIN", 30),
		QueriesPerMin: envInt("QUERIES_PER_MIN", 30),
		PageSize:      envInt("PAGE_SIZE", 30),
	}, st)

	go runVerifier(ctx, st, log)

	bot := botapi.New(token)
	log.Info("bot running")
	return bot.Run(ctx, func(ctx context.Context, id int64, isBot bool, text string) string {
		return svc.Handle(ctx, service.From{ID: id, IsBot: isBot}, text)
	}, log)
}

// runVerifier keeps the user account connected and checks pending links. Links stay
// pending, and the bot keeps working, while no session exists.
func runVerifier(ctx context.Context, st *store.Store, log *slog.Logger) {
	id, hash, err := telegramKeys()
	if err != nil {
		log.Warn("verifier off", "reason", err)
		return
	}
	wait := 5 * time.Second
	for ctx.Err() == nil {
		client := userauth.NewClient(id, hash, dataDir())
		err := client.Run(ctx, func(ctx context.Context) error {
			if err := userauth.Authorized(ctx, client); err != nil {
				return err
			}
			log.Info("verifier running")
			wait = 5 * time.Second
			(&verify.Worker{
				Store:  st,
				Fetch:  verify.NewTelegram(client.API()),
				PerMin: envInt("VERIFY_PER_MIN", 20),
				Log:    log,
			}).Run(ctx)
			return ctx.Err()
		})
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, userauth.ErrNotLoggedIn) {
			wait = 5 * time.Minute
		}
		log.Warn("verifier stopped", "err", err, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		if wait < 5*time.Minute {
			wait *= 2
		}
	}
}

func parseIDs(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}
