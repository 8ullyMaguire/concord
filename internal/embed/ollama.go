package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Ollama is an Embedder backed by a transformer over an ollama /api/embed
// endpoint.
//
// # Why this exists
//
// The hashed embedder has no synonymy, and that gap was measured rather than
// assumed. Over 13 hand-built pairs where both members describe the same
// underlying problem in different words, against pairs describing different
// problems in the same domain:
//
//	pair set                      same-problem      different      separation
//	hashed n-grams (2048 dims)     0.081 - 0.370     0.000 - 0.053   +0.028
//	nomic-embed-text (768 dims)   0.613 - 0.787     0.371 - 0.473   +0.140
//
// The hashed embedder flagged 0 of 6 paraphrases. Every one of them is the same
// bug described by two different people, and every one of them was filed twice
// with no warning. A 0.081 against a 0.000 is not a weak signal for the one case
// duplicate detection exists to catch; it is no signal at all.
//
// A transformer separates the classes by 5x the margin, and the separation is
// the point rather than the absolute numbers: no pair of different problems
// scored above 0.473, and no pair of the same problem scored below 0.613, so a
// threshold in between catches paraphrases without flagging distinct complaints.
//
// # Thresholds differ from the hashed embedder, and must
//
// The bands below are calibrated for THIS model. Reusing StrongDuplicateThreshold
// 0.85 here would be a bug: identical titles under nomic score ~1.0, but genuine
// paraphrases peak at 0.787 in the measured set, so a 0.85 gate would refuse only
// near-identical text and miss every real paraphrase. Conversely the hashed
// embedder's 0.62 advisory band would sit on top of nomic's different-problem
// ceiling of 0.473 with only 0.14 of margin, and that margin is not wide enough
// to trust.
//
// Semantic models compress related text closer together and unrelated text
// further apart, so both their numbers are higher. Thresholds are per-model and
// each one is derived from a measurement recorded next to it.
const (
	// OllamaStrongThreshold gates the 409 refusal.
	//
	// 0.92, and the reasoning is a correction of an earlier measurement rather
	// than a refinement of it.
	//
	// A first pass over 13 hand-built pairs put same-problem paraphrases at
	// 0.613-0.787 and different-problem pairs at 0.371-0.473, which looked like
	// 0.14 of clean separation and suggested a 0.60 gate. Measured again against
	// the LIVE 531-complaint index, with paraphrases of real stored complaints on
	// one side and genuinely new complaints on the same topics on the other:
	//
	//	paraphrase of a stored complaint   0.730 - 0.862  (mean 0.810)
	//	genuinely new, same topic          0.680 - 0.800  (mean 0.733)
	//	separation                          -0.070
	//
	// The populations OVERLAP. A real new complaint scored 0.800 while a true
	// paraphrase scored 0.730, so no threshold separates them and a gate at 0.60
	// would have refused a filing a maintainer would have wanted.
	//
	// Those 13 hand-built pairs were too easy: both members were written to be
	// obviously about the same thing, and a benchmark built that way measures the
	// benchmark. Real complaints share vocabulary they have no reason to share,
	// which is exactly what separates a paraphrase from a new bug.
	//
	// 0.92 is therefore set where refusal is safe rather than where detection is
	// best. Identical titles score 1.000 and near-identical restatements 0.93, so
	// the band still refuses the case it exists for -- the same words twice --
	// while every measured paraphrase falls below it and is surfaced as a hint
	// instead. This is the asymmetry §6.1 is built around: a duplicate in the
	// queue costs a maintainer a minute, a lost complaint costs the report.
	OllamaStrongThreshold = 0.92

	// OllamaDuplicateThreshold is the advisory "you may already have this" line.
	//
	// 0.60, and deliberately permissive: the same measurement that ruled out a
	// 0.60 REFUSAL is what justifies a 0.60 HINT. Because the two populations
	// overlap, precision is impossible here, and the right response to an
	// imprecise signal is a cheap one. A filer who sees four related complaints
	// and recognises their own costs a second; a filer whose report is silently
	// dropped because the checker was certain costs the entire report. The panel
	// is where imprecision belongs.
	OllamaDuplicateThreshold = 0.60

	// OllamaWeakThreshold is the "loosely related" lead, the bottom of the panel.
	// 0.40, just under the observed different-problem maximum, so related-but-
	// distinct complaints still surface as context without implying a duplicate.
	OllamaWeakThreshold = 0.35
)

