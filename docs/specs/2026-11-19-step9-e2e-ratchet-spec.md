# Step 9 — the suites nobody runs

Date: 2026-11-19
Status: spec accepted; plan in `.hermes/plans/2026-11-19_050000-step9-e2e-ratchet.md`.

## Problem

Measured at `c7870ef`:
- `tests/e2e/*_e2e.py`: 29 suites.
- `scripts/gate.sh` names 8 in its live loop (`project_panels`, `feature`, `board`, `documents`, `requests`,
  `consensus`, `ai_declaration`, `duplicates`).
- `tests/e2e/harness_selftest_test.py:150` records `RECORDED_UNRUN = 21`, and its own docstring says four suites were
  found dead on arrival because nothing ran them.
- `~/.venvs/concord-e2e/bin/python` exists and imports `pytest` and `playwright`; the earlier "pytest/playwright
  absent" blocker no longer holds.
- Candidate binary exists at `~/.cargo-target/concord/debug/concord-cli`.

The ratchet makes the unwired count honest but does not make the suites run. A refusal path added in steps 1, 2 and 5
can only be proved from outside the process.

## Goals

1. Wire the 21 unwired suites into `scripts/gate.sh`'s live loop, one per commit, in dependency order.
2. Drop `RECORDED_UNRUN` by exactly one in each commit that adds a suite, and record the suite name in the gate log.
3. Fix any suite that fails when actually run; a suite that cannot run for an external reason is recorded in Blocked
   with the exact unblock and owner, and the ratchet still drops because the decision is recorded.
4. End with 29 suites named in the gate, `RECORDED_UNRUN = 0`, and a full gate proving `suites: 29 · … passed`.

## Non-goals

- Rewriting the e2e harness or the 8 already-wired suites.
- Changing production behaviour to make a suite pass. A suite that fails against the built binary reveals a real
  defect; fix the defect and prove it with that suite.
- Running the suites against the live URL. They each start their own candidate binary and scratch database.

## Decisions

- D-125: keep one suite per commit, matching the roadmap. The commit message names the suite and the exact gate line.
- D-126: use `~/.cargo-target/concord/debug/concord-cli` as `CONCORD_E2E_CANDIDATE` when the caller has not set it;
  that is the binary the gate already builds in debug for smoke tests.
- D-127: a suite that fails because of a stale SQLite assumption is fixed in the same commit as its wiring, not left
  unwired with a lowered ratchet.

## Success criteria

1. `grep -c 'RECORDED_UNRUN = ' tests/e2e/harness_selftest_test.py` → the number falls 21 → 0 across the row.
2. Gate log prints `suites: 29 · … passed` with no skipped suite.
3. `python3 -m pytest tests/e2e/harness_selftest_test.py -q` (e2e venv) passes at each ratchet value.
4. Stamped deploy, live probes, CI green, docs/vault/handoff rewritten in place.

## Order

1. Add row 9 and D-125..D-127; commit spec and plan.
2. Wire `lifecycle_e2e`, run it with the e2e venv and candidate, fix what it reveals, drop ratchet to 20.
3. Continue in the roadmap's order until all 21 are wired.
4. Full gate, deploy, live probe, final docs.
