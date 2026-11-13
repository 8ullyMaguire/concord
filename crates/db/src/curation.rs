//! §4's curation queue: one page over every proposal kind and every moderation act.
//!
//! The shape is a **projection, not a model**: each kind keeps its own table and its own rules, and this
//! exists so one page can list them and one filter set narrow them. The UNION's column order IS the scan
//! order below, which is why the SQL and the reader sit in one file.
//!
//! Three rules that are easy to get wrong and are therefore stated here:
//!
//! * **`open` is not a state any row carries.** A proposal is open while it is `pending`; an act is open
//!   while it is `applied` — an act has no pending phase, it exists only after somebody did it. A default
//!   filter of the literal "pending" would show every proposal and no act at all, which is the half of this
//!   queue the owner asked for.
//! * **the cursor is (opened_at, kind, id), all three.** `ledger.Cursor` is `(at, id)` and cannot express
//!   this order: two kinds can share an id and a millisecond, and a page boundary landing exactly there
//!   would drop a row.
//! * **a malformed cursor is an error, never a silent first page**, because a silent first page turns a
//!   paging bug into an infinite scroll.

use serde::Serialize;
use sqlx::PgPool;

/// The most a caller may ask for.
pub const MAX_LIMIT: i64 = 100;

/// How many dissenters make an item contested — the instance's existing answer, not a second threshold
/// invented for this page.
pub const DISPUTE_QUORUM: i64 = 2;

/// The acts this queue reviews. Everything else in the audit log is a fact, not a pending question.
pub const CURATION_ACTIONS: [&str; 4] =
    ["arena.remove", "arena.restore", "feature.reject", "feature.unreject"];

/// One row of the queue: a proposal of any kind, or a moderation act.
#[derive(Debug, Clone, Serialize)]
pub struct Item {
    /// `taxonomy`, `strategy` or `act`.
    pub kind: String,
    /// The row in its own table.
    pub id: i64,
    /// The project it belongs to, 0 for instance-wide.
    pub project_id: i64,
    /// Its slug, so the page can link without a second read.
    pub project_slug: String,
    /// Who proposed or acted.
    pub actor_id: i64,
    /// What was proposed or done.
    pub action: String,
    /// What it was about.
    pub target: String,
    /// The one-line summary a reviewer reads first.
    pub summary: String,
    /// `pending`, `ratified`, `rejected`, `withdrawn`, `applied` or `undone`.
    pub state: String,
    /// When it entered the queue.
    pub opened_at: f64,
    /// When it was decided, 0 while open.
    pub decided_at: f64,
    /// Consents or approve reviews.
    pub yes: i64,
    /// Rejections.
    pub no: i64,
    /// Derived, never stored: the instance's existing dissent threshold.
    pub contested: bool,
    /// 1, -1 or 0.
    pub my_vote: i64,
    /// Filled here rather than in the store, because eligibility depends on the request's actor and on
    /// rules the store does not own. A page that renders the button regardless offers an action the server
    /// will refuse, and the refusal reads as a broken page rather than as a rule.
    pub can_vote: bool,
}

/// One page of the queue plus the position after it.
#[derive(Debug, Clone, Serialize)]
pub struct Page {
    /// The rows.
    pub items: Vec<Item>,
    /// Omitted when this is the last page.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub next_cursor: String,
    /// The limit that was applied after clamping.
    pub limit: i64,
}

/// What to narrow the queue by. The zero value matches everything, which is what the log view wants.
#[derive(Debug, Clone, Default)]
pub struct Filter {
    /// Only this kind.
    pub kind: String,
    /// `open`, or a literal state.
    pub state: String,
    /// Only this project.
    pub project_id: i64,
    /// Only this actor.
    pub actor_id: i64,
    /// Substring of the summary, target or action.
    pub text: String,
    /// Opened at or after.
    pub since: f64,
    /// Opened at or before.
    pub until: f64,
    /// Restrict to rows this reader has not voted on, and did not propose.
    pub unvoted_by: i64,
    /// The page position.
    pub cursor: String,
    /// How many to return.
    pub limit: i64,
    /// The reader, for `my_vote`.
    pub viewer_id: i64,
}

