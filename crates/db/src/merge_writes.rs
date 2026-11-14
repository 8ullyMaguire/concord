//! Merge requests: §5.7's gate, and the state machine it protects.
//!
//! `execute` is the destructive half of this module — it ships a feature and closes every complaint linked to it — so the rules
//! that guard it are here, beside the state change, where no handler can route around them:
//!
//! * **the request must belong to the project named by the caller.** Without this, a member of any readable project could
//!   execute any merge request by id and have the record filed under their own project;
//! * **`open` is the only state that can move**, once. Without it a second execute re-ran the whole landing (re-shipping the
//!   feature, re-closing complaints already closed) and a reject after a merge flipped a landed change to rejected;
//! * **the gate must pass**, and the gate's arithmetic is `governance::check_merge_gate` — the one implementation, with its own
//!   tests, not a second copy here. When the charter drops the quorum the merge is legal and the audit row says so, because §5.7
//!   allows the exception only *"every such merge is logged"*;
//! * **an approval is counted only if it is from somebody who could still hold the gate open**: not the request's own author,
//!   and still an eligible collaborator. Eligibility is decided when counting rather than when inserting, so an approval
//!   recorded before this rule existed does not count retroactively either.

use sqlx::PgPool;

/// A merge request.
#[derive(Debug, Clone, serde::Serialize)]
pub struct MergeRequest {
    /// The request.
    pub id: i64,
    /// Its project.
    pub project_id: i64,
    /// The feature it lands.
    pub feature_id: i64,
    /// Who opened it.
    pub author_id: i64,
    /// Its title.
    pub title: String,
    /// The external change, when one exists.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub external_ref: Option<String>,
    /// `open`, `merged` or `rejected`.
    pub status: String,
    /// Unix seconds.
    pub opened_at: f64,
    /// Unix seconds, once it left `open`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub closed_at: Option<f64>,
}

/// §5.7's verdict for one merge request. Served with the request so a client can show *why* the button is disabled instead of
/// working the quorum out for itself — a client that re-derives §5.7 is a second implementation, and the one that gets it wrong.
///
/// `approvals` is the **counted** number, not the number of rows: an approval by the request's author, or by somebody who is no
/// longer an eligible collaborator, exists in the table and does not count. A page that counted rows would show a filled quorum
/// the server refuses to honour.
#[derive(Debug, Clone, serde::Serialize)]
pub struct MergeGateState {
    /// Counted confirmations.
    pub approvals: i64,
    /// How many were needed; 0 when the charter dropped the quorum.
    pub needed: i64,
    /// How many eligible collaborators there are.
    pub eligible: i64,
    /// Whether the quorum is met.
    pub quorum_ok: bool,
    /// Whether a reviewer-level approval is present or not required.
    pub reviewer_ok: bool,
    /// Whether the charter requires a quorum at all.
    pub requires_quorum: bool,
    /// Whether the merge may proceed.
    pub passed: bool,
}

impl MergeGateState {
    /// Why the gate is unmet, in the client's own terms.
    ///
    /// A refusal that says only "gate not met" leaves a reader unable to tell whether they need another confirmation or a
    /// reviewer, so the shortfall names which.
    #[must_use]
    pub fn shortfall(&self) -> String {
        let mut why: Vec<String> = Vec::new();
        if self.requires_quorum && !self.quorum_ok {
            why.push(format!("{} of {} confirmations given", self.approvals, self.needed));
        }
        if !self.reviewer_ok {
            why.push("a reviewer approval is required".to_string());
        }
        if why.is_empty() {
            "the merge gate is not met".to_string()
        } else {
            why.join("; ")
        }
    }
}

/// Why a merge action was refused.
#[derive(Debug)]
pub enum Refused {
    /// The request is not there, **or is not in the project the caller named** — deliberately indistinguishable.
    NotFound,
    /// The request has already left `open`.
    AlreadyClosed(String),
    /// §5.7's gate is unmet, with the shortfall as its sentence.
    GateUnmet(String),
    /// The author tried to confirm their own request.
    SelfApproval,
    /// The approver is not a member with role ≥ contributor: the request is fine, this account is not part of the decision.
    NotAMember,
    /// The database said no.
    Database(sqlx::Error),
}

impl From<sqlx::Error> for Refused {
    fn from(error: sqlx::Error) -> Self {
        Self::Database(error)
    }
}

