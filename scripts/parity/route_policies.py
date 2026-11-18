#!/usr/bin/env python3
"""Derive a starting policy table from the handlers' own code. A derivation AID, not a gate.

**The authority is `crates/app/src/policy.rs`**: its tests parse `router()` out of `lib.rs` with `include_str!` and
fail by name on a route with no row, a row with no route, or a growing `Unverified` count. That runs inside
`cargo test --workspace`, which is the unit layer of `make gate`. This script exists because classifying 216 rows by
reading 216 handlers is a day's work, and it did it once: `--derive` prints the table a first reading of the code
implies, which a human then reviews. It is wired to nothing.

Why this exists. The critique of 2026-11-15 measured 122 `bearer_user(...)` call sites inside handlers and no
declaration anywhere: authentication was a line a handler was supposed to contain. A route added without it
compiles, boots, answers, and nothing fails — the only thing that would catch it is an e2e suite that happens to
walk that route anonymously, and those suites exist because somebody remembered to write them.

So there are two sources of truth and this script compares them:

  * the routes, parsed out of `crates/app/src/lib.rs` (`router()`'s `.route("…", get(h).post(h2))` calls);
  * the policies, parsed out of `crates/app/src/policy.rs` (`ROUTES` — `(method, path, Policy::X)`).

A route in the first set and not the second is a route nobody decided about: exit 1 and name it. The reverse (a
policy for a route that no longer exists) is also a failure, because a table that keeps stale rows stops being
read.

The initial table is *derived* from what each handler already does (`--derive`), then reviewed and pasted into
`policy.rs`. Derivation is a starting point, not an authority: a handler that calls `bearer_user` and tolerates
`None` is `Optional`, one that refuses `None` is `Session`, one that refuses a guest is `Account`. Run without
`--derive` and it only checks.

Usage:
    python3 scripts/parity/route_policies.py            # check; exit 1 on any unclassified route
    python3 scripts/parity/route_policies.py --derive   # print the ROUTES table this code implies
"""
from __future__ import annotations

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
APP = ROOT / "crates/app/src/lib.rs"
APP_SRC = ROOT / "crates/app/src"
POLICY = ROOT / "crates/app/src/policy.rs"

POLICIES = ("Public", "Optional", "Session", "Account", "Operator")

# `.route("/a/{b}", get(one).post(two))` — read by a scanner, not a regex: the bodies span lines and the handlers
# are sometimes module-qualified (`finder::start_finder_session`), and a regex that guesses where a call ends reported
# a confident wrong answer on the first run (a `post` route came out as `get`). Parentheses are counted instead.
METHOD_RE = re.compile(r'\b(get|post|put|delete|patch)\(\s*(?:[A-Za-z_][A-Za-z0-9_]*::)*([A-Za-z_][A-Za-z0-9_]*)')


def _matching_paren(text: str, open_at: int) -> int:
    depth = 0
    for index in range(open_at, len(text)):
        char = text[index]
        if char == '"':
            # skip a string literal, including escapes
            index += 1
            while index < len(text) and text[index] != '"':
                index += 2 if text[index] == "\\" else 1
            continue
        if char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
            if depth == 0:
                return index
    return len(text)


AUTH_ANY = re.compile(r'bearer_user\(|acting_user\(|project_maintainer\(')
AUTH_HARD = re.compile(r'bearer_user\([^)]*\)\s*\.await\?\s*\.is_none\(\)|ApiError::unauthenticated')
AUTH_TOLERANT = re.compile(r'bearer_user\([^)]*\)[^;]{0,120}\?\.map\(|unwrap_or\(0\)|if let Some\(|Ok\(None\)')
GUEST_CHECK = re.compile(r'is_guest\(')
OPERATOR_CHECK = re.compile(r'is_operator\(|require_operator\(|list_admin_ledger|admin_ledger')


