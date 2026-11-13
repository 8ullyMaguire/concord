//! Feature tallies — the per-feature win/loss/skip counts behind the arena panel.
//!
//! **This ports a behaviour that is wrong, on purpose, and records it.** Three things here are the Go service's, not a
//! reasonable reading of the spec:
//!
//! * `Title` has no JSON tag, so the wire key is `"Title"` — every other field is snake_case. A port that "tidies"
//!   this into `title` breaks the deployed frontend, which reads the key the service actually sends.
//! * `votes_cast = wins + losses`, so **skips do not count as votes cast**. A feature with three wins, one loss and
//!   four skips reports `votes_cast: 4` while five people voted on it.
//! * the win/loss split is computed from the PAIR's outcome without checking which SIDE the feature was on:
//!   `SUM(CASE WHEN outcome IN ('a','both'))` counts for the row whichever of `feature_a`/`feature_b` it is, so a
//!   vote where this feature lost still increments its `wins`. In practice `wins` and `losses` are closer to
//!   "matches where the winner said a" and "matches where somebody said b", which is why they usually sum to the
//!   number of matches rather than partitioning it.
//!
//! Reproducing a defect is the right call for a port whose gate is "the same answers on the same data": fixing it here
//! would make the two services disagree and hide real port bugs inside a well-intentioned improvement. The defect is
//! recorded in the ledger for the cutover row, where it can be fixed once, knowingly, in one place.

use sqlx::PgPool;

/// One feature's tally.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Tally {
    /// The feature.
    pub feature_id: i64,
    /// Its title — the wire key is `Title`, capitalised, because the Go struct has no tag on this field.
    #[serde(rename = "Title")]
    pub title: String,
    /// Votes whose outcome was `a` or `both`, from EITHER side of the pair (see the module note).
    pub wins: i64,
    /// Votes whose outcome was `b` or `neither`, from either side.
    pub losses: i64,
    /// Votes whose outcome was `skip` or `neither`.
    pub skips: i64,
    /// `wins + losses` — deliberately NOT `wins + losses + skips`.
    pub votes_cast: i64,
}

/// Every feature of a project, by id, with its tallies.
///
/// Returns `None` for a project with no features, because the Go's `var out []FeatureTally` stays nil and marshals to
/// `null`. The port's other list routes answer `[]`; this one answers what the service answers.
///
/// # Errors
/// `sqlx::Error`.
pub async fn tallies(pool: &PgPool, project_id: i64) -> Result<Option<Vec<Tally>>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, String, i64, i64, i64)>(
        "select f.id, f.title,
                coalesce(sum(case when pv.outcome in ('a', 'both') then 1 else 0 end), 0)::bigint,
                coalesce(sum(case when pv.outcome in ('b', 'neither') then 1 else 0 end), 0)::bigint,
                coalesce(sum(case when pv.outcome in ('skip', 'neither') then 1 else 0 end), 0)::bigint
           from features f
           left join pairwise_votes pv on pv.feature_a = f.id or pv.feature_b = f.id
          where f.project_id = $1
          group by f.id, f.title
          order by f.id asc",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    if rows.is_empty() {
        return Ok(None);
    }
    Ok(Some(
        rows.into_iter()
            .map(|(feature_id, title, wins, losses, skips)| Tally {
                feature_id,
                title,
                wins,
                losses,
                skips,
                // The Go's arithmetic, kept exactly: skips are counted but not cast.
                votes_cast: wins + losses,
            })
            .collect(),
    ))
}

#[cfg(test)]
mod tests {
    use super::Tally;

    #[test]
    fn the_wire_key_for_the_title_is_capitalised_and_skips_are_not_cast() {
        let tally = Tally {
            feature_id: 7,
            title: "Arena".to_string(),
            wins: 3,
            losses: 1,
            skips: 4,
            votes_cast: 4,
        };
        let json = serde_json::to_value(&tally).unwrap();
        // The frontend reads `Title`; renaming it here would be a silent break at cutover.
        assert!(json.get("Title").is_some());
        assert!(json.get("title").is_none());
        // Eight votes happened; four were cast. That is the service's arithmetic, reproduced.
        assert_eq!(json["votes_cast"], 4);
        assert_eq!(json["skips"], 4);
    }
}
