#!/bin/sh
# Start the candidate against the e2e scratch database on a port of its own, for looking at a page by hand.
# $1 = port. The database is `concord_test`, the one scripts/parity/e2e_candidate.sh recreates.
set -e
port="${1:-8201}"
cd "$HOME"
set -a
. "$HOME/.config/concord/.env"
set +a
DSN=$(python3 -c 'import os,urllib.parse; u=urllib.parse.urlparse(os.environ["DATABASE_URL"]); print(u._replace(path="/concord_test").geturl())')
pkill -f "addr 127.0.0.1:$port" >/dev/null 2>&1 || true
sleep 1
CONCORD_DB_URL="$DSN" CONCORD_BASE_URL="http://127.0.0.1:$port" \
  setsid -f /tmp/concord-cli serve --addr "127.0.0.1:$port" > "/tmp/e2e-server-$port.log" 2>&1 < /dev/null
sleep 3
echo "listening: $(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/healthz")"
echo "projects:  $(psql "$DSN" -tAc 'select count(*) from projects')"
