//! The forge half of `healthsync`: what a repository's numbers are, asked of GitHub or Forgejo.
//!
//! No estate crate owns a forge client (`ls ~/code/polaris-*` has none), so this lives here rather than being
//! invented as a shared component. It uses `reqwest` with the same features the four sibling estate crates use
//! (`default-features = false, features = ["rustls-tls", "json"]`), so it adds no second HTTP stack.
//!
//! Five rules, each of which the Go binary learned from a real run, and each of which changes a number or the
//! unit's exit status:
//!
//! 1. **A repository URL that does not parse is a FAILURE, not a guess.** The column is written through the
//!    same normalisation (`ParseRepoUrl`), so a corrupt row is a bug to report rather than a repository to
//!    invent.
//! 2. **A rate limit is DEFERRED, not failed.** Counting it as failed made the timer exit 1 every hour on a
//!    shared IP, which trains an operator to ignore the unit — and the unit has to stay loud for the failures
//!    that ARE bugs. A 404 is one of those: it says the repository moved.
//! 3. **A pull request nobody answered is NOT a fast response.** It contributes nothing to the median, and the
//!    sample size is reported so the median can be read honestly.
//! 4. **A response is the earliest comment OR review by somebody else** — not review alone. Counting only
//!    formal reviews left five of six repositories unmeasurable on the live instance: the metric was not
//!    missing, the fetch was looking in one place.
//! 5. **A full page of contributors is a FLOOR, not a count.** The API caps the page at 100, and the scorer
//!    rewards breadth, so a 3,000-contributor project reads as 100 — the safe direction, recorded rather than
//!    silently rounded.

use std::time::Duration;

/// Which API to speak.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Host {
    /// `api.github.com`.
    GitHub,
    /// A Forgejo/Gitea instance under `/api/v1`.
    Forgejo,
}

impl Host {
    /// The host a stored `repo_host` value names.
    #[must_use]
    pub fn from_repo_host(text: &str) -> Option<Self> {
        match text {
            "github" => Some(Self::GitHub),
            "forgejo" | "codeberg" => Some(Self::Forgejo),
            _ => None,
        }
    }
}

/// One repository reference, normalised.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Repo {
    /// The forge's own host, for the API root.
    pub host_name: String,
    /// Which API.
    pub host: Host,
    /// `owner/repo`.
    pub full: String,
}

/// Why a URL was refused.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RefusedUrl(pub String);

/// The three forges the ingest knows how to call. A host outside this set is refused rather than stored:
/// `repo_host` is the switch the fetch dispatches on, and an unknown host would either never be fetched
/// (silent) or be fetched with the wrong API (wrong numbers).
const KNOWN_HOSTS: [(&str, &str); 3] = [
    ("github.com", "github"),
    ("codeberg.org", "codeberg"),
    ("git.polarisocial.xyz", "forgejo"),
];

