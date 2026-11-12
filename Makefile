BINARY  := concord
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := ./cmd/concord
DEPLOY_DIR ?= $(HOME)/concord-deploy
DEPLOY_HOST ?= localhost
DEPLOY_PORT ?= 8006

.PHONY: build test fmt vet verify run clean tidy deploy admin-build deploy-admin

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) $(PKG)

# -timeout is a WALL-CLOCK budget for each package's test binary, and `go test ./...`
# runs packages in parallel. httpapi alone takes ~246s on thinkcentre (measured
# 2026-11-11, after R12-R15 added the stats and forge-event suites); under the
# parallel load of the full run it crossed the old 300s and the gate failed with
# "test timed out after 4m0s" while every test in it passed. A gate that fails on
# wall-clock is a flaky gate, so the budget is 3x the slowest package.
test:
	go test -timeout 900s ./...

# Playwright end-to-end tests.
#
# Needs: pip install --user pygments packaging pytest playwright && playwright install chromium
#
# THREE separate pytest processes, for two different reasons, and it is worth
# keeping them straight:
#
#  1. The two browser suites cannot share a process: Playwright's sync API
#     refuses a second sync_playwright() context in one interpreter ("It looks
#     like you are using Playwright Sync API inside the asyncio loop"). Proved
#     with a two-line script, not assumed.
#  2. They no longer need a pkill between them. There used to be one, because
#     each fixture killed any process matching 'bin/concord' to clear a leaked
#     server. That pattern also matched the shell running THIS recipe -- whose
#     argv contains the binary path -- so `make e2e` SIGTERM'd itself on the
#     recipe's first line and never reached pytest. Not once. The suites now
#     own distinct ports (8421, 8422) and reap only a server recorded in a
#     stable pidfile, so no kill is needed and no invocation is self-destructive.
#
#  3. Each line is a separate process anyway, so a suite that aborts on a port
#     clash cannot take the ones after it with it. scout_e2e takes 8423.
# The interpreter the UI mutation gates hand to pytest. They run pytest as a
# subprocess, so "which python3" has to be answerable separately from the pytest
# flags: with a `python3` that cannot import pytest, a gate gets a non-zero exit code
# for every mutant and reports seven kills it did not earn.
#
# RESOLVED, NOT ASSUMED. The default used to be bare `python3`, and on a machine where
# PATH's python3 is a venv without pytest that answered "No module named pytest" for the
# harness selftest -- while `make gates` (which only needs the interpreter, never pytest
# itself) stayed green. A red suite harness and a green mutation gate sat beside each
# other for a day because each was started with a different interpreter, and every
# `E2E_EXIT=0` in that day was the exit status of `tail`, not of make.
#
# So the first candidate that can actually `import pytest` wins, and if none can, the
# e2e-python check below fails with the candidate list instead of running 27 suites
# that all answer "No module named pytest" and calling the survivors a pass.
E2E_PYTHON_CANDIDATES ?= python3 /usr/bin/python3 /usr/bin/python
E2E_PYTHON ?= $(shell for p in $(E2E_PYTHON_CANDIDATES); do \
	$$p -c 'import pytest' >/dev/null 2>&1 && { echo $$p; break; }; done)
E2E_PYTEST ?= $(E2E_PYTHON) -m pytest -p no:cacheprovider

# Every target that needs pytest as a subprocess depends on this. Order-only is not
# enough: an empty E2E_PYTHON must stop the recipe before it reports anything.
.PHONY: e2e-python
e2e-python:
	@test -n "$(E2E_PYTHON)" || { \
		echo "no interpreter of [$(E2E_PYTHON_CANDIDATES)] can import pytest." >&2; \
		echo "Install it (pip install --user pytest playwright) or pass E2E_PYTHON=/path/to/python3." >&2; \
		exit 1; }
	@echo "e2e interpreter: $(E2E_PYTHON) ($$($(E2E_PYTHON) -V 2>&1))"

