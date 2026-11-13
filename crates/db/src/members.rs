//! A project's members, in the order the Go store returns them.
//!
//! The ordering is a rule and not a convenience: **most privileged first, then by id**. A list in join order would
//! put the person who arrived first at the top of the owner column, which reads as "most senior" to every human
//! who has ever used a members page — and the Go comment says username while the code ties on `user_id`, so the
//! comment is wrong and the code is what the live instance answers. The port follows the code and records that
//! disagreement here rather than reproducing either silently.

use sqlx::PgPool;

/// One membership.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Member {
    /// The project.
    pub project_id: i64,
    /// The person.
    pub user_id: i64,
    /// Their handle, joined from `users`.
    pub username: String,
    /// `guest`, `user`, `contributor`, `reviewer`, `maintainer` or `owner`.
    pub role: String,
    /// Whether they hold the moderation bit, which is separate from the role.
    pub is_moderator: bool,
    /// When they joined, unix seconds.
    pub joined_at: f64,
}

/// The rank of each role, highest first. A role outside the map ranks 0, exactly as the Go's `ranks[role]`
/// does for a missing key — a value the table's CHECK constraint is supposed to make impossible, and a list that
/// silently sorts it last is better than one that panics on it.
fn rank(role: &str) -> i32 {
    match role {
        "owner" => 5,
        "maintainer" => 4,
        "reviewer" => 3,
        "contributor" => 2,
        "user" => 1,
        _ => 0,
    }
}

/// Every member of a project, most privileged first.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, project_id: i64) -> Result<Vec<Member>, sqlx::Error> {
    let rows = sqlx::query_as::<_, (i64, i64, String, String, bool, f64)>(
        "select m.project_id, m.user_id, u.username, m.role, m.is_moderator,
                extract(epoch from m.joined_at)::double precision
           from members m join users u on u.id = m.user_id
          where m.project_id = $1",
    )
    .bind(project_id)
    .fetch_all(pool)
    .await?;
    let mut members: Vec<Member> = rows
        .into_iter()
        .map(|(project_id, user_id, username, role, is_moderator, joined_at)| Member {
            project_id,
            user_id,
            username,
            role,
            is_moderator,
            joined_at,
        })
        .collect();
    // Highest rank first, then by id: a stable order that does not depend on when somebody joined.
    members.sort_by(|left, right| {
        rank(&right.role)
            .cmp(&rank(&left.role))
            .then(left.user_id.cmp(&right.user_id))
    });
    Ok(members)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_ranks_order_owners_above_maintainers_and_a_stranger_last() {
        assert!(rank("owner") > rank("maintainer"));
        assert!(rank("maintainer") > rank("reviewer"));
        assert!(rank("reviewer") > rank("contributor"));
        assert!(rank("contributor") > rank("user"));
        assert!(rank("user") > rank("guest"));
        assert_eq!(rank("nonsense"), 0, "an unknown role sorts with guests rather than panicking");
    }
}
