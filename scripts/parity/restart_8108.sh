#!/bin/sh
# Restart the port's staging instance (127.0.0.1:8108) on the binary just built.
#
# 8108 is the PORT's instance against the production database; it is not the live site — `concord.polarisocial.xyz` is
# still the Go service, and this script does not touch it. It exists so `probe_pages.py` and the probes have real data
# (73 projects) instead of a scratch database with fixtures.
set -e
cd "$HOME/personal/documents/code/rust/concord" 2>/dev/null || cd "$(dirname "$0")/../.."
cp /tmp/concord-cli deploy/concord-cli
pkill -f "addr 127.0.0.1:8108" >/dev/null 2>&1 || true
sleep 1
set -a
. "$HOME/.config/concord/.env"
set +a
CONCORD_BASE_URL=https://concord.polarisocial.xyz setsid -f ./deploy/concord-cli serve --addr 127.0.0.1:8108 \
  > /tmp/cli-8108.log 2>&1 < /dev/null
sleep 4
echo "home  $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8108/projects/suki)"
echo "rank  $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8108/projects/suki/rank)"
echo "board $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8108/projects/suki/board)"
