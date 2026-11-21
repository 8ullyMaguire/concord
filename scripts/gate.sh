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
# `_test` is in the name on purpose: `tests/e2e/harness.py` refuses to seed a database whose name does not say it is a
# test database, and the page fixture seeds this one. The refusal is right; the name was the thing that was wrong.
DBNAME="gate_test_$$"
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
# The case this block did NOT cover, found on 2026-11-15 by running `make gate` on a host that had not sourced ~/.config/concord/.env:
# with NEITHER variable set, the refusal below never fired, `cargo test` ran the store suite with no DSN, and every test in
# `-p concord-db --test store` panicked at `CONCORD_TEST_DSN must point at a scratch PostgreSQL database … NotPresent` — six
# named failures and `gate: FAIL` over a missing environment variable. A gate that says FAIL when it means "I was not configured"
# teaches the operator to distrust it, which is the same disease as a gate that cannot fail. Name the two variables instead.
if [ -z "${CONCORD_TEST_DSN:-}" ] && [ -z "${DATABASE_URL:-}" ]; then
  echo "gate: neither CONCORD_TEST_DSN nor DATABASE_URL is set, so there is no scratch database to test against."
  echo "gate: source ~/.config/concord/.env (the gate creates and migrates its own scratch database from DATABASE_URL)."
  FAILED=1
  SKIP_UNIT=1
fi
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

# Parsing proves the asset loads, not that the page still keeps the credential where any script can read it. This executes the
# shipped session.js against a fake browser: a reloaded page must stay signed in without a bearer header, and no code path may
# put the token back into localStorage. Mutating `store` to write the key again reddens this arm (measured 2026-11-16).
if command -v node >/dev/null 2>&1; then
  if node scripts/parity/check_session_storage.js crates/web/assets/js/session.js >"$SCRATCH/session-storage.log" 2>&1; then
    tail -1 "$SCRATCH/session-storage.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/session-storage.log"
    bad "the session page persisted a token in localStorage (or lost the cookie-only session)"
  fi
fi

