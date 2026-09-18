#!/usr/bin/env bash
# Redeploy the bot to its box: ships `git archive HEAD`, keeps the server's
# .env (add LLM_API_KEY / CLICKUP_TOKEN there to enable news analysis), rebuilds
# the image on the server, waits for the healthcheck.
#
#   ./deploy.sh                    # HOST defaults to root@46.101.115.227
set -euo pipefail
HOST="${HOST:-root@46.101.115.227}"
DIR="${DIR:-/root/exchange-api-update-bot}"

cd "$(dirname "$0")"
git archive --format=tar.gz -o /tmp/bot.tar.gz HEAD
scp -o StrictHostKeyChecking=accept-new /tmp/bot.tar.gz "$HOST:/root/bot.tar.gz"
rm -f /tmp/bot.tar.gz

ssh -o StrictHostKeyChecking=accept-new "$HOST" bash -s "$DIR" <<'REMOTE'
set -euo pipefail
DIR="$1"
mkdir -p "$DIR"; cd "$DIR"
tar xzf /root/bot.tar.gz && rm -f /root/bot.tar.gz
[ -f .env ] || { cp .env.example .env; echo "!!! $DIR/.env created — fill TELEGRAM_* and rerun" >&2; exit 2; }
chmod 600 .env
docker compose up -d --build --remove-orphans
for i in $(seq 1 30); do
  st=$(docker inspect -f '{{.State.Health.Status}}' exchange-api-update-bot 2>/dev/null || echo starting)
  [ "$st" = healthy ] && { echo "healthy after ${i}x3s"; docker compose ps; exit 0; }
  sleep 3
done
echo "!!! bot not healthy; last logs:" >&2; docker compose logs --tail=60 >&2; exit 1
REMOTE