/// Turn what a person typed into one canonical repository reference, or refuse it with a reason.
///
/// Accepted, all producing `github.com/owner/repo`: `https://github.com/owner/repo`, `…/repo.git`,
/// `http://github.com/owner/repo/`, `git@github.com:owner/repo.git`, `ssh://git@github.com/owner/repo`.
///
/// A permissive parse is how `https://example.com/x` becomes a project's "repository" and every later surface
/// links a stranger at a 404.
///
/// # Errors
/// [`RefusedUrl`], naming the reason a person can act on.
pub fn parse_repo_url(raw: &str) -> Result<Repo, RefusedUrl> {
    let text = raw.trim();
    if text.is_empty() {
        return Err(RefusedUrl("empty repository URL".to_string()));
    }
    let (host, path) = if let Some(rest) = text.split_once("://") {
        let rest = rest.1;
        let (host, path) = rest.split_once('/').ok_or_else(|| {
            RefusedUrl(format!("{raw:?} is not a repository URL: no path after the host"))
        })?;
        // ssh://git@host/owner/repo — drop the user, keep host and path.
        let host = host.rsplit('@').next().unwrap_or(host);
        (host.to_string(), path.to_string())
    } else if let Some((user_host, path)) = text.split_once(':') {
        // scp-like: git@host:owner/repo
        if !user_host.contains('@') {
            return Err(RefusedUrl(format!(
                "{raw:?} is not a repository URL: expected https://host/owner/repo"
            )));
        }
        let host = user_host.rsplit('@').next().unwrap_or(user_host);
        (host.to_string(), path.to_string())
    } else {
        return Err(RefusedUrl(format!(
            "{raw:?} is not a repository URL: expected https://host/owner/repo"
        )));
    };
    // `:443` and `:22` are transport details, not identity.
    let host = host
        .trim_end_matches('/')
        .split(':')
        .next()
        .unwrap_or("")
        .to_lowercase();
    let Some((_, repo_host)) = KNOWN_HOSTS.iter().find(|(known, _)| *known == host) else {
        return Err(RefusedUrl(format!(
            "host {host:?} is not a known forge (want one of github.com, codeberg.org, \
             git.polarisocial.xyz)"
        )));
    };
    let path = path.trim_matches('/').trim_end_matches(".git");
    let parts: Vec<&str> = path.split('/').collect();
    if parts.len() != 2 || parts[0].is_empty() || parts[1].is_empty() {
        return Err(RefusedUrl(format!(
            "{raw:?} has {} path segments; a repository is exactly owner/repo",
            parts.len()
        )));
    }
    let api = Host::from_repo_host(repo_host).ok_or_else(|| {
        RefusedUrl(format!("host {host:?} maps to {repo_host:?}, which has no API"))
    })?;
    Ok(Repo {
        host_name: host,
        host: api,
        full: format!("{}/{}", parts[0], parts[1]),
    })
}

/// What one forge answered.
#[derive(Debug, Clone, PartialEq)]
pub struct Snapshot {
    /// Stars.
    pub stars: i64,
    /// Forks.
    pub forks: i64,
    /// Open issues.
    pub open_issues: i64,
    /// When the default branch was last pushed to, in unix seconds.
    pub pushed_at: f64,
    /// The SPDX licence the forge reports, when it reports one it is sure of.
    pub license: Option<String>,
    /// Distinct contributors on the first page: a FLOOR, not a count (rule 5).
    pub contributors: i64,
    /// Releases published in the last 90 days.
    pub releases_90d: i64,
    /// Commits on the default branch.
    pub commits: i64,
    /// Median hours to the first response by somebody else.
    pub median_review_hours: f64,
    /// How many pull requests were ANSWERED — the sample the median was taken over.
    pub review_sample: usize,
    /// How many pull requests were looked at.
    pub prs: usize,
}

/// Why a fetch did not produce a snapshot.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FetchError {
    /// The forge asked for a pause. Deferred, not failed: the data is unchanged and the next pass reads it.
    RateLimited(String),
    /// The repository is not there. A data bug, and it stays a failure.
    Gone(String),
    /// Anything else.
    Failed(String),
}

/// The fetcher: one client, one timeout, and the tokens the environment holds.
pub struct Fetcher {
    http: reqwest::Client,
    github_token: Option<String>,
    forgejo_token: Option<String>,
    /// How many pull requests to sample. Three unauthenticated, ten authenticated — the Go binary's numbers,
    /// because each pull request costs two more calls against a rate limit.
    review_window: usize,
    /// The GitHub API root, empty in production so [`Fetcher::api_root`] uses the real one.
    github_api: String,
    /// The Forgejo API root, empty in production.
    forgejo_api: String,
}

