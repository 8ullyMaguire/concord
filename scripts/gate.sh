#!/usr/bin/env bash
# The gate: one command that runs what this repository already proves, in one order, so that "the suite is green" is a claim a machine
# makes rather than a memory an operator has. Review decision 7.
#
# Three layers, because they fail for different reasons:
#   1. unit      — every crate's tests. The store tests need a real PostgreSQL (`CONCORD_TEST_DSN`), which is why this script creates one
#                  before it asserts anything: a suite that fails because nobody set a variable teaches people to ignore it.
#   2. smoke     — the deployed binary on that same fresh database, real HTTP. The layer that catches "it compiles and the route is not
#                  mounted", which no unit test in this tree can see.
#   3. live      — the probes against a target, when one is named. Slow, and the only layer that can be wrong about the deployed
#                  instance rather than about the build.
#
# Usage:
#   scripts/gate.sh                    # unit + smoke against a scratch database
#   scripts/gate.sh --live URL         # the above, then probes against URL
#   SKIP_SMOKE=1 scripts/gate.sh       # unit only, using DATABASE_URL's own database
set -uo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
PORT=${GATE_PORT:-18099}
FAILED=0
DBNAME="gate_$$"
SCRATCH=$(mktemp -d)
CREATED_DB=0
SERVER_PID=""
say() { printf '\n=== %s\n' "$*"; }
bad() { printf 'FAIL: %s\n' "$*"; FAILED=1; }

LIVE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --live) LIVE=${2:-}; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  if [ "$CREATED_DB" = "1" ]; then
    psql "$DATABASE_URL" -q -c "drop database if exists $DBNAME with (force)" 2>/dev/null
  fi
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

