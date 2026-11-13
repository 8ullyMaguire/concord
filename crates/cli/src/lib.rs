//! The operator binary. One entry point, subcommands as arguments — the Go tree spread these across
//! `cmd/*` binaries, and one binary with `serve` as a subcommand is the estate's shape.
//!
//! Usage:
//!     concord-cli serve --addr 127.0.0.1:8106        # CONCORD_DB_URL names the database
//!     concord-cli check                              # connect, run one query, exit
//!     concord-cli statsnapshot [--dry-run] [--day YYYY-MM-DD]
//!     concord-cli proposalsweep [--dry-run] [--ttl-days N] [--verbose]
//!     concord-cli healthsync --from measurements.json [--dry-run]
//!     concord-cli healthsync --newcomer slug=hours,sample ...
//!
//! The two timer commands follow the same three rules the Go ones do, because a timer's output is read by an
//! operator who is looking for a reason it failed:
//!
//! * **`--dry-run` first.** The first run against a live database is the only one that can surprise anybody,
//!   and it is the one an operator must be able to rehearse.
//! * **one line on success.** Silence and success must not look alike: silence is indistinguishable from a
//!   timer that never fired.
//! * **a non-zero exit on failure**, so systemd records it rather than a journal quietly holding a traceback
//!   from a process that reported success.

use std::net::SocketAddr;

/// Arguments for one command: `--flag` booleans and `--key value` pairs.
struct Args {
    /// The subcommand.
    command: String,
    /// `--flags` with no value.
    flags: std::collections::HashSet<String>,
    /// `--key value` pairs.
    values: std::collections::HashMap<String, String>,
}

impl Args {
    /// Read `argv`. A flag whose neighbour is another flag has no value.
    fn parse() -> anyhow::Result<Self> {
        let mut args = std::env::args().skip(1);
        let command = args.next().unwrap_or_else(|| "serve".to_string());
        let mut flags = std::collections::HashSet::new();
        let mut values = std::collections::HashMap::new();
        let rest: Vec<String> = args.collect();
        let mut index = 0;
        while index < rest.len() {
            let arg = rest[index].clone();
            if !arg.starts_with("--") {
                anyhow::bail!("unexpected argument {arg}");
            }
            let name = arg.trim_start_matches("--").to_string();
            let takes_value = rest
                .get(index + 1)
                .is_some_and(|next| !next.starts_with("--"));
            if takes_value {
                values.insert(name, rest[index + 1].clone());
                index += 2;
            } else {
                flags.insert(name);
                index += 1;
            }
        }
        Ok(Self {
            command,
            flags,
            values,
        })
    }

    /// Whether a boolean flag was given.
    fn flag(&self, name: &str) -> bool {
        self.flags.contains(name)
    }

    /// A flag's value.
    fn value(&self, name: &str) -> Option<&str> {
        self.values.get(name).map(String::as_str)
    }
}

/// Today in UTC, as `YYYY-MM-DD`.
fn today() -> String {
    chrono::Utc::now().format("%Y-%m-%d").to_string()
}

/// Run the binary. Arguments are read positionally: a subcommand, then `--key value` pairs.
///
/// # Errors
/// Anything the caller must act on: an unreachable database, a port already bound, a bad address.
/// A service that starts and silently answers 500 is worse than one that refuses to start.
pub async fn run() -> anyhow::Result<()> {
    // LOGS GO TO STDERR, and the summary goes to stdout. A timer's journal captures both, but an operator
    // reading `concord-cli statsnapshot | grep day=` must get the summary and not a slow-query warning --
    // measured before the change: the first stdout line was `WARN sqlx::query: slow statement …`.
    tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,sqlx=warn".into()),
        )
        .init();

    let args = Args::parse()?;
    let addr = args.value("addr").unwrap_or("127.0.0.1:8106").to_string();

    let url = std::env::var("CONCORD_DB_URL")
        .or_else(|_| std::env::var("DATABASE_URL"))
        .map_err(|_| anyhow::anyhow!("CONCORD_DB_URL (or DATABASE_URL) is not set"))?;
    let pool = concord_db::pool(&url).await?;

    match args.command.as_str() {
        "serve" => {
            let state = concord_app::AppState { pool };
            let listener = tokio::net::TcpListener::bind(&addr).await?;
            tracing::info!(addr = %addr, "serving");
            axum::serve(
                listener,
                concord_app::router(state).into_make_service_with_connect_info::<SocketAddr>(),
            )
            .await?;
            Ok(())
        }
        "check" => {
            // One round trip: a pool that connected but cannot query is not a working database.
            let projects = concord_db::projects::list_projects(&pool).await?;
            println!("{} project(s)", projects.len());
            Ok(())
        }
        "healthsync" => healthsync(&pool, &args).await,
        "statsnapshot" => statsnapshot(&pool, &args).await,
        "proposalsweep" => proposalsweep(&pool, &args).await,
        other => anyhow::bail!("unknown command {other}"),
    }
}