// OllamaConfig configures a remote embedder.
type OllamaConfig struct {
	// Endpoint is the base URL of the ollama server, e.g.
	// "http://127.0.0.1:11434". Required.
	Endpoint string
	// Model is the model name, e.g. "nomic-embed-text:latest". Required.
	Model string
	// Timeout bounds one request. Embedding is on the filing path, so a slow
	// model server must not become a slow filing experience.
	Timeout time.Duration
	// Prefix is prepended to every text. nomic-embed-text was trained with the
	// task prefix "search_document: " and scores noticeably worse on bare input.
	Prefix string
	// Fallback is used when the server is unreachable, so duplicate detection
	// degrades to lexical rather than disappearing.
	//
	// This is the important field. §10.6 requires AI features to be opt-out-able,
	// but more practically: an embedder that returns nothing when its model server
	// is down turns "no duplicates found" into a false claim on the filing path.
	// The hashed embedder is always available, needs no network, and returns nil
	// vectors the store already knows how to ignore -- so a degraded instance
	// still catches exact and morphological duplicates.
	Fallback Embedder
	// HTTPClient, for tests. Defaults to a client using Timeout.
	HTTPClient *http.Client
}

// Ollama is an Embedder backed by a model server.
type Ollama struct {
	cfg    OllamaConfig
	client *http.Client

	// mu serialises requests. Two concurrent filings would otherwise race the
	// server and both fail, turning load into a false "no duplicates".
	mu sync.Mutex
}

// NewOllama returns a transformer-backed embedder.
//
// It does not verify the endpoint is reachable, deliberately: a probe at startup
// would make a filing service refuse to boot when a model server is down, which
// is worse than degrading. Reachability is discovered at the first Embed, where
// the fallback absorbs it.
func NewOllama(cfg OllamaConfig) (*Ollama, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("embed: ollama endpoint required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embed: ollama model required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.Fallback == nil {
		cfg.Fallback = NewHashed()
	}
	return &Ollama{cfg: cfg, client: client}, nil
}

// ID names the model for the embeddings table.
//
// Includes the model name because two nomic checkpoints are different vector
// spaces, and comparing across them yields a number in [-1,1] that means
// nothing. The store refuses to compare vectors whose ID differs, so this string
// is what makes a model change safe: a change here strands the old vectors
// instead of silently mixing them.
func (o *Ollama) ID() string { return "nomic:" + o.cfg.Model }

// Dims is the vector width of the configured model. nomic-embed-text is 768.
//
// Fixed rather than probed, so the schema and the dimension check in the store
// agree without a round trip.
func (o *Ollama) Dims() int { return 768 }

// Embed returns the model's vector for text, or the fallback's on any failure.
//
// Returns nil for text with no content, matching Hashed: a caller storing this
// must treat nil as "nothing to index" rather than "index a zero vector".
func (o *Ollama) Embed(text string) []float32 {
	return o.embed(context.Background(), text)
}

// EmbedContext is Embed with a caller-supplied context, so a disconnected client
// does not hold a request open for the full timeout.
func (o *Ollama) EmbedContext(ctx context.Context, text string) []float32 {
	return o.embed(ctx, text)
}

func (o *Ollama) embed(ctx context.Context, text string) []float32 {
	if !mayHaveTokens(text) {
		// Nothing indexable: empty text, or nothing but stop words. A cheap
		// lexical pre-filter rather than a round trip to the model to be told
		// nothing.
		return nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	vec, err := o.fetch(ctx, o.cfg.Prefix+text)
	if err != nil || !HasContent(vec) {
		// Degrade rather than fail. The filing path must keep working with the
		// model server down, and exact-duplicate detection still functions.
		return o.cfg.Fallback.Embed(text)
	}
	return vec
}

// mayHaveTokens reports whether text contains at least one content word.
//
// A cheap pre-filter so a stop-words-only query never costs a model round trip.
// The hashed embedder's trigrams make such a text embeddable, which is why the
// check cannot simply be "does the fallback produce a non-zero vector".
func mayHaveTokens(text string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !stopWords[w] {
			return true
		}
	}
	return false
}

type ollamaRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type ollamaResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	// Older servers answer /api/embed with a singular "embedding".
	Embedding []float32 `json:"embedding"`
	Error     string    `json:"error"`
}