# A scratch database, because the store tests write, and a gate must never empty the database it was pointed at.
# The application role usually cannot CREATE DATABASE, and a gate that skips its own smoke layer because of a privilege is a gate that
# reports PASS while testing half of itself. So: direct create first, then the cluster owner with the app role as owner of the new
# database. Both are ordinary moves; the failure they replace read as "the suite is red" for a reason that had nothing to do with code.
# Note: the scratch database is created even under SKIP_SMOKE, because the store tests need one either way and the fallback below
# points CONCORD_TEST_DSN at DATABASE_URL — which would run a writing test suite against whatever database the operator exported.
if [ -n "${DATABASE_URL:-}" ]; then
  PG_PORT=$(psql "$DATABASE_URL" -tAc "select current_setting('port')" 2>/dev/null)
  PG_USER=$(psql "$DATABASE_URL" -tAc "select current_user" 2>/dev/null)
  if psql "$DATABASE_URL" -q -c "create database $DBNAME" 2>/dev/null \
     || sudo -n -u postgres psql -p "${PG_PORT:-5432}" -q -c "create database $DBNAME owner \"$PG_USER\"" 2>/dev/null; then
    CREATED_DB=1
    DSN=$(python3 - "$DATABASE_URL" "$DBNAME" <<'PY'
import sys, urllib.parse
u = urllib.parse.urlparse(sys.argv[1])
print(u._replace(path="/" + sys.argv[2]).geturl())
PY
)
    export CONCORD_TEST_DSN="$DSN"
    # A scratch database is an EMPTY database, and the store tests are not a schema: they assume one exists and fail with `relation
    # "users" does not exist` — which reads like a broken test rather than an un-migrated database. So the gate applies the migrations it
    # ships, in order, before it asserts anything. Same files the binary runs at boot, so the schema under the tests is the schema in
    # production.
    for migration in crates/db/sql/*.sql; do
      psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$migration" >/dev/null 2>&1 || {
        echo "gate: $migration did not apply to the scratch database"
        FAILED=1
      }
    done
    echo "scratch database: $DBNAME (migrated: $(ls crates/db/sql/*.sql | wc -l) files)"
  else
    echo "gate: could not create a scratch database — the store tests will fail on CONCORD_TEST_DSN"
  fi
fi
# A fallback that points the store tests at DATABASE_URL is a fallback that runs a WRITING suite against whatever database the
# operator had exported — production, on this host. It was here and it was reachable: on 2026-11-15 `cargo test --workspace` with
# `DATABASE_URL` exported from ~/.config/concord/.env reached the production database and reported `error: 1 target failed`
# (measured afterwards: no row in users, projects, features, complaints, project_documents or api_tokens has a created_at later
# than 2026-11-14, so nothing was written). "Nothing was written" is luck, not a design. The fallback now refuses unless the
# operator says so explicitly.
if [ -z "${CONCORD_TEST_DSN:-}" ]; then
  if [ -n "${DATABASE_URL:-}" ]; then
    if [ "${CONCORD_ALLOW_PROD_TESTS:-}" = "1" ]; then
      echo "gate: CONCORD_ALLOW_PROD_TESTS=1 — the store tests will run against DATABASE_URL"
      export CONCORD_TEST_DSN="$DATABASE_URL"
    else
      echo "gate: no scratch database and no CONCORD_TEST_DSN; refusing to point the store tests at DATABASE_URL."
      echo "gate: set CONCORD_TEST_DSN to a migrated scratch database, or CONCORD_ALLOW_PROD_TESTS=1 to do it anyway."
      FAILED=1
      SKIP_UNIT=1
    fi
  fi
fi

# The frontend layer no suite had: `node --check` over every asset. Four scripts shipped syntactically broken for a day
# (`merge.js`, `ranking.js`, `requests-index.js`, `scout.js`) after a bulk edit removed a helper and left its closing brace —
# every page still answered 200, every suite still passed, because they assert HTML and these files were never parsed as a
# whole. A script that does not parse is a page whose controls do nothing, and it costs one second to know.
say "0. frontend syntax"
JS_BAD=0
if command -v node >/dev/null 2>&1; then
  for js in crates/web/assets/js/*.js; do
    node --check "$js" >/dev/null 2>&1 || { echo "  does not parse: $js"; JS_BAD=1; }
  done
  if [ "$JS_BAD" -eq 0 ]; then echo "js: all $(ls crates/web/assets/js/*.js | wc -l) assets parse"; else bad "a frontend asset does not parse"; fi
else
  echo "js: skipped (node not installed)"
fi

# Parsing is not the same as reaching a route. The port matches exactly, so a path with a trailing slash is a 404 -- and
# three call sites carried one for a day (`/similar/{kind}/`, `/documents/`, `/features/`), each wrapped in a `.catch` or a
# `getOrNull`, so the page looked fine and the feature was dead. See scripts/parity/api_paths.py.
if python3 scripts/parity/api_paths.py > /tmp/gate-api-paths.txt 2>&1; then
  grep "stray trailing" /tmp/gate-api-paths.txt | sed 's/^/js: /'
  grep "allowed:" /tmp/gate-api-paths.txt | sed 's/^/js: /' || true
else
  cat /tmp/gate-api-paths.txt
  bad "an API path ends in a stray slash (the server answers 404)"
fi

say "1. unit tests"
# `--no-fail-fast` on purpose: one red crate must not hide the four behind it, and the per-crate counts are printed because the number to
# distrust is the one that says everything ran.
UNIT_LOG="$SCRATCH/unit.log"
# `--locked` on both cargo calls. Without it a workspace whose lockfile no longer describes it passes the gate by silently
# rewriting the lock — which is exactly what happened when the seven estate crates were added to the workspace in `eb20faf` and
# `Cargo.lock` was left behind: the gate was green locally and in CI while the committed tree was not reproducible (`30590a6`
# committed the lock afterwards). A stale lock must fail here, not be repaired here.
if [ "${SKIP_UNIT:-0}" = "1" ]; then
  echo "unit: not run — there is no database the tests are allowed to write to"
else
  cargo test --workspace --no-fail-fast --locked >"$UNIT_LOG" 2>&1
  UNIT_STATUS=$?
  grep -E "^test result: (ok|FAILED)" "$UNIT_LOG" | sed 's/^/  /'
  if [ "$UNIT_STATUS" -ne 0 ]; then
    bad "unit tests"
    grep -E "^(error|failures:)" -A6 "$UNIT_LOG" | head -30
  else
    echo "unit: ok"
  fi
fi  # SKIP_UNIT

if [ "${SKIP_SMOKE:-0}" = "1" ] || [ "$CREATED_DB" != "1" ]; then
  echo "smoke: skipped"
else
  say "2. smoke — fresh database, the real binary, real HTTP"
  cargo build --release -p concord-cli --locked --quiet || bad "build"
  BIN="$HOME/.cache/cargo-target/release/concord-cli"
  [ -x "$BIN" ] || BIN="$ROOT/target/release/concord-cli"
  if [ ! -x "$BIN" ]; then
    bad "no concord-cli binary at $BIN"
  else
    echo "migrating the scratch database:"
    CONCORD_DB_URL="$CONCORD_TEST_DSN" "$BIN" migrate 2>&1 | tail -2 || bad "migrate"
    CONCORD_DB_URL="$CONCORD_TEST_DSN" CONCORD_BASE_URL="http://127.0.0.1:$PORT" \
      "$BIN" serve --addr "127.0.0.1:$PORT" >"$SCRATCH/server.log" 2>&1 &
    SERVER_PID=$!
    for _ in $(seq 1 40); do
      curl -fsS -m 2 "http://127.0.0.1:$PORT/api/v1/version" >/dev/null 2>&1 && break
      sleep 0.5
    done
    VERSION=$(curl -fsS -m 5 "http://127.0.0.1:$PORT/api/v1/version" 2>/dev/null)
    if [ -z "$VERSION" ]; then
      bad "the binary did not answer /api/v1/version"
      tail -15 "$SCRATCH/server.log"
    else
      echo "version: $VERSION"
      # Only routes that exist in this tree (`/healthz`; there is no sitemap route). A route that 404s is not mounted, and that is the
      # defect this layer exists for; 401/403 are mounted routes correctly refusing a stranger.
      ROUTES="/ /projects /search /stats /scout /finder /taxonomy /curation /contribute /projects/new /api/v1/projects /api/v1/version /healthz"
      MISSING=0
      for route in $ROUTES; do
        code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' "http://127.0.0.1:$PORT$route")
        case "$code" in
          200|401|403) ;;
          *) printf '  %-22s %s\n' "$route" "$code"; MISSING=$((MISSING+1)) ;;
        esac
      done
      [ "$MISSING" -eq 0 ] && echo "routes: all $(echo $ROUTES | wc -w) answered" || bad "$MISSING route(s) did not answer"
    fi
  fi
fi

if [ -n "$LIVE" ]; then
  say "3. live — probes against $LIVE"
  CANDIDATE="$LIVE" python3 scripts/parity/probe_pages.py 2>&1 | tail -1 || bad "probe_pages"
  CANDIDATE="$LIVE" python3 scripts/parity/probe_project_segments.py 2>&1 | tail -1 || bad "probe_project_segments"
else
  echo "live: not run (--live <url> to include the probes)"
fi

say "result"
if [ "$FAILED" -eq 0 ]; then echo "gate: PASS"; else echo "gate: FAIL"; fi
exit "$FAILED"