/// One merge request, with its gate.
///
/// # Errors
/// `sqlx::Error`.
pub async fn get(pool: &PgPool, id: i64) -> Result<Option<MergeRequest>, sqlx::Error> {
    let row: Option<(i64, i64, i64, i64, String, Option<String>, String, f64, Option<f64>)> =
        sqlx::query_as(
            "select id, project_id, feature_id, author_id, title, external_ref, status,
                    extract(epoch from opened_at)::double precision,
                    extract(epoch from closed_at)::double precision
               from merge_requests where id = $1",
        )
        .bind(id)
        .fetch_optional(pool)
        .await?;
    Ok(row.map(shape))
}

fn shape(
    row: (i64, i64, i64, i64, String, Option<String>, String, f64, Option<f64>),
) -> MergeRequest {
    MergeRequest {
        id: row.0,
        project_id: row.1,
        feature_id: row.2,
        author_id: row.3,
        title: row.4,
        external_ref: row.5,
        status: row.6,
        opened_at: row.7,
        closed_at: row.8,
    }
}

/// A project's merge requests, newest first. **Empty, never null.**
///
/// There was no read side in the reference at all: the table had a create, a get and three transitions, and nothing that listed
/// them, so a merge-request page could not exist and the four write endpoints had no list to appear on. This is the read side.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, project_id: i64) -> Result<Vec<MergeRequest>, sqlx::Error> {
    let rows: Vec<(i64, i64, i64, i64, String, Option<String>, String, f64, Option<f64>)> =
        sqlx::query_as(
            "select id, project_id, feature_id, author_id, title, external_ref, status,
                    extract(epoch from opened_at)::double precision,
                    extract(epoch from closed_at)::double precision
               from merge_requests where project_id = $1
              order by opened_at desc, id desc",
        )
        .bind(project_id)
        .fetch_all(pool)
        .await?;
    Ok(rows.into_iter().map(shape).collect())
}

/// Open a merge request.
///
/// # Errors
/// `sqlx::Error`.
pub async fn create(
    pool: &PgPool,
    project_id: i64,
    feature_id: i64,
    author_id: i64,
    title: &str,
) -> Result<MergeRequest, sqlx::Error> {
    let id: i64 = sqlx::query_scalar(
        "insert into merge_requests (project_id, feature_id, author_id, title, status, opened_at)
         values ($1,$2,$3,$4,'open',now()) returning id",
    )
    .bind(project_id)
    .bind(feature_id)
    .bind(author_id)
    .bind(title.trim())
    .fetch_one(pool)
    .await?;
    get(pool, id)
        .await?
        .ok_or(sqlx::Error::RowNotFound)
}