# Step 2b: the renderer has no HTML-parsing API. This executes the shipped dom.js and the first migrated page
# (notifications.js) against hostile prose; a text node or setAttribute regression reddens it. Mutating el() to write
# innerHTML was measured RED (`el() did not create exactly one text child`) and the restore GREEN (2026-11-17).
if command -v node >/dev/null 2>&1; then
  if node scripts/parity/check_dom_builder.js crates/web/assets/js/dom.js crates/web/assets/js/notifications.js >"$SCRATCH/dom-builder.log" 2>&1; then
    tail -1 "$SCRATCH/dom-builder.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-builder.log"
    bad "the DOM builder or notifications page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_list.js crates/web/assets/js/dom.js crates/web/assets/js/list.js >"$SCRATCH/dom-list.log" 2>&1; then
    tail -1 "$SCRATCH/dom-list.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-list.log"
    bad "the list page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_criteria.js crates/web/assets/js/dom.js crates/web/assets/js/criteria.js >"$SCRATCH/dom-criteria.log" 2>&1; then
    tail -1 "$SCRATCH/dom-criteria.log" | sed 's/^/js: /'
    # Count write/call syntax, not the word in a comment: the census is a ratchet on sinks, not on prose.
    echo "js: string sinks remaining in assets: $(grep -rhoE '\.innerHTML[[:space:]]*=|\.insertAdjacentHTML[[:space:]]*\(' crates/web/assets/js/*.js | wc -l)"
    echo "js: allowlisted markdown sink: $(grep -cE '\.innerHTML[[:space:]]*=' crates/web/assets/js/markdown.js)"
  else
    cat "$SCRATCH/dom-criteria.log"
    bad "the criteria page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_consensus.js crates/web/assets/js/dom.js crates/web/assets/js/consensus.js >"$SCRATCH/dom-consensus.log" 2>&1; then
    tail -1 "$SCRATCH/dom-consensus.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-consensus.log"
    bad "the consensus page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_complaint.js crates/web/assets/js/dom.js crates/web/assets/js/complaint.js >"$SCRATCH/dom-complaint.log" 2>&1; then
    tail -1 "$SCRATCH/dom-complaint.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-complaint.log"
    bad "the complaint page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_audit.js crates/web/assets/js/dom.js crates/web/assets/js/audit.js >"$SCRATCH/dom-audit.log" 2>&1; then
    tail -1 "$SCRATCH/dom-audit.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-audit.log"
    bad "the audit page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_comments.js crates/web/assets/js/dom.js crates/web/assets/js/comments.js >"$SCRATCH/dom-comments.log" 2>&1; then
    tail -1 "$SCRATCH/dom-comments.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-comments.log"
    bad "the comment thread regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_feature.js crates/web/assets/js/dom.js crates/web/assets/js/feature.js >"$SCRATCH/dom-feature.log" 2>&1; then
    tail -1 "$SCRATCH/dom-feature.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-feature.log"
    bad "the feature page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_projects.js crates/web/assets/js/dom.js crates/web/assets/js/projects.js >"$SCRATCH/dom-projects.log" 2>&1; then
    tail -1 "$SCRATCH/dom-projects.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-projects.log"
    bad "the projects page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_ranking.js crates/web/assets/js/dom.js crates/web/assets/js/ranking.js >"$SCRATCH/dom-ranking.log" 2>&1; then
    tail -1 "$SCRATCH/dom-ranking.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-ranking.log"
    bad "the ranking page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_requests.js crates/web/assets/js/dom.js crates/web/assets/js/requests.js >"$SCRATCH/dom-requests.log" 2>&1; then
    tail -1 "$SCRATCH/dom-requests.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-requests.log"
    bad "the request page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_requests_index.js crates/web/assets/js/dom.js crates/web/assets/js/requests-index.js >"$SCRATCH/dom-requests-index.log" 2>&1; then
    tail -1 "$SCRATCH/dom-requests-index.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-requests-index.log"
    bad "the request index regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_settings.js crates/web/assets/js/dom.js crates/web/assets/js/settings.js >"$SCRATCH/dom-settings.log" 2>&1; then
    tail -1 "$SCRATCH/dom-settings.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-settings.log"
    bad "the settings page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_taxonomy.js crates/web/assets/js/dom.js crates/web/assets/js/taxonomy.js >"$SCRATCH/dom-taxonomy.log" 2>&1; then
    tail -1 "$SCRATCH/dom-taxonomy.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-taxonomy.log"
    bad "the taxonomy page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_stats.js crates/web/assets/js/dom.js crates/web/assets/js/stats.js >"$SCRATCH/dom-stats.log" 2>&1; then
    tail -1 "$SCRATCH/dom-stats.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-stats.log"
    bad "the stats page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_finder.js crates/web/assets/js/dom.js crates/web/assets/js/finder.js >"$SCRATCH/dom-finder.log" 2>&1; then
    tail -1 "$SCRATCH/dom-finder.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-finder.log"
    bad "the finder page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_rank.js crates/web/assets/js/dom.js crates/web/assets/js/rank.js >"$SCRATCH/dom-rank.log" 2>&1; then
    tail -1 "$SCRATCH/dom-rank.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-rank.log"
    bad "the ranking page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_arena.js crates/web/assets/js/dom.js crates/web/assets/js/project-arena.js >"$SCRATCH/dom-arena.log" 2>&1; then
    tail -1 "$SCRATCH/dom-arena.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-arena.log"
    bad "the arena page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_documents.js crates/web/assets/js >"$SCRATCH/dom-documents.log" 2>&1; then
    tail -1 "$SCRATCH/dom-documents.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-documents.log"
    bad "the documents page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_board.js crates/web/assets/js/dom.js crates/web/assets/js/board.js >"$SCRATCH/dom-board.log" 2>&1; then
    tail -1 "$SCRATCH/dom-board.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-board.log"
    bad "the kanban board regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_search.js crates/web/assets/js/dom.js crates/web/assets/js/search.js >"$SCRATCH/dom-search.log" 2>&1; then
    tail -1 "$SCRATCH/dom-search.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-search.log"
    bad "the search page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_new_project.js crates/web/assets/js/dom.js crates/web/assets/js/new-project.js >"$SCRATCH/dom-new-project.log" 2>&1; then
    tail -1 "$SCRATCH/dom-new-project.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-new-project.log"
    bad "the new-project page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_milestones.js crates/web/assets/js/dom.js crates/web/assets/js/milestones.js >"$SCRATCH/dom-milestones.log" 2>&1; then
    tail -1 "$SCRATCH/dom-milestones.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-milestones.log"
    bad "the milestones pages regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_project_writes.js crates/web/assets/js/dom.js crates/web/assets/js/project-writes.js >"$SCRATCH/dom-project-writes.log" 2>&1; then
    tail -1 "$SCRATCH/dom-project-writes.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-project-writes.log"
    bad "the project write helpers regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_lists.js crates/web/assets/js/dom.js crates/web/assets/js/lists.js >"$SCRATCH/dom-lists.log" 2>&1; then
    tail -1 "$SCRATCH/dom-lists.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-lists.log"
    bad "the lists page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_scout_merge.js crates/web/assets/js/dom.js crates/web/assets/js/scout.js crates/web/assets/js/merge.js >"$SCRATCH/dom-scout-merge.log" 2>&1; then
    tail -1 "$SCRATCH/dom-scout-merge.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-scout-merge.log"
    bad "the scout or merge page regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_curation.js crates/web/assets/js/dom.js crates/web/assets/js/curation.js >"$SCRATCH/dom-curation.log" 2>&1; then
    tail -1 "$SCRATCH/dom-curation.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-curation.log"
    bad "the curation queue regained an HTML string sink"
  fi
  if node scripts/parity/check_dom_project.js crates/web/assets/js/dom.js crates/web/assets/js/skeleton.js crates/web/assets/js/project-render.js crates/web/assets/js/project.js >"$SCRATCH/dom-project.log" 2>&1; then
    tail -1 "$SCRATCH/dom-project.log" | sed 's/^/js: /'
  else
    cat "$SCRATCH/dom-project.log"
    bad "the project page's outer states regained an HTML string sink"
  fi
