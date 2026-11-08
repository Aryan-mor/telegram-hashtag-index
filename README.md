# telegram-hashtag-index

A Telegram bot that keeps links to public posts under hashtags, so a post can be found by its
hashtag without joining the channel it lives in.

Other bots send it `#tag link` pairs. Other bots (or people) send it a hashtag and get the links
back, newest first, page by page. A signed-in user account checks every link in the background
and records what the post holds (video, audio, document, ...).

## Use

Everything is a message to the bot. Only members get answers; everyone else gets silence.

| Message | Result |
|---|---|
| `/add #tag https://t.me/channel/123` | Stores the link. Several lines can follow in one message. |
| `#tag` or `/q #tag` | Verified links for the tag, 30 per page, newest first. |
| `/q #tag kind:video,audio` | Only these kinds. |
| `/q #tag all` | Also links that are not verified yet (marked `unverified`). |
| `/q #tag [options] 4812` | Next page. The reply ends with `next: ...`, copy that line. |
| `/q #tm_tv_322280_1_6*` | Wildcard. `*` matches any run of characters. |
| `/q #tm_tv_322280_1_6_*_fa` | Wildcards also work in the middle. |

Each result line is `link kind #tag`, for example `https://t.me/chan1/10 video #tm_tv_5_1_2_en_fa`.

Kinds: `video audio voice document photo animation sticker text other`.

Tags use `a-z 0-9 _` only (64 characters at most). A wildcard needs at least 6 literal
characters before the first `*`, so no search can read the whole table. Regular expressions are
not supported on purpose.

Only public post links are accepted: `https://t.me/<channel>/<id>`. Private links
(`t.me/c/...`) are rejected with an error.

### Admin commands

`/allow <id> write|read|both`, `/deny <id>`, `/purge <id>` (removes the member and all links they
added), `/del #tag link`, `/stats`. Admins are the ids in `ADMIN_IDS`.

## Rules for members

- Writers may submit 30 links per minute and readers may run 30 searches per minute (set with
  `ITEMS_PER_MIN` and `QUERIES_PER_MIN`). A tag holds at most 100 links.
- The bot answers commands only. It never answers plain text, so two bots cannot start a
  conversation with it, and no reply starts with `/` or `#`.
- Send many links in one message instead of many messages. One message can hold 30 lines.

## Verification

A background worker takes pending links oldest first, 20 per minute (`VERIFY_PER_MIN`), and reads
each post with a user account. The post must exist in a public channel. The worker records the
kind, size, duration and MIME type exactly as Telegram reports them. Links that cannot be read
for good are marked dead and no longer returned. Temporary errors are retried and turn the link
dead after 10 failures.

## Run

You need a bot from @BotFather (turn on bot-to-bot communication for it), an API id and hash
from <https://my.telegram.org> and a Telegram user account for the verifier.

```sh
cp .env.example .env        # fill it in
docker compose build
docker compose run --rm index login    # once: phone number, code, 2FA password
docker compose up -d
```

The bot polls Telegram, so it opens no port and needs no domain. Data (the database and the
session file) lives in `./data`. Keep that folder private: the session file is a login.

Without a session the bot still works and links stay pending until you run `login`.

## Development

```sh
go test ./...
```