e2e: build e2e-python
	$(E2E_PYTEST) tests/e2e/harness_selftest_test.py -q
	$(E2E_PYTEST) tests/e2e/e2e_test.py -q
	$(E2E_PYTEST) tests/e2e/finder_e2e.py -q
	$(E2E_PYTEST) tests/e2e/project_panels_e2e.py -q
	$(E2E_PYTEST) tests/e2e/scout_e2e.py -q
	$(E2E_PYTEST) tests/e2e/consensus_e2e.py -q
	$(E2E_PYTEST) tests/e2e/feeds_e2e.py -q
	$(E2E_PYTEST) tests/e2e/curation_e2e.py -q
	$(E2E_PYTEST) tests/e2e/audit_undo_e2e.py -q
	$(E2E_PYTEST) tests/e2e/audit_e2e.py -q
	# M3's outbound links: the standings page's per-row Moderate link (server-decided standing,
	# no probe) and the pairwise page's per-card idea link. `gates` cannot see either -- the rows
	# are rendered client-side from an attribute, and the hrefs are built in JavaScript.
	$(E2E_PYTEST) tests/e2e/rank_links_e2e.py -q
	# R16/R17/R18's pages: the roadmap, one milestone, the board's milestone filter and
	# the feature page's Planning panel. Every claim in it is a DOM claim -- the routes
	# are covered in Go, and a route that answers correctly while the page renders
	# nothing is the exact failure this repository keeps finding.
	$(E2E_PYTEST) tests/e2e/milestones_e2e.py -q
	$(E2E_PYTEST) tests/e2e/feature_e2e.py -q
	# §6.2's themes: the propose form, the consent state, and the expiry a reader has to be
	# able to see. Its own suite because it needs a signed-in page and a charter of its own.
	$(E2E_PYTEST) tests/e2e/settings_themes_e2e.py -q
	$(E2E_PYTEST) tests/e2e/requests_e2e.py -q
	$(E2E_PYTEST) tests/e2e/lists_e2e.py -q
	$(E2E_PYTEST) tests/e2e/board_e2e.py -q
	$(E2E_PYTEST) tests/e2e/settings_e2e.py -q
	$(E2E_PYTEST) tests/e2e/duplicates_e2e.py -q
	$(E2E_PYTEST) tests/e2e/taxonomy_e2e.py -q
	$(E2E_PYTEST) tests/e2e/documents_e2e.py -q
	$(E2E_PYTEST) tests/e2e/criteria_e2e.py -q
	$(E2E_PYTEST) tests/e2e/merge_e2e.py -q
	$(E2E_PYTEST) tests/e2e/readers_e2e.py -q
	$(E2E_PYTEST) tests/e2e/guest_e2e.py -q
	# §5's arena loop: the pair to judge, the log, the verdict and the repair, all of
	# them on the alternatives panel. Its own suite because it needs an arena with two
	# competitors owned by somebody else (a self-vote is refused, correctly).
	$(E2E_PYTEST) tests/e2e/arena_e2e.py -q
	# §4.5.7's work-item panel: the distinctions (sized/unsized, claimed/free,
	# validation/implementation, draft absent), the claim, the refusal, and the label.
	$(E2E_PYTEST) tests/e2e/workitems_e2e.py -q
	# The reader's own inbox (docs/specs/notification-spec.md): the badge is in the FIRST PAINT,
	# looking marks nothing read, and pressing Mark read clears both the card and the header.
	$(E2E_PYTEST) tests/e2e/notifications_e2e.py -q
	# The audit VIEWER's mutants. Not part of `make gates` because it rebuilds
	# the binary and runs the browser suite seven times, which is a different
	# cost from the Go mutants: `gates` is a fast inner loop and this is not.
	$(E2E_PYTHON) tests/e2e/audit_ui_mutants.py
	$(E2E_PYTHON) tests/e2e/consensus_ui_mutants.py
	# §M1's complaint verdicts on a complaint card. The Go reachability gate cannot see
	# these: it asserts project.js MENTIONS the route, and a swapped verb posts Reject
	# to /validate with every Go test green.
	$(E2E_PYTHON) tests/e2e/project_ui_mutants.py
	$(E2E_PYTHON) tests/e2e/arena_ui_mutants.py
	$(E2E_PYTHON) tests/e2e/guest_ui_mutants.py
	# §6.2's themes and tags. The Go gates cannot see a 202 reported as "ratified" or a
	# remove button posting to the add endpoint: both answer 200.
	$(E2E_PYTHON) tests/e2e/settings_ui_mutants.py
	$(E2E_PYTHON) tests/e2e/feature_ui_mutants.py
	# §4.5.7's work-item panel. The reader gate asserts project.js MENTIONS each
	# route; these mutants break the PAIRING between a card and its call, which is
	# the defect class that answers 200 either way.
	$(E2E_PYTHON) tests/e2e/workitems_ui_mutants.py
	# The inbox: a control that renders and does nothing, a read that drops the credential, and
	# an empty state that talks about the feature instead of the reader.
	$(E2E_PYTHON) tests/e2e/notifications_ui_mutants.py

