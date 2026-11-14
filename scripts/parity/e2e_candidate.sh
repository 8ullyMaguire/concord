#!/usr/bin/env bash
# Run one browser suite against the port, on a database that was created for it.
#
# # Why the database is recreated and not reused
#
# The suites seed fixtures by inserting rows with fixed usernames and slugs (`feat-owner`, `feat-demo`). Run twice against the same
# database and the second run dies before its first assertion with
# `duplicate key value violates unique constraint "uniq_users_username"` — which reads as a broken fixture and is really a dirty
# database. `crates/db/sql/0001_baseline.sql` is the schema, so a fresh database costs a second and makes the run repeatable.
#
# # Why the interpreter and the binary are arguments
#
# The checkout lives on a network mount where a venv created inside it cannot execute (`Operation not permitted` — noexec), so the
# interpreter lives in `~/.venvs/concord-e2e`. The candidate binary is whatever was just built and copied to the host.
#
# Usage:  scripts/parity/e2e_candidate.sh tests/e2e/feature_e2e.py [pytest args...]
set -euo pipefail
cd "$(dirname "$0")/../.."
set -a
# shellcheck disable=SC1090
. "$HOME/.config/concord/.env"
set +a

SUITE=${1:?usage: e2e_candidate.sh <suite.py> [pytest args]}
shift || true

CANDIDATE=${CONCORD_E2E_CANDIDATE:-/tmp/concord-cli}
PYTHON=${CONCORD_E2E_PYTHON:-$HOME/.venvs/concord-e2e/bin/python}
PGPORT=$(python3 - <<'PY'
import os, urllib.parse
print(urllib.parse.urlparse(os.environ["DATABASE_URL"]).port or 5432)
PY
)

# `concord_test`, never the live database: the suites insert fixtures and the port's own probes read production.
DSN=$(python3 - <<'PY'
import os, urllib.parse
u = urllib.parse.urlparse(os.environ["DATABASE_URL"])
print(u._replace(path="/concord_test").geturl())
PY
)
sudo -u postgres psql -p "$PGPORT" -q \
  -c "drop database if exists concord_test with (force)" \
  -c "create database concord_test owner concord"
psql "$DSN" -q -v ON_ERROR_STOP=1 -f crates/db/sql/0001_baseline.sql >/tmp/e2e-baseline.log 2>&1
projects=$(psql "$DSN" -tAc "select count(*) from projects")
if [ "$projects" != "0" ]; then
  echo "refusing to run: the scratch database has $projects project(s) after the baseline" >&2
  exit 1
fi

CONCORD_E2E_CANDIDATE="$CANDIDATE" \
CONCORD_E2E_DSN="$DSN" \
CONCORD_BASE_URL="http://127.0.0.1:8200" \
  "$PYTHON" -m pytest "$SUITE" "$@"
