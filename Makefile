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
