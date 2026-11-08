#!/usr/bin/env bash
# Installs or updates hashtag-index on a server that has Docker and git.
#
#   ssh -t YOUR_SERVER "curl -fsSL https://raw.githubusercontent.com/Aryan-mor/telegram-hashtag-index/main/deploy/install.sh | bash"
#
# The first run asks for the settings one by one, pulls the image, signs the verifier's
# Telegram account in and starts the bot. Run it again later to update.
set -euo pipefail

DIR="${INSTALL_DIR:-/srv/hashtag-index}"
REPO="https://github.com/Aryan-mor/telegram-hashtag-index.git"

# Questions come from the terminal, because this script itself is read from a pipe.
[ -r /dev/tty ] || { echo "run this through ssh -t so it can ask questions" >&2; exit 1; }
ask() { # ask VAR "question" [secret]
  local v
  if [ "${3:-}" = secret ]; then read -rsp "$2: " v </dev/tty; echo; else read -rp "$2: " v </dev/tty; fi
  [ -n "$v" ] || { echo "empty answer, stopping" >&2; exit 1; }
  printf -v "$1" '%s' "$v"
}

command -v docker >/dev/null || { echo "docker is not installed" >&2; exit 1; }
command -v git >/dev/null || { echo "git is not installed" >&2; exit 1; }

if [ -d "$DIR/.git" ]; then
  git -C "$DIR" pull --ff-only
else
  git clone "$REPO" "$DIR"
fi
cd "$DIR"

if [ ! -f .env ]; then
  echo "Settings (stored only in $DIR/.env, readable by root only)"
  ask BOT_TOKEN "Bot token from @BotFather" secret
  ask ADMIN_IDS "Your Telegram user id (several: comma separated)"
  echo "API id and hash come from https://my.telegram.org -> API development tools"
  ask TG_API_ID "API id"
  ask TG_API_HASH "API hash" secret
  umask 077
  cat > .env <<ENV
BOT_TOKEN=$BOT_TOKEN
ADMIN_IDS=$ADMIN_IDS
TG_API_ID=$TG_API_ID
TG_API_HASH=$TG_API_HASH
ENV
  umask 022
fi

mkdir -p data
chown 10001:10001 data
# The image is built by GitHub Actions. Building here could starve a small shared server of memory.
docker compose pull || { echo "could not pull the image; check that the GitHub Actions build passed" >&2; exit 1; }

if [ ! -f data/user.session ]; then
  echo
  echo "Now sign in the Telegram account the verifier will use (phone number, code, 2FA password)."
  docker compose run --rm index login </dev/tty
fi

docker compose up -d
sleep 3
docker compose ps
docker compose logs --tail 15
echo
echo "Done. Open your bot in Telegram and send /help."
