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

release:
	CONCORD_VERSION="concord-rs 0.0.0+$$(git rev-parse --short HEAD)" cargo build --release -p concord-cli

# The deploy is four commands that have gone wrong in four different ways; they live here so the next person does not rediscover them.
# `concord.service` (the Go rollback) stays disabled at boot; `concord-rs.service` is the one that serves.
deploy: release
	scp -q $$HOME/.cache/cargo-target/release/concord-cli thinkcentre:/home/alvaro/concord-deploy/concord-rs.new
	ssh thinkcentre 'chmod 755 ~/concord-deploy/concord-rs.new && systemctl --user stop concord-rs.service \
	  && mv ~/concord-deploy/concord-rs.new ~/concord-deploy/concord-rs \
	  && systemctl --user start concord-rs.service && sleep 4 \
	  && curl -s -m 8 https://concord.polarisocial.xyz/api/v1/version'
