# Plan: signed-in visual pass

Run from /personal/documents/code/rust/concord on port-rust. Compile only on thinkcentre.

1. Write `tests/e2e/signed_in_visual_e2e.py`.
   - Fixture `server`: reuse the `harness.start_server` pattern from `site_review_e2e.py` on port 8461; seed project `signed-in-visual` and owner user `signed-in-visual`.
   - Fixture `signed_browser`: use `harness.api(BASE,"POST","/api/v1/auth/register", {...})` to mint a real account token; `GET /api/v1/auth/me`; add cookies `concord_nav=<token>` and `concord_consent=1` to the browser context; navigate home and set `localStorage.concord.user` to the parsed `/auth/me` body. Promote the user to project owner with SQL. Yield token/user id; revoke with `POST /api/v1/auth/logout` in teardown.
   - Test A: signed-out page at `/notifications` contains the gate copy; signed-in page does not.
   - Test B: walk six routes (`/`, `/notifications`, `/projects/signed-in-visual/settings`, `/board`, `/rank`, `/consensus`) at 1440x1000 and 390x844; for each, assert a non-empty `main` reading, zero page errors, no sign-in gate copy, and 0px horizontal overflow; save screenshots to `/tmp/signed-in-visual-shots/`.
   - Test C: write a JSON report to `docs/evidence/2026-11-21-signed-in-visual.json` with route, title, text length, mount, overflow and gate presence; assert six entries and no gate.
   - Test D: `POST /api/v1/auth/logout`; `GET /api/v1/auth/me` with the old token returns 401.

2. Verification command and expected output:
   `CONCORD_E2E_CANDIDATE=$PWD/target/debug/concord-cli scripts/parity/e2e_candidate.sh tests/e2e/signed_in_visual_e2e.py -q`
   Expected: all tests pass. Record the actual pytest summary line verbatim in the roadmap and handoff; never guess the count.

3. Wire `signed_in_visual_e2e` into the e2e loop in `scripts/gate.sh` immediately after `site_review_e2e`; keep `RECORDED_UNRUN = 0`.

4. Commit code plus the measured proof in the same commit. Run `make push` once at the end and record its real gate line.

5. Live: mint a guest session on the public host with `POST /api/v1/guest` and consent cookie, set browser cookies/localStorage, walk `/` and `/notifications`, confirm the gate difference, then revoke the token; write the measured result into the roadmap and handoff.

6. Rewrite `docs/HANDOFF.md`, `docs/plans/refactor-roadmap.md`, `docs/reviews/2026-11-14-from-scratch-review.md` and the vault handoff in place with the real commands and results; mark suggestion 1 done and leave 2-8 unchanged.