/// `statsnapshot` — record today's row, and one row per project.
async fn statsnapshot(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    let day = match args.value("day") {
        Some(value) => {
            chrono::NaiveDate::parse_from_str(value, "%Y-%m-%d")
                .map_err(|_| anyhow::anyhow!("bad --day {value}: want YYYY-MM-DD"))?;
            value.to_string()
        }
        None => today(),
    };
    let snapshot = concord_db::snapshot::compute(pool, &day).await?;
    let projects = concord_db::snapshot::compute_projects(pool).await?;
    // An unmeasured median prints `n/a`, never `0.0`: the console is where an operator first sees the number,
    // and a zero there is the best possible answer.
    let median = |value: Option<f64>| match value {
        Some(hours) => format!("{hours:.1}"),
        None => "n/a".to_string(),
    };
    if args.flag("dry-run") {
        println!(
            "day={} projects={} measured={} features={} seed={} complaints={} cseed={} no_origin={} \
             triage={}(n={}) delivery={}(n={}) decision={}(n={})",
            snapshot.day,
            snapshot.projects,
            snapshot.projects_measured,
            snapshot.features_total,
            snapshot.features_seed,
            snapshot.complaints_total,
            snapshot.complaints_seed,
            snapshot.no_origin,
            median(snapshot.triage_median_h),
            snapshot.triage_n,
            median(snapshot.delivery_median_h),
            snapshot.delivery_n,
            median(snapshot.decision_median_h),
            snapshot.decision_n,
        );
        println!("project rows={}", projects.len());
        return Ok(());
    }
    concord_db::snapshot::write(pool, &snapshot).await?;
    concord_db::snapshot::write_projects(pool, &day, &projects).await?;
    println!("recorded {} ({} project rows)", day, projects.len());
    Ok(())
}

/// `proposalsweep` — mark proposals nobody decided as stale.
async fn proposalsweep(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    let dry_run = args.flag("dry-run");
    // The cutoff is an operator decision. A TTL of zero DISABLES the sweep, which is a legitimate setting for a
    // quiet instance and not an error.
    let days: i64 = match args.value("ttl-days") {
        Some(raw) => raw
            .parse()
            .map_err(|_| anyhow::anyhow!("bad --ttl-days {raw}: want a whole number of days"))?,
        None => std::env::var("CONCORD_PROPOSAL_TTL_DAYS")
            .ok()
            .and_then(|raw| raw.parse().ok())
            .unwrap_or(30),
    };
    if days <= 0 {
        println!("proposalsweep: disabled (ttl {days} days); nothing to do");
        return Ok(());
    }
    let cutoff = (chrono::Utc::now() - chrono::Duration::days(days)).timestamp() as f64;
    let report = concord_db::proposals::sweep(pool, cutoff, dry_run).await?;
    if dry_run || args.flag("verbose") {
        for line in &report.lines {
            println!("{line}");
        }
    }
    let verb = if dry_run { "would mark" } else { "marked" };
    println!(
        "proposalsweep: {verb} {} proposal(s) older than {days} days as stale",
        report.total
    );
    Ok(())
}

