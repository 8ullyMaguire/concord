//! The transformer-backed embedder: POST text to a model server, get a vector back, degrade to the local one when the
//! server cannot answer.
//!
//! # Why the fallback is the important field
//!
//! An embedder that returns nothing when its model server is down turns *"no duplicates found"* into a **false claim on
//! the filing path**. The local hashed embedder needs no network and the store already knows how to ignore a vector with
//! no content, so a degraded instance still catches exact and morphological duplicates. Degrading is a smaller lie than
//! disappearing.
//!
//! # Why requests are serialised
//!
//! Two concurrent filings would otherwise race the model server and both fail, turning load into a false "no
//! duplicates". The mutex is held across the HTTP call, which is why it is a `tokio` mutex rather than a `std` one: the
//! wait happens in async code and must not block the runtime thread.

use std::future::Future;
use std::pin::Pin;
use std::time::Duration;

use tokio::sync::Mutex;

use crate::{has_content, Embedder, Hashed};

/// A future returned by [`Embedder::embed`], boxed so the trait stays object-safe.
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// Configuration for a remote embedder.
pub struct OllamaConfig {
    /// Base URL of the server, e.g. `http://127.0.0.1:11434`. Required.
    pub endpoint: String,
    /// Model name, e.g. `nomic-embed-text:latest`. Required.
    pub model: String,
    /// Bound on one request. Embedding is on the filing path, so a slow model server must not become a slow filing
    /// experience.
    pub timeout: Duration,
    /// Prepended to every text. `nomic-embed-text` was trained with the task prefix `search_document: ` and scores
    /// noticeably worse on bare input.
    pub prefix: String,
    /// Used when the server is unreachable or answers with nothing usable.
    pub fallback: Option<Box<dyn Embedder>>,
}

impl OllamaConfig {
    /// A config with the reference's defaults and no fallback (the constructor fills it in).
    #[must_use]
    pub fn new(endpoint: impl Into<String>, model: impl Into<String>) -> Self {
        Self {
            endpoint: endpoint.into(),
            model: model.into(),
            timeout: Duration::from_secs(10),
            prefix: String::new(),
            fallback: None,
        }
    }
}

/// An [`Embedder`] backed by a model server.
pub struct Ollama {
    config: OllamaConfig,
    client: reqwest::Client,
    /// Held across the round trip: see the module note.
    lock: Mutex<()>,
    fallback: Box<dyn Embedder>,
}

/// Why an embedding could not be configured.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ConfigError {
    /// No endpoint.
    EndpointRequired,
    /// No model.
    ModelRequired,
    /// The HTTP client could not be built.
    Client(String),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::EndpointRequired => write!(f, "embed: ollama endpoint required"),
            Self::ModelRequired => write!(f, "embed: ollama model required"),
            Self::Client(detail) => write!(f, "embed: http client: {detail}"),
        }
    }
}

impl std::error::Error for ConfigError {}

impl Ollama {
    /// Return a transformer-backed embedder.
    ///
    /// Reachability is **not** verified, deliberately: a probe at startup would make a filing service refuse to boot when
    /// a model server is down, which is worse than degrading. Reachability is discovered at the first embed, where the
    /// fallback absorbs it.
    ///
    /// # Errors
    /// [`ConfigError`] for a missing endpoint or model, or a client that cannot be built.
    pub fn new(mut config: OllamaConfig) -> Result<Self, ConfigError> {
        if config.endpoint.is_empty() {
            return Err(ConfigError::EndpointRequired);
        }
        if config.model.is_empty() {
            return Err(ConfigError::ModelRequired);
        }
        let client = reqwest::Client::builder()
            .timeout(config.timeout)
            .build()
            .map_err(|error| ConfigError::Client(error.to_string()))?;
        let fallback = config
            .fallback
            .take()
            .unwrap_or_else(|| Box::new(Hashed::new()));
        Ok(Self {
            config,
            client,
            lock: Mutex::new(()),
            fallback,
        })
    }

    /// The model id, for the embeddings table.
    ///
    /// Includes the model name because two nomic checkpoints are **different vector spaces**, and comparing across them
    /// yields a number in `[-1,1]` that means nothing. The store refuses to compare vectors whose id differs, so this
    /// string is what makes a model change safe: it strands the old vectors instead of silently mixing them.
    #[must_use]
    pub fn model_id(&self) -> String {
        format!("nomic:{}", self.config.model)
    }

    /// The id of the fallback, for the log line when a call degrades.
    #[must_use]
    pub fn fallback_id(&self) -> String {
        self.fallback.id()
    }

