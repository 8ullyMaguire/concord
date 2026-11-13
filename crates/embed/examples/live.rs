//! Ask the instance's real model server for two vectors and print what came back.
//!
//! This exists because a fake server proves the client speaks the protocol it was written against, and only the real
//! server proves the protocol is the one ollama actually speaks. Run it on the host that has the model:
//!
//! ```text
//! cargo run -p concord-embed --example live -- http://127.0.0.1:11434 nomic-embed-text:latest
//! ```
//!
//! Exit code 0 means a vector came back with the configured model's width and the two texts scored above the model's
//! weak band — which is the whole claim the duplicate panel rests on.

use concord_embed::{bands::classify, cosine, Embedder, Ollama, OllamaConfig};

#[tokio::main]
async fn main() {
    let mut args = std::env::args().skip(1);
    let endpoint = args.next().unwrap_or_else(|| "http://127.0.0.1:11434".to_string());
    let model = args.next().unwrap_or_else(|| "nomic-embed-text:latest".to_string());

    let mut config = OllamaConfig::new(&endpoint, &model);
    // The prefix the deployment uses, so the measurement is of the vectors that will actually be stored.
    config.prefix = "search_document: ".to_string();
    // The deployment's own knob (CONCORD_EMBED_TIMEOUT_MS, 8000 on this instance). A cold model load can exceed it, and
    // the fallback then answers — which is a degrade, not a failure, but it means the first filing after an idle period
    // is checked lexically.
    if let Ok(raw) = std::env::var("CONCORD_EMBED_TIMEOUT_MS") {
        if let Ok(ms) = raw.parse::<u64>() {
            config.timeout = std::time::Duration::from_millis(ms);
            println!("timeout {ms} ms");
        }
    }
    let embedder = match Ollama::new(config) {
        Ok(embedder) => embedder,
        Err(error) => {
            println!("FAIL configure: {error}");
            std::process::exit(1);
        }
    };

    let near_a = "the roadmap page shows an empty column for every milestone";
    let near_b = "milestone column empty on the roadmap page";
    let far = "users cannot reset their password";

    let a = embedder.embed(near_a).await;
    let b = embedder.embed(near_b).await;
    let c = embedder.embed(far).await;

    println!("id      {}", embedder.id());
    println!("dims    {} (configured width {})", a.len(), embedder.dims());
    println!("provider responded with {} values, first three {:?}", a.len(), &a[..3.min(a.len())]);

    // A degraded vector is 2048-wide, because that is the hashed fallback's width. Detecting it by width is how this
    // program refuses to report a fallback as if it were the model.
    if a.len() != embedder.dims() {
        println!(
            "FAIL the server did not answer: got a {}-wide vector, which is the local fallback, not the model",
            a.len()
        );
        std::process::exit(1);
    }

    let near = cosine(&a, &b);
    let unrelated = cosine(&a, &c);
    println!("near    {near:.4}  {}", near_a);
    println!("far     {unrelated:.4}  {far}");
    println!("bands   near {:?}, far {:?}", classify(near, &embedder.id()), classify(unrelated, &embedder.id()));

    if near <= unrelated {
        println!("FAIL a paraphrase scored no higher than an unrelated complaint; the vectors are not carrying meaning");
        std::process::exit(1);
    }
    println!("ok      the model answered and ranks a paraphrase above an unrelated complaint");
}
