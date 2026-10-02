package embed

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables for the embedder, read by FromEnv.
//
// The model server is opt-in by endpoint, not by a boolean flag: naming a
// reachable endpoint is itself the opt-in, so there is no configuration state
// where embedding is "on" but points nowhere. With the variable unset the
// hashed embedder is used and the service has no external dependency.
const (
	// EnvOllamaURL points at an ollama-compatible server. Unset or empty means
	// use the local hashed embedder.
	EnvOllamaURL = "CONCORD_EMBED_URL"
	// EnvOllamaModel overrides the model name. Defaults to nomic-embed-text.
	EnvOllamaModel = "CONCORD_EMBED_MODEL"
	// EnvOllamaTimeoutMS bounds one embedding request. Defaults to 8000.
	EnvOllamaTimeoutMS = "CONCORD_EMBED_TIMEOUT_MS"
)

// DefaultOllamaModel is the model used when none is named.
//
// nomic-embed-text: 768 dims, 274 MB, and the separation between same-problem
// and different-problem pairs measured well clear of the hashed embedder's (see
// Ollama). It is also already present in the local ollama model store, so
// enabling it costs no download.
const DefaultOllamaModel = "nomic-embed-text:latest"

// FromEnv builds the configured embedder.
//
// Always succeeds with a usable embedder: the hashed one is the floor, and a
// misconfigured model server degrades to it at request time rather than at
// startup. Startup is the wrong place to reject this configuration -- refusing
// to boot a filing service because a search helper is unreachable inverts the
// importance of the two.
func FromEnv() (Embedder, error) {
	endpoint := strings.TrimSpace(os.Getenv(EnvOllamaURL))
	if endpoint == "" {
		return NewHashed(), nil
	}

	model := strings.TrimSpace(os.Getenv(EnvOllamaModel))
	if model == "" {
		model = DefaultOllamaModel
	}
	timeout := 8 * time.Second
	if v := strings.TrimSpace(os.Getenv(EnvOllamaTimeoutMS)); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	remote, err := NewOllama(OllamaConfig{
		Endpoint: endpoint,
		Model:    model,
		Timeout:  timeout,
		// nomic-embed-text was trained with this task prefix; without it the
		// vectors are measurably worse on retrieval-style comparisons.
		Prefix:   "search_document: ",
		Fallback: NewHashed(),
	})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	return remote, nil
}