impl Fetcher {
    /// Build a fetcher from the environment, with the estate's client features and a 20-second timeout.
    ///
    /// # Errors
    /// When the TLS client cannot be built.
    pub fn from_env() -> Result<Self, FetchError> {
        let http = reqwest::Client::builder()
            .timeout(Duration::from_secs(20))
            .user_agent("concord-healthsync")
            .build()
            .map_err(|error| FetchError::Failed(format!("building the HTTP client: {error}")))?;
        let github_token = std::env::var("CONCORD_FORGE_TOKEN").ok().filter(|t| !t.is_empty());
        let forgejo_token = std::env::var("CONCORD_FORGEJO_TOKEN").ok().filter(|t| !t.is_empty());
        let review_window = if github_token.is_some() { 10 } else { 3 };
        Ok(Self {
            http,
            github_token,
            forgejo_token,
            review_window,
            // The probe's seam: a fake forge on 127.0.0.1 so the gate is a real HTTP exchange. Production
            // never sets these, and an empty value means "the real API".
            github_api: std::env::var("CONCORD_GITHUB_API").unwrap_or_default(),
            forgejo_api: std::env::var("CONCORD_FORGEJO_API").unwrap_or_default(),
        })
    }

    /// A fetcher pointed at an arbitrary root, for a probe against a local forge that speaks the API.
    #[must_use]
    pub fn with_roots(github_api: &str, forgejo_api: &str) -> Self {
        Self {
            http: reqwest::Client::builder()
                .timeout(Duration::from_secs(20))
                .user_agent("concord-healthsync")
                .build()
                .expect("the probe's client builds"),
            github_token: None,
            forgejo_token: None,
            review_window: 3,
            github_api: github_api.to_string(),
            forgejo_api: forgejo_api.to_string(),
        }
    }

    /// Override the API roots. Production never calls this; the probe does, so the gate is a real HTTP exchange
    /// against a fake forge rather than a unit test of a URL builder.
    #[must_use]
    pub fn with_api_overrides(mut self, github_api: String, forgejo_api: String) -> Self {
        self.github_api = github_api;
        self.forgejo_api = forgejo_api;
        self
    }

    /// One repository's numbers.
    ///
    /// # Errors
    /// [`FetchError::RateLimited`], [`FetchError::Gone`] or [`FetchError::Failed`].
    pub async fn snapshot(&self, repo: &Repo) -> Result<Snapshot, FetchError> {
        match repo.host {
            Host::GitHub => self.github(repo).await,
            Host::Forgejo => self.forgejo(repo).await,
        }
    }

    fn token(&self, host: Host) -> Option<&str> {
        match host {
            Host::GitHub => self.github_token.as_deref(),
            Host::Forgejo => self.forgejo_token.as_deref(),
        }
    }

    fn api_root(&self, host: Host) -> String {
        let override_root = match host {
            Host::GitHub => &self.github_api,
            Host::Forgejo => &self.forgejo_api,
        };
        if !override_root.is_empty() {
            return override_root.clone();
        }
        match host {
            Host::GitHub => "https://api.github.com/repos".to_string(),
            Host::Forgejo => "https://git.polarisocial.xyz/api/v1/repos".to_string(),
        }
    }

    async fn get(&self, host: Host, url: &str) -> Result<(Option<String>, Vec<u8>), FetchError> {
        let mut request = self.http.get(url);
        if let Some(token) = self.token(host) {
            if !token.is_empty() {
                request = request.bearer_auth(token);
            }
        }
        let response = request
            .send()
            .await
            .map_err(|error| FetchError::Failed(format!("{url} -> {error}")))?;
        let status = response.status();
        let headers = response.headers().clone();
        let body = response
            .bytes()
            .await
            .map_err(|error| FetchError::Failed(format!("{url} -> reading the body: {error}")))?;
        if status.is_success() {
            let total = headers
                .get("x-total-count")
                .and_then(|value| value.to_str().ok())
                .map(str::to_string);
            return Ok((total, body.to_vec()));
        }
        let text = String::from_utf8_lossy(&body);
        let line = text.lines().next().unwrap_or("").chars().take(200).collect::<String>();
        // Rule 2: a 403 or 429 that says "rate limit" is a pause. A 404 is a repository that moved, which is a
        // data bug and must stay a failure.
        let limited = (status == reqwest::StatusCode::FORBIDDEN
            || status == reqwest::StatusCode::TOO_MANY_REQUESTS)
            && text.to_lowercase().contains("rate limit");
        if limited {
            return Err(FetchError::RateLimited(format!("{url} -> {status}: {line}")));
        }
        if status == reqwest::StatusCode::NOT_FOUND || status == reqwest::StatusCode::GONE {
            return Err(FetchError::Gone(format!("{url} -> {status}: {line}")));
        }
        Err(FetchError::Failed(format!("{url} -> {status}: {line}")))
    }

