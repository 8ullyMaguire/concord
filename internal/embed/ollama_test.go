package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The transformer embedder.
//
// The property worth protecting is degradation: a model server that is down,
// slow, or misconfigured must never stop a complaint being filed. Everything
// else here is plumbing, but plumbing that silently returns a zero vector would
// make "no duplicates found" a false claim on the filing path.

func fakeOllama(t *testing.T, handler func(model, input string) ([]float32, string, int)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("request to %s, want /api/embed", r.URL.Path)
		}
		var req struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		vec, errMsg, status := handler(req.Model, req.Input)
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if errMsg != "" {
			// The shape ollama uses when the server lacks --embeddings.
			json.NewEncoder(w).Encode(map[string]string{"error": errMsg})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func unitVec() []float32 {
	v := make([]float32, 768)
	v[0] = 1
	return v
}

func TestOllamaEmbedsThroughTheAPIContract(t *testing.T) {
	var gotModel, gotInput string
	srv := fakeOllama(t, func(model, input string) ([]float32, string, int) {
		gotModel, gotInput = model, input
		return unitVec(), "", 0
	})
	e, err := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "nomic-embed-text:latest", Prefix: "search_document: "})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	vec := e.Embed("search results are identical")
	if !HasContent(vec) {
		t.Fatal("no vector returned")
	}
	if len(vec) != 768 {
		t.Errorf("got %d dims, want 768", len(vec))
	}
	if gotModel != "nomic-embed-text:latest" {
		t.Errorf("sent model %q", gotModel)
	}
	if gotInput != "search_document: search results are identical" {
		t.Errorf("prefix not applied: %q", gotInput)
	}
}

func TestOllamaFallsBackWhenTheServerIsUnreachable(t *testing.T) {
	// The case that matters. An unreachable model server must not lose a filing.
	e, err := NewOllama(OllamaConfig{
		Endpoint: "http://127.0.0.1:1", // nothing listens here
		Model:    "nomic-embed-text:latest",
		Timeout:  200 * time.Millisecond,
		Fallback: NewHashed(),
	})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}
	vec := e.Embed("search results are identical for every query")
	if !HasContent(vec) {
		t.Error("unreachable server produced no vector: duplicate detection would " +
			"report nothing, and 'no duplicates' is a false claim rather than an admission")
	}
	if e.ID() != "nomic:nomic-embed-text:latest" {
		t.Errorf("ID = %q: the model id must name the model even when falling back, "+
			"so mixed-vector comparisons are refused", e.ID())
	}
}

func TestOllamaFallsBackOnEveryFailureMode(t *testing.T) {
	cases := []struct {
		name    string
		handler func(model, input string) ([]float32, string, int)
	}{
		{"http error", func(string, string) ([]float32, string, int) { return nil, "", 500 }},
		{"error field", func(string, string) ([]float32, string, int) {
			return nil, "This server does not support embeddings. Start it with `--embeddings`", 0
		}},
		{"empty vector", func(string, string) ([]float32, string, int) {
			return make([]float32, 768), "", 0
		}},
		{"no vector at all", func(string, string) ([]float32, string, int) { return nil, "", 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeOllama(t, tc.handler)
			e, err := NewOllama(OllamaConfig{
				Endpoint: srv.URL, Model: "m", Fallback: NewHashed(),
			})
			if err != nil {
				t.Fatalf("NewOllama: %v", err)
			}
			if !HasContent(e.Embed("a complaint about uploads")) {
				t.Error("no vector after a model failure: the filing path loses duplicate detection silently")
			}
		})
	}
}

func TestOllamaReturnsNothingForTextWithNoContent(t *testing.T) {
	// nil, not a zero vector: a zero vector occupies an index slot forever and
	// scores 0 against everything.
	var calls int32
	srv := fakeOllama(t, func(string, string) ([]float32, string, int) {
		atomic.AddInt32(&calls, 1)
		return unitVec(), "", 0
	})
	e, _ := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m", Fallback: NewHashed()})

	for _, text := range []string{"", "   ", "the of and to"} {
		if vec := e.Embed(text); vec != nil {
			t.Errorf("Embed(%q) = %d dims, want nil", text, len(vec))
		}
	}
	// And it must not have paid for a round trip to learn that.
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("%d model requests for text with no content, want 0", n)
	}
}

