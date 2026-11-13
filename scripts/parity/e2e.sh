#!/usr/bin/env bash
# The S1 store's own tests, against a scratch database: `cargo test -p concord-db`.
#
# The database is recreated each run, because these tests write projects, users and cards, and a
# test that passes only because a previous run left a row behind is not evidence about the code.
set -euo pipefail
cd /personal/documents/code/projects/concord
set -a
# shellcheck disable=SC1090
. "$HOME/.config/concord/.env"
set +a
DSN=$(python3 - <<'PY'
import os, urllib.parse
u = urllib.parse.urlparse(os.environ["DATABASE_URL"])
print(u._replace(path="/concord_test").geturl())
PY
)
sudo -u postgres psql -p 55450 -q -c "drop database if exists concord_test with (force)" \
                                -c "create database concord_test owner concord"
psql "$DSN" -q -v ON_ERROR_STOP=1 -f crates/db/sql/0001_baseline.sql >/tmp/concord-test-baseline.log 2>&1
CONCORD_TEST_DSN="$DSN" CARGO_TARGET_DIR="$HOME/.cargo-target/concord" \
  cargo test -p concord-db --test store 2>&1 | tail -25
