//! Moderation acts: take an idea out of an arena, and put it back.
//!
//! Two rules make this the most dangerous surface in the product, and both are about what an act leaves
//! behind rather than what it does:
//!
//! * **The act states its reason, and the reason is a sentence, not a flag.** `insert_act` refuses a blank
//!   one, because an act that removes somebody's standing and explains nothing is indistinguishable from a
//!   grudge — and the log is read by the person on the other end of it.
//! * **The prior state is written INTO the log, not read from the subject at undo time.** `arena.remove`
//!   records `{status, arena_id, elo_r, elo_rd, sigma, games}`; `arena.restore` replays it from the entry
//!   being undone and never from the caller. A restore that took the values from the request would be a
//!   second way to write a rating, and a second way to write a rating is a way to write any rating.
//!
//! Plus three refusals that exist because the database would happily allow them: a shipped feature is not
//! competing any more, a baseline entry is the thing everyone else is scored against, and an act can only be
//! reversed once.

use serde::Serialize;
use serde_json::json;
use sqlx::PgPool;

use polaris_authz::{Capability, Grant, Policy, Requirement, Scope, Subject, Verdict};

use crate::comments::Comment;
use crate::feature_writes::WriteError;

/// The capability a moderation act needs: `resource.action`, parsed rather than typed as a string, because two
/// spellings of one permission is how a check ends up agreeing with itself and nothing else.
pub fn moderation_capability() -> Capability {
    Capability::parse("moderation.act").expect("moderation.act is a valid capability")
}

/// The roles that may moderate a project's content, as grants.
///
/// This replaces a hand-written ladder (`guest < user < contributor < reviewer < maintainer < owner`) that used to
/// live here. Gate 45 fails a site for keeping its own permission check beside `polaris-authz`, and the ladder had
/// exactly the defect the crate removes: a bool answer with no reason, no expiry, and no way to say *why*.
///
/// Two doors, as the product has them: an instance-wide trust level (`CONCORD_MOD_TRUST_MIN`, default 1) or a
/// project role of `reviewer` and up. The trust door is a `Requirement` on the `everyone` role rather than a
/// separate branch, so the crate answers both with one call.
pub fn moderation_policy(trust_min: u8) -> Policy {
    let capability = moderation_capability();
    let mut policy = Policy {
        capabilities: [capability.clone()].into_iter().collect(),
        grants: Vec::new(),
        overrides: Vec::new(),
    };
    // Anyone the trust floor admits: the product's first door.
    policy.grants.push(
        Grant::role("everyone", capability.clone(), Scope::Curated)
            .requiring(Requirement::TrustAtLeast(trust_min)),
    );
    // And the project roles: `reviewer` is the floor, and `maintainer`/`owner` are above it. `Scope::Curated` is
    // what a moderation act reaches -- the project's own content, not the whole instance.
    for role in ["reviewer", "maintainer", "owner"] {
        policy
            .grants
            .push(Grant::role(role, capability.clone(), Scope::Curated));
    }
    policy
}

/// The act, as the HTTP layer returns it.
#[derive(Debug, Clone, Serialize)]
pub struct ActResult {
    /// The audit row.
    pub id: i64,
    /// The project it was taken in.
    pub project_id: i64,
    /// `arena.remove` or `arena.restore`.
    pub action: String,
    /// Always `feature` today.
    pub entity: String,
    /// The feature.
    pub entity_id: i64,
    /// When.
    pub at: f64,
}

/// May this person moderate this project?
///
/// Two doors, because the owner asked for "a high-trust user or an admin" and the repo has both: an
/// instance-wide trust level (`CONCORD_MOD_TRUST_MIN`, default 1) and a per-project role (`reviewer` or
/// above). Either is enough. Two copies of this pair — one for the answer and one for the request — is how
/// one surface ends up granting what the other denies, so there is one copy.
pub async fn may_moderate(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    trust_min: i32,
) -> Result<bool, sqlx::Error> {
    Ok(moderation_verdict(pool, project_id, actor_id, trust_min)
        .await?
        .is_allowed())
}

