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

// Operating note for the model server this was calibrated against.
//
// ollama 0.35.0 on this host serves /api/embed with NO extra flag. There is no
// --embeddings option; passing one makes the binary exit 1 with "unknown flag" and
// systemd then restart-loops it. The "This server does not support embeddings"
// message seen while developing this belongs to other ollama builds, not this one.
//
// Two things about the host's setup, both discovered the hard way:
//
//   - The systemd unit is disabled and cannot own the port. It runs as
//     User=ollama, which cannot traverse /home/alvaro (mode 0700), so it starts
//     against an empty model directory and /api/embed answers "model not found".
//     The 23GB model store is under the user's home, so the working arrangement is
//     ollama run as the user, which is how it has always run here. It does not
//     survive a reboot, and did not before this either.
//   - So there is no boot-time ordering requirement to honour: if ollama is not
//     running, Ollama.Embed falls back to the hashed embedder and filing continues
//     with weaker duplicate detection. Nothing needs to wait for it.
//
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
