//! The write half of the capability matrix: register a capability, assert a value against a project.
//!
//! The matrix is what Finder asks questions from, and a matrix nobody has asserted anything into carries no information: the question
//! selector picks by information gain over the candidate set, so with every capability absent from every project there is nothing to gain
//! and the engine correctly declines to ask anything — honest, and useless. These two writes are what turn the schema into questions.
//!
//! Three refusals the schema cannot express, each because the cheap alternative silently destroys a measured set:
//!
//! * **the key is canonicalised on write**, lowercased and trimmed: it is a namespace in the query language (`cap:wip-limits=yes`) and a
//!   primary key here, and two spellings of one capability would be two capabilities holding half the assertions each;
//! * **re-registering a capability updates its label and category and refuses to change its kind or value set**, because the assertions
//!   already recorded point at the old set and a changed set would make them unreadable without saying so;
//! * **a value must be one the capability permits** — an assertion of `maybe` against a boolean capability is a typo that would read
//!   downstream as information.
//!
//! Seeding writes assertions as a claim, never as a confirmed one: a seed file that could manufacture a quorum would let whoever edits the
//! file promote their own claims, and confirmation is supposed to mean two real accounts agreed.

use sqlx::PgPool;

/// A value a boolean capability permits.
pub const BOOLEAN_VALUES: [&str; 4] = ["yes", "partial", "no", "unknown"];

/// One registered capability.
#[derive(Debug, Clone)]
pub struct Capability {
    /// The canonical key.
    pub key: String,
    /// The human label.
    pub label: String,
    /// The category the matrix groups it under.
    pub category: String,
    /// `boolean` or `enum`.
    pub kind: String,
    /// The permitted values, in declaration order.
    pub values: Vec<String>,
}

/// Why a capability write was refused.
#[derive(Debug)]
pub enum Refused {
    /// A required field was empty.
    Invalid(String),
    /// The capability exists with a different kind or value set.
    ShapeChanged(String),
    /// The value is not one the capability permits.
    ValueNotAllowed {
        /// The value that was sent.
        value: String,
        /// The capability's permitted values.
        allowed: Vec<String>,
    },
}

impl std::fmt::Display for Refused {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refused::Invalid(message) => write!(formatter, "{message}"),
            Refused::ShapeChanged(message) => write!(formatter, "{message}"),
            Refused::ValueNotAllowed { value, allowed } => {
                write!(formatter, "{value:?} is not one of {allowed:?}")
            }
        }
    }
}

impl std::error::Error for Refused {}

/// A named deliberate defect, armed at runtime so a control run needs no rebuild.
fn armed(name: &str) -> bool {
    std::env::var("CONCORD_MUTANT")
        .unwrap_or_default()
        .split(',')
        .any(|mutant| mutant.trim() == name)
}

/// Canonicalise a capability key: lowercase, trimmed.
///
/// Done on the way in rather than on the way out: at read time one caller would forget, and the capability would quietly acquire a second
/// identity with the assertions split between the two.
#[must_use]
pub fn canonical_key(key: &str) -> String {
    key.trim().to_lowercase()
}

/// The value set a kind implies, or `None` for a kind that does not exist.
#[must_use]
pub fn values_for(kind: &str) -> Option<Vec<String>> {
    match kind {
        "boolean" => Some(BOOLEAN_VALUES.iter().map(|v| (*v).to_string()).collect()),
        "enum" => Some(Vec::new()),
        _ => None,
    }
}

/// Every capability, optionally filtered to one category, in category-then-key order.
///
/// # Errors
/// `sqlx::Error`.
pub async fn list(pool: &PgPool, category: Option<&str>) -> Result<Vec<Capability>, sqlx::Error> {
    let rows: Vec<(String, String, String, String, String)> = sqlx::query_as(
        "select key, label, category, kind, values from capabilities
          where ($1::text is null or category = $1)
          order by category, key",
    )
    .bind(category)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(key, label, category, kind, values)| Capability {
            key,
            label,
            category,
            kind,
            values: serde_json::from_str(&values).unwrap_or_default(),
        })
        .collect())
}

