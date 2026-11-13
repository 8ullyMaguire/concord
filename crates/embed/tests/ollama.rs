//! The remote embedder against a fake model server, plus the per-model bands.
//!
//! The fake is a hand-written HTTP/1.1 server on a loopback port. No mocking framework: what needs proving is the wire
//! behaviour — which path is called, what the body says, what happens on a 500, an `error` field, an empty vector and an
//! old-style singular answer — and a fake that answers on a socket proves all of it.

use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};

use concord_embed::bands::{classify, is_duplicate_for, is_strong_duplicate_for, thresholds_for, Class};
use concord_embed::{Embedder, Hashed, Ollama, OllamaConfig};

/// What the fake server answered with, and what it was asked.
#[derive(Default)]
struct Asked {
    requests: Vec<(String, String)>,
}

/// Start a fake `/api/embed` that answers `body` with `status`, and returns the base URL plus a log of requests.
fn fake_server(status: u16, body: String) -> (String, Arc<Mutex<Asked>>) {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
    let port = listener.local_addr().expect("addr").port();
    let asked = Arc::new(Mutex::new(Asked::default()));
    let log = Arc::clone(&asked);

    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut stream) = stream else { continue };
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));
            let mut request_line = String::new();
            if reader.read_line(&mut request_line).is_err() {
                continue;
            }
            let mut content_length = 0_usize;
            let mut headers = Vec::new();
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).is_err() || line.trim().is_empty() {
                    break;
                }
                if let Some(value) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                    content_length = value.trim().parse().unwrap_or(0);
                }
                headers.push(line.trim().to_string());
            }
            let mut payload = vec![0_u8; content_length];
            if content_length > 0 {
                use std::io::Read as _;
                let _ = reader.read_exact(&mut payload);
            }
            let request_body = String::from_utf8_lossy(&payload).to_string();
            let parts: Vec<&str> = request_line.split_whitespace().collect();
            let path = parts.get(1).unwrap_or(&"").to_string();
            log.lock().expect("log").requests.push((path, request_body));

            let response = format!(
                "HTTP/1.1 {status} X\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            let _ = stream.write_all(response.as_bytes());
            let _ = stream.flush();
        }
    });

    (format!("http://127.0.0.1:{port}"), asked)
}

fn ollama(endpoint: &str) -> Ollama {
    Ollama::new(OllamaConfig::new(endpoint, "nomic-embed-text:latest")).expect("configured")
}

/// A 768-wide vector with one hot bucket, which is enough to be a distinct, comparable vector.
fn one_hot(seed: f32) -> Vec<f32> {
    let mut v = vec![0.0_f32; 768];
    v[100] = seed;
    v[7] = 0.5 * seed;
    concord_embed::normalize(&v)
}

#[test]
fn a_remote_embedder_calls_api_embed_with_the_model_and_the_prefix() {
    let body = serde_json::json!({ "embeddings": [one_hot(1.0)] }).to_string();
    let (endpoint, asked) = fake_server(200, body);

    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    let mut config = OllamaConfig::new(&endpoint, "nomic-embed-text:latest");
    config.prefix = "search_document: ".to_string();
    let embedder = Ollama::new(config).expect("configured");
    let vector = runtime.block_on(embedder.embed("upload fails"));

    assert_eq!(vector.len(), 768);
    assert_eq!(embedder.id(), "nomic:nomic-embed-text:latest");
    assert_eq!(embedder.dims(), 768);

    let log = asked.lock().expect("log");
    assert_eq!(log.requests.len(), 1, "one round trip, not two");
    let (path, request) = &log.requests[0];
    assert_eq!(path, "/api/embed");
    let parsed: serde_json::Value = serde_json::from_str(request).expect("json body");
    assert_eq!(parsed["model"], "nomic-embed-text:latest");
    assert_eq!(
        parsed["input"], "search_document: upload fails",
        "nomic was trained with the task prefix and scores worse without it"
    );
}

#[test]
fn a_500_degrades_to_the_local_embedder_instead_of_returning_nothing() {
    let (endpoint, asked) = fake_server(500, "does not support embeddings".to_string());
    let embedder = ollama(&endpoint);
    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    let vector = runtime.block_on(embedder.embed("upload fails"));

    // The fallback is the hashed embedder: 2048 dims, and it has content.
    assert_eq!(vector.len(), 2048, "a failed call must degrade, not disappear");
    assert!(concord_embed::has_content(&vector));
    assert_eq!(asked.lock().expect("log").requests.len(), 1);
}

#[test]
fn an_error_field_degrades_too() {
    // Some ollama versions answer 200 with an error inside the body.
    let body = serde_json::json!({ "error": "model does not support embeddings" }).to_string();
    let (endpoint, _) = fake_server(200, body);
    let embedder = ollama(&endpoint);
    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    let vector = runtime.block_on(embedder.embed("crash on export"));
    assert_eq!(vector.len(), 2048);
    assert!(concord_embed::has_content(&vector));
}

