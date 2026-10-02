// Command embedbackfill indexes existing complaints, features and requests for
// duplicate detection.
//
// It exists because the index is not populated by the migration: embedding ~1000
// rows on server startup would block the listen path, and a partial index
// silently produces "no duplicates found", which a filer reads as "I am the
// first to report this". So the index is built deliberately, by an operator, and
// reported when it is done.
//
// Re-running is cheap and idempotent: only rows with no embedding from the
// CURRENT model are selected, so after a model change this re-embeds exactly the
// stale rows and nothing else.
//
// Usage:
//
//	embedbackfill                  # complaints, features, requests, projects
//	embedbackfill -kind complaint  # one kind
//	embedbackfill -status          # report coverage without writing
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/embed"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

func main() {
	var (
		kindFlag = flag.String("kind", "", "only this kind (complaint, feature, request, project)")
		status   = flag.Bool("status", false, "report coverage and exit")
		dbFlag   = flag.String("db", "", "database path (overrides $CONCORD_DB)")
	)
	flag.Parse()

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("CONCORD_DB")
	}
	if dbPath == "" {
		dbPath = os.ExpandEnv("$HOME/.local/share/concord/concord.db")
	}

	sqlDB, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db %s: %v\n", dbPath, err)
		os.Exit(1)
	}
	defer sqlDB.Close()
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	st := &store.DB{DB: sqlDB}

	// Same embedder the server uses, or the backfill writes vectors the server
	// will not read. $CONCORD_EMBED_URL must be set identically for both.
	emb, err := embed.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "embedder: %v\n", err)
		os.Exit(1)
	}
	st.SetEmbedder(emb)
	fmt.Fprintf(os.Stderr, "embedder %s (%d dims)\n", st.EmbedderID(), emb.Dims())

	kinds := []string{store.KindComplaint, store.KindFeature, store.KindRequest, store.KindProject}
	if *kindFlag != "" {
		kinds = []string{*kindFlag}
	}

	if *status {
		for _, k := range kinds {
			cov, err := st.EmbeddingCoverage(context.Background(), k)
			if err != nil {
				fmt.Fprintf(os.Stderr, "coverage %s: %v\n", k, err)
				os.Exit(1)
			}
			fmt.Printf("%-11s %6.1f%%  (%v/%v embedded, model %v)\n",
				k, 100*cov["coverage"].(float64), cov["embedded"], cov["total"], cov["model_id"])
		}
		return
	}

	done, err := st.BackfillEmbeddingIndex(context.Background(), kinds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backfill: %v\n", err)
		os.Exit(1)
	}
	for _, k := range kinds {
		fmt.Printf("%-11s embedded %d\n", k, done[k])
	}

	// Coverage after the run, so the operator sees whether detection is actually
	// usable now rather than assuming a successful exit means that.
	fmt.Println()
	for _, k := range kinds {
		cov, err := st.EmbeddingCoverage(context.Background(), k)
		if err != nil {
			continue
		}
		complete, _ := cov["complete"].(bool)
		mark := "OK"
		if !complete {
			mark = "INCOMPLETE -- duplicate detection will miss rows"
		}
		// Distinguish "nothing to index" from "indexed". With zero rows the
		// coverage percentage is 0.0 and the "complete" flag is trivially true,
		// so printing OK reads as a successful index of nothing -- and for
		// requests, a filer would be told detection is fine while the table is
		// empty.
		total, _ := cov["total"].(int)
		switch {
		case total == 0:
			mark = "NO ROWS -- nothing to index yet"
		case !complete:
			mark = "INCOMPLETE -- duplicate detection will miss rows"
		}
		fmt.Printf("%-11s %6.1f%%  %v (%d rows)\n", k, 100*cov["coverage"].(float64), mark, total)
	}
}