/// The same question, answered with the crate's `Verdict` instead of a bool, so a refusal can say why.
///
/// The product still shows its own sentence for a denial (the page names the role it wanted, which the crate
/// cannot know); the verdict is what decides, and `Verdict::reason()` is available for the log.
pub async fn moderation_verdict(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    trust_min: i32,
) -> Result<Verdict, sqlx::Error> {
    let trust: Option<i64> = sqlx::query_scalar("select trust_level from users where id = $1")
        .bind(actor_id)
        .fetch_optional(pool)
        .await?;
    // The subject is built from Concord's own rows: its role in THIS project, and its instance trust level. The
    // crate never reads a session or a token, which is why this is a query and not a header.
    let role: Option<String> =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(project_id)
            .bind(actor_id)
            .fetch_optional(pool)
            .await?;
    let mut subject = Subject::new(actor_id.to_string(), trust.unwrap_or(0).clamp(0, 255) as u8);
    if let Some(role) = role {
        // An unknown role is not added: a typo in a members row must not read as a grant, and the crate denies a
        // role it has no grant for anyway.
        subject.roles.push(role);
    }
    let policy = moderation_policy(trust_min.clamp(0, 255) as u8);
    Ok(polaris_authz::authorize(
        &policy,
        &subject,
        moderation_capability(),
        Scope::Curated,
        now_unix(),
    ))
}

/// Unix seconds, from the system clock. Named so every expiry check in this module reads the same clock, and so a
/// test can see it is the only one.
pub fn now_unix() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs() as i64)
        .unwrap_or(0)
}

/// Every arena this feature is entered in, in id order.
pub async fn arenas_for_feature(pool: &PgPool, feature_id: i64) -> Result<Vec<i64>, sqlx::Error> {
    sqlx::query_scalar(
        "select arena_id from arena_entries where entity_type = 'feature' and entity_id = $1 \
         order by arena_id",
    )
    .bind(feature_id)
    .fetch_all(pool)
    .await
}

/// The act's `detail` column: the reason, the prior state, and what it undoes.
fn act_detail(reason: &str, prev: Option<serde_json::Value>, undoes: i64) -> String {
    let mut value = json!({ "reason": reason.trim() });
    if let Some(prev) = prev {
        value["prev"] = prev;
    }
    if undoes != 0 {
        value["undoes"] = json!(undoes);
    }
    value.to_string()
}

/// Write one act, refusing a blank reason.
async fn insert_act(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    action: &str,
    entity: &str,
    entity_id: i64,
    reason: &str,
    prev: Option<serde_json::Value>,
    undoes: i64,
) -> Result<ActResult, WriteError> {
    let reason = reason.trim();
    if reason.is_empty() {
        // The crate's sentence, verbatim: the page shows it, and the test asserts on it.
        return Err(WriteError::Invalid(
            "this act requires a reason, and none was given".into(),
        ));
    }
    let id: i64 = sqlx::query_scalar(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at, \
                               undoes) \
         values ($1, $2, $3, $4, $5, $6, now(), $7) returning id",
    )
    .bind(project_id)
    .bind(actor_id)
    .bind(action)
    .bind(entity)
    .bind(entity_id)
    .bind(act_detail(reason, prev, undoes))
    .bind(if undoes == 0 { None } else { Some(undoes) })
    .fetch_one(pool)
    .await?;
    let at: f64 = sqlx::query_scalar(
        "select extract(epoch from created_at)::double precision from audit_log where id = $1",
    )
    .bind(id)
    .fetch_one(pool)
    .await?;
    Ok(ActResult {
        id,
        project_id,
        action: action.to_string(),
        entity: entity.to_string(),
        entity_id,
        at,
    })
}