fi

# The domain crate owns product rules and must never grow a database dependency: a rule that needs SQL is a rule the
# store has not extracted yet.
if [ "$(cargo tree -p concord-domain 2>/dev/null | grep -c sqlx)" = "0" ]; then
  echo "domain: 0 sqlx deps"
else
  bad "concord-domain gained a sqlx dependency"
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


# A collection handler that takes no page parameter can answer with an entire table. The 11.7 MB documents list was
# one of those; the shared query is now structural, not a convention someone remembers.
if python3 scripts/parity/collection_pages.py > /tmp/gate-collection-pages.txt 2>&1; then
  grep "collection_pages:" /tmp/gate-collection-pages.txt | sed 's/^/api: /'
else
  cat /tmp/gate-collection-pages.txt
  bad "a collection handler does not take the shared page query"
fi

# A vendored crate nobody imports is a copy that will drift. The roadmap's corpse check is structural, not a one-time
# cleanup: if somebody vendors another crate and forgets to use it, the gate fails.
if python3 scripts/parity/vendor_corpses.py > /tmp/gate-vendor-corpses.txt 2>&1; then
  grep "corpse check:" /tmp/gate-vendor-corpses.txt | sed 's/^/vendor: /'
else
  cat /tmp/gate-vendor-corpses.txt
  bad "a vendored crate has no importer"
fi

# A route with no declared policy is a route nobody decided about: authentication used to be a line inside each
# handler (122 of them), so a new route without it compiled, booted, answered, and nothing failed. The check is a
# unit test (`crates/app/src/policy.rs` parses `router()` out of its own source with `include_str!`), so it is
# enforced by the unit layer below rather than by a second script here — one authority, not two.

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
    # `CONCORD_RATE_LOGIN=3` for the same reason the scratch database is named `_test`: the budget probe has to be told
    # what the server's budget is, and proving a 429 must not mean flooding a real one. Three is enough to show the
    # fourth request refused and too few to matter to any other probe in this layer.
    CONCORD_DB_URL="$CONCORD_TEST_DSN" CONCORD_BASE_URL="http://127.0.0.1:$PORT" CONCORD_RATE_LOGIN=3 \
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

      # 3. the page layer: does each document exist AND carry the mount its script fills? `probe_pages` needs the project
      # whose tabs it walks, so the fixture is seeded into the scratch database the server just opened — that is the
      # whole reason this probe belongs here and not in the live layer (a live URL reads production, and seeding that is
      # what the harness refuses to do).
      if CONCORD_E2E_DSN="$CONCORD_TEST_DSN" python3 scripts/parity/probe_fixture.py >"$SCRATCH/fixture.log" 2>&1; then
        tail -1 "$SCRATCH/fixture.log" | sed 's/^/  /'
        if CANDIDATE="http://127.0.0.1:$PORT" CONCORD_E2E_DSN="$CONCORD_TEST_DSN" \
             python3 scripts/parity/probe_pages.py >"$SCRATCH/probe-pages.log" 2>&1; then
          tail -1 "$SCRATCH/probe-pages.log" | sed 's/^/pages: /'
        else
          cat "$SCRATCH/probe-pages.log"
          bad "probe_pages (a document is missing its mount or its title)"
        fi
      else
        cat "$SCRATCH/fixture.log"
        bad "the page fixture could not be seeded"
      fi

      # 4. the budget: is a login loop refused, and does the refusal say when to come back? `--budget 3` must match the
      # `CONCORD_RATE_LOGIN=3` above; the probe checks the server's own X-RateLimit-Limit against it, so a mismatch is a
      # failure rather than a silent pass.
      if python3 scripts/parity/probe_rate_limit.py --candidate "http://127.0.0.1:$PORT" --budget 3 \
           >"$SCRATCH/rate.log" 2>&1; then
        grep -E '429|second client|api read' "$SCRATCH/rate.log" | sed 's/^/  /'
        tail -1 "$SCRATCH/rate.log" | sed 's/^/rate: /'
      else
        tail -12 "$SCRATCH/rate.log"
        bad "probe_rate_limit (a login loop was not refused, or the refusal is unusable)"
      fi
    fi
  fi