# The mutation gates. Each rewrites one source file per mutant and asserts the
# suite goes red, so a green run means every mutant was killed. A SURVIVED entry
# is either missing coverage or a defective mutant -- see the skill reference on
# wiring gaps that pass tests.
gates:
	python3 internal/mutate.py internal/finder/finder.go ./internal/finder/ internal/finder/finder_test.go internal/finder/mutants.json
	# R11's contribution feed: its own arm because the feed's four dimensions are new keys in
	# the same package, and the finder arm above runs finder_test.go, which never builds a work
	# item. Pointing it here would arm tests that cannot see the code being mutated.
	python3 internal/mutate.py internal/finder/contribute.go ./internal/finder/ internal/finder/contribute_test.go internal/finder/contribute_mutants.json
	python3 internal/mutate.py internal/httpapi/contribute.go ./internal/httpapi/ internal/httpapi/contribute_test.go internal/httpapi/contribute_mutants.json
	python3 internal/mutate.py internal/httpapi/finder.go ./internal/httpapi/ internal/httpapi/finder_test.go internal/httpapi/finder_mutants.json
	python3 internal/mutate.py internal/store/capabilities.go ./internal/store/ internal/store/capabilities_test.go internal/store/capabilities_mutants.json
	python3 internal/mutate.py internal/store/themes.go ./internal/store/ internal/store/themes_test.go internal/store/themes_mutants.json
	python3 internal/mutate.py internal/store/feature_tags.go ./internal/store/ internal/store/feature_tags_test.go internal/store/feature_tags_mutants.json
	python3 internal/mutate.py internal/store/features.go ./internal/store/ internal/store/themes_test.go,internal/store/complaint_links_test.go internal/store/features_mutants.json
	# §4.5.7's labels, the claim guard and the ordering. Its own arm because the rules
	# live in workitems.go and workitems_test.go is the only file that reaches them --
	# pointing the features arm here would arm tests that never call this code, which
	# reads as missing coverage and is a wiring problem.
	python3 internal/mutate.py internal/store/workitems.go ./internal/store/ internal/store/workitems_test.go internal/store/workitems_mutants.json
	# The charter's own bounds: a lifetime that makes every theme born expired is refused here,
	# and an unproven refusal is decoration.
	python3 internal/mutate.py internal/store/store.go ./internal/store/ internal/store/themes_test.go internal/store/store_mutants.json
	python3 internal/mutate.py internal/store/field_reports.go ./internal/store/ internal/store/field_reports_test.go internal/store/field_reports_mutants.json
	# STAT-3.4's four rules -- the span floor, the rate's divisor, the visibility filter and
	# the ordering. The floor is the one that carries the milestone: with a daily timer, two
	# snapshots one day apart turn a single shipped feature into "seven per week".
	python3 internal/mutate.py internal/store/rising.go ./internal/store/ internal/store/rising_test.go internal/store/rising_mutants.json
	# The series' own rules, which live in the writer rather than the reader: organic only
	# (or the slope charts an import script), correct-not-append on a second run of the same
	# day, and a zero recorded for a project with no work yet (or "absent" means two things).
	python3 internal/mutate.py internal/store/projectstats.go ./internal/store/ internal/store/rising_test.go internal/store/projectstats_mutants.json
	python3 internal/mutate.py internal/store/solutions.go ./internal/store/ internal/store/solutions_test.go,internal/store/solutions_count_test.go internal/store/solutions_mutants.json
	python3 internal/mutate.py internal/store/arenas.go ./internal/store/ internal/store/arenas_test.go internal/store/arenas_mutants.json
	# §30's weight rule lives in internal/ranking/arena.go, and its own arm is the only
	# place a mutant of THAT file can be killed: the store arm mutates the store's copy of
	# the call, so a mutation of the engine's rule would read as NOT APPLIED there.
	python3 internal/mutate.py internal/ranking/arena.go ./internal/ranking/ internal/ranking/arena_test.go internal/ranking/arenas_mutants.json
	# §30: the alternatives vote route is where the age gate was MISSING, and the other
	# four vote routes share one helper now -- so this arm holds the call, not the formula.
	python3 internal/mutate.py internal/httpapi/alternatives.go ./internal/httpapi/ internal/httpapi/alternatives_test.go internal/httpapi/alternatives_mutants.json
	python3 internal/mutate.py internal/store/consensus.go ./internal/store/ internal/store/consensus_solutions_test.go,internal/store/consensus_quorum_test.go,internal/store/override_window_test.go internal/store/consensus_mutants.json
	# §6.6's second window has its own gates, because its rules live in override.go and
	# the tests that hold them are in override_window_test.go / consensus_override_test.go
	# -- pointing another gate at those files would arm tests that never call the code it
	# mutates, which reads as missing coverage and is a wiring problem. (That is also why
	# the consensus.go gate above now names override_window_test.go: that file holds the
	# only assertion that an open objection is COUNTED, and without it a mutation dropping
	# the count survived the gate.)
	python3 internal/mutate.py internal/store/override.go ./internal/store/ internal/store/override_window_test.go,internal/store/consensus_override_test.go internal/store/override_mutants.json
	python3 internal/mutate.py internal/store/consensus.go ./internal/store/ internal/store/consensus_override_test.go internal/store/consensus_override_mutants.json
	python3 internal/mutate.py internal/httpapi/handlers.go ./internal/httpapi/ internal/httpapi/override_window_test.go internal/httpapi/override_mutants.json
	# §354's extra link at the edge: the role gate, the body guard, and the audit line. Its own
	# arm because handlers.go's arm runs override_window_test.go, which never reaches these.
	python3 internal/mutate.py internal/httpapi/complaint_links.go ./internal/httpapi/ internal/httpapi/complaint_links_test.go internal/httpapi/complaint_links_mutants.json
	# §27's inventory gate. It mutates the TEST file, because the declaration it checks lives there:
	# a wrong class on one entry, a missing reason, and a walker that stops finding callers.
	python3 internal/mutate.py internal/store/export_inventory_test.go ./internal/store/ internal/store/export_inventory_test.go internal/store/export_inventory_mutants.json
	# §28's field-report write half: the in-project proof, the outcome and successor pass-throughs,
	# the author, and the audit line.
	python3 internal/mutate.py internal/httpapi/field_reports_write.go ./internal/httpapi/ internal/httpapi/field_reports_write_test.go internal/httpapi/field_reports_write_mutants.json
	python3 internal/mutate.py internal/store/audit.go ./internal/store/ internal/store/audit_test.go internal/store/audit_mutants.json
	# §6.5's decision record: the outcome guard, the hold, the ranking the record
	# carries, and the date on it. The record's own gate because consensus_solutions.go
	# holds a whole pillar and one mutant list per file is how the others are kept
	# readable.
	python3 internal/mutate.py internal/store/consensus_solutions.go ./internal/store/ internal/store/consensus_decision_record_test.go internal/store/consensus_decision_record_mutants.json
	python3 internal/mutate.py internal/governance/governance.go ./internal/governance/ internal/governance/governance_test.go internal/governance/consensus_mutants.json