/// The queue, narrowed and paged.
///
/// # Errors
/// [`sqlx::Error::Decode`] for a malformed cursor, deliberately, so a paging bug is loud.
pub async fn list(pool: &PgPool, filter: &Filter) -> Result<Page, sqlx::Error> {
    use sqlx::Row;
    let limit = if filter.limit <= 0 || filter.limit > MAX_LIMIT { 20 } else { filter.limit };

    // The viewer's id twice, and first: the act branch's vote value and the proposal branch's consent test.
    // `$1` and `$2` rather than a repeated literal so a caller cannot forget one.
    let mut args: Vec<Arg> = vec![Arg::Int(filter.viewer_id), Arg::Int(filter.viewer_id)];
    let mut where_parts: Vec<String> = vec!["1=1".to_string()];

    if !filter.kind.is_empty() {
        where_parts.push(format!("q.kind = ${}", args.len() + 1));
        args.push(Arg::Text(filter.kind.clone()));
    }
    if filter.state == "open" {
        where_parts.push(
            "((q.kind = 'act' AND q.state = 'applied') OR (q.kind <> 'act' AND q.state = 'pending'))"
                .to_string(),
        );
    } else if !filter.state.is_empty() {
        where_parts.push(format!("q.state = ${}", args.len() + 1));
        args.push(Arg::Text(filter.state.clone()));
    }
    if filter.project_id != 0 {
        where_parts.push(format!("q.project_id = ${}", args.len() + 1));
        args.push(Arg::Int(filter.project_id));
    }
    if filter.actor_id != 0 {
        where_parts.push(format!("q.actor_id = ${}", args.len() + 1));
        args.push(Arg::Int(filter.actor_id));
    }
    if !filter.text.is_empty() {
        // Escaped like the audit log's search: a `%` in a search term is a literal, not a wildcard.
        let escaped = filter
            .text
            .replace('\\', "\\\\")
            .replace('%', "\\%")
            .replace('_', "\\_");
        let next = args.len() + 1;
        where_parts.push(format!(
            "(q.summary like ${next} escape '\\' or q.target like ${next} escape '\\' \
             or q.action like ${next} escape '\\')"
        ));
        args.push(Arg::Text(format!("%{escaped}%")));
    }
    if filter.since != 0.0 {
        where_parts.push(format!("q.opened_at >= ${}", args.len() + 1));
        args.push(Arg::Float(filter.since));
    }
    if filter.until != 0.0 {
        where_parts.push(format!("q.opened_at <= ${}", args.len() + 1));
        args.push(Arg::Float(filter.until));
    }
    if filter.unvoted_by != 0 {
        // Their own proposals are excluded too: a queue that shows you your own work never empties.
        where_parts.push(format!("q.actor_id <> ${}", args.len() + 1));
        args.push(Arg::Int(filter.unvoted_by));
        where_parts.push(format!(
            "not exists (select 1 from curation_votes v where v.kind = q.kind \
             and v.ref_id = q.id and v.user_id = ${})",
            args.len() + 1
        ));
        args.push(Arg::Int(filter.unvoted_by));
    }
    if !filter.cursor.is_empty() {
        let cursor = decode_cursor(&filter.cursor)?;
        let base = args.len();
        // Strictly after the cursor in (opened_at desc, kind asc, id asc) scan order.
        where_parts.push(format!(
            "(q.opened_at < ${a} or (q.opened_at = ${a} and (q.kind > ${b} or (q.kind = ${b} and q.id > ${c}))))",
            a = base + 1,
            b = base + 2,
            c = base + 3
        ));
        args.push(Arg::Float(cursor.at));
        args.push(Arg::Text(cursor.kind.clone()));
        args.push(Arg::Int(cursor.id));
    }

    let limit_placeholder = args.len() + 1;
    args.push(Arg::Int(limit + 1));
    let sql = format!(
        "{QUEUE_SQL} where {} order by q.opened_at desc, q.kind asc, q.id asc limit ${limit_placeholder}",
        where_parts.join(" and ")
    );

    // The placeholders are positional and were built by counting, so they are rebound in order rather than
    // typed: a mismatch is a runtime error on the first run of a filter, not a wrong page.
    let mut query = sqlx::query(&sql);
    for value in &args {
        // Typed binds, because the comparison is against a double precision, a bigint or a text column and
        // PostgreSQL does not infer through a uniformly-text parameter: `q.opened_at >= $3` with a text
        // argument is `operator does not exist: double precision >= text`, not a coercion.
        query = match value {
            Arg::Int(value) => query.bind(*value),
            Arg::Float(value) => query.bind(*value),
            Arg::Text(value) => query.bind(value.clone()),
        };
    }
    let rows = query.fetch_all(pool).await?;

    let mut items = Vec::with_capacity(rows.len());
    for row in &rows {
        let no: i64 = row.try_get(12)?;
        items.push(Item {
            kind: row.try_get(0)?,
            id: row.try_get(1)?,
            project_id: row.try_get(2)?,
            project_slug: row.try_get(3)?,
            actor_id: row.try_get(4)?,
            action: row.try_get(5)?,
            target: row.try_get(6)?,
            summary: row.try_get(7)?,
            state: row.try_get(8)?,
            opened_at: row.try_get(9)?,
            decided_at: row.try_get(10)?,
            yes: row.try_get(11)?,
            no,
            contested: no >= DISPUTE_QUORUM,
            my_vote: row.try_get(13)?,
            can_vote: false,
        });
    }

    let mut page = Page { items, next_cursor: String::new(), limit };
    if page.items.len() as i64 > limit {
        let last = page.items[limit as usize - 1].clone();
        page.items.truncate(limit as usize);
        page.next_cursor = encode_cursor(&last);
    }
    Ok(page)
}

