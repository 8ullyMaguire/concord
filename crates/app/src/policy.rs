//! Who may call each route, as a table, because a rule that lives in 122 handler bodies is not a rule.
//!
//! The defect this file answers: authentication was a line a handler was supposed to contain. A route added
//! without it compiled, booted, answered 200, and nothing failed — the only thing that would have caught it is
//! an e2e suite that happens to walk that route anonymously, and those suites exist because somebody
//! remembered to write them.
//!
//! `ROUTES` now declares, for every `/api/…` route, `(method, path, policy, enforced)`, and `auth_gate` (in
//! `lib.rs`) reads it through `policy_for` on the path **axum matched** — `{slug}` and all, so a caller cannot
//! reach a policy by inventing a path. A route with no row is a `500`, not a pass: an undeclared route is a
//! deployment missing a decision.
//!
//! ## Why `enforced` is a column and not a detail
//!
//! The table was derived from the code (`scripts/parity/route_policies.py --derive`), and the derivation was
//! then **measured against the live instance**: on 2026-11-15, 34 routes whose handlers looked like they
//! required a session were called anonymously. Twenty-two answered `200`. A table like that is a description
//! of the derivation's confidence, not of the site's behaviour, and turning all of it into enforcement in one
//! deploy would have 401'd the project page's own reads.
//!
//! So enforcement is per row and started where the measurement was unambiguous:
//!
//! * `GET /api/v1/auth/me`, `/api/v1/notifications`, `/api/v1/projects/{slug}/invites`,
//!   `/api/v1/projects/{slug}/votes/next` answered `401` to an anonymous caller — the handler already refused,
//!   and the gate now refuses first, with the same envelope;
//! * `GET /api/v1/admin-ledger` answered **`200`** to an anonymous caller, which is the hole this step exists
//!   for: an operator's ledger served to nobody in particular.
//!
//! Every other row is declared and not enforced (`false`): the policy is what the handler does today, and
//! flipping it on is step 1b, one surface at a time, with the e2e suites watching — because enforcement moves
//! a refusal from inside a handler to in front of it, and that changes both the message and what the body
//! costs. The count of `true` rows is a ratchet in the tests below: it may grow, never shrink.
//!
//! ## Where the table comes from, and how it is checked
//!
//! `scripts/parity/route_policies.py` derives it from the handlers (following one level of the context helpers
//! this tree uses, like `milestone_write_context`), prints it, and compares it with the router on every gate
//! run. Nine rows carry a hand-written reason in that script's `EXCEPTIONS`, because the code's shape led the
//! derivation astray and a person read the handler. The tests here re-read the router out of `lib.rs`, so a
//! route added without a row fails the build in two places instead of one.

/// Who may call a route.
///
/// Four values, because four is what the handlers actually enforce. A richer vocabulary (owner, maintainer,
/// role) would be a table describing intentions this layer cannot check — those refusals need the project row
/// and stay in the handlers until step 1b.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Policy {
    /// Nobody has to be signed in, and no token is read. `healthz`, `version`, the taxonomy lists.
    Public,
    /// Nobody has to be signed in, but an identity narrows the answer: a private project's rows appear for a
    /// member and not for a stranger. Anonymous callers are still answered.
    Optional,
    /// Somebody must be signed in. Guests count: a guest is an identity with a name the record cannot hold.
    Session,
    /// Somebody signed in who is not a guest. Creating a namespace, claiming one, declaring AI involvement.
    Account,
    /// The instance operator. One route: the admin ledger.
    Operator,
}

impl Policy {
    /// Whether an anonymous caller must be refused, once the row is enforced.
    pub(crate) const fn requires_identity(self) -> bool {
        matches!(self, Policy::Session | Policy::Account | Policy::Operator)
    }

    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Policy::Public => "public",
            Policy::Optional => "optional",
            Policy::Session => "session",
            Policy::Account => "account",
            Policy::Operator => "operator",
        }
    }
}

