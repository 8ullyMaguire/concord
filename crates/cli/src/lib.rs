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

pub mod forge;

use std::net::SocketAddr;

/// Arguments for one command: `--flag` booleans and `--key value` pairs.
struct Args {
    /// The subcommand.
    command: String,
    /// `--flags` with no value.
    flags: std::collections::HashSet<String>,
    /// `--key value` pairs.
    values: std::collections::HashMap<String, String>,
    /// Bare arguments, in order. The Go binaries took positionals (`granttrust <user-id> <level>`) and an operator copying a documented
    /// line should not have to learn a second spelling; flags win where both are given.
    positional: Vec<String>,
}

impl Args {
    /// Read `argv`. A flag whose neighbour is another flag has no value.
    fn parse() -> anyhow::Result<Self> {
        let mut args = std::env::args().skip(1);
        let command = args.next().unwrap_or_else(|| "serve".to_string());
        let mut flags = std::collections::HashSet::new();
        let mut values = std::collections::HashMap::new();
        let mut positional = Vec::new();
        let rest: Vec<String> = args.collect();
        let mut index = 0;
        while index < rest.len() {
            let arg = rest[index].clone();
            if !arg.starts_with("--") {
                positional.push(arg);
                index += 1;
                continue;
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
            positional,
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

    /// The `n`th bare argument, if one was given.
    fn arg(&self, n: usize) -> Option<&str> {
        self.positional.get(n).map(String::as_str)
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
            let state = concord_app::AppState {
                pool,
                embedder: concord_app::embedder_from_env(),
            };
            // A configuration variable whose absence only shows up as a 503 on twelve routes is a variable nobody will set.
            // `CONCORD_BASE_URL` is what every feed builds its absolute `<id>` and `<guid>` from, and the feeds refuse to invent
            // one — so the operator reads the consequence HERE, at boot, rather than from a reader that silently shows nothing.
            // Not fatal: everything except the feeds works without it, and a service that will not start over a feed is worse.
            if std::env::var("CONCORD_BASE_URL").unwrap_or_default().is_empty() {
                eprintln!(
                    "WARNING: CONCORD_BASE_URL is unset — all 13 feed routes will answer 503. \
                     Set it to this site's public origin (e.g. https://concord.polarisocial.xyz)."
                );
            }
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
        "granttrust" => granttrust(&pool, &args).await,
        "capableseed" => capableseed(&pool, &args).await,
        "forgeseed" => forgeseed(&pool, &args).await,
        "embedbackfill" => embedbackfill(&pool, &args).await,
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

/// `embedbackfill` — index existing complaints, features and requests for duplicate detection.
///
/// `embedbackfill [--kind complaint] [--status]`
///
/// The index is not populated by the migration, for a reason worth keeping in view: embedding every row on startup would block the listen
/// path, and a PARTIAL index is worse than none because it produces "no duplicates found" — which a filer reads as "I am the first to report
/// this". So the index is built deliberately, by an operator, and reported when it is done.
///
/// `--status` reports coverage without writing. That mode exists because the question an operator asks before and after a run is the same
/// one, and answering it the same way both times is what makes the numbers comparable.
///
/// The report distinguishes three states, because two of them look identical in a percentage: `OK` (every embeddable row indexed),
/// `INCOMPLETE` (duplicate detection will miss rows), and `NO ROWS` (nothing to index yet — a 0.0% that is not a failure and not a success).
async fn embedbackfill(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    let embedder = concord_app::embedder_from_env();
    let kinds: Vec<&str> = match args.value("kind") {
        Some(kind) => vec![match kind {
            "complaint" => concord_db::embeddings::KIND_COMPLAINT,
            "feature" => concord_db::embeddings::KIND_FEATURE,
            "request" => concord_db::embeddings::KIND_REQUEST,
            "project" => concord_db::embeddings::KIND_PROJECT,
            other => anyhow::bail!("unknown kind {other:?}: want complaint, feature, request or project"),
        }],
        None => vec![
            concord_db::embeddings::KIND_COMPLAINT,
            concord_db::embeddings::KIND_FEATURE,
            concord_db::embeddings::KIND_REQUEST,
            concord_db::embeddings::KIND_PROJECT,
        ],
    };
    // The embedder identity goes to stderr with the rest of the logs: a backfill with the wrong model writes vectors the server will not
    // read, and the model id is the only way to see which one wrote them.
    eprintln!("embedder {} ({} dims)", embedder.id(), embedder.dims());

    // The embedder must be the one the table already holds. A backfill run with the wrong model writes vectors no reader will use — and
    // worse, leaves TWO models' vectors in one table, so a later reader comparing against the wrong one silently scores nonsense. The Go
    // tree stated this as a comment ("$CONCORD_EMBED_URL must be set identically for both") and a comment refuses nothing; this was found by
    // running the command against a live database whose index is `nomic` and watching `hashed-v1` appear beside it.
    let stored_models: Vec<(String, i64)> =
        sqlx::query_as("select model_id, count(*) from embeddings group by 1 order by 2 desc")
            .fetch_all(pool)
            .await?;
    let mine = stored_models.iter().any(|(model, _)| model.as_str() == embedder.id());
    if !args.flag("status") && !mine && !stored_models.is_empty() && !args.flag("force") {
        let names: Vec<String> = stored_models
            .iter()
            .map(|(model, count)| format!("{model} ({count})"))
            .collect();
        anyhow::bail!(
            "the index holds {} and nothing from {}; point CONCORD_EMBED_URL/model at the same model, or pass --force to re-embed under a new one",
            names.join(", "),
            embedder.id()
        );
    }

    if !args.flag("status") {
        // `embedbackfill-title-only` indexes the title and not the full text, leaving every entity half-indexed while coverage (which counts
        // the full-text field) reports it correctly — the panel then compares against a title alone.
        let title_only = std::env::var("CONCORD_MUTANT").unwrap_or_default().split(',').any(|m| m.trim() == "embedbackfill-title-only");
        for kind in &kinds {
            let done = if title_only {
                concord_db::embeddings::backfill_titles_only(pool, embedder.as_ref(), kind).await?
            } else {
                concord_db::embeddings::backfill(pool, embedder.as_ref(), kind).await?
            };
            println!("{kind:<11} embedded {done}");
        }
        println!();
    }

    for kind in &kinds {
        let coverage = concord_db::embeddings::coverage(pool, embedder.as_ref(), kind).await?;
        let mark = if coverage.indexed == Some(false) {
            "NO BACKING TABLE -- nothing to index yet".to_string()
        } else if coverage.total == 0 {
            "NO ROWS -- nothing to index yet".to_string()
        } else if coverage.complete == Some(true) {
            "OK".to_string()
        } else {
            "INCOMPLETE -- duplicate detection will miss rows".to_string()
        };
        let percent = coverage.coverage.unwrap_or(0.0) * 100.0;
        println!("{kind:<11} {percent:6.1}%  {mark} ({} rows)", coverage.total);
    }
    Ok(())
}

/// `forgeseed` — state where each project's upstream repository lives (§4.5.7.1).
///
/// `forgeseed --file seed/forge_repos.json [--dry-run]`
///
/// It refuses more than it accepts, on purpose, and each refusal is a question the operator can answer:
///
/// * a slug in the file that is not in the catalogue is a REFUSAL — a typo would silently measure nothing;
/// * a project in the catalogue the file does not mention is a REFUSAL — "a new project was added" must be a visible decision, not a project
///   that quietly never gets a repository and therefore never gets health;
/// * a project with no repository must be listed with `"skip": true` **and a reason**, so "we have not stated this one" is written down.
///
/// Every problem is reported before anything is written, and the whole file is refused if any of them is present: a partial application is
/// a catalogue where half the projects are declared and the operator has no way to know which half.
///
/// It prints counts and slugs, never URLs: a URL list in a terminal transcript is a list of places to send traffic, and the file it read is
/// right there.
async fn forgeseed(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    let path = args.value("file").unwrap_or("seed/forge_repos.json");
    let dry_run = args.flag("dry-run");
    let raw = std::fs::read_to_string(path).map_err(|error| anyhow::anyhow!("read mapping {path}: {error}"))?;
    let entries: Vec<ForgeEntry> =
        serde_json::from_str(&raw).map_err(|error| anyhow::anyhow!("parse {path}: {error}"))?;
    if entries.is_empty() {
        // An empty mapping applied successfully is the worst outcome available: it reports a clean run that did nothing.
        anyhow::bail!("{path} lists no projects; an empty mapping is a refusal, not a sync");
    }

    let slugs = concord_db::forge_repos::project_slugs(pool).await?;
    let in_catalog: std::collections::HashSet<&str> = slugs.iter().map(String::as_str).collect();
    let mut problems = Vec::new();
    let mut seen = std::collections::HashSet::new();
    for entry in &entries {
        if entry.slug.is_empty() {
            problems.push("an entry has no slug".to_string());
            continue;
        }
        if !seen.insert(entry.slug.clone()) {
            problems.push(format!("{} is listed twice", entry.slug));
        }
        if !entry.skip && (entry.repo_url.is_empty() || entry.repo_host.is_empty()) {
            problems.push(format!(
                "{} has neither a repository nor \"skip\": true",
                entry.slug
            ));
        }
        if entry.skip && entry.reason.trim().is_empty() {
            problems.push(format!("{} is skipped with no reason", entry.slug));
        }
        if !in_catalog.contains(entry.slug.as_str()) {
            problems.push(format!("{} is in the file and not in the catalogue", entry.slug));
        }
    }
    for slug in &slugs {
        if !seen.contains(slug) {
            problems.push(format!(
                "{slug} is in the catalogue and not in the file (list it, or list it as a skip)"
            ));
        }
    }
    if !problems.is_empty() {
        problems.sort();
        for problem in &problems {
            eprintln!("REFUSED: {problem}");
        }
        anyhow::bail!(
            "refusing to apply a mapping that does not account for the catalogue: {} problem(s)",
            problems.len()
        );
    }

    let current: std::collections::HashMap<String, concord_db::forge_repos::ProjectRepo> =
        concord_db::forge_repos::projects_with_repo(pool)
            .await?
            .into_iter()
            .map(|project| (project.slug.clone(), project))
            .collect();

    let (mut applied, mut unchanged, mut skipped, mut cleared) = (0_usize, 0_usize, 0_usize, 0_usize);
    for entry in &entries {
        if entry.skip {
            // A project that becomes a skip must LOSE the repository it used to have.
            // `forgeseed-skip-keeps-repo` leaves the old URL on a project that became a skip: the hourly ingest then keeps retrying a URL
            // the mapping no longer contains, which is the failure this clear exists for.
            let keep = std::env::var("CONCORD_MUTANT").unwrap_or_default().split(',').any(|m| m.trim() == "forgeseed-skip-keeps-repo");
            if !keep && current.get(&entry.slug).is_some_and(|project| !project.url.is_empty()) {
                if dry_run {
                    println!("would clear {} (was set, now skipped: {})", entry.slug, entry.reason);
                    cleared += 1;
                    continue;
                }
                concord_db::forge_repos::clear_repo(pool, &entry.slug).await?;
                cleared += 1;
            }
            skipped += 1;
            continue;
        }
        let want = forge::parse_repo_url(&entry.repo_url).map_err(|error| anyhow::anyhow!("{}: {error}", entry.slug))?;
        if let Some(project) = current.get(&entry.slug) {
            if project.url == want.url() && project.host == entry.repo_host {
                unchanged += 1;
                continue;
            }
        }
        if dry_run {
            println!("would set {} -> {} ({})", entry.slug, want.url(), entry.repo_host);
            applied += 1;
            continue;
        }
        concord_db::forge_repos::set_repo(pool, &entry.slug, &want.url(), &entry.repo_host).await?;
        applied += 1;
    }
    println!(
        "applied {applied} of {} ({unchanged} already set, {cleared} cleared, {skipped} skipped)",
        entries.len()
    );
    Ok(())
}

/// One line of the forge mapping file.
#[derive(serde::Deserialize)]
struct ForgeEntry {
    /// The project's slug.
    #[serde(default)]
    slug: String,
    /// The repository URL as typed.
    #[serde(default)]
    repo_url: String,
    /// The forge family the URL belongs to.
    #[serde(default)]
    repo_host: String,
    /// Whether this project deliberately has no repository.
    #[serde(default)]
    skip: bool,
    /// Why it has none. Required when `skip` is set.
    #[serde(default)]
    reason: String,
}

/// `capableseed` — register a capability set, and optionally assert values against projects.
///
/// `capableseed --list [--category design]` prints the registered matrix. `capableseed --apply capabilities.json [--assert --as <username>]`
/// registers what the file declares and, with `--assert`, writes the per-project values in its `assert` maps.
///
/// The file is the operator's statement of what the matrix should contain, and it is read as a whole: a file that declares assertions while
/// `--assert` was not given is a REFUSAL, not a silent registration, because the operator would otherwise leave believing the values are in
/// the database. Assertions are written as claims, never as confirmed ones — a seed that could manufacture a quorum would let whoever
/// edits the file promote their own claims, and confirmation means two real accounts agreed.
///
/// The final line reports what was achieved, not what was attempted: a seed that registers definitions and writes no assertion has built a
/// matrix with nothing in it, and Finder will correctly decline to ask anything.
async fn capableseed(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    if args.flag("list") {
        let capabilities = concord_db::capability_writes::list(pool, args.value("category")).await?;
        for capability in &capabilities {
            println!(
                "{:<24} {:<20} {:<8} {:?}",
                capability.key, capability.category, capability.kind, capability.values
            );
        }
        println!("{} capability(ies)", capabilities.len());
        return Ok(());
    }

    let path = args
        .value("apply")
        .ok_or_else(|| anyhow::anyhow!("usage: capableseed --list [--category c] | --apply <file> [--assert --as <username>]"))?;
    let raw = std::fs::read_to_string(path).map_err(|error| anyhow::anyhow!("read {path}: {error}"))?;
    let file: SeedFile = serde_json::from_str(&raw).map_err(|error| anyhow::anyhow!("parse {path}: {error}"))?;
    if file.capabilities.is_empty() {
        anyhow::bail!("{path} declares no capabilities");
    }

    let mutate = args.flag("assert");
    let author = if mutate {
        let name = args
            .value("as")
            .ok_or_else(|| anyhow::anyhow!("--assert requires --as <username>"))?;
        concord_db::capability_writes::user_id(pool, name)
            .await?
            .ok_or_else(|| anyhow::anyhow!("no account named {name:?}"))?
    } else {
        0
    };

    // Checked before the first write: a file that declares assertions and was run without `--assert` must not register half of itself and
    // then stop.
    if !mutate {
        if let Some(capability) = file
            .capabilities
            .iter()
            .find(|capability| capability.assert.as_ref().is_some_and(|map| !map.is_empty()))
        {
            anyhow::bail!(
                "capability {:?} declares assertions but --assert was not given",
                capability.key
            );
        }
    }

    let mut created = 0_usize;
    let mut updated = 0_usize;
    let mut asserted = 0_usize;
    let mut categories = std::collections::BTreeMap::new();
    for capability in &file.capabilities {
        let kind = capability.kind.as_deref().unwrap_or("boolean");
        let made = concord_db::capability_writes::ensure(
            pool,
            &capability.key,
            &capability.label,
            &capability.category,
            kind,
            capability.values.as_deref().unwrap_or(&[]),
        )
        .await
        .map_err(|error| anyhow::anyhow!("{error}"))?;
        if made {
            created += 1;
        } else {
            updated += 1;
        }
        *categories.entry(capability.category.clone()).or_insert(0_usize) += 1;

        for (slug, value) in capability.assert.iter().flatten() {
            concord_db::capability_writes::assert_value(
                pool,
                &capability.key,
                slug,
                value,
                "seed file",
                author,
            )
            .await
            .map_err(|error| anyhow::anyhow!("{error}"))?;
            asserted += 1;
        }
    }

    print!(
        "registered {created} new and {updated} existing capabilities across {} categories",
        categories.len()
    );
    if mutate {
        print!(", {asserted} assertions");
    }
    println!();
    let names: Vec<&str> = categories.keys().map(String::as_str).collect();
    println!("categories: {}", names.join(", "));
    if mutate && asserted == 0 {
        println!(
            "WARNING: no assertions were written; every capability is unasserted and Finder will have no capability questions to ask."
        );
    }
    Ok(())
}

/// The seed file's shape: capabilities, with the values to assert keyed by project slug so the file does not depend on autoincrement ids.
#[derive(serde::Deserialize)]
struct SeedFile {
    /// The capability definitions.
    capabilities: Vec<SeedCapability>,
}

/// One capability definition plus the values seeded for specific projects.
#[derive(serde::Deserialize)]
struct SeedCapability {
    /// The capability's name in the query language.
    key: String,
    /// The human label.
    label: String,
    /// The category the matrix groups it under.
    category: String,
    /// `boolean` or `enum`; absent means boolean.
    #[serde(default)]
    kind: Option<String>,
    /// An enum's values.
    #[serde(default)]
    values: Option<Vec<String>>,
    /// Values to assert, keyed by project slug.
    #[serde(default)]
    assert: Option<std::collections::BTreeMap<String, String>>,
}

/// `granttrust` — assign a trust level, and record who did it.
///
/// There is no HTTP route that writes a trust level, so this is the only way an account reaches the configured minimum. Without it every
/// trust-gated endpoint is unreachable for everyone, including the operator — which is why the command exists at all rather than being an
/// SQL snippet in a runbook: it goes through the same function the application uses, so `trust_grants` and `users.trust_level` cannot
/// disagree.
///
/// Interface: `granttrust --user <id> --level <n>` or the Go spelling `granttrust <id> <n>`.
///
/// Authorisation is reaching the database: there is no admin role on a local instance to check against, and pretending there is would mean
/// a second permission system with one user. `granted_by` is the target account, which keeps a self-grant legible in the history.
///
/// The ceiling is a refusal, not a clamp: a clamped write reports success while storing something else, and the operator leaves believing
/// the level they asked for is in force.
async fn granttrust(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    let user = args
        .value("user")
        .or_else(|| args.arg(0))
        .ok_or_else(|| anyhow::anyhow!("usage: granttrust <user-id> <level>  (or --user <id> --level <n>)"))?
        .parse::<i64>()
        .map_err(|_| anyhow::anyhow!("bad user id {:?}: want a number", args.value("user").or_else(|| args.arg(0))))?;
    let level = args
        .value("level")
        .or_else(|| args.arg(1))
        .ok_or_else(|| anyhow::anyhow!("usage: granttrust <user-id> <level>  (or --user <id> --level <n>)"))?
        .parse::<i64>()
        .map_err(|_| anyhow::anyhow!("bad level {:?}: want a number", args.value("level").or_else(|| args.arg(1))))?;
    let reason = args
        .value("reason")
        .unwrap_or("operator grant: concord-cli granttrust");
    concord_db::trust::set_trust_level(pool, user, level, user, reason)
        .await
        .map_err(|error| anyhow::anyhow!("{error}"))?;
    let current = concord_db::trust::get_trust_level(pool, user).await?;
    let ceiling = concord_db::trust::trust_ceiling(pool).await?;
    // One line, and the same shape as the Go's, because the operator reads this to know what is now in force.
    println!("user {user} trust_level={current} ceiling={ceiling}");
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

    if args.flags.contains("fetch") || args.value("fetch").is_some() {
        return healthsync_fetch(pool, args).await;
    }

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

/// `healthsync --fetch` — ask each project's forge for its numbers, then write them.
///
/// The four outcomes are separate on purpose, because they mean different things to an operator:
///
/// * **synced** — the numbers are written;
/// * **deferred** — the forge asked for a pause. Not a failure: the data is unchanged and the next pass reads
///   it. Counting it as failed made the Go timer exit 1 every hour on a shared IP, which trains an operator to
///   ignore the unit — and the unit has to stay loud for the failures that ARE bugs;
/// * **failed** — a corrupt `repo_url`, a moved repository (404), or a broken answer. Exit 1, because these are
///   the ones somebody has to look at;
/// * **skipped** — no measurement reached the health score: the project is not a repository's problem.
///
/// The newcomer figure is read from `forge_events` BEFORE the write, so a database error skips a project rather
/// than leaving half a measurement landed — the same rule as a failed fetch writing nothing.
async fn healthsync_fetch(pool: &sqlx::PgPool, args: &Args) -> anyhow::Result<()> {
    use concord_db::metrics::{self, Measurement};
    use concord_ranking::health::{newcomer_response, ForgeEvent};

    let fetcher = crate::forge::Fetcher::from_env()
        .map_err(|error| anyhow::anyhow!("{error:?}"))?;
    let dry_run = args.flags.contains("dry-run");
    let only = args.value("only").map(|text| text.split(',').map(str::to_string).collect::<Vec<_>>());
    let (mut synced, mut deferred, mut failed, mut skipped) = (0_usize, 0_usize, 0_usize, 0_usize);
    for row in metrics::repos(pool).await? {
        if let Some(only) = &only {
            if !only.iter().any(|slug| slug == &row.slug) {
                continue;
            }
        }
        let repo = match crate::forge::parse_repo_url(&row.repo_url) {
            Ok(repo) => repo,
            Err(refusal) => {
                // The column is written through this same normalisation, so this is a corrupt row rather than a
                // user error: report it and do not guess a repository.
                println!("FAILED {}: stored repo_url does not parse: {}", row.slug, refusal.0);
                failed += 1;
                continue;
            }
        };
        let snapshot = match fetcher.snapshot(&repo).await {
            Ok(snapshot) => snapshot,
            Err(crate::forge::FetchError::RateLimited(detail)) => {
                println!("DEFERRED {}: {detail}", row.slug);
                deferred += 1;
                continue;
            }
            Err(crate::forge::FetchError::Gone(detail))
            | Err(crate::forge::FetchError::Failed(detail)) => {
                println!("FAILED {}: {detail}", row.slug);
                failed += 1;
                continue;
            }
        };

        // No answered pull request is NOT a reason to skip the project: recency, breadth and cadence were
        // measured, and they are three quarters of the score. The responsiveness term is passed as unknown so
        // the score excludes it and renormalises, rather than writing a zero that reads as "reviewed instantly".
        let (review_hours, verdict) = if snapshot.review_sample == 0 {
            (
                concord_db::metrics::UNKNOWN_REVIEW_HOURS,
                format!(
                    "review latency unknown ({} pull request(s) sampled, none answered)",
                    snapshot.prs
                ),
            )
        } else {
            (
                snapshot.median_review_hours,
                format!(
                    "review latency {:.1}h over {} answered pull request(s)",
                    snapshot.median_review_hours, snapshot.review_sample
                ),
            )
        };

        let events = metrics::forge_events(pool, &repo.full).await?;
        let stream: Vec<ForgeEvent<'_>> = events
            .iter()
            .map(|event| ForgeEvent {
                repo: &repo.full,
                kind: &event.kind,
                number: event.number,
                actor: &event.actor,
                at: event.at,
            })
            .collect();
        let newcomer = newcomer_response(&stream);

        let now = chrono::Utc::now().timestamp() as f64;
        let measurement = Measurement {
            slug: row.slug.clone(),
            measured: true,
            stars: snapshot.stars,
            forks: snapshot.forks,
            open_issues: snapshot.open_issues,
            commit_count: snapshot.commits,
            contributors: snapshot.contributors,
            last_commit_age_days: if snapshot.pushed_at > 0.0 {
                ((now - snapshot.pushed_at) / 86400.0).max(0.0)
            } else {
                concord_db::metrics::UNKNOWN_AGE
            },
            median_review_hours: review_hours,
            releases_90d: snapshot.releases_90d,
            license: snapshot.license.clone(),
        };
        if dry_run {
            println!(
                "dry-run {}: stars={} forks={} issues={} commits={} contributors={} releases90={} {} \
                 newcomer={}",
                row.slug,
                snapshot.stars,
                snapshot.forks,
                snapshot.open_issues,
                snapshot.commits,
                snapshot.contributors,
                snapshot.releases_90d,
                verdict,
                match newcomer {
                    Some((hours, sample)) => format!("{hours:.1}h over {sample}"),
                    None => "none".to_string(),
                }
            );
            continue;
        }
        match metrics::sync(pool, &measurement).await? {
            Ok(written) => {
                metrics::write_newcomer(pool, &row.slug, newcomer).await?;
                synced += 1;
                println!(
                    "synced {}: health {:.6} ({verdict}){}",
                    row.slug,
                    written.health_score,
                    if written.responsiveness_known { "" } else { ", responsiveness excluded" }
                );
            }
            Err(concord_db::metrics::Skipped::Unmeasured) => {
                println!("skipped {}: nothing measured; nothing written", row.slug);
                skipped += 1;
            }
            Err(concord_db::metrics::Skipped::UnknownProject) => {
                println!("FAILED {}: the project vanished between the read and the write", row.slug);
                failed += 1;
            }
        }
    }
    println!("healthsync: synced {synced}, deferred {deferred}, skipped {skipped}, failed {failed}");
    if failed > 0 {
        // Deferred alone is NOT a failure: the unit has to stay loud only for the ones somebody must fix.
        anyhow::bail!("{failed} project(s) failed");
    }
    Ok(())
}