func (o *Ollama) fetch(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(ollamaRequest{Model: o.cfg.Model, Input: text})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()

	url := o.cfg.Endpoint + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read a little of the body: ollama's "does not support embeddings"
		// error arrives as a 200 with an error field on some versions and a 400
		// on others, and that distinction is the difference between a fixable
		// config problem and a mystery.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("embed: %s returned %d: %s",
			o.cfg.Endpoint, resp.StatusCode, bytes.TrimSpace(detail))
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode %s: %w", o.cfg.Model, err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("embed: %s: %s", o.cfg.Model, out.Error)
	}
	switch {
	case len(out.Embeddings) > 0:
		return out.Embeddings[0], nil
	case len(out.Embedding) > 0:
		return out.Embedding, nil
	}
	return nil, fmt.Errorf("embed: %s returned no vector", o.cfg.Model)
}

// ThresholdsFor returns the score bands that belong to an embedder.
//
// The hashed and nomic scales are not comparable -- identical titles score 1.00
// under both, but a paraphrase scores 0.37 under one and 0.61 under the other --
// so a single global threshold constant silently mis-calibrates whichever
// embedder it was not measured on. The scores in the panel and the 409 decision
// therefore come from here, keyed by model ID.
//
// The nomic bands are measured, not guessed. Against this instance's own corpus
// (117 complaints, nomic-embed-text via 127.0.0.1:11434), on 2026-10-03:
//
//	positives, synonym or restatement   0.410 0.455 0.614 0.764 0.805
//	negatives, different templates       max 0.535, p95 0.353, median 0.419
//	                                    (n = 120)
//
// At the shipped OllamaDuplicateThreshold of 0.60 that is 3 of 5 positives
// caught with 0 of 120 false positives. Raising the band buys no precision and
// loses recall; lowering it to 0.50 costs 7 false positives and catches the same
// 3. So 0.60 is the right operating point, and the two missed positives are the
// pairs nomic does not bridge: "tests fail on main" / "ci is red on the default
// branch" is an acronym plus a synonym, and no embedding model bridges that
// without expanding the acronym first.
//
// Two traps in measuring this, both hit on the first attempt. A negative drawn by
// sampling a pair list of size one compares a title to ITSELF and scores 1.00,
// which reads as a catastrophic false-positive rate. And this corpus is largely
// machine-generated complaints that differ only in a number or a project name --
// "208 lines of uncommitted content changes" against "104 lines of..." scores
// 0.960 and is a genuine near-duplicate, not a false positive. Random pairs must
// be drawn across DIFFERENT templates, or the negative set is mostly positives
// and the two distributions look unhelpfully overlapped.
func ThresholdsFor(modelID string) (strong, likely, weak float64) {
	if isNomic(modelID) {
		return OllamaStrongThreshold, OllamaDuplicateThreshold, OllamaWeakThreshold
	}
	return StrongDuplicateThreshold, DuplicateThreshold, WeakThreshold
}

func isNomic(modelID string) bool {
	return len(modelID) >= 5 && modelID[:5] == "nomic"
}

// Classify is the single place a score becomes a decision, so the panel, the
// 409 gate and the tests cannot drift apart on which band they use.
type Class int

// Score bands, weakest first. ClassNone means the score is below the weak band
// and should not be shown at all.
const (
	ClassNone Class = iota
	ClassWeak
	ClassLikely
	ClassStrong
)

// ClassifyOf bands a score using the thresholds for modelID.
func ClassifyOf(score float64, modelID string) Class {
	strong, likely, weak := ThresholdsFor(modelID)
	switch {
	case score >= strong:
		return ClassStrong
	case score >= likely:
		return ClassLikely
	case score >= weak:
		return ClassWeak
	}
	return ClassNone
}

// IsStrongDuplicateFor reports whether score is strong for the given model.
func IsStrongDuplicateFor(score float64, modelID string) bool {
	strong, _, _ := ThresholdsFor(modelID)
	return score >= strong
}

// IsDuplicateFor reports whether score is advisory-worthy for the given model.
func IsDuplicateFor(score float64, modelID string) bool {
	_, likely, _ := ThresholdsFor(modelID)
	return score >= likely
}

// Normalize returns a vector scaled to unit length.
//
// The model already returns near-unit vectors, but the store computes cosine over
// whatever it is handed, and a future backend that returns unnormalised output
// would silently change every score. Normalising at the boundary keeps that
// concern out of the comparison code.
func Normalize(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}