def routes() -> list[tuple[str, str, str]]:
    """[(method, path, handler)] parsed from router()'s `.route("…", handlers)` calls."""
    source = APP.read_text()
    start = source.index("pub fn router(")
    # D-40: the old end marker moved into api/catalog.rs; find the first
    # function after router() instead so any pure move stays scanner-neutral.
    tail = re.search(r"\n(?:pub\(crate\) )?(?:async )?fn ", source[start:])
    assert tail is not None, "no handler found after router()"
    end = start + tail.start()
    body = source[start:end]
    # Step 3: module-owned routes live in api/*.rs, not in router()'s body.
    for path in sorted((APP_SRC / "api").glob("*.rs")):
        body += "\n" + path.read_text()
    # D-89: finder and feeds are root-owned modules that also declare route tables. A scanner that read only
    # lib.rs reported their seven API routes as stale after the route move; read their sources too.
    for name in ("finder.rs", "feeds.rs"):
        body += "\n" + (APP_SRC / name).read_text()
    found: list[tuple[str, str, str]] = []
    cursor = 0
    while True:
        at = body.find(".route(", cursor)
        if at == -1:
            break
        open_at = at + len(".route")
        close_at = _matching_paren(body, open_at)
        call = body[open_at + 1 : close_at]
        cursor = close_at
        quote = call.find('"')
        if quote == -1:
            continue
        path_end = call.find('"', quote + 1)
        path = call[quote + 1 : path_end]
        handlers = call[path_end + 1 :]
        for method, handler in METHOD_RE.findall(handlers):
            found.append((method.upper(), path, handler))
    return found


def sources() -> str:
    """Every app source, concatenated — a handler is not always in lib.rs (finder.rs, feeds.rs)."""
    return "\n".join(p.read_text() for p in sorted(APP_SRC.glob("*.rs")))


def handler_body(name: str, source: str) -> str:
    """The text of `async fn name(…)` up to the next top-level `fn`, or '' if the name is not a local handler."""
    needle = f"async fn {name}("
    at = source.find(needle)
    if at == -1:
        needle = f"fn {name}("
        at = source.find(needle)
        if at == -1:
            return ""
    following = re.search(r"\n(?:pub )?(?:async )?fn ", source[at + len(needle):])
    return source[at : at + len(needle) + (following.start() if following else 400)]


# Hand-decided exceptions to the derivation, each with the reason it is not what the code's shape suggests.
# A line here is a claim somebody read: the derivation is a lead, and these are the places the lead is wrong.
EXCEPTIONS = {
    ("POST", "/api/v1/auth/login"): ("Public", "this is how a session is obtained; its `ApiError::unauthenticated` is \"invalid credentials\", not \"you must sign in\""),
    ("POST", "/api/v1/auth/register"): ("Public", "an account is created by somebody who has none"),
    ("POST", "/api/v1/guest"): ("Public", "a nameless visitor becoming a guest is the entry point, not a protected act"),
    ("POST", "/api/v1/consent"): ("Public", "the notice is accepted before there is an identity to attach it to"),
    ("POST", "/api/v1/finder/sessions"): ("Public", "the finder is a quiz; it must run before you have an account"),
    ("GET", "/api/v1/finder/questions"): ("Public", "the quiz's first page, read before an identity exists"),
    ("GET", "/api/v1/finder/sessions/{session}"): ("Public", "the session id is the credential; it is handed to an anonymous quiz-taker"),
    ("POST", "/api/v1/finder/sessions/{session}/answers"): ("Public", "answering a quiz you hold the session id for"),
    # The derivation said `Operator` here because a callee's text matched the operator pattern. The handler's
    # own body reads no token and answers the same thing to everybody: it reports whether a guest identity may
    # still be created, which is instance state, not a person's.
    ("GET", "/api/v1/guest"): ("Public", "guest status is instance state, answered before anybody has an identity"),
    # Measured on the live instance: these three answer 401 to a caller with no token, so the derivation's
    # `Optional` was wrong in the direction that breaks readers if the gate ever enforced it.
    ("GET", "/api/v1/projects/{project_id}/strategy/themes"): ("Session", "measured 401 anonymously on 2026-11-16"),
    ("GET", "/api/v1/projects/{project_id}/strategy/theme-proposals"): ("Session", "measured 401 anonymously on 2026-11-16"),
    ("GET", "/api/v1/projects/{project_id}/strategy/weight-proposals"): ("Session", "measured 401 anonymously on 2026-11-16"),
}

CALL_RE = re.compile(r'\b([a-z_][a-z0-9_]*)\(')

# Named context helpers are how this tree actually gates most writes: `milestone_write_context(…)` and
# `strategy_project(…)` decide who may act, and a handler that calls them has no auth line of its own. So the
# derivation follows one level of local calls — a handler is as protected as the helpers it starts with.
_HANDLERS = ("bearer_user", "acting_user", "project_maintainer")


