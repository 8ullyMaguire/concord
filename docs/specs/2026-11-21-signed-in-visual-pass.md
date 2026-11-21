# Spec: signed-in visual pass (suggestion 1)

## Problem
The 34-route review on 2026-11-20 covered only signed-out shells and gates. It explicitly did not exercise account/settings/proposal surfaces, so the review cannot say what a signed-in reader actually sees.

## Goals
- Mint a short-lived session the way a browser does (register over HTTP, keep the returned token), not by hand-writing localStorage.
- Walk the surfaces that change with identity: home navigation, `/notifications`, project settings, board, vote and consensus.
- Capture per-route DOM facts and screenshots at 1440x1000 and 390x844, and record what the signed-out sweep could not see.
- Prove the signed-in state is real (signed-out gate disappears) and that the session is revoked afterwards.

## Non-goals
- No production account is created and no user-supplied credential is used.
- No new page design is shipped; this is an audit, not a refactor.
- No full-site re-audit of the 34 signed-out routes.

## Constraints
- Targeted e2e suite only; never the full gate for iteration.
- Spec/plan first, then code, then one targeted suite, then one repository gate at the end.
- Builds/deploys are tracked background jobs.
- No secret value is written into docs, logs, screenshots or chat.

## Success criteria
- `scripts/parity/e2e_candidate.sh tests/e2e/signed_in_visual_e2e.py -q` passes with zero page errors.
- A signed-out baseline gate on `/notifications` is present; the same route with the minted session has no gate.
- Six signed-in routes produce non-empty DOM readings and screenshots, with 0px horizontal overflow at both widths.
- `POST /api/v1/auth/logout` then `GET /api/v1/auth/me` returns 401, proving the session is deleted.
- Live browser probe on https://concord.polarisocial.xyz with a minted guest session confirms the same gate difference; the session is revoked afterwards.