/// Take a feature out of an arena: `POST …/moderation/arena/remove`.
///
/// Refused, with the reason named, when the feature is shipped (it is not competing any more, and "rejecting"
/// it would rewrite the record of something that happened) or when the entry is the arena's baseline (the
/// thing every other entry is scored against).
///
/// The author is told in the same transaction as the act, unless the moderator acted on their own feature —
/// they performed it, and the log records it.
///
/// # Errors
/// [`WriteError::NotFound`] when the feature or its arena entry does not exist; [`WriteError::Invalid`] for a
/// shipped feature, a baseline entry, or a blank reason.
pub async fn remove_feature_from_arena(
    pool: &PgPool,
    project_id: i64,
    arena_id: i64,
    feature_id: i64,
    actor_id: i64,
    reason: &str,
) -> Result<ActResult, WriteError> {
    // THE REASON IS CHECKED FIRST, BEFORE ANY WRITE.
    //
    // The first version of this function checked it at the end, inside `insert_act` -- the same place the Go
    // store checks it -- and relied on the router to refuse early, as the Go handler does. It did not, so an
    // act with no reason deleted the arena entry, rejected the feature, returned 400, and left no act in the
    // log: a silent removal with no record of who did it or why. The probe found it on its fourth assertion.
    //
    // A validation that runs after the side effect is not a validation; it is a report.
    if reason.trim().is_empty() {
        return Err(WriteError::Invalid(
            "this act requires a reason, and none was given".into(),
        ));
    }

    let feature: Option<(i64, String, String, i64)> = sqlx::query_as(
        "select project_id, status, title, author_id from features where id = $1",
    )
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    let Some((feature_project, status, title, author_id)) = feature else {
        return Err(WriteError::NotFound(format!("feature {feature_id}")));
    };
    if feature_project != project_id {
        return Err(WriteError::NotFound(format!("feature {feature_id}")));
    }
    if status == "shipped" {
        return Err(WriteError::Invalid(
            "a shipped feature cannot be removed from an arena".into(),
        ));
    }
    let entry: Option<(f64, f64, f64, i64, bool)> = sqlx::query_as(
        "select r, rd, sigma, games, is_baseline from arena_entries \
          where arena_id = $1 and entity_type = 'feature' and entity_id = $2",
    )
    .bind(arena_id)
    .bind(feature_id)
    .fetch_optional(pool)
    .await?;
    let Some((r, rd, sigma, games, is_baseline)) = entry else {
        return Err(WriteError::NotFound(format!(
            "feature {feature_id} is not in arena {arena_id}"
        )));
    };
    if is_baseline {
        return Err(WriteError::Invalid(
            "the baseline entry of an arena cannot be removed".into(),
        ));
    }

    let mut tx = pool.begin().await?;
    sqlx::query(
        "delete from arena_entries where arena_id = $1 and entity_type = 'feature' and entity_id = $2",
    )
    .bind(arena_id)
    .bind(feature_id)
    .execute(&mut *tx)
    .await?;
    sqlx::query("update features set status = 'rejected', updated_at = now() where id = $1 and project_id = $2")
        .bind(feature_id)
        .bind(project_id)
        .execute(&mut *tx)
        .await?;
    if author_id != actor_id {
        sqlx::query(
            "insert into notifications (user_id, kind, project_id, subject_id, title, reason, actor_id, \
                                        created_at) \
             values ($1, 'feature_removed', $2, $3, $4, $5, $6, now())",
        )
        .bind(author_id)
        .bind(project_id)
        .bind(feature_id)
        .bind(&title)
        .bind(reason.trim())
        .bind(actor_id)
        .execute(&mut *tx)
        .await?;
    }
    tx.commit().await?;

    insert_act(
        pool,
        project_id,
        actor_id,
        "arena.remove",
        "feature",
        feature_id,
        reason,
        Some(json!({
            "status": status,
            "arena_id": arena_id,
            "elo_r": r,
            "elo_rd": rd,
            "sigma": sigma,
            "games": games,
        })),
        0,
    )
    .await
}

