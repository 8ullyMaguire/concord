//! The operator binary. One entry point, subcommands as arguments — the Go tree spread these across
//! `cmd/*` binaries, and one binary with `serve` as a subcommand is the estate's shape.
//!
//! Usage:
//!     concord-cli serve --addr 127.0.0.1:8106        # CONCORD_DB_URL names the database
//!     concord-cli check                              # connect, run one query, exit

use std::net::SocketAddr;

/// Run the binary. Arguments are read positionally: a subcommand, then `--key value` pairs.
///
/// # Errors
/// Anything the caller must act on: an unreachable database, a port already bound, a bad address.
/// A service that starts and silently answers 500 is worse than one that refuses to start.
pub async fn run() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,sqlx=warn".into()),
        )
        .init();

    let mut args = std::env::args().skip(1);
    let command = args.next().unwrap_or_else(|| "serve".to_string());
    let mut addr = "127.0.0.1:8106".to_string();
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--addr" => addr = args.next().expect("--addr needs a value"),
            other => anyhow::bail!("unknown argument {other}"),
        }
    }

    let url = std::env::var("CONCORD_DB_URL")
        .or_else(|_| std::env::var("DATABASE_URL"))
        .map_err(|_| anyhow::anyhow!("CONCORD_DB_URL (or DATABASE_URL) is not set"))?;
    let pool = concord_db::pool(&url).await?;

    match command.as_str() {
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
        other => anyhow::bail!("unknown command {other}"),
    }
}