#[test]
fn a_zero_vector_degrades_because_it_carries_no_signal() {
    let body = serde_json::json!({ "embeddings": [vec![0.0_f32; 768]] }).to_string();
    let (endpoint, _) = fake_server(200, body);
    let embedder = ollama(&endpoint);
    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    let vector = runtime.block_on(embedder.embed("network drop on resume"));
    assert_eq!(vector.len(), 2048, "an inert vector is not an answer");
}

#[test]
fn an_old_server_answering_singular_embedding_is_accepted() {
    let body = serde_json::json!({ "embedding": one_hot(2.0) }).to_string();
    let (endpoint, _) = fake_server(200, body);
    let embedder = ollama(&endpoint);
    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");
    let vector = runtime.block_on(embedder.embed("milestone column empty"));
    assert_eq!(vector.len(), 768, "the singular form is older servers, not a broken one");
    assert_eq!(vector[100], one_hot(2.0)[100]);
}

#[test]
fn stop_words_only_never_cost_a_round_trip() {
    let body = serde_json::json!({ "embeddings": [one_hot(1.0)] }).to_string();
    let (endpoint, asked) = fake_server(200, body);
    let embedder = ollama(&endpoint);
    let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("runtime");

    assert!(runtime.block_on(embedder.embed("")).is_empty());
    assert!(runtime.block_on(embedder.embed("   ")).is_empty());
    assert!(
        runtime.block_on(embedder.embed("the of and is")).is_empty(),
        "nothing indexable, and the model must not be asked"
    );
    assert_eq!(
        asked.lock().expect("log").requests.len(),
        0,
        "the pre-filter is the whole point: no request for text with no content word"
    );
}

#[test]
fn the_fallback_is_the_hashed_embedder_and_can_be_replaced() {
    let body = serde_json::json!({ "embeddings": [one_hot(1.0)] }).to_string();
    let (endpoint, _) = fake_server(500, body);
    let embedder = ollama(&endpoint);
    assert_eq!(embedder.fallback_id(), "hashed-v1");

    let mut config = OllamaConfig::new(&endpoint, "nomic-embed-text");
    config.fallback = Some(Box::new(Hashed::new()));
    let explicit = Ollama::new(config).expect("configured");
    assert_eq!(explicit.fallback_id(), "hashed-v1");
}

#[test]
fn an_endpoint_or_model_is_required() {
    assert!(Ollama::new(OllamaConfig::new("", "m")).is_err());
    assert!(Ollama::new(OllamaConfig::new("http://127.0.0.1:1", "")).is_err());
}

#[test]
fn the_bands_are_per_model_and_each_is_a_decision() {
    // The two scales are not comparable, which is why one constant cannot serve both.
    assert_eq!(thresholds_for("nomic:nomic-embed-text:latest"), (0.92, 0.60, 0.35));
    assert_eq!(thresholds_for("hashed-v1"), (0.85, 0.62, 0.40));

    // A 0.90 score is a strong duplicate under the hashed bands and only advisory under nomic's: the same number, two
    // different answers, and picking the wrong one refuses a filing a maintainer wanted.
    assert!(is_strong_duplicate_for(0.90, "hashed-v1"));
    assert!(!is_strong_duplicate_for(0.90, "nomic:any"));
    assert!(is_duplicate_for(0.90, "nomic:any"));

    assert_eq!(classify(0.95, "nomic:x"), Class::Strong);
    assert_eq!(classify(0.92, "nomic:x"), Class::Strong, "the band is inclusive");
    assert_eq!(classify(0.9199, "nomic:x"), Class::Likely);
    assert_eq!(classify(0.60, "nomic:x"), Class::Likely);
    assert_eq!(classify(0.59, "nomic:x"), Class::Weak);
    assert_eq!(classify(0.35, "nomic:x"), Class::Weak);
    assert_eq!(classify(0.3499, "nomic:x"), Class::None);
    assert_eq!(classify(0.0, "nomic:x"), Class::None);

    // And a model the code has never seen gets the hashed bands rather than a guess.
    assert_eq!(thresholds_for("some-future-model"), (0.85, 0.62, 0.40));
}

#[test]
fn normalize_scales_to_unit_length_and_leaves_zero_alone() {
    let scaled = concord_embed::normalize(&[3.0, 4.0]);
    assert!((scaled[0] - 0.6).abs() < 1e-6);
    assert!((scaled[1] - 0.8).abs() < 1e-6);
    assert_eq!(concord_embed::normalize(&[0.0, 0.0]), vec![0.0, 0.0]);
}