/// Register a capability, or update the label and category of one that already exists.
///
/// Returns `true` when the row was created and `false` when an existing one was updated — the caller prints a count, and "registered 40"
/// when nine were already there is a lie an operator repeats.
///
/// # Errors
/// [`Refused`] for a bad definition or a changed shape, `sqlx::Error` for the database.
pub async fn ensure(
    pool: &PgPool,
    key: &str,
    label: &str,
    category: &str,
    kind: &str,
    values: &[String],
) -> Result<bool, Box<dyn std::error::Error + Send + Sync>> {
    let key = canonical_key(key);
    if key.is_empty() {
        return Err(Box::new(Refused::Invalid("a capability key must not be empty".into())));
    }
    if label.trim().is_empty() {
        return Err(Box::new(Refused::Invalid(format!("capability {key:?} needs a label"))));
    }
    if category.trim().is_empty() {
        return Err(Box::new(Refused::Invalid(format!("capability {key:?} needs a category"))));
    }
    let mut permitted = values_for(kind)
        .ok_or_else(|| Box::new(Refused::Invalid(format!("capability kind {kind:?} must be boolean or enum"))))?;
    if kind == "enum" {
        permitted = values.iter().map(|v| v.trim().to_lowercase()).filter(|v| !v.is_empty()).collect();
        if permitted.is_empty() {
            return Err(Box::new(Refused::Invalid(format!(
                "enum capability {key:?} needs at least one value"
            ))));
        }
    }
    let encoded = serde_json::to_string(&permitted)?;

    let existing: Option<(String, String)> =
        sqlx::query_as("select kind, values from capabilities where key = $1")
            .bind(&key)
            .fetch_optional(pool)
            .await?;

    // The upsert deliberately does not touch `kind` or `values`, so the comparison below is against what is stored, not against a stale read.
    sqlx::query(
        "insert into capabilities (key, label, category, kind, values, created_at)
         values ($1, $2, $3, $4, $5, now())
         on conflict (key) do update set label = excluded.label, category = excluded.category",
    )
    .bind(&key)
    .bind(label.trim())
    .bind(category.trim())
    .bind(kind)
    .bind(&encoded)
    .execute(pool)
    .await?;

    if let Some((stored_kind, stored_values)) = existing {
        let stored: Vec<String> = serde_json::from_str(&stored_values).unwrap_or_default();
        // `capableseed-shape` accepts a changed kind or value set, which re-points every existing assertion at a vocabulary it was never
        // recorded against.
        if !armed("capableseed-shape") && (stored_kind != kind || stored != permitted) {
            return Err(Box::new(Refused::ShapeChanged(format!(
                "capability {key:?} is {stored_kind} over {stored:?} and cannot become {kind} over {permitted:?} while assertions exist"
            ))));
        }
        return Ok(false);
    }
    Ok(true)
}

/// Assert a value for a capability against a project, by slug.
///
/// One assertion per (capability, project): asserting again replaces the value, the evidence, the author and the time, because the matrix
/// answers "what is true now" and a history of a superseded claim is what the audit log is for.
///
/// # Errors
/// [`Refused`] when the capability does not exist, the value is not permitted, or the slug does not resolve; `sqlx::Error` for the database.
pub async fn assert_value(
    pool: &PgPool,
    capability: &str,
    slug: &str,
    value: &str,
    evidence: &str,
    author: i64,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let key = canonical_key(capability);
    if author == 0 {
        return Err(Box::new(Refused::Invalid("a capability assertion needs an author".into())));
    }
    let values: Option<String> = sqlx::query_scalar("select values from capabilities where key = $1")
        .bind(&key)
        .fetch_optional(pool)
        .await?;
    let values = values.ok_or_else(|| {
        Box::new(Refused::Invalid(format!("capability {key:?} is not registered")))
            as Box<dyn std::error::Error + Send + Sync>
    })?;
    let allowed: Vec<String> = serde_json::from_str(&values).unwrap_or_default();
    let value = value.trim().to_lowercase();
    if !allowed.contains(&value) {
        return Err(Box::new(Refused::ValueNotAllowed { value, allowed }));
    }
    let project: Option<i64> = sqlx::query_scalar("select id from projects where slug = $1")
        .bind(slug)
        .fetch_optional(pool)
        .await?;
    let project = project.ok_or_else(|| {
        Box::new(Refused::Invalid(format!("no project with slug {slug:?}")))
            as Box<dyn std::error::Error + Send + Sync>
    })?;
    sqlx::query(
        "insert into capability_assertions (capability, project_id, value, evidence, asserted_by, asserted_at)
         values ($1, $2, $3, $4, $5, now())
         on conflict (capability, project_id) do update set
             value = excluded.value,
             evidence = excluded.evidence,
             asserted_by = excluded.asserted_by,
             asserted_at = excluded.asserted_at",
    )
    .bind(&key)
    .bind(project)
    .bind(&value)
    .bind(evidence)
    .bind(author)
    .execute(pool)
    .await?;
    Ok(())
}

/// An account's id by username.
///
/// # Errors
/// `sqlx::Error`.
pub async fn user_id(pool: &PgPool, username: &str) -> Result<Option<i64>, sqlx::Error> {
    sqlx::query_scalar("select id from users where username = $1")
        .bind(username)
        .fetch_optional(pool)
        .await
}