/// Reverse one `arena.remove`: `POST …/moderation/arena/restore`.
///
/// The prior values come from the act being undone, never from the caller. A second reversal of the same act
/// is refused: the log is append-only, and "undo twice" would re-insert an entry the second act did not
/// remove.
///
/// # Errors
/// [`WriteError::NotFound`] when the act or the feature is missing or has moved; [`WriteError::Invalid`] when
/// the act is not a restorable removal, has already been reversed, carries no prior state, or states no reason.
pub async fn restore_feature_to_arena(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    undo_of: i64,
    reason: &str,
) -> Result<ActResult, WriteError> {
    // Checked before the reads for the same reason as the removal above: a refusal that arrives after a
    // write is a report, not a guard.
    if reason.trim().is_empty() {
        return Err(WriteError::Invalid(
            "this act requires a reason, and none was given".into(),
        ));
    }

    let act: Option<(String, String, Option<i64>, String)> = sqlx::query_as(
        "select action, entity, entity_id, detail from audit_log where id = $1 and project_id = $2",
    )
    .bind(undo_of)
    .bind(project_id)
    .fetch_optional(pool)
    .await?;
    let Some((action, entity, entity_id, detail)) = act else {
        return Err(WriteError::NotFound(format!("act {undo_of}")));
    };
    if action != "arena.remove" || entity != "feature" || entity_id.is_none() {
        return Err(WriteError::Invalid(format!(
            "act {undo_of} is {action:?} and cannot be restored"
        )));
    }
    let already: i64 = sqlx::query_scalar("select count(*) from audit_log where undoes = $1")
        .bind(undo_of)
        .fetch_one(pool)
        .await?;
    if undo_of <= 0 {
        return Err(WriteError::Invalid("undoes is required".into()));
    }
    if already > 0 {
        return Err(WriteError::Invalid(format!(
            "act {undo_of} has already been reversed"
        )));
    }

    let parsed: serde_json::Value = serde_json::from_str(&detail).unwrap_or(serde_json::Value::Null);
    let prev = parsed.get("prev").cloned().unwrap_or(serde_json::Value::Null);
    let number = |key: &str| prev.get(key).and_then(serde_json::Value::as_f64);
    let (Some(status), Some(arena_id)) = (
        prev.get("status").and_then(|v| v.as_str()).filter(|s| !s.is_empty()),
        prev.get("arena_id").and_then(serde_json::Value::as_i64).filter(|v| *v != 0),
    ) else {
        return Err(WriteError::Invalid(format!(
            "act {undo_of} carries no prior state"
        )));
    };
    let (Some(r), Some(rd), Some(sigma), Some(games)) = (
        number("elo_r"),
        number("elo_rd"),
        number("sigma"),
        number("games"),
    ) else {
        return Err(WriteError::Invalid(format!(
            "act {undo_of}'s prior state is incomplete"
        )));
    };
    let feature_id = entity_id.unwrap_or_default();

    // The subject must still be this project's: a project's log cannot restore a feature that has since moved.
    let feature_project: Option<i64> =
        sqlx::query_scalar("select project_id from features where id = $1")
            .bind(feature_id)
            .fetch_optional(pool)
            .await?;
    match feature_project {
        Some(owner) if owner == project_id => {}
        _ => return Err(WriteError::NotFound(format!("feature {feature_id}"))),
    }

    let mut tx = pool.begin().await?;
    sqlx::query(
        "insert into arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, updated_at) \
         values ($1, 'feature', $2, $3, $4, $5, $6, now()) \
         on conflict (arena_id, entity_type, entity_id) do update set \
             r = excluded.r, rd = excluded.rd, sigma = excluded.sigma, \
             games = excluded.games, updated_at = excluded.updated_at",
    )
    .bind(arena_id)
    .bind(feature_id)
    .bind(r)
    .bind(rd)
    .bind(sigma)
    .bind(games as i64)
    .execute(&mut *tx)
    .await?;
    sqlx::query("update features set status = $1, updated_at = now() where id = $2 and project_id = $3")
        .bind(status)
        .bind(feature_id)
        .bind(project_id)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;

    insert_act(
        pool,
        project_id,
        actor_id,
        "arena.restore",
        "feature",
        feature_id,
        reason,
        None,
        undo_of,
    )
    .await
}

