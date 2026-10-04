BINARY  := concord
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := ./cmd/concord
DEPLOY_DIR ?= $(HOME)/concord-deploy
DEPLOY_HOST ?= localhost
DEPLOY_PORT ?= 8006

.PHONY: build test fmt vet verify run clean tidy deploy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) $(PKG)

test:
	go test -timeout 300s ./...

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
E2E_PYTEST ?= python3 -m pytest -p no:cacheprovider
e2e: build
	$(E2E_PYTEST) tests/e2e/harness_selftest_test.py -q
	$(E2E_PYTEST) tests/e2e/e2e_test.py -q
	$(E2E_PYTEST) tests/e2e/finder_e2e.py -q
	$(E2E_PYTEST) tests/e2e/project_panels_e2e.py -q
	$(E2E_PYTEST) tests/e2e/scout_e2e.py -q
	$(E2E_PYTEST) tests/e2e/consensus_e2e.py -q
	$(E2E_PYTEST) tests/e2e/feeds_e2e.py -q

# The mutation gates. Each rewrites one source file per mutant and asserts the
# suite goes red, so a green run means every mutant was killed. A SURVIVED entry
# is either missing coverage or a defective mutant -- see the skill reference on
# wiring gaps that pass tests.
gates:
	python3 internal/mutate.py internal/finder/finder.go ./internal/finder/ internal/finder/finder_test.go internal/finder/mutants.json
	python3 internal/mutate.py internal/httpapi/finder.go ./internal/httpapi/ internal/httpapi/finder_test.go internal/httpapi/finder_mutants.json
	python3 internal/mutate.py internal/store/capabilities.go ./internal/store/ internal/store/capabilities_test.go internal/store/capabilities_mutants.json
	python3 internal/mutate.py internal/store/field_reports.go ./internal/store/ internal/store/field_reports_test.go internal/store/field_reports_mutants.json
	python3 internal/mutate.py internal/store/solutions.go ./internal/store/ internal/store/solutions_test.go,internal/store/solutions_count_test.go internal/store/solutions_mutants.json
	python3 internal/mutate.py internal/store/arenas.go ./internal/store/ internal/store/arenas_test.go internal/store/arenas_mutants.json
	python3 internal/mutate.py internal/store/consensus.go ./internal/store/ internal/store/consensus_solutions_test.go,internal/store/consensus_quorum_test.go internal/store/consensus_mutants.json
	python3 internal/mutate.py internal/store/audit.go ./internal/store/ internal/store/audit_test.go internal/store/audit_mutants.json
	python3 internal/mutate.py internal/governance/governance.go ./internal/governance/ internal/governance/governance_test.go internal/governance/consensus_mutants.json

fmt:
	gofmt -l -w .

vet:
	go vet ./...

verify: vet test build

run:
	CGO_ENABLED=0 go run $(PKG)

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