/// One bound parameter, typed for the column it is compared with.
enum Arg {
    /// A row id.
    Int(i64),
    /// A unix timestamp.
    Float(f64),
    /// A name, a state, or a search pattern.
    Text(String),
}

/// The UNION, ported with PostgreSQL's explicit casts.
///
/// `||` needs text on both sides there, where SQLite concatenated anything: `'feature:' || s.feature_id`
/// is a type error, not a string, and the casts below are the only change the projection needed.
const QUEUE_SQL: &str = "
with queue as (
    select 'taxonomy' as kind, p.id as id, coalesce(p.project_id, 0) as project_id,
           p.proposed_by as actor_id, p.action as action,
           p.target_tag as target, coalesce(p.rationale, '') as summary,
           p.status as state, extract(epoch from p.created_at)::double precision as opened_at,
           coalesce(extract(epoch from p.decided_at), 0)::double precision as decided_at,
           (select count(*) from taxonomy_consents c where c.proposal_id = p.id) as yes_count,
           0::bigint as no_count
      from taxonomy_proposals p
    union all
    select 'strategy', s.id, s.project_id, s.proposed_by, 'weight',
           'feature:' || s.feature_id::text, coalesce(s.rationale, ''),
           s.status, extract(epoch from s.created_at)::double precision,
           coalesce(extract(epoch from s.decided_at), 0)::double precision,
           (select count(*) from strategic_weight_consents c where c.proposal_id = s.id),
           0::bigint
      from strategic_weight_proposals s
    union all
    select 'act', a.id, coalesce(a.project_id, 0), coalesce(a.actor_id, 0),
           a.action,
           coalesce(a.entity, '') || ':' || coalesce(a.entity_id, 0)::text,
           coalesce(a.detail, ''),
           case when exists (select 1 from audit_log u where u.undoes = a.id) then 'undone'
                else 'applied' end,
           extract(epoch from a.created_at)::double precision, 0::double precision,
           (select count(*) from act_reviews r where r.audit_id = a.id and r.vote = 1),
           (select count(*) from act_reviews r where r.audit_id = a.id and r.vote = -1)
      from audit_log a
     where a.action in ('arena.remove','arena.restore','feature.reject','feature.unreject')
)
select q.kind, q.id, q.project_id, coalesce(pr.slug, ''), q.actor_id, q.action, q.target,
       q.summary, q.state, q.opened_at, q.decided_at, q.yes_count, q.no_count,
       case
         when q.kind = 'act' then coalesce(
              (select r.vote from act_reviews r where r.audit_id = q.id and r.user_id = $1), 0)
         when exists (select 1 from curation_votes x
                       where x.kind = q.kind and x.ref_id = q.id and x.user_id = $2) then 1
         else 0
       end as my_vote
  from queue q
  left join projects pr on pr.id = q.project_id";

/// A cursor's position: the sort key of the queue, in full.
#[derive(Debug, Clone)]
struct Cursor {
    at: f64,
    kind: String,
    id: i64,
}

fn encode_cursor(item: &Item) -> String {
    format!("{:.6}|{}|{}", item.opened_at, item.kind, item.id)
}

fn decode_cursor(raw: &str) -> Result<Cursor, sqlx::Error> {
    let parts: Vec<&str> = raw.split('|').collect();
    if parts.len() != 3 {
        return Err(sqlx::Error::Decode("this cursor is not a position in the queue".into()));
    }
    let at = parts[0]
        .parse::<f64>()
        .map_err(|_| sqlx::Error::Decode("this cursor is not a position in the queue".into()))?;
    let id = parts[2]
        .parse::<i64>()
        .map_err(|_| sqlx::Error::Decode("this cursor is not a position in the queue".into()))?;
    if parts[1].is_empty() {
        return Err(sqlx::Error::Decode("this cursor is not a position in the queue".into()));
    }
    Ok(Cursor { at, kind: parts[1].to_string(), id })
}