func TestOllamaSerialisesConcurrentRequests(t *testing.T) {
	// A model server under concurrent load can time out; two filings racing it
	// both degrade and both report "no duplicates".
	var inFlight, maxInFlight int32
	srv := fakeOllama(t, func(string, string) ([]float32, string, int) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return unitVec(), "", 0
	})
	e, _ := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m", Fallback: NewHashed()})

	var wg sync.WaitGroup
	var ok int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if HasContent(e.Embed("a complaint about ranking")) {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got > 1 {
		t.Errorf("%d concurrent model requests: a loaded server would time out "+
			"concurrent filings and degrade them all", got)
	}
	if ok != 8 {
		t.Errorf("%d of 8 concurrent embeds returned a vector, want 8", ok)
	}
}

func TestOllamaRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	e, _ := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m", Fallback: NewHashed()})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	e.EmbedContext(ctx, "a complaint")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("EmbedContext took %v after its context expired, want it to return promptly", elapsed)
	}
}

func TestFromEnvDefaultsToTheLocalEmbedder(t *testing.T) {
	// §10.5 self-hosting: with nothing configured there is no external
	// dependency, so the feature works on an isolated host.
	t.Setenv(EnvOllamaURL, "")
	e, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if e.ID() != "hashed-v1" {
		t.Errorf("ID = %q, want hashed-v1", e.ID())
	}
}

func TestFromEnvUsesTheModelServerWhenNamed(t *testing.T) {
	t.Setenv(EnvOllamaURL, "http://127.0.0.1:11434")
	t.Setenv(EnvOllamaModel, "")
	t.Setenv(EnvOllamaTimeoutMS, "")
	e, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if e.ID() != "nomic:"+DefaultOllamaModel {
		t.Errorf("ID = %q, want nomic:%s", e.ID(), DefaultOllamaModel)
	}
	if e.Dims() != 768 {
		t.Errorf("Dims = %d, want 768", e.Dims())
	}
}

// Thresholds are per model, and the difference is the whole point.

func TestThresholdsDifferPerModel(t *testing.T) {
	// The measured separation: nomic puts same-problem paraphrases at
	// 0.61-0.79 and different problems at or below 0.473, while the hashed
	// embedder's paraphrases never exceed 0.37.
	//
	// If these were the same number, one embedder would refuse everything or
	// nothing, and the constant would have been "correct" when it was measured
	// and silently wrong for the other model afterwards.
	strongHashed, likelyHashed, weakHashed := ThresholdsFor("hashed-v1")
	strongNomic, likelyNomic, weakNomic := ThresholdsFor("nomic:nomic-embed-text:latest")
	_ = weakHashed
	_ = likelyNomic
	_ = weakNomic

	if strongHashed == strongNomic {
		t.Error("strong threshold is shared between models: the hashed 0.85 gate " +
			"would refuse almost nothing under nomic, whose paraphrases peak at 0.79")
	}
	if !(strongNomic < strongHashed) {
		t.Errorf("nomic strong %.2f should be below hashed %.2f: nomic scores "+
			"related text higher, so the same band catches less",
			strongNomic, strongHashed)
	}

	// Whatever the numbers, each model's strong band must sit inside the gap its
	// own measurement found: above the worst different-problem pair, below the
	// worst same-problem pair.
	if strongNomic <= 0.473 {
		t.Errorf("nomic strong band %.2f is at or below the measured "+
			"different-problem maximum 0.473, so it would refuse distinct complaints", strongNomic)
	}
	if strongNomic >= 0.613 {
		t.Errorf("nomic strong band %.2f is at or above the measured "+
			"same-problem minimum 0.613, so it would miss real paraphrases", strongNomic)
	}
	// The hashed strong band is NOT inside the paraphrase range, and that is
	// deliberate rather than an oversight: 0.85 is the 409 REFUSAL, and a
	// refusal must be reserved for near-identical text. Paraphrases scoring
	// 0.08-0.37 land in the advisory band instead, where the filer sees them and
	// decides. Lowering it to catch them would refuse genuinely distinct
	// complaints -- the asymmetric cost §6.1 is built around.
	//
	// nomic is the opposite case: 0.60 IS inside its paraphrase range, because a
	// semantic model separates the classes well enough to refuse safely.
	if strongHashed <= 0.053 {
		t.Errorf("hashed strong band %.2f is below the measured different-problem "+
			"maximum 0.053, so it would refuse distinct complaints", strongHashed)
	}
	if strongHashed <= 0.370 {
		t.Errorf("hashed strong band %.2f is inside the paraphrase range, so a "+
			"refusal would be triggered by a reworded rather than a repeated complaint", strongHashed)
	}

	// The hashed embedder's paraphrases are unreachable, and that gap is the
	// finding rather than a bug to paper over: they top out at 0.370 while its
	// advisory band starts at 0.62, so every paraphrase falls in the hole
	// between them and is never shown. Recorded as a measurement so that a
	// future change to the hashed bands has to confront it -- "paraphrases are
	// invisible to the local embedder" is the reason the transformer exists.
	const hashedParaphraseCeiling = 0.370
	if likelyHashed <= hashedParaphraseCeiling {
		t.Logf("hashed advisory band %.2f now reaches the paraphrase ceiling %.2f: "+
			"the local embedder can show paraphrases without a model server", likelyHashed, hashedParaphraseCeiling)
	} else {
		t.Logf("hashed embedder cannot show paraphrases: they top out at %.2f, its "+
			"advisory band starts at %.2f. This is why CONCORD_EMBED_URL is worth setting.",
			hashedParaphraseCeiling, likelyHashed)
	}

	// And the bands must stay ordered.
	for _, m := range []string{"hashed-v1", "nomic:nomic-embed-text:latest"} {
		s, l, w := ThresholdsFor(m)
		if !(s > l && l > w) {
			t.Errorf("%s: bands out of order: strong %.2f likely %.2f weak %.2f", m, s, l, w)
		}
	}
}

