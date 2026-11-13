//! The operator binary's entry point: `concord-cli <command> [--key value]`.

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    concord_cli::run().await
}