/// Record one collaborator's confirmation.
///
/// Two rules, neither of which existed in the reference until it was written down: **the approver must be a member with role ≥
/// contributor** — not "an eligible collaborator", because approving somebody else's merge request IS itself a qualifying
/// contribution, and requiring eligibility first would make that route unreachable, so nobody could ever become a collaborator
/// by reviewing, which is what reviewing is for — and **the author cannot confirm their own request**.
///
/// # Errors
/// See [`Refused`].
pub async fn approve(pool: &PgPool, mr_id: i64, user_id: i64) -> Result<(), Refused> {
    let mr = get(pool, mr_id).await?.ok_or(Refused::NotFound)?;
    if user_id == mr.author_id {
        return Err(Refused::SelfApproval);
    }
    let role: Option<String> =
        sqlx::query_scalar("select role from members where project_id = $1 and user_id = $2")
            .bind(mr.project_id)
            .bind(user_id)
            .fetch_optional(pool)
            .await?;
    // The role ladder is the governance crate's (`Role::at_least`), not a second copy: a rank table here would be a second
    // answer to "who outranks whom", and gate 45 fails a site for exactly that.
    let role = role.ok_or(Refused::NotAMember)?;
    if !concord_governance::Role::parse(&role).at_least(concord_governance::Role::Contributor) {
        return Err(Refused::NotAMember);
    }
    sqlx::query(
        "insert into merge_approvals (mr_id, user_id, weight, created_at) values ($1,$2,1.0,now())
         on conflict (mr_id, user_id) do nothing",
    )
    .bind(mr_id)
    .bind(user_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// §5.7's verdict for one merge request, with the inputs it is computed from.
///
/// # Errors
/// [`Refused::NotFound`] when the request is gone.
pub async fn gate(pool: &PgPool, mr: &MergeRequest) -> Result<MergeGateState, Refused> {
    let charter = crate::charter::charter(pool, mr.project_id).await?;
    let eligible_ids = crate::arena::eligible_collaborators(pool, mr.project_id, 0.0).await?;
    let eligible_count = eligible_ids.len() as i64;
    let approvals: Vec<(i64, String)> = sqlx::query_as(
        "select a.user_id, coalesce(m.role, '') from merge_approvals a
           left join members m on m.project_id = $1 and m.user_id = a.user_id
          where a.mr_id = $2",
    )
    .bind(mr.project_id)
    .bind(mr.id)
    .fetch_all(pool)
    .await?;
    let mut counted = 0i64;
    let mut reviewer = false;
    for (user_id, role) in approvals {
        if user_id == mr.author_id || !eligible_ids.contains(&user_id) {
            continue;
        }
        counted += 1;
        if !reviewer
            && concord_governance::Role::parse(&role).at_least(concord_governance::Role::Reviewer)
        {
            reviewer = true;
        }
    }
    let verdict = concord_governance::check_merge_gate(counted, eligible_count, reviewer, &charter);
    Ok(MergeGateState {
        approvals: verdict.approvals,
        needed: verdict.quorum_needed,
        eligible: eligible_count,
        quorum_ok: verdict.quorum_ok,
        reviewer_ok: verdict.reviewer_ok,
        requires_quorum: verdict.merge_requires_quorum,
        passed: verdict.passed(),
    })
}

/// §5.7's gate for one merge request by id.
///
/// # Errors
/// [`Refused::NotFound`].
pub async fn gate_for(pool: &PgPool, mr_id: i64) -> Result<MergeGateState, Refused> {
    let mr = get(pool, mr_id).await?.ok_or(Refused::NotFound)?;
    gate(pool, &mr).await
}

/// Land the change. The destructive one.
///
/// Three writes make the landing: the request closes, the feature becomes `shipped`, and every complaint linked to the feature
/// closes. One transaction, because a feature shipped without its complaints closed leaves a board that says the pain is
/// solved and a complaint list that says it is not.
///
/// # Errors
/// [`Refused::NotFound`] when the request is not in `project_id`, [`Refused::AlreadyClosed`], [`Refused::GateUnmet`].
pub async fn execute(pool: &PgPool, mr_id: i64, project_id: i64) -> Result<(), Refused> {
    let mr = get(pool, mr_id).await?.ok_or(Refused::NotFound)?;
    if mr.project_id != project_id {
        // Indistinguishable from a request that does not exist, the way every other project-scoped read behaves: a 403 would
        // confirm that the id is real somewhere else.
        return Err(Refused::NotFound);
    }
    if mr.status != "open" {
        return Err(Refused::AlreadyClosed(mr.status.clone()));
    }
    let state = gate(pool, &mr).await?;
    if !state.passed {
        return Err(Refused::GateUnmet(state.shortfall()));
    }
    let detail = if state.requires_quorum {
        "merged".to_string()
    } else {
        "merged without a quorum (the charter dropped it)".to_string()
    };

    let mut tx = pool.begin().await?;
    sqlx::query("update merge_requests set status = 'merged', closed_at = now() where id = $1")
        .bind(mr_id)
        .execute(&mut *tx)
        .await?;
    sqlx::query("update features set status = 'shipped', updated_at = now() where id = $1")
        .bind(mr.feature_id)
        .execute(&mut *tx)
        .await?;
    sqlx::query(
        "update complaints set status = 'closed', updated_at = now()
          where id in (select complaint_id from feature_complaints where feature_id = $1)
            and status <> 'closed'",
    )
    .bind(mr.feature_id)
    .execute(&mut *tx)
    .await?;
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,null,'merge','merge_request',$2,$3,now())",
    )
    .bind(project_id)
    .bind(mr_id)
    .bind(&detail)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(())
}

/// Refuse a merge request **without landing anything**: the feature keeps its status and its complaints stay open.
///
/// # Errors
/// [`Refused::NotFound`], [`Refused::AlreadyClosed`].
pub async fn reject(pool: &PgPool, mr_id: i64, project_id: i64) -> Result<(), Refused> {
    let mr = get(pool, mr_id).await?.ok_or(Refused::NotFound)?;
    if mr.project_id != project_id {
        return Err(Refused::NotFound);
    }
    if mr.status != "open" {
        return Err(Refused::AlreadyClosed(mr.status.clone()));
    }
    sqlx::query("update merge_requests set status = 'rejected', closed_at = now() where id = $1")
        .bind(mr_id)
        .execute(pool)
        .await?;
    sqlx::query(
        "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)
         values ($1,null,'merge_reject','merge_request',$2,'rejected',now())",
    )
    .bind(project_id)
    .bind(mr_id)
    .execute(pool)
    .await?;
    Ok(())
}