    /// A GitHub repository.
    async fn github(&self, repo: &Repo) -> Result<Snapshot, FetchError> {
        let base = format!("{}/{}", self.api_root(Host::GitHub), repo.full);
        let (_, body) = self.get(Host::GitHub, &base).await?;
        let value: serde_json::Value = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("{base}: {error}")))?;
        let mut snapshot = Snapshot {
            stars: value.get("stargazers_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            forks: value.get("forks_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            open_issues: value.get("open_issues_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            pushed_at: timestamp(value.get("pushed_at")),
            license: value
                .get("license")
                .and_then(|license| license.get("spdx_id"))
                .and_then(serde_json::Value::as_str)
                .filter(|spdx| *spdx != "NOASSERTION")
                .map(str::to_string),
            contributors: 0,
            releases_90d: 0,
            commits: 0,
            median_review_hours: 0.0,
            review_sample: 0,
            prs: 0,
        };

        let (_, body) = self
            .get(Host::GitHub, &format!("{base}/contributors?per_page=100&anon=0"))
            .await?;
        let contributors: Vec<serde_json::Value> = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("contributors: {error}")))?;
        snapshot.contributors = contributors.len() as i64;

        let (_, body) = self.get(Host::GitHub, &format!("{base}/releases?per_page=100")).await?;
        let releases: Vec<serde_json::Value> = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("releases: {error}")))?;
        let cutoff = now_unix() - 90.0 * 86400.0;
        snapshot.releases_90d = releases
            .iter()
            .filter(|release| timestamp(release.get("published_at")) > cutoff)
            .count() as i64;

        // The total is the Link header's last page with `per_page=1`, not the page length.
        let commits_url = format!("{base}/commits?per_page=1");
        let (_, body) = self.get(Host::GitHub, &commits_url).await?;
        snapshot.commits = self.total_from_link(Host::GitHub, &commits_url, &body).await?;

        let (hours, sample, prs) = self.review_hours(Host::GitHub, &base, &repo.full).await?;
        snapshot.median_review_hours = hours;
        snapshot.review_sample = sample;
        snapshot.prs = prs;
        Ok(snapshot)
    }

    /// Read `rel="last"` from a `Link` header, or fall back to the page length.
    async fn total_from_link(
        &self,
        host: Host,
        url: &str,
        body: &[u8],
    ) -> Result<i64, FetchError> {
        let request = self.http.head(url);
        let request = match (host, self.token(host)) {
            (_, Some(token)) if !token.is_empty() => request.bearer_auth(token),
            _ => request,
        };
        if let Ok(response) = request.send().await {
            if let Some(link) = response.headers().get("link").and_then(|v| v.to_str().ok()) {
                for part in link.split(',') {
                    if !part.contains("rel=\"last\"") {
                        continue;
                    }
                    // `page=` and NOT `per_page=`: the first match in a real Link header is `per_page=1`, so a
                    // plain find reports the page size as the total — which is what the probe caught.
                    let Some(start) = part
                        .match_indices("page=")
                        .find(|(at, _)| *at == 0 || part.as_bytes()[at - 1] != b'_')
                        .map(|(at, _)| at)
                    else {
                        continue;
                    };
                    {
                        let digits: String = part[start + 5..]
                            .chars()
                            .take_while(char::is_ascii_digit)
                            .collect();
                        if let Ok(total) = digits.parse::<i64>() {
                            return Ok(total);
                        }
                    }
                }
            }
        }
        let items: Vec<serde_json::Value> = serde_json::from_slice(body).unwrap_or_default();
        Ok(items.len() as i64)
    }

    /// A Forgejo repository.
    async fn forgejo(&self, repo: &Repo) -> Result<Snapshot, FetchError> {
        let base = format!("{}/{}", self.api_root(Host::Forgejo), repo.full);
        let (_, body) = self.get(Host::Forgejo, &base).await?;
        let value: serde_json::Value = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("{base}: {error}")))?;
        let mut snapshot = Snapshot {
            stars: value.get("stars_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            forks: value.get("forks_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            open_issues: value.get("open_issues_count").and_then(serde_json::Value::as_i64).unwrap_or(0),
            pushed_at: timestamp(value.get("updated_at")),
            license: None,
            contributors: 0,
            releases_90d: 0,
            commits: 0,
            median_review_hours: 0.0,
            review_sample: 0,
            prs: 0,
        };

        let (_, body) = self.get(Host::Forgejo, &format!("{base}/commits?limit=50")).await?;
        let commits: Vec<serde_json::Value> = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("commits: {error}")))?;
        // Distinct authors on one page, exactly as the Go `countForgejoAuthors` does: a floor.
        let mut authors = std::collections::BTreeSet::new();
        for commit in &commits {
            if let Some(login) = commit
                .get("author")
                .and_then(|author| author.get("login"))
                .and_then(serde_json::Value::as_str)
            {
                authors.insert(login.to_string());
            }
        }
        snapshot.contributors = authors.len() as i64;

        let (_, body) = self.get(Host::Forgejo, &format!("{base}/releases?limit=50")).await?;
        let releases: Vec<serde_json::Value> = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("releases: {error}")))?;
        let cutoff = now_unix() - 90.0 * 86400.0;
        snapshot.releases_90d = releases
            .iter()
            .filter(|release| timestamp(release.get("published_at")) > cutoff)
            .count() as i64;

        let commits_url = format!("{base}/commits?limit=1");
        let (total, _) = self.get(Host::Forgejo, &commits_url).await?;
        snapshot.commits = total
            .and_then(|text| text.parse::<i64>().ok())
            .unwrap_or(0);

        let (hours, sample, prs) = self.review_hours(Host::Forgejo, &base, &repo.full).await?;
        snapshot.median_review_hours = hours;
        snapshot.review_sample = sample;
        snapshot.prs = prs;
        Ok(snapshot)
    }

    /// The median hours to the first response by somebody else, over the most recently updated pull requests.
    ///
    /// Rule 3 and rule 4 live here: an unanswered pull request contributes nothing, and a response is the
    /// earliest comment OR review by somebody who is not the author.
    async fn review_hours(
        &self,
        host: Host,
        base: &str,
        _full: &str,
    ) -> Result<(f64, usize, usize), FetchError> {
        let (path, number_key, author_key, at_key) = match host {
            Host::GitHub => (
                format!("{base}/pulls?state=all&per_page={}&sort=updated", self.review_window),
                "number",
                "user",
                "created_at",
            ),
            Host::Forgejo => (
                format!("{base}/pulls?state=all&limit={}", self.review_window),
                "number",
                "user",
                "created_at",
            ),
        };
        let (_, body) = self.get(host, &path).await?;
        let pulls: Vec<serde_json::Value> = serde_json::from_slice(&body)
            .map_err(|error| FetchError::Failed(format!("pulls: {error}")))?;
        let mut hours: Vec<f64> = Vec::new();
        for pull in &pulls {
            let number = pull.get(number_key).and_then(serde_json::Value::as_i64).unwrap_or(0);
            let opened = timestamp(pull.get(at_key));
            let author = pull
                .get(author_key)
                .and_then(|user| user.get("login"))
                .and_then(serde_json::Value::as_str)
                .unwrap_or("")
                .to_string();
            let (reviews_path, comments_path) = match host {
                Host::GitHub => (
                    format!("{base}/pulls/{number}/reviews?per_page=100"),
                    format!("{base}/issues/{number}/comments?per_page=100"),
                ),
                Host::Forgejo => (
                    format!("{base}/pulls/{number}/reviews"),
                    format!("{base}/issues/{number}/comments"),
                ),
            };
            let mut first: Option<f64> = None;
            for path in [reviews_path, comments_path] {
                let (_, body) = self.get(host, &path).await?;
                let entries: Vec<serde_json::Value> = serde_json::from_slice(&body)
                    .map_err(|error| FetchError::Failed(format!("{path}: {error}")))?;
                for entry in &entries {
                    // A review says `submitted_at`, a comment says `created_at`.
                    let at = timestamp(entry.get("submitted_at")).max(timestamp(entry.get("created_at")));
                    let who = entry
                        .get("user")
                        .and_then(|user| user.get("login"))
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or("");
                    if at <= 0.0 || at < opened || who.is_empty() || who == author {
                        continue;
                    }
                    if first.is_none_or(|current| at < current) {
                        first = Some(at);
                    }
                }
            }
            if let Some(first) = first {
                hours.push((first - opened) / 3600.0);
            }
        }
        Ok((median(&hours), hours.len(), pulls.len()))
    }
}

