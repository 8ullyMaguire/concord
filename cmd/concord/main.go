package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/embed"
	"git.polarisocial.xyz/concord/concord/internal/httpapi"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// embeddingCoverage measures every kind in one call, so the startup warning
// cannot itself become the thing that fails: one unreadable kind reports and the
// rest are still checked.
//
// A kind with no backing table yet returns a map with no "total" key, which is
// skipped by the caller rather than treated as zero -- "not applicable" and
// "nothing to index" are different answers and only the second warrants a
// warning.
func embeddingCoverage(ctx context.Context, st *store.DB) (map[string]map[string]any, error) {
	kinds := []string{
		store.KindComplaint, store.KindFeature, store.KindRequest, store.KindProject,
	}
	out := make(map[string]map[string]any, len(kinds))
	for _, k := range kinds {
		cov, err := st.EmbeddingCoverage(ctx, k)
		if err != nil {
			return nil, fmt.Errorf("coverage %s: %w", k, err)
		}
		if _, ok := cov["total"]; !ok {
			continue
		}
		out[k] = cov
	}
	return out, nil
}

func main() {
	var (
		flagDB      = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		flagListen  = flag.String("listen", "", "listen address (overrides $CONCORD_LISTEN)")
		showVer     = flag.Bool("version", false, "print version and exit")
		migrateOnly = flag.Bool("migrate-only", false,
			"apply migrations, report integrity, and exit without serving")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	cfg := config.Load()
	if *flagDB != "" {
		cfg.DBPath = *flagDB
	}
	if *flagListen != "" {
		cfg.Listen = *flagListen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db %s: %v", cfg.DBPath, err)
	}
	defer sqlDB.Close()

	if err := db.Migrate(ctx, sqlDB); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// -migrate-only exists so a deploy can apply and verify the schema without
	// starting a server. Migrating by starting the server means the schema is
	// only correct if the server also came up, so a failed bind leaves the
	// database half-migrated with nothing to distinguish that from a healthy
	// start. It also makes "apply migrations to a copy and diff the result" --
	// the only way to find out what a migration really does before running it on
	// production -- impossible, since the process would keep the port.
	if *migrateOnly {
		var report string
		if err := sqlDB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&report); err != nil {
			log.Fatalf("integrity_check: %v", err)
		}
		var version int
		if err := sqlDB.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
			log.Fatalf("read schema version: %v", err)
		}
		fmt.Printf("schema version %d, integrity_check: %s\n", version, report)
		return
	}

	st := &store.DB{DB: sqlDB}

	// Duplicate detection (§6.1). With $CONCORD_EMBED_URL unset the local hashed
	// embedder is used, so the default has no model server and no network
	// dependency -- §10.5 requires embeddings to be self-hostable, and making
	// that contingent on a reachable inference server would not be. Set it and a
	// transformer is used instead, with the hashed one as a fallback if the
	// server is down, so filing never depends on it.
	emb, err := embed.FromEnv()
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}
	st.SetEmbedder(emb)
	// Logged because vectors from two models are not comparable: which one is
	// live decides whether the existing index is usable or must be re-embedded.
	log.Printf("duplicate detection: embedder %s (%d dims)", st.EmbedderID(), emb.Dims())

	// ...and then check whether the index that embedder is meant to read is
	// actually populated.
	//
	// An empty or partial index makes duplicate detection fail SILENTLY:
	// /api/v1/similar answers 200 with an empty list, filing returns 201 for a
	// verbatim duplicate, nothing logs, and healthz says ok. KNOWN-ISSUES.md
	// records this happening for real -- an index that was never built, missed
	// for a long time precisely because all new work looked fine, since writes
	// embed on write and only PRE-EXISTING rows need a backfill.
	//
	// It bit this instance again after that fix: 25 complaints had no embedding
	// because nothing in the deploy path or the service ever runs
	// `embedbackfill`, so the gap reopens quietly every time rows are inserted
	// by a path that skips the embedder. So this is a startup WARNING with the
	// exact remediation command, not a fatal error: the index is an optimisation
	// for duplicate detection, and refusing to serve a forge because a cosine
	// search came back empty would be trading a real feature for a cosmetic one.
	if covs, err := embeddingCoverage(ctx, st); err != nil {
		log.Printf("WARNING: could not measure embedding coverage: %v", err)
	} else {
		for _, kind := range []string{
			store.KindComplaint, store.KindFeature, store.KindRequest, store.KindProject,
		} {
			cov, ok := covs[kind]
			if !ok {
				continue
			}
			total, _ := cov["total"].(int)
			embedded, _ := cov["embedded"].(int)
			switch {
			case total == 0:
				// Nothing of this kind exists yet, so there is nothing to be
				// missing and a warning here would be noise on a fresh install.
				continue
			case embedded == 0:
				log.Printf("WARNING: %s duplicate detection is OFF: 0/%d embedded. "+
					"Similarity search will return empty results and filing will not "+
					"detect duplicates. Run: bin/embedbackfill -kind %s",
					kind, total, kind)
			case embedded < total:
				log.Printf("WARNING: %s embedding index is incomplete: %d/%d embedded "+
					"(%d never indexed). Those rows cannot be found by similarity "+
					"search. Run: bin/embedbackfill -kind %s",
					kind, embedded, total, total-embedded, kind)
			}
		}
	}

	srv, err := httpapi.NewServer(st, version, cfg.WebhookSecret)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Printf("concord %s listening on %s (db: %s)", version, cfg.Listen, cfg.DBPath)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