/// `healthsync` — write forge measurements over `project_metrics`.
///
/// Two inputs, because the two halves of the row come from different places:
///
/// * `--from <file>` — the measurements the forge answered with, one JSON object per project. This is where the
///   fetch lives: **the fetching is NOT ported** (see the ledger), because no estate crate owns a forge client
///   and a third hand-rolled GitHub client is exactly the component the estate's inventory exists to prevent.
///   Until one exists, the dump is produced by the Go `healthsync -dump` and this writer applies it, which is
///   the same split the Go binary itself has between fetching and storing.
/// * `--newcomer slug=hours,sample` — the responsiveness figure computed from `forge_events`, which is a reading
///   of this database rather than of the forge.
///
/// Three rules, and the gate asserts each of them:
///
/// 1. a measurement the forge did not return (`"measured": false`) writes **nothing** — not zeros, not a fresh
///    `computed_at` over stale numbers;
/// 2. an unknown review time is written as `NULL`, never as `-1`;
/// 3. one scorer: the stored score is `concord_ranking::health::health_score` of the metrics stored beside it.
async fn healthsync(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    use concord_db::metrics::{self, Measurement};

    if let Some(list) = args.value("newcomer") {
        for pair in list.split_whitespace() {
            let (slug, rest) = pair
                .split_once('=')
                .ok_or_else(|| anyhow::anyhow!("--newcomer wants slug=hours,sample, got {pair}"))?;
            let response = if rest.is_empty() || rest == "none" {
                None
            } else {
                let (hours, sample) = rest
                    .split_once(',')
                    .ok_or_else(|| anyhow::anyhow!("--newcomer wants hours,sample, got {rest}"))?;
                Some((hours.parse::<f64>()?, sample.parse::<usize>()?))
            };
            metrics::write_newcomer(pool, slug, response).await?;
            println!("newcomer {slug}: {}", match response {
                Some((hours, sample)) => format!("{hours:.1}h over {sample} first pull requests"),
                None => "no number (below the sample floor)".to_string(),
            });
        }
        return Ok(());
    }

    let path = args
        .value("from")
        .ok_or_else(|| anyhow::anyhow!("healthsync needs --from <measurements.json> or --newcomer …"))?;
    let raw = std::fs::read_to_string(&path)
        .map_err(|error| anyhow::anyhow!("reading {path}: {error}"))?;
    let parsed: Vec<serde_json::Value> = serde_json::from_str(&raw)
        .map_err(|error| anyhow::anyhow!("parsing {path}: {error}"))?;
    let dry_run = args.flags.contains("dry-run");
    let (mut written, mut unmeasured, mut unknown) = (0_usize, 0_usize, 0_usize);
    for entry in parsed {
        let measurement = Measurement {
            slug: text(&entry, "slug")?,
            // Absent means unmeasured: a dump that omits the field because the fetch failed must not be read as
            // a successful measurement of zero.
            measured: entry.get("measured").and_then(serde_json::Value::as_bool).unwrap_or(false),
            stars: integer(&entry, "stars").unwrap_or(0),
            forks: integer(&entry, "forks").unwrap_or(0),
            open_issues: integer(&entry, "open_issues").unwrap_or(0),
            commit_count: integer(&entry, "commit_count").unwrap_or(0),
            contributors: integer(&entry, "contributors").unwrap_or(0),
            last_commit_age_days: float(&entry, "last_commit_age_days")
                .unwrap_or(concord_db::metrics::UNKNOWN_AGE),
            median_review_hours: float(&entry, "median_review_hours")
                .unwrap_or(concord_db::metrics::UNKNOWN_REVIEW_HOURS),
            releases_90d: integer(&entry, "releases_90d").unwrap_or(0),
            license: entry.get("license").and_then(serde_json::Value::as_str).map(str::to_string),
        };
        if dry_run {
            println!(
                "dry-run {}: measured={} stars={} review={}",
                measurement.slug, measurement.measured, measurement.stars, measurement.median_review_hours
            );
            continue;
        }
        match metrics::sync(pool, &measurement).await? {
            Ok(synced) => {
                written += 1;
                println!(
                    "synced {}: health {:.6}{}",
                    measurement.slug,
                    synced.health_score,
                    if synced.responsiveness_known { "" } else { " (responsiveness unknown, renormalised)" }
                );
            }
            Err(concord_db::metrics::Skipped::Unmeasured) => {
                unmeasured += 1;
                println!("skipped {}: the forge did not answer; nothing written", measurement.slug);
            }
            Err(concord_db::metrics::Skipped::UnknownProject) => {
                unknown += 1;
                println!("skipped {}: no such project", measurement.slug);
            }
        }
    }
    println!("healthsync: {written} written, {unmeasured} unmeasured, {unknown} unknown");
    Ok(())
}

fn text(entry: &serde_json::Value, key: &str) -> anyhow::Result<String> {
    entry
        .get(key)
        .and_then(serde_json::Value::as_str)
        .map(str::to_string)
        .ok_or_else(|| anyhow::anyhow!("a measurement has no {key}"))
}

fn integer(entry: &serde_json::Value, key: &str) -> Option<i64> {
    entry.get(key).and_then(serde_json::Value::as_i64)
}

fn float(entry: &serde_json::Value, key: &str) -> Option<f64> {
    entry.get(key).and_then(serde_json::Value::as_f64)
}