fmt:
	gofmt -l -w .

# Formatting, checked in `verify` rather than only rewritten by `fmt` above. `make fmt` fixes
# the tree, so nothing ever REPORTED a file that had drifted: three had (a missing final newline
# each, one of them from a pass that committed it), and a formatting diff is the one kind of
# churn that makes every later diff of that file noisy. A rewrite target with no check beside it
# is a control nobody runs.
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then \
		echo "gofmt would rewrite these files -- run 'make fmt':"; echo "$$out"; exit 1; \
	fi
	@echo "gofmt: clean"

vet:
	go vet ./...

# Documentation drift, in `verify` rather than only in a note, because the
# failure it catches is entirely invisible to the compiler and to every test: a
# document that tells a reader to implement from a spec two revisions back reads
# exactly like one that does not. KNOWN-ISSUES carried that finding for
# revisions without anyone acting on it, which is the strongest available
# argument that a finding only in a markdown list is a note, not a control.
docs-check:
	python3 scripts/check_spec_drift.py
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/check_doc_numbers.py
	# docs/IDEAS.md is rendered from seed/concord_ideas.json, and the renderer grew a
	# --check mode that nothing called: a document a reader opens and the rows the
	# instance holds could disagree with no gate saying so. It needs no token and no
	# database, so it belongs here rather than in the e2e half.
	python3 scripts/render_ideas.py --check

