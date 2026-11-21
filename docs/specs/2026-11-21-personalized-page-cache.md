# Spec: personalized pages must not be cached

## Problem
The signed-in browser probe sometimes kept the signed-out `/notifications` gate after a valid session cookie was installed. All later probes showed `/auth/me 200` and a fresh first visit opens the page correctly; the differing factor is a previously cached anonymous HTML response. The live `/notifications` response has no `cache-control` header.

## Goals
- Make every server-rendered document answer `Cache-Control: no-store`.
- Prove the header on a targeted e2e probe using the candidate binary.
- Prove the live public host carries the same header after deploy.

## Non-goals
- No change to API responses or static assets.
- No change to the session cookie itself.

## Success criteria
- `signed_in_visual_e2e` includes a probe asserting `cache-control: no-store` on `/notifications`; it fails before the fix and passes after.
- Public host returns `cache-control: no-store` on `/notifications`; a signed-in browser still opens the page with no gate.
- Services remain `concord-rs active`, `concord inactive`.