fi

if [ -n "$LIVE" ]; then
  say "3. live — probes against $LIVE"
  run_probe() {
    local name=$1; shift
    local out status
    out=$("$@" 2>&1); status=$?
    echo "  $(echo "$out" | tail -1)"
    [ "$status" -eq 0 ] || bad "$name"
  }
  # The browser probes need playwright, which lives in the e2e venv and not in the system python: `probe_requests` failed with
  # `ModuleNotFoundError: No module named 'playwright'` on the first full pass of this layer, while the suites beside it passed.
  PY=python3
  [ -x "$HOME/.venvs/concord-e2e/bin/python" ] && PY="$HOME/.venvs/concord-e2e/bin/python"
  # probe_pages is NOT here, and the reason is structural: it seeds a hostile-named project and asserts the page it
  # lands on, so the instance under test must read the database the probe writes to. A live URL reads the production
  # database, seeding that is what `refuse_a_non_test_database` exists to stop, and pointing the probe's DSN at a
  # scratch database makes it assert a page the live server cannot serve. It runs in the smoke layer below, against
  # the binary and database the gate itself owns.
  CANDIDATE="$LIVE" run_probe probe_project_segments python3 scripts/parity/probe_project_segments.py
  CANDIDATE="$LIVE" run_probe probe_requests "$PY" scripts/parity/probe_requests.py
  # The one layer that asks the policy table's own question of the running instance: does a route declared as refusing
  # strangers actually refuse one, and does a route declared public still answer? A table nobody walks is a table that
  # describes the code's shape rather than the deployment's behaviour, which is how step 1 nearly 401'd the project page.
  run_probe anonymous_walk python3 scripts/parity/anonymous_walk.py --candidate "$LIVE"

  # The suites the gate never ran, which is why four of them were found dead on arrival (they had been erroring in setup
  # against SQLite since cutover and nothing that called itself "the gate" ever executed them). Each starts its own candidate
  # binary against its own scratch database, so they need the binary path and the harness DSN -- not the live URL.
  if [ -n "${CONCORD_E2E_CANDIDATE:-}" ] && [ -x "${CONCORD_E2E_CANDIDATE:-}" ]; then
    for suite in project_panels_e2e feature_e2e board_e2e documents_e2e requests_e2e consensus_e2e \
                 ai_declaration_e2e duplicates_e2e lifecycle_e2e milestones_e2e new_project_e2e rank_links_e2e audit_e2e audit_undo_e2e arena_e2e feeds_e2e settings_themes_e2e workitems_e2e notifications_e2e guest_e2e taxonomy_e2e merge_e2e curation_e2e lists_e2e readers_e2e settings_e2e criteria_e2e finder_e2e scout_e2e site_review_e2e signed_in_visual_e2e a11y_axe_e2e settings_fields_e2e taxonomy_clarity_e2e board_list_nav_e2e header_consent_e2e; do
      out=$(timeout 900 scripts/parity/e2e_candidate.sh "tests/e2e/$suite.py" -q 2>&1); status=$?
      echo "  $(echo "$out" | tail -1)"
      [ "$status" -eq 0 ] || bad "$suite"
    done
    out=$(timeout 300 "$PY" scripts/parity/probe_claim_button.py 2>&1); status=$?
    echo "  $(echo "$out" | tail -1)"
    [ "$status" -eq 0 ] || bad "probe_claim_button"
  else
    echo "  suites: skipped — CONCORD_E2E_CANDIDATE is not set to an executable"
  fi
else
  echo "live: not run (--live <url> to include the probes)"
fi

say "result"
if [ "$FAILED" -eq 0 ]; then echo "gate: PASS"; else echo "gate: FAIL"; fi
exit "$FAILED"
