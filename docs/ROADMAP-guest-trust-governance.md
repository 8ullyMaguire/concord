# Guest access, trust levels and self-governance — concord

Status: **proposal**, from the owner's request of 2026-10-16. It records intent in this repo's own
terms; nothing here is implemented. The estate-wide standard (items GS-1…GS-5, every setting
configurable, defaults and firewalls) is:

    secondbrain/10-Projects/2026-10-16-guest-access-trust-and-self-governance.md

This file is the site-specific half: what exists here today, what this site adopts, and the
firewalls that differ from the standard because this site is not the same shape as the others.

Stack: Go, with e2e suites and a migration chain.

## Where concord stands today (measured 2026-10-16)

- **the estate's reference for GS-1 … GS-3**: `docs/specs/guest-interactions-spec.md` and `docs/PLAN-guest.md` already specify guest-as-real-user (`is_guest`, `password_hash=''`), `consented_at`, the audit-log MAU read, the server-side consent gate, and the schema-driven fold — with `CONCORD_GUEST_MAU_LIMIT` defaulting to 1000
- `reputation` (131), `trust_level` (17), `is_moderator` (12) exist; `docs/specs/membership-and-voting.md` covers membership and quorum

## What this site adopts

- GS-4's elasticity and GS-5's jury as **additions to what exists**: the ladder and quorum are already discussed in `membership-and-voting.md`, and the standard's contribution is the fall-when-empty rule and the recusal-by-draw rule
- GS-1's default MAU limit: the standard says 500; concord ships 1000 — either is fine, but the number should be *stated* where the operator looks, which it already is

## Config deltas (everything else is the standard's default)

- `CONCORD_GUEST_MAU_LIMIT` keep as-is (1000) or align to 500 — operator's choice, recorded in the standard's §6 table
- `trust.elastic_min_holders`, `trust.floor_level`
- `governance.jury_elastic_min`

## Firewalls specific to this site

- Concord's existing rules are the ones the standard cites: a guest may vote on a comparison but not on a taxonomy proposal (quorum eligibility), and a guest has no standing to extend trust. Nothing here should weaken them.

## Order of work

GS-4 (elastic) → GS-5 (jury), on top of the specified GS-1…GS-3. The standard's §9 explains the
ordering: consent first, because the guest identity *is* a cookie and the gate has to exist before
the thing it gates; the fold second, because offering guest access without it creates work to clean
up later.

## If this is implemented

Every step needs a test that fails without it, and the numbers belong in this file when they
are real — the estate's rule is that a claim of done carries the command that proves it. The
two items in the standard that nothing in this repo has yet are the elastic trust rule (§4)
and the jury drawn from the eligible pool (§5): both are cheap to fake and therefore need a
test that would pass for the wrong reason to be written, and killed, on purpose.