/// Every `/api/…` route: `(method, path, policy, enforced)`.
///
/// Page routes (`/`, `/projects/{slug}`, the thirteen `.xml` documents) are not here: they are `concord_web`'s
/// and they decide their own visibility. The API is where the middleware gates.
/// **19 GET rows were reclassified on 2026-11-16 from `Session` to `Optional`.** `anonymous_walk.py` against the live host
/// answered 200/404 anonymously for each, and their handlers either read no identity or use it only through `readable()` to
/// narrow private projects — `arena_verification` says "any reader may run". They are not unenforced holes: flipping them
/// would 401 the project page's own panels (`project.js` fetches `/lists` and `/work-items` on every page). The
/// `anonymous_reads_are_not_declared_private` test pins the list.
pub(crate) const ROUTES: &[(&str, &str, Policy, bool)] = &[
    ("GET", "/api/v1/admin-ledger", Policy::Operator, true),
    ("POST", "/api/v1/answers/{request_id}/vote", Policy::Optional, false),
    ("POST", "/api/v1/auth/login", Policy::Public, false),
    ("POST", "/api/v1/auth/logout", Policy::Optional, false),
    ("GET", "/api/v1/auth/me", Policy::Session, true),
    ("POST", "/api/v1/auth/redeem-invite", Policy::Session, true),
    ("POST", "/api/v1/auth/register", Policy::Public, false),
    ("POST", "/api/v1/consent", Policy::Public, false),
    ("GET", "/api/v1/contribute/next", Policy::Optional, false),
    ("GET", "/api/v1/curation", Policy::Optional, false),
    ("GET", "/api/v1/curation/", Policy::Optional, false),
    ("POST", "/api/v1/curation/{kind}/{id}/vote", Policy::Session, true),
    ("POST", "/api/v1/emergency-holds/{hold_id}/release", Policy::Session, false),
    ("GET", "/api/v1/finder/questions", Policy::Public, false),
    ("POST", "/api/v1/finder/sessions", Policy::Public, false),
    ("GET", "/api/v1/finder/sessions/{session}", Policy::Public, false),
    ("POST", "/api/v1/finder/sessions/{session}/answers", Policy::Public, false),
    ("POST", "/api/v1/finder/sessions/{session}/back", Policy::Optional, false),
    ("POST", "/api/v1/finder/sessions/{session}/lift", Policy::Optional, false),
    ("GET", "/api/v1/finder/sessions/{session}/results", Policy::Public, false),
    ("GET", "/api/v1/guest", Policy::Public, false),
    ("POST", "/api/v1/guest", Policy::Public, false),
    ("GET", "/api/v1/healthz", Policy::Public, false),
    ("POST", "/api/v1/hooks/forgejo", Policy::Optional, false),
    ("GET", "/api/v1/lists", Policy::Public, false),
    ("GET", "/api/v1/notifications", Policy::Session, true),
    ("POST", "/api/v1/notifications/read-all", Policy::Session, false),
    ("POST", "/api/v1/notifications/{id}/read", Policy::Session, false),
    ("GET", "/api/v1/projects", Policy::Public, false),
    ("POST", "/api/v1/projects", Policy::Account, true),
    ("GET", "/api/v1/projects/{project_id}/arenas/{arena_id}/next-pair", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/arenas/{arena_id}/verification", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/arenas/{arena_id}/votes", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/capabilities", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/charter", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{project_id}/charter", Policy::Session, true),
    ("GET", "/api/v1/projects/{project_id}/complaints/{complaint}", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/complaints/{complaint}/features", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/criteria", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/criteria", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/criteria-profiles", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/criteria-profiles", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/criteria-profiles/", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/criteria/", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{project_id}/criteria/{criterion}/active", Policy::Session, true),
    ("POST", "/api/v1/projects/{project_id}/criteria/{criterion}/vote", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/features/{id}/solutions", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/features/{id}/solutions", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/features/{id}/solutions/vote", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/features/{id}/solutions/{solution_id}/coverage/{complaint_id}/contest", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/field-reports", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/field-reports", Policy::Session, true),
    ("POST", "/api/v1/projects/{project_id}/field-reports/{report_id}/responses", Policy::Session, false),
    ("PUT", "/api/v1/projects/{project_id}/intake-mode", Policy::Session, true),
    ("GET", "/api/v1/projects/{project_id}/lists", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/lists", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/lists/", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/lists/{list}", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/lists/{list}/", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/lists/{list}/entries", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/lists/{list}/entries", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/lists/{list}/entries/vote", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/members", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/milestones/{milestone}", Policy::Session, false),
    ("PATCH", "/api/v1/projects/{project_id}/milestones/{milestone}", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/milestones/{milestone}/close", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/milestones/{milestone}/features", Policy::Session, false),
    ("PUT", "/api/v1/projects/{project_id}/milestones/{milestone}/order", Policy::Session, false),
    ("PUT", "/api/v1/projects/{project_id}/milestones/{milestone}/status", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/rank/composite", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/requests", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/requests", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/solutions", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/strategy/theme-proposals", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/strategy/themes", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/themes", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/themes/{id}/consent", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/themes/{id}/withdraw", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/strategy/weight-proposals", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/weight-proposals", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/consent", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/strategy/weight-proposals/{id}/withdraw", Policy::Session, false),
    ("GET", "/api/v1/projects/{project_id}/tallies", Policy::Optional, false),
    ("GET", "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}", Policy::Optional, false),
    ("POST", "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}", Policy::Session, false),
    ("DELETE", "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}", Policy::Session, false),
    ("POST", "/api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id}/{id}/vote", Policy::Optional, false),
    ("GET", "/api/v1/projects/{slug}", Policy::Public, false),
    ("PUT", "/api/v1/projects/{slug}/ai-involvement", Policy::Account, true),
    ("GET", "/api/v1/projects/{slug}/alternatives", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/alternatives", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/alternatives/{arena}/entries", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/alternatives/{arena}/vote", Policy::Session, false),
    ("DELETE", "/api/v1/projects/{slug}/archive", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/archive", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/arenas/{arena_id}/recompute", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/audit", Policy::Optional, false),
    ("GET", "/api/v1/projects/{slug}/audit/actions", Policy::Public, false),
    ("GET", "/api/v1/projects/{slug}/board", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{slug}/board/cards/{card_id}/move", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/claim", Policy::Account, true),
    ("GET", "/api/v1/projects/{slug}/complaints", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/complaints", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/complaints/{id}/impact", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/complaints/{id}/merge/{target_id}", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/complaints/{id}/reject", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/complaints/{id}/validate", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/consensus", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/consensus", Policy::Session, true),
    ("PUT", "/api/v1/projects/{slug}/consensus/objections/{objection}/resolve", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/consensus/{call}", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/close", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/consensus/{call}/fallback", Policy::Public, false),
    ("GET", "/api/v1/projects/{slug}/consensus/{call}/hold", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/hold", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/objection", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/outcome", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/consensus/{call}/override", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/override", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/override/close", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/override/position", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/consensus/{call}/position", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/documents", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{slug}/documents", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/documents/kinds", Policy::Optional, false),
    ("GET", "/api/v1/projects/{slug}/documents/search", Policy::Optional, false),
    ("DELETE", "/api/v1/projects/{slug}/documents/{document}", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/documents/{document}", Policy::Public, false),
    ("GET", "/api/v1/projects/{slug}/features", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/features", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/features/{feature}", Policy::Public, false),
    ("PUT", "/api/v1/projects/{slug}/features/{feature}", Policy::Account, false),
    ("DELETE", "/api/v1/projects/{slug}/features/{feature}/claim", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/claim", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/complaints", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/complaints", Policy::Session, false),
    ("DELETE", "/api/v1/projects/{slug}/features/{feature}/complaints/{complaint}", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/consensus", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/consensus", Policy::Session, false),
    ("DELETE", "/api/v1/projects/{slug}/features/{feature}/deps", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/deps", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/deps", Policy::Session, false),
    ("DELETE", "/api/v1/projects/{slug}/features/{feature}/evidence", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/evidence", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/evidence", Policy::Session, false),
    ("PUT", "/api/v1/projects/{slug}/features/{feature}/milestone", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/solution-call", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/solution-call", Policy::Session, false),
    ("PUT", "/api/v1/projects/{slug}/features/{feature}/status", Policy::Session, false),
    ("PUT", "/api/v1/projects/{slug}/features/{feature}/strategic-weight", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/features/{feature}/tags", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/tags", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/tags/remove", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/features/{feature}/vote", Policy::Session, true),
    ("PUT", "/api/v1/projects/{slug}/features/{feature}/work-item", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/invites", Policy::Session, true),
    ("POST", "/api/v1/projects/{slug}/invites", Policy::Session, true),
    ("POST", "/api/v1/projects/{slug}/invites/{invite}/revoke", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/join", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/languages", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{slug}/languages", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/merge", Policy::Optional, false),
    ("POST", "/api/v1/projects/{slug}/merge", Policy::Session, false),
    ("PUT", "/api/v1/projects/{slug}/merge/{merge}/approve", Policy::Session, true),
    ("PUT", "/api/v1/projects/{slug}/merge/{merge}/execute", Policy::Session, true),
    ("PUT", "/api/v1/projects/{slug}/merge/{merge}/reject", Policy::Session, true),
    ("PUT", "/api/v1/projects/{slug}/metrics", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/milestones", Policy::Public, false),
    ("POST", "/api/v1/projects/{slug}/milestones", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/moderation/arena/remove", Policy::Session, false),
    ("POST", "/api/v1/projects/{slug}/moderation/arena/restore", Policy::Session, false),
    ("GET", "/api/v1/projects/{slug}/priorities", Policy::Public, false),
    ("GET", "/api/v1/projects/{slug}/roadmap", Policy::Public, false),
    ("GET", "/api/v1/projects/{slug}/tags", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{slug}/tags", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/tags/completeness", Policy::Optional, false),
    ("PUT", "/api/v1/projects/{slug}/visibility", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/votes/next", Policy::Session, true),
    ("GET", "/api/v1/projects/{slug}/work-items", Policy::Optional, false),
    ("GET", "/api/v1/requests", Policy::Public, false),
    ("POST", "/api/v1/requests", Policy::Session, false),
    ("GET", "/api/v1/requests/{request_id}", Policy::Session, false),
    ("DELETE", "/api/v1/requests/{request_id}/accept", Policy::Optional, false),
    ("PUT", "/api/v1/requests/{request_id}/accept", Policy::Optional, false),
    ("GET", "/api/v1/requests/{request_id}/answers", Policy::Session, false),
    ("POST", "/api/v1/requests/{request_id}/answers", Policy::Session, false),
    ("GET", "/api/v1/requests/{request_id}/tags", Policy::Session, false),
    ("PUT", "/api/v1/requests/{request_id}/tags", Policy::Session, false),
    ("GET", "/api/v1/scout", Policy::Session, false),
    ("GET", "/api/v1/search", Policy::Optional, false),
    ("GET", "/api/v1/similar/{kind}", Policy::Optional, false),
    ("GET", "/api/v1/stats", Policy::Public, false),
    ("GET", "/api/v1/stats/history", Policy::Optional, false),
    ("GET", "/api/v1/stats/responsiveness", Policy::Public, false),
    ("GET", "/api/v1/stats/rising", Policy::Public, false),
    ("GET", "/api/v1/stats/summary", Policy::Public, false),
    ("GET", "/api/v1/taxonomy/proposals", Policy::Public, false),
    ("POST", "/api/v1/taxonomy/proposals", Policy::Optional, false),
    ("POST", "/api/v1/taxonomy/proposals/{proposal_id}/consent", Policy::Session, false),
    ("GET", "/api/v1/taxonomy/tags", Policy::Public, false),
    ("GET", "/api/v1/version", Policy::Public, false),
];

/// The policy for a route, matched exactly — `(METHOD, path-as-axum-registered-it)`, plus whether the gate
/// enforces it today.
///
/// Exact matching is the point: a prefix rule would let `/api/v1/projects/{slug}/anything` inherit a policy
/// nobody declared for it, and a route could then be reached through its neighbour.
pub(crate) fn policy_for(method: &str, route: &str) -> Option<(Policy, bool)> {
    ROUTES
        .iter()
        .find(|(declared_method, declared_path, _, _)| {
            declared_method.eq_ignore_ascii_case(method) && *declared_path == route
        })
        .map(|(_, _, policy, enforced)| (*policy, *enforced))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The router as written, not as described.
    const SOURCE: &str = include_str!("lib.rs");

    /// 5 on 2026-11-15 (four routes that already refused a stranger, plus the admin ledger that did not); 6 on
    /// 2026-11-16 when `POST /api/v1/projects` moved its refusal into the gate. It may grow, never shrink.
    const ENFORCED: usize = 25;

    /// The source with string literals and comments replaced by spaces, so a brace or a paren inside either
    /// cannot move the scan. Same length as the input, so an index means the same thing in both.
    fn blank_out(source: &str) -> String {
        let bytes = source.as_bytes();
        let mut out = bytes.to_vec();
        let mut index = 0;
        while index < bytes.len() {
            match bytes[index] {
                b'"' => {
                    let start = index;
                    index += 1;
                    while index < bytes.len() {
                        if bytes[index] == b'\\' {
                            index += 2;
                            continue;
                        }
                        if bytes[index] == b'"' {
                            break;
                        }
                        index += 1;
                    }
                    let end = (index + 1).min(bytes.len());
                    for byte in &mut out[start..end] {
                        *byte = b' ';
                    }
                    index = end;
                }
                b'/' if bytes.get(index + 1) == Some(&b'/') => {
                    let start = index;
                    while index < bytes.len() && bytes[index] != b'\n' {
                        index += 1;
                    }
                    for byte in &mut out[start..index] {
                        *byte = b' ';
                    }
                }
                b'/' if bytes.get(index + 1) == Some(&b'*') => {
                    let start = index;
                    index += 2;
                    while index + 1 < bytes.len() && !(bytes[index] == b'*' && bytes[index + 1] == b'/') {
                        index += 1;
                    }
                    index = (index + 2).min(bytes.len());
                    for byte in &mut out[start..index] {
                        *byte = b' ';
                    }
                }
                _ => index += 1,
            }
        }
        String::from_utf8_lossy(&out).into_owned()
    }

    /// The router function's body: raw (for the path literals) and blanked (for the structure).
    ///
    /// Reading to the end of the file was wrong, and the test said so: a doc comment after `router()` mentions
    /// `.route(`, the next paren match ran off the end, and 68 routes looked unregistered. The body is
    /// brace-matched on the blanked source, where the braces inside `{slug}` cannot be counted.
    fn router_body() -> (String, String) {
        let blanked = blank_out(SOURCE);
        let start = SOURCE.find("pub fn router(").expect("router() is in this file");
        let open = blanked[start..].find('{').expect("router() has a body") + start;
        let bytes = blanked.as_bytes();
        let mut depth = 0usize;
        let mut end = blanked.len();
        for index in open..bytes.len() {
            match bytes[index] {
                b'{' => depth += 1,
                b'}' => {
                    depth -= 1;
                    if depth == 0 {
                        end = index;
                        break;
                    }
                }
                _ => {}
            }
        }
        (SOURCE[open..end].to_string(), blanked[open..end].to_string())
    }

    /// The index of the `)` that closes the `(` at `open`.
    fn closing_paren(blanked: &str, open: usize) -> Option<usize> {
        let mut depth = 0usize;
        for (offset, byte) in blanked.as_bytes()[open..].iter().enumerate() {
            match byte {
                b'(' => depth += 1,
                b')' => {
                    depth -= 1;
                    if depth == 0 {
                        return Some(open + offset);
                    }
                }
                _ => {}
            }
        }
        None
    }

    /// `(METHOD, path)` for every `/api/…` route registered in `router()`.
    ///
    /// The same walk `scripts/parity/route_policies.py` does — strings and comments blanked, parentheses
    /// counted, the path read from the raw text at the same offsets — because two parsers that disagree are
    /// worse than one, and this one has to agree with the checker to be worth running.
    fn registered() -> Vec<(String, String)> {
        let (raw, blanked) = router_body();
        let mut out = Vec::new();
        let mut cursor = 0usize;
        while let Some(at) = blanked[cursor..].find(".route(") {
            let at = cursor + at;
            let open = at + ".route".len();
            let Some(close) = closing_paren(&blanked, open) else { break };
            let Some(quote) = raw[open..close].find('"') else {
                cursor = close;
                continue;
            };
            let after = &raw[open + quote + 1..close];
            let Some(end) = after.find('"') else {
                cursor = close;
                continue;
            };
            let path = &after[..end];
            let expression = &blanked[open..close];
            if path.starts_with("/api/") {
                for (method, call) in [
                    ("GET", "get("),
                    ("POST", "post("),
                    ("PUT", "put("),
                    ("PATCH", "patch("),
                    ("DELETE", "delete("),
                ] {
                    if expression.contains(call) {
                        out.push((method.to_string(), path.to_string()));
                    }
                }
            }
            cursor = close;
        }
        out
    }

    /// The oracle is the anonymous walk against the running instance, not a reading of this table: these reads answered a
    /// stranger on 2026-11-15. A `Session` label claims the product refuses them when it does not.
    #[test]
    fn anonymous_reads_are_not_declared_private() {
        let reads = [
            "/api/v1/projects/{project_id}/arenas/{arena_id}/verification",
            "/api/v1/projects/{project_id}/arenas/{arena_id}/votes",
            "/api/v1/projects/{project_id}/complaints/{complaint}",
            "/api/v1/projects/{project_id}/complaints/{complaint}/features",
            "/api/v1/projects/{project_id}/features/{id}/solutions",
            "/api/v1/projects/{project_id}/lists",
            "/api/v1/projects/{project_id}/lists/",
            "/api/v1/projects/{project_id}/lists/{list}",
            "/api/v1/projects/{project_id}/lists/{list}/",
            "/api/v1/projects/{project_id}/lists/{list}/entries",
            "/api/v1/projects/{project_id}/members",
            "/api/v1/projects/{slug}/alternatives",
            "/api/v1/projects/{slug}/documents/kinds",
            "/api/v1/projects/{slug}/languages",
            "/api/v1/projects/{slug}/merge",
            "/api/v1/projects/{slug}/tags",
            "/api/v1/projects/{slug}/tags/completeness",
            "/api/v1/projects/{slug}/work-items",
            "/api/v1/search",
        ];
        for path in reads {
            let Some((policy, enforced)) = policy_for("GET", path) else {
                panic!("{path} lost its policy row");
            };
            assert_eq!(policy, Policy::Optional, "{path} is an anonymous read, not session-only");
            assert!(!enforced, "{path} narrows by readable(); it cannot enforce identity");
        }
    }

    #[test]
    fn every_api_route_declares_a_policy() {
        let routes = registered();
        assert!(
            routes.len() > 150,
            "parsed only {} routes: the scanner is wrong",
            routes.len()
        );
        let missing: Vec<String> = routes
            .iter()
            .filter(|(method, path)| policy_for(method, path).is_none())
            .map(|(method, path)| format!("{method} {path}"))
            .collect();
        assert!(
            missing.is_empty(),
            "{} route(s) declared no policy: {missing:?}",
            missing.len()
        );
    }

    #[test]
    fn no_policy_describes_a_route_that_no_longer_exists() {
        let routes = registered();
        let stale: Vec<String> = ROUTES
            .iter()
            .filter(|(method, path, _, _)| {
                !routes
                    .iter()
                    .any(|(m, p)| m.eq_ignore_ascii_case(method) && p == path)
            })
            .map(|(method, path, _, _)| format!("{method} {path}"))
            .collect();
        assert!(stale.is_empty(), "{} stale row(s): {stale:?}", stale.len());
    }

    #[test]
    fn the_enforced_set_is_a_ratchet() {
        let enforced = ROUTES.iter().filter(|(_, _, _, on)| *on).count();
        assert!(
            enforced >= ENFORCED,
            "enforcement fell from {ENFORCED} to {enforced}: a route that refused a stranger yesterday passes one today"
        );
        for (method, path, policy, on) in ROUTES {
            if *on {
                assert!(
                    policy.requires_identity(),
                    "{method} {path} is enforced with policy {} — there is nothing to enforce",
                    policy.as_str()
                );
            }
        }
    }

    #[test]
    fn an_undeclared_route_has_no_policy_rather_than_a_neighbours() {
        assert_eq!(policy_for("GET", "/api/v1/projects"), Some((Policy::Public, false)));
        assert_eq!(policy_for("POST", "/api/v1/projects"), Some((Policy::Account, true)));
        // Not a prefix rule: a path nobody declared gets nothing, so the gate answers 500 and names it.
        assert_eq!(policy_for("GET", "/api/v1/projects/anything"), None);
        assert_eq!(policy_for("DELETE", "/api/v1/projects"), None);
        // The method is canonicalised; the path is exact, because axum matches it exactly.
        assert_eq!(policy_for("get", "/api/v1/projects"), Some((Policy::Public, false)));
    }

    #[test]
    fn the_ledger_is_the_one_row_that_was_measured_as_a_hole() {
        assert_eq!(
            policy_for("GET", "/api/v1/admin-ledger"),
            Some((Policy::Operator, true))
        );
        // The reads the front page walks anonymously must stay anonymous, or the site 401s for visitors.
        assert!(!policy_for("GET", "/api/v1/projects").unwrap().1);
        assert!(!policy_for("GET", "/api/v1/search").unwrap().1);
    }

    /// The first handler to give up its own refusal. If either half is removed — the gate's insertion or the handler's
    /// extractor — this fails at build time instead of leaving a route that silently trusts a caller-supplied extension.
    #[test]
    fn the_enforced_gate_supplies_identity_to_its_handler() {
        /// The whole function, braces matched on the comment-blanked source so a brace in a string cannot end it early.
        fn function(source: &str, name: &str) -> String {
            let start = source.find(&format!("async fn {name}(")).unwrap_or_else(|| panic!("{name} exists"));
            let blanked = blank_out(source);
            let open = start + blanked[start..].find('{').expect("a function body opens");
            let mut depth = 0usize;
            let mut close = open;
            for (offset, ch) in blanked[open..].char_indices() {
                match ch {
                    '{' => depth += 1,
                    '}' => {
                        depth -= 1;
                        if depth == 0 {
                            close = open + offset;
                            break;
                        }
                    }
                    _ => {}
                }
            }
            source[start..=close].to_string()
        }

        for name in [
            "create_project",
            "declare_ai_involvement",
            "claim_project",
            "redeem_invite",
            "curation_vote",
            "set_project_visibility",
            "set_criterion_active",
            "set_intake_mode",
            "approve_merge",
            "execute_merge",
            "reject_merge",
            "cast_feature_vote",
            "set_project_metrics",
            "join_project",
            "update_charter",
            "file_feature",
            "create_invite",
            "open_consensus",
            "apply_project_tags",
            "create_field_report",
            "auth_me",
            "list_notifications",
            "list_invites",
            "next_vote_pair",
        ] {
            let body = function(SOURCE, name);
            assert!(
                body.contains("Extension(user): Extension<concord_db::auth::User>"),
                "{name} no longer consumes the identity the gate supplies"
            );
            assert!(!body.contains("bearer_user("), "{name} kept its duplicate in-handler refusal");
        }
        assert!(
            function(SOURCE, "create_project").contains("is_guest("),
            "the guest check stays: the gate proves identity, not an account"
        );

        let gate_start = SOURCE.find("async fn auth_gate(").expect("the gate exists");
        let gate = &SOURCE[gate_start..gate_start + 3000];
        assert!(
            gate.contains("extensions_mut().insert(user)"),
            "the gate stopped supplying identity to enforced handlers"
        );
    }
}