/// A comment thread's moderation read: who may delete what, for the page.
///
/// Kept here rather than in [`crate::comments`] because it is a permission question, and the answer is the
/// same function [`may_moderate`] gives the acts above.
pub async fn may_delete_comment(
    pool: &PgPool,
    project_id: i64,
    actor_id: i64,
    comment: &Comment,
    trust_min: i32,
) -> Result<bool, sqlx::Error> {
    if comment.author_id == actor_id {
        return Ok(true);
    }
    may_moderate(pool, project_id, actor_id, trust_min).await
}

/// Is this a moderation act at all? The audit page's `kind=moderation` filter asks.
pub fn is_moderation_action(action: &str) -> bool {
    action.starts_with("arena.")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn subject_with(roles: &[&str], trust: u8) -> Subject {
        let mut subject = Subject::new("7", trust);
        for role in roles {
            subject.roles.push((*role).to_string());
        }
        subject
    }

    fn verdict(roles: &[&str], trust: u8, trust_min: u8) -> Verdict {
        polaris_authz::authorize(
            &moderation_policy(trust_min),
            &subject_with(roles, trust),
            moderation_capability(),
            Scope::Curated,
            now_unix(),
        )
    }

    #[test]
    fn a_reviewer_may_moderate_and_a_contributor_may_not() {
        assert!(verdict(&["reviewer"], 0, 1).is_allowed());
        assert!(verdict(&["maintainer"], 0, 1).is_allowed());
        assert!(!verdict(&["contributor"], 0, 1).is_allowed());
        assert!(!verdict(&[], 0, 1).is_allowed());
    }

    #[test]
    fn the_trust_door_is_the_crates_requirement_not_a_second_branch() {
        // Trust at the floor, no role: allowed. Below it: denied, and the denial names the unmet requirement.
        assert!(verdict(&[], 1, 1).is_allowed());
        let below = verdict(&[], 0, 1);
        assert!(!below.is_allowed());
        assert!(below.reason().contains("trust"), "{}", below.reason());
    }

    #[test]
    fn an_unknown_role_is_not_a_grant() {
        assert!(!verdict(&["maintaner"], 0, 1).is_allowed());
        assert!(!verdict(&["admin"], 0, 1).is_allowed());
    }

    #[test]
    fn the_capability_is_not_declared_until_the_policy_declares_it() {
        let empty = Policy {
            capabilities: [].into_iter().collect(),
            grants: vec![Grant::role("owner", moderation_capability(), Scope::Site)],
            overrides: Vec::new(),
        };
        let refused = polaris_authz::authorize(
            &empty,
            &subject_with(&["owner"], 9),
            moderation_capability(),
            Scope::Curated,
            now_unix(),
        );
        assert!(!refused.is_allowed());
        assert!(refused.reason().contains("not declared"), "{}", refused.reason());
    }

    #[test]
    fn the_act_detail_carries_the_reason_and_the_prior_state() {
        let detail = act_detail("  spam  ", Some(json!({ "status": "discussion" })), 0);
        let parsed: serde_json::Value = serde_json::from_str(&detail).expect("json");
        assert_eq!(parsed["reason"], "spam");
        assert_eq!(parsed["prev"]["status"], "discussion");
        // No `undoes` key at all rather than a zero: the audit page asks `undone = exists(undoes = id)`.
        assert!(parsed.get("undoes").is_none());
    }

    #[test]
    fn a_restore_records_what_it_undoes() {
        let detail = act_detail("not spam", None, 41);
        let parsed: serde_json::Value = serde_json::from_str(&detail).expect("json");
        assert_eq!(parsed["undoes"], 41);
        assert!(parsed.get("prev").is_none());
    }
}
