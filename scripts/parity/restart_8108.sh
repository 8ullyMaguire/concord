#!/bin/sh
# Restart the port's staging instance (127.0.0.1:8108) with a binary given as $1 (default: /tmp/concord-probe).
#
# 8108 is the PORT's instance against the production database; it is not the live site — `concord.polarisocial.xyz` is
# still the Go service, and this script does not touch it. It exists so `probe_pages.py` and the probes have real data
# (73 projects) instead of a scratch database with fixtures.
#
# The install is `mv`, not `cp` over the running file: a running binary cannot be overwritten ("Text file busy"), so the
# old process is stopped, the new file takes its place by rename, and the service starts from the same path.
set -e
cd "$HOME/personal/documents/code/rust/concord" 2>/dev/null || cd "$(dirname "$0")/../.."
source_binary="${1:-/tmp/concord-probe}"
pkill -f "addr 127.0.0.1:8108" >/dev/null 2>&1 || true
sleep 2
cp "$source_binary" deploy/concord-cli.new
chmod 755 deploy/concord-cli.new
mv deploy/concord-cli.new deploy/concord-cli
set -a
. "$HOME/.config/concord/.env"
set +a
CONCORD_BASE_URL=https://concord.polarisocial.xyz setsid -f ./deploy/concord-cli serve --addr 127.0.0.1:8108 \
  > /tmp/cli-8108.log 2>&1 < /dev/null
sleep 4
for path in /projects/suki /projects/suki/settings /projects/suki/consensus /projects/suki/board; do
  printf '%-30s %s\n' "$path" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:8108$path")"
done
