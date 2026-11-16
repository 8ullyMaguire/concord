# The gate, so that running it is one word rather than a sequence remembered from the last session.
#
# `make gate` runs unit tests and a smoke test against a scratch database. `make gate-live` adds the probes against the live instance.
# Both call `scripts/gate.sh`, which is the same thing CI runs — one gate, three places to invoke it, no second definition to drift.
.PHONY: gate gate-live gate-unit build deploy release smoke help

help:
	@echo "gate        unit tests + smoke against a scratch database (what CI runs)"
	@echo "gate-live   the above, then probes against https://concord.polarisocial.xyz"
	@echo "gate-unit   unit tests only (no Postgres needed)"
	@echo "push        push to both hosts, then dispatch the GitHub gate (a plain push creates no run)"
	@echo "release     release build of the deployed binary"
	@echo "deploy      release build, install to ~/concord-deploy, restart the unit"

gate:
	@scripts/gate.sh

gate-live:
	@scripts/gate.sh --live https://concord.polarisocial.xyz

gate-unit:
	@SKIP_SMOKE=1 scripts/gate.sh

# `git push` to this repository does not create a workflow run; `workflow_dispatch` does. Three candidate causes were measured and
# ruled out on 2026-11-15 (branch glob, default branch, Actions enabled) — see the header of the script. So the push path asks.
push:
	@scripts/push-and-gate.sh $(if $(B),$(B),$$(git rev-parse --abbrev-ref HEAD))

# Where cargo actually put it. `make deploy` used to scp `$$HOME/.cache/cargo-target/release/concord-cli`, a path that has not
# existed since the target directory moved into the tree — so the recipe either failed or, with a stale file left there, shipped an
# older binary under the new commit's name. Ask cargo where the binary is; honour CARGO_TARGET_DIR when it is set.
TARGET_DIR := $(shell cargo metadata --format-version 1 --no-deps 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["target_directory"])' 2>/dev/null)
BIN := $(if $(TARGET_DIR),$(TARGET_DIR),./target)/release/concord-cli

release:
	CONCORD_VERSION="concord-rs 0.0.0+$$(git rev-parse --short HEAD)+vendor" cargo build --release -p concord-cli --locked

# The deploy is four commands that have gone wrong in four different ways; they live here so the next person does not rediscover them.
# `concord.service` (the Go rollback) stays disabled at boot; `concord-rs.service` is the one that serves.
# `scp thinkcentre:` FROM thinkcentre goes out to sshd and back, and under load sshd drops the self-connection:
# `scp: Connection closed` with the binary already built, which reads as a broken deploy. On the host the copy is a
# `cp` and the restart is local; anywhere else it is the ssh path. Decided here rather than in the caller's head.
# The host calls itself `M720q` in `hostname` and `thinkcentre` in ssh config; both mean "this is the machine".
ON_TARGET := $(shell hostname | grep -qiE '^(m720q|thinkcentre)$$' && echo yes || echo no)
DEPLOY_DIR := $(if $(filter yes,$(ON_TARGET)),$(HOME)/concord-deploy,/home/alvaro/concord-deploy)
deploy: release
	@test -x "$(BIN)" || { echo "deploy: $(BIN) does not exist; run \`make release\` first"; exit 1; }
ifeq ($(ON_TARGET),yes)
	@echo "deploy: on the target host, copying $(BIN) to $(DEPLOY_DIR)/concord-rs.new"
	cp "$(BIN)" "$(DEPLOY_DIR)/concord-rs.new" && chmod 755 "$(DEPLOY_DIR)/concord-rs.new"
	@systemctl --user stop concord-rs.service && mv "$(DEPLOY_DIR)/concord-rs.new" "$(DEPLOY_DIR)/concord-rs" \
	  && systemctl --user start concord-rs.service && sleep 4 \
	  && curl -s -m 8 https://concord.polarisocial.xyz/api/v1/version
else
	scp -q "$(BIN)" thinkcentre:/home/alvaro/concord-deploy/concord-rs.new
	ssh thinkcentre 'chmod 755 ~/concord-deploy/concord-rs.new && systemctl --user stop concord-rs.service \
	  && mv ~/concord-deploy/concord-rs.new ~/concord-deploy/concord-rs \
	  && systemctl --user start concord-rs.service && sleep 4 \
	  && curl -s -m 8 https://concord.polarisocial.xyz/api/v1/version'
endif