    async fn fetch(&self, text: &str) -> Result<Vec<f32>, String> {
        let body = serde_json::json!({ "model": self.config.model, "input": text });
        let response = self
            .client
            .post(format!("{}/api/embed", self.config.endpoint))
            .json(&body)
            .send()
            .await
            .map_err(|error| format!("embed: {} unreachable: {error}", self.config.endpoint))?;

        let status = response.status();
        if !status.is_success() {
            // Read a little of the body: ollama's "does not support embeddings" error arrives as a 200 with an `error`
            // field on some versions and a 400 on others, and that distinction is the difference between a fixable
            // config problem and a mystery.
            let detail = response
                .text()
                .await
                .unwrap_or_default()
                .chars()
                .take(200)
                .collect::<String>();
            return Err(format!(
                "embed: {} returned {}: {}",
                self.config.endpoint,
                status.as_u16(),
                detail.trim()
            ));
        }

        let out: Response = response
            .json()
            .await
            .map_err(|error| format!("embed: decode {}: {error}", self.config.model))?;
        if let Some(error) = out.error {
            if !error.is_empty() {
                return Err(format!("embed: {}: {error}", self.config.model));
            }
        }
        // Plural first: that is what current servers answer. The singular form is older servers, and dropping it would
        // make a working instance look unconfigured.
        if let Some(first) = out.embeddings.into_iter().next() {
            return Ok(first);
        }
        if let Some(single) = out.embedding {
            return Ok(single);
        }
        Err(format!("embed: {} returned no vector", self.config.model))
    }
}

/// The shape `/api/embed` answers with.
#[derive(serde::Deserialize)]
struct Response {
    #[serde(default)]
    embeddings: Vec<Vec<f32>>,
    /// Older servers answer with a singular `embedding`.
    #[serde(default)]
    embedding: Option<Vec<f32>>,
    #[serde(default)]
    error: Option<String>,
}

impl Embedder for Ollama {
    fn id(&self) -> String {
        self.model_id()
    }

    fn dims(&self) -> usize {
        // Fixed rather than probed, so the schema and the store's dimension check agree without a round trip.
        768
    }

    fn embed<'a>(&'a self, text: &'a str) -> BoxFuture<'a, Vec<f32>> {
        Box::pin(async move {
            if !may_have_tokens(text) {
                // Nothing indexable: empty text, or nothing but stop words. A cheap lexical pre-filter rather than a
                // round trip to the model to be told nothing.
                return Vec::new();
            }
            // Serialised: two concurrent filings racing the server would both fail, and a failure here reads as "no
            // duplicates".
            let _guard = self.lock.lock().await;
            let prefixed = format!("{}{}", self.config.prefix, text);
            match self.fetch(&prefixed).await {
                Ok(vector) if has_content(&vector) => vector,
                // Degrade rather than fail: the filing path must keep working with the model server down, and exact
                // duplicate detection still functions. The reason is printed only when asked for, because a degraded
                // embed on every filing would fill the journal with the same line.
                outcome => {
                    if std::env::var_os("CONCORD_EMBED_DEBUG").is_some() {
                        let why = match &outcome {
                            Ok(_) => "the server answered with a vector that carries no signal".to_string(),
                            Err(error) => error.clone(),
                        };
                        eprintln!("embed: degraded to {}: {why}", self.fallback.id());
                    }
                    self.fallback.embed(text).await
                }
            }
        })
    }
}

/// Whether text contains at least one content word.
///
/// A cheap pre-filter so a stop-words-only query never costs a model round trip. The hashed embedder's trigrams make
/// such a text embeddable, which is why the check cannot simply be "does the fallback produce a non-zero vector".
#[must_use]
pub fn may_have_tokens(text: &str) -> bool {
    text.to_lowercase()
        .split(|c: char| !c.is_alphanumeric())
        .filter(|word| !word.is_empty())
        .any(|word| !crate::is_stop_word(word))
}

/// Return a vector scaled to unit length.
///
/// The model already returns near-unit vectors, but the store computes cosine over whatever it is handed, and a future
/// backend returning unnormalised output would silently change every score. Normalising at the boundary keeps that
/// concern out of the comparison code.
#[must_use]
pub fn normalize(v: &[f32]) -> Vec<f32> {
    let norm = v
        .iter()
        .map(|x| f64::from(*x) * f64::from(*x))
        .sum::<f64>()
        .sqrt();
    if norm == 0.0 {
        return v.to_vec();
    }
    v.iter().map(|x| (f64::from(*x) / norm) as f32).collect()
}