/// The lower-middle median — the same rule as the newcomer figure, and for the same reason: an interpolated
/// median invents a response time nobody had.
fn median(values: &[f64]) -> f64 {
    if values.is_empty() {
        return 0.0;
    }
    let mut sorted = values.to_vec();
    sorted.sort_by(f64::total_cmp);
    sorted[(sorted.len() - 1) / 2]
}

/// Unix seconds now, from the same clock the rest of the binary uses.
fn now_unix() -> f64 {
    chrono::Utc::now().timestamp() as f64
}

/// A JSON timestamp as unix seconds, `0.0` when absent or unparseable.
fn timestamp(value: Option<&serde_json::Value>) -> f64 {
    let Some(text) = value.and_then(serde_json::Value::as_str) else {
        return 0.0;
    };
    chrono::DateTime::parse_from_rfc3339(text)
        .map(|at| at.timestamp() as f64)
        .unwrap_or(0.0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_three_shapes_of_one_url_normalise_to_one_value() {
        for raw in [
            "https://github.com/owner/repo",
            "https://github.com/owner/repo.git",
            "http://github.com/owner/repo/",
            "git@github.com:owner/repo.git",
            "ssh://git@github.com/owner/repo",
        ] {
            let repo = parse_repo_url(raw).unwrap_or_else(|error| panic!("{raw}: {error:?}"));
            assert_eq!(repo.full, "owner/repo", "{raw}");
            assert_eq!(repo.host, Host::GitHub);
        }
    }

    #[test]
    fn an_unknown_host_is_refused_naming_it() {
        let error = parse_repo_url("https://example.com/owner/repo").unwrap_err();
        assert!(error.0.contains("example.com"), "{error:?}");
    }

    #[test]
    fn a_path_that_is_not_owner_repo_is_refused() {
        for raw in ["https://github.com/owner", "https://github.com/a/b/c"] {
            assert!(parse_repo_url(raw).is_err(), "{raw}");
        }
    }

    #[test]
    fn a_port_is_transport_and_not_identity() {
        let repo = parse_repo_url("https://github.com:443/owner/repo").expect("port stripped");
        assert_eq!(repo.full, "owner/repo");
    }

    #[test]
    fn the_median_is_lower_middle() {
        assert_eq!(median(&[10.0, 2.0]), 2.0);
        assert_eq!(median(&[1.0, 2.0, 3.0]), 2.0);
        assert_eq!(median(&[]), 0.0);
    }
}
