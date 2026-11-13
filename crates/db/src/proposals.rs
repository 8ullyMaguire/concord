//! §8.6's sweep: mark proposals nobody decided as stale.
//!
//! A one-shot command under a timer, not a loop inside the server: the server restarts on every deploy, so a
//! background loop would lose its schedule and re-run the pass on each restart, and a pass that dies with the
//! process leaves nothing in the journal where a timer failure is visible.
//!
//! Three rules, in the order an operator meets them:
//!
//! 1. **Dry run first.** `--dry-run` prints the same list the sweep would mark. The first run against a live
//!    database is the only one that can surprise anyone, and it is the one an operator should be able to
//!    rehearse.
//! 2. **The cutoff is an operator decision.** `--ttl-days` overrides `CONCORD_PROPOSAL_TTL_DAYS`; `0` disables
//!    the sweep entirely, which is a legitimate setting for a quiet instance and not an error.
//! 3. **One line on success.** A timer's journal is read by `grep`, so the pass prints one line with the count
//!    and exits 0; silence would be indistinguishable from a timer that never fired.
//!
//! Two rules inside the pass, each of which is a way an expiry lies:
//!
//! * **zero rows affected means another writer decided it** between the SELECT and the UPDATE — reported as
//!   expired, the log would repeat the lie;
//! * **`actor_id` is NULL, and it stays NULL.** Recording a user there — the proposer, the reviewer, whoever
//!   last looked — would attribute an automatic expiry to a person.

use sqlx::{PgPool, Row};

use crate::now_unix;

/// One kind of proposal: its table, its consents table, and the audit vocabulary it uses.
struct Kind {
    /// The word a reader sees.
    name: &'static str,
    /// The proposals table.
    table: &'static str,
    /// The consents table, counted into the act's detail.
    consents: &'static str,
    /// The audit action.
    action: &'static str,
    /// The audit entity.
    entity: &'static str,
}

const KINDS: &[Kind] = &[
    Kind {
        name: "taxonomy",
        table: "taxonomy_proposals",
        consents: "taxonomy_consents",
        action: "stale_taxonomy_proposal",
        entity: "taxonomy_proposal",
    },
    Kind {
        name: "weight",
        table: "strategic_weight_proposals",
        consents: "strategic_weight_consents",
        action: "stale_weight_proposal",
        entity: "strategic_weight_proposal",
    },
    Kind {
        name: "theme",
        table: "strategic_theme_proposals",
        consents: "strategic_theme_consents",
        action: "stale_theme_proposal",
        entity: "strategic_theme_proposal",
    },
];

/// What one pass did.
pub struct Report {
    /// Proposals the pass expired (or would).
    pub total: usize,
    /// One line per proposal, for `--dry-run` and `--verbose`.
    pub lines: Vec<String>,
}

/// Mark every pending proposal created before `cutoff` as stale, one audit act each.
///
/// # Errors
/// `sqlx::Error`.
pub async fn sweep(pool: &PgPool, cutoff: f64, dry_run: bool) -> Result<Report, sqlx::Error> {
    let now = now_unix();
    let mut report = Report {
        total: 0,
        lines: Vec::new(),
    };
    let mut transaction = pool.begin().await?;
    for kind in KINDS {
        let sql = format!(
            "select p.id, p.project_id, p.proposed_by, \
                    (select count(*) from {} c where c.proposal_id = p.id) \
               from {} p where p.status = 'pending' and p.created_at < \
                    to_timestamp($1) order by p.created_at",
            kind.consents, kind.table
        );
        let rows = sqlx::query(&sql).bind(cutoff).fetch_all(&mut *transaction).await?;
        for row in rows {
            let id: i64 = row.try_get(0)?;
            let project_id: i64 = row.try_get(1)?;
            let proposed_by: Option<i64> = row.try_get(2).unwrap_or_default();
            let consents: i64 = row.try_get(3)?;
            if !dry_run {
                let updated = sqlx::query(&format!(
                    "update {} set status = 'stale' where id = $1 and status = 'pending'",
                    kind.table
                ))
                .bind(id)
                .execute(&mut *transaction)
                .await?;
                // Zero rows means somebody decided it between the SELECT and the UPDATE: reporting it as
                // expired would be a lie the log then repeats.
                if updated.rows_affected() == 0 {
                    continue;
                }
                let detail = serde_json::json!({
                    "reason": "no consensus within the configured lifetime",
                    "proposed_by": proposed_by,
                    "consents": consents,
                    "cutoff": cutoff,
                });
                sqlx::query(
                    "insert into audit_log (project_id, actor_id, action, entity, entity_id, detail, \
                                            created_at) values ($1, NULL, $2, $3, $4, $5, to_timestamp($6))",
                )
                .bind(project_id)
                .bind(kind.action)
                .bind(kind.entity)
                .bind(id)
                .bind(detail.to_string())
                .bind(now)
                .execute(&mut *transaction)
                .await?;
            }
            report.lines.push(format!(
                "{}\tstale\t{}\tproject={}\tproposer={}\tconsents={}",
                kind.name,
                id,
                project_id,
                proposed_by.unwrap_or_default(),
                consents
            ));
            report.total += 1;
        }
    }
    if dry_run {
        transaction.rollback().await?;
    } else {
        transaction.commit().await?;
    }
    Ok(report)
}