# E2E synchronisation. In `verify` because the pattern it bans is invisible to
# every other check: a sleep-then-read test passes on a fast machine and fails on
# a busy one, so it is green in CI and broken for whoever runs it locally. The
# finder suite had twenty of them and the recorded symptom was a flake that could
# not be reproduced in nine consecutive runs.
e2e-sync-check:
	python3 scripts/check_e2e_sync.py

verify: vet fmt-check test build docs-check e2e-sync-check

run:
	CGO_ENABLED=0 go run $(PKG)

# The two admin commands R9 adds, built and installed beside the server binary, plus their units.
#
# NOT part of `deploy`, deliberately: `deploy` restarts the server, and installing a timer that
# reaches out to three forges is a decision about the instance, not a side effect of shipping a
# build. The timer is installed DISABLED -- enabling it is printed, not performed, because an
# hourly job against shared infrastructure should be switched on by whoever accepts the rate
# limit it spends.
admin-build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/forgeseed ./cmd/forgeseed
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/healthsync ./cmd/healthsync
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/statsnapshot ./cmd/statsnapshot
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/proposalsweep ./cmd/proposalsweep

deploy-admin: admin-build
	@echo "Installing admin commands into $(DEPLOY_DIR)..."
	cp bin/forgeseed $(DEPLOY_DIR)/forgeseed
	cp bin/healthsync $(DEPLOY_DIR)/healthsync
	cp bin/statsnapshot $(DEPLOY_DIR)/statsnapshot
	cp bin/proposalsweep $(DEPLOY_DIR)/proposalsweep
	mkdir -p $(HOME)/.config/systemd/user
	cp deploy/concord-healthsync.service deploy/concord-healthsync.timer $(HOME)/.config/systemd/user/
	cp deploy/concord-statsnapshot.service deploy/concord-statsnapshot.timer $(HOME)/.config/systemd/user/
	cp deploy/concord-proposalsweep.service deploy/concord-proposalsweep.timer $(HOME)/.config/systemd/user/
	systemctl --user daemon-reload
	@# No `disable --now` here. This target INSTALLS units; it does not decide whether a job is
	@# running. Disabling on every install meant a redeploy silently switched off an hourly pass
	@# somebody had turned on deliberately, and the only symptom was data that quietly stopped
	@# arriving. A fresh install leaves them disabled by default, which is the intended state.
	@echo "Installed. Enable the hourly pass with:"
	@echo "  systemctl --user enable --now concord-healthsync.timer"
	@echo "Enable the daily snapshot (STAT-3) with:"
	@echo "  systemctl --user enable --now concord-statsnapshot.timer"
	@# proposalsweep is the one timer this target ENABLES. The other two are switched on by whoever
	@# accepts the rate limit they spend against shared forges; this one reads and writes only the
	@# local database, and a sweep that is installed but never enabled is a queue that never drains.
	systemctl --user enable --now concord-proposalsweep.timer
	@echo "proposalsweep enabled (daily, 03:20 UTC + up to 5m)."