def _classify_text(body: str) -> str:
    if not body:
        return ""
    if OPERATOR_CHECK.search(body):
        return "Operator"
    if AUTH_TOLERANT.search(body) and not GUEST_CHECK.search(body):
        return "Optional"
    if GUEST_CHECK.search(body):
        return "Account"
    if AUTH_HARD.search(body) or AUTH_ANY.search(body):
        return "Session"
    return ""


def derive(handler: str, source: str) -> str:
    """What the handler's own code — and the helpers it calls first — says the policy is."""
    body = handler_body(handler, source)
    if not body:
        return "Unknown"
    direct = _classify_text(body)
    if direct:
        return direct
    for callee in CALL_RE.findall(body):
        if callee in _HANDLERS or callee == handler:
            continue
        inner = handler_body(callee, source)
        if not inner:
            continue
        indirect = _classify_text(inner)
        if indirect:
            return indirect
    # Nothing found: the handler takes no identity at all, or it takes one to narrow what it returns. The
    # signature decides, because that is a fact about the code rather than a guess about it — a handler with
    # no `HeaderMap` cannot read a token, so `Public`; one that has a `HeaderMap` but never refuses a missing
    # token is `Optional` (anonymous allowed, identity narrows).
    if "headers: HeaderMap" in body or "headers: &HeaderMap" in body:
        return "Optional"
    return "Public"


def declared() -> set[tuple[str, str]]:
    """{(method, path)} from policy.rs's ROUTES table."""
    if not POLICY.exists():
        return set()
    text = POLICY.read_text()
    return set(re.findall(r'\(\s*"(GET|POST|PUT|DELETE|PATCH)"\s*,\s*"([^"]+)"\s*,', text))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--derive", action="store_true", help="print the table this code implies")
    args = parser.parse_args()

    source = sources()
    found = routes()
    if not found:
        print("route_policies: parsed 0 routes out of lib.rs — the parser is wrong, not the code", file=sys.stderr)
        return 2

    if args.derive:
        lines = []
        applied = 0
        for method, path, handler in sorted(found, key=lambda r: (r[1], r[0])):
            if not path.startswith("/api/"):
                continue
            policy = derive(handler, source)
            exception = EXCEPTIONS.get((method, path))
            if exception:
                policy = exception[0]
                applied += 1
            lines.append(f'    ("{method}", "{path}", Policy::{policy}),')
        print("\n".join(lines))
        print(f"// exceptions applied: {applied} of {len(EXCEPTIONS)}", file=sys.stderr)
        return 0

    # D-47: a module can declare routes perfectly and still be absent from router().
    # The policy table then agrees with an unmounted route set and every targeted
    # gate stays green while the live site answers 404. Require every api module
    # that declares routes() to be merged into lib.rs.
    lib_text = APP.read_text()
    unmerged = []
    modules = sorted(APP_SRC.glob("api/*.rs"))
    for module in modules:
        text = module.read_text()
        if "pub fn routes()" not in text:
            continue
        call = f"api::{module.stem}::routes()"
        if call not in lib_text:
            unmerged.append(module.stem)
    print(f"api route modules: {len(modules)}; unmerged: {len(unmerged)}")
    for name in unmerged:
        print(f"  unmerged: {name} - routes() exists but lib.rs never mounts api::{name}::routes()")

    api = [(m, p, h) for m, p, h in found if p.startswith("/api/")]
    pages = [(m, p, h) for m, p, h in found if not p.startswith("/api/")]
    have = declared()
    want = {(method, path) for method, path, _ in api}
    missing = sorted(want - have)
    stale = sorted(have - want)

    print(f"api routes registered: {len(want)}")
    print(f"page routes (server-rendered, outside the API policy table): {len(pages)}")
    print(f"routes with a declared policy: {len(want & have)}")
    print(f"routes with no policy: {len(missing)}")
    for method, path in missing[:12]:
        handler = next(h for m, p, h in found if (m, p) == (method, path))
        print(f"  unclassified: {method:6} {path}  ({handler} → {derive(handler, source)})")
    if len(missing) > 12:
        print(f"  … and {len(missing) - 12} more")
    print(f"policies for routes that no longer exist: {len(stale)}")
    for method, path in stale[:6]:
        print(f"  stale: {method:6} {path}")

    if missing or stale or unmerged:
        print("route_policies: FAIL")
        return 1
    print("route_policies: PASS — every registered route declares who may call it")
    return 0


if __name__ == "__main__":
    sys.exit(main())