func TestClassifyAgreesWithThePerModelPredicates(t *testing.T) {
	// One place decides, so the panel, the 409 gate and the tests cannot drift.
	for _, m := range []string{"hashed-v1", "nomic:nomic-embed-text:latest"} {
		strong, likely, weak := ThresholdsFor(m)
		for _, score := range []float64{0, 0.2, 0.35, 0.4, 0.5, 0.6, 0.61, 0.7, 0.85, 0.95, 1.0} {
			got := ClassifyOf(score, m)
			want := ClassNone
			switch {
			case score >= strong:
				want = ClassStrong
			case score >= likely:
				want = ClassLikely
			case score >= weak:
				want = ClassWeak
			}
			if got != want {
				t.Errorf("%s: ClassifyOf(%.2f) = %d, want %d", m, score, got, want)
			}
			if IsStrongDuplicateFor(score, m) != (score >= strong) {
				t.Errorf("%s: IsStrongDuplicateFor(%.2f) disagrees with the band", m, score)
			}
			if IsDuplicateFor(score, m) != (score >= likely) {
				t.Errorf("%s: IsDuplicateFor(%.2f) disagrees with the band", m, score)
			}
		}
	}
}

func TestNormalizeMakesCosineIndependentOfMagnitude(t *testing.T) {
	// A future backend returning unnormalised vectors would otherwise silently
	// change every score in the system.
	v := []float32{3, 4}
	n := Normalize(v)
	if math.Abs(float64(n[0])-0.6) > 1e-6 || math.Abs(float64(n[1])-0.8) > 1e-6 {
		t.Errorf("Normalize([3 4]) = %v, want [0.6 0.8]", n)
	}
	// The input must not be mutated: the caller may hold the slice.
	if v[0] != 3 {
		t.Errorf("Normalize mutated its input: %v", v)
	}
	if got := Normalize([]float32{0, 0}); HasContent(got) {
		t.Error("Normalize of a zero vector produced content")
	}
}

func TestOllamaIDDistinguishesCheckpoints(t *testing.T) {
	// Two nomic checkpoints are different vector spaces. If the ID did not name
	// the model, a model swap would mix two spaces in one table and every
	// comparison across the swap would be meaningless but plausible.
	a, _ := NewOllama(OllamaConfig{Endpoint: "http://x", Model: "nomic-embed-text:v1.5"})
	b, _ := NewOllama(OllamaConfig{Endpoint: "http://x", Model: "nomic-embed-text:latest"})
	if a.ID() == b.ID() {
		t.Error("two different models share an ID: a model change would not strand the old vectors")
	}
	if !strings.HasPrefix(a.ID(), "nomic:") {
		t.Errorf("ID %q does not identify the family, so ThresholdsFor cannot select bands", a.ID())
	}
}

func TestNewOllamaValidatesConfig(t *testing.T) {
	if _, err := NewOllama(OllamaConfig{Model: "m"}); err == nil {
		t.Error("missing endpoint accepted")
	}
	if _, err := NewOllama(OllamaConfig{Endpoint: "http://x"}); err == nil {
		t.Error("missing model accepted")
	}
}