# Q5: regenerate seed/forge_repos.json from the live catalog and the two forges. Verification of
# every URL happens inside the script -- a URL that does not answer 200 anonymously is written as a
# skip with the reason, because healthsync has no token on this host and would fail on it hourly.
# CONCORD_LIVE_DB overrides the database, which is otherwise the deployed instance's.
forge-seed:
	python3 scripts/build_forge_repos.py \
		--db "$${CONCORD_LIVE_DB:-$$HOME/.local/share/concord/concord.db}" \
		--out seed/forge_repos.json

tidy:
	go mod tidy

clean:
	rm -rf bin

deploy: build
	@# Refuse to deploy a tree with uncommitted changes. VERSION carries --dirty, so
	@# the running service would advertise a build nobody can name, and every
	@# verification pinned to a version string would be pinned to a commit that does
	@# not exist. Commit first, or pass ALLOW_DIRTY=1 if that is genuinely intended.
	@if git diff --quiet && git diff --cached --quiet && test -z "$$(git ls-files --others --exclude-standard)"; then \
		:; \
	elif [ "$$ALLOW_DIRTY" = "1" ]; then \
		echo "WARNING: deploying with uncommitted changes (ALLOW_DIRTY=1)"; \
	else \
		echo "ERROR: uncommitted changes in the working tree."; \
		echo "       Commit them, or re-run with ALLOW_DIRTY=1 to deploy anyway."; \
		git status --short; \
		exit 1; \
	fi
	@echo "Deploying $(BINARY) to $(DEPLOY_DIR)..."
	systemctl --user stop concord || true
	cp bin/$(BINARY) $(DEPLOY_DIR)/$(BINARY)
	@# statsnapshot RIDES ALONG. Its timer is enabled on the instance, it reads only the local
	@# database, and since 0033 it shares a schema with the server: a deploy that refreshed the
	@# server and not the writer would serve /api/v1/stats/rising from a table nothing fills --
	@# no error, an empty card, and a scheduler quietly running last month's code. forgeseed and
	@# healthsync stay in deploy-admin, because they reach out to three forges and enabling that
	@# is a decision about the instance rather than a side effect of shipping a build.
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/statsnapshot ./cmd/statsnapshot
	cp bin/statsnapshot $(DEPLOY_DIR)/statsnapshot
	systemctl --user start concord
	@echo "Verifying healthz on 127.0.0.1:$(DEPLOY_PORT)..."
	@sleep 5
	@for i in 1 2 3; do \
		if curl -sf http://127.0.0.1:$(DEPLOY_PORT)/api/v1/healthz 2>/dev/null | grep -q '"status": *"ok"'; then \
			echo "✓ healthz OK"; \
			echo "Deployed successfully."; \
			exit 0; \
		fi; \
		echo "  retry $$i..."; \
		sleep 3; \
	done; \
	echo "✗ healthz FAILED after retries" && exit 1
