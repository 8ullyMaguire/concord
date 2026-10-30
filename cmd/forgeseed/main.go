// Command forgeseed applies seed/forge_repos.json to the catalog: it states where each
// project's upstream repository lives (R9, spec §4.5.7.1).
//
// It refuses more than it accepts, on purpose:
//
//   - a slug in the file that is not in the catalog is a REFUSAL, because a typo would
//     silently measure nothing;
//   - a project in the catalog that the file does not mention is a REFUSAL, because
//     "a new project was added" must be a visible decision, not a project that quietly
//     never gets a repository;
//   - a project with no repository must be listed with `"skip": true` and a reason, so
//     "we have not stated this one" is written down rather than inferred from absence.
//
// It prints counts and slugs, never URLs: a URL list in a terminal transcript is a list of
// places to send traffic, and the file it read is right there.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

type entry struct {
	Slug     string `json:"slug"`
	RepoURL  string `json:"repo_url"`
	RepoHost string `json:"repo_host"`
	Skip     bool   `json:"skip"`
	Reason   string `json:"reason"`
}

func main() {
	var (
		file   = flag.String("file", "seed/forge_repos.json", "the mapping file")
		dbPath = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		dryRun = flag.Bool("dry-run", false, "print what would be applied and write nothing")
	)
	flag.Parse()

	cfg := config.Load()
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		fatal("open db %s: %v", cfg.DBPath, err)
	}
	defer sqlDB.Close()
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		fatal("migrate: %v", err)
	}
	st := store.New(sqlDB)
	ctx := context.Background()

	entries, err := load(*file)
	if err != nil {
		fatal("%v", err)
	}

	slugs, err := st.ProjectSlugs(ctx)
	if err != nil {
		fatal("%v", err)
	}
	inCatalog := map[string]bool{}
	for _, s := range slugs {
		inCatalog[s] = true
	}

	var problems []string
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Slug == "" {
			problems = append(problems, "an entry has no slug")
			continue
		}
		if seen[e.Slug] {
			problems = append(problems, fmt.Sprintf("%s is listed twice", e.Slug))
		}
		seen[e.Slug] = true
		if !e.Skip && (e.RepoURL == "" || e.RepoHost == "") {
			problems = append(problems, fmt.Sprintf("%s has neither a repository nor \"skip\": true", e.Slug))
		}
		if e.Skip && e.Reason == "" {
			problems = append(problems, fmt.Sprintf("%s is skipped with no reason", e.Slug))
		}
	}
	for _, e := range entries {
		if e.Slug != "" && !inCatalog[e.Slug] {
			problems = append(problems, fmt.Sprintf("%s is in the file and not in the catalog", e.Slug))
		}
	}
	for _, s := range slugs {
		if !seen[s] {
			problems = append(problems, fmt.Sprintf("%s is in the catalog and not in the file (list it, or list it as a skip)", s))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "REFUSED:", p)
		}
		fatal("refusing to apply a mapping that does not account for the catalog: %d problem(s)", len(problems))
	}

	current := map[string]store.ProjectForge{}
	have, err := st.ListProjectsWithRepo(ctx)
	if err != nil {
		fatal("%v", err)
	}
	for _, p := range have {
		current[p.Slug] = p
	}

	applied, unchanged, skipped, cleared := 0, 0, 0, 0
	for _, e := range entries {
		if e.Skip {
			// A project that becomes a skip must LOSE the repository it used to have. Found on
			// the instance: gravity's remote started answering 404, the file was changed to a
			// skip, and the row kept its repo_url -- so the hourly ingest kept failing on a URL
			// the mapping no longer contained. "Not stated" has to mean nothing is set.
			if c, ok := current[e.Slug]; ok && c.RepoURL != "" {
				if *dryRun {
					fmt.Printf("would clear %s (was set, now skipped: %s)\n", e.Slug, e.Reason)
					cleared++
					continue
				}
				if err := st.ClearProjectRepo(ctx, e.Slug); err != nil {
					fatal("%s: clear: %v", e.Slug, err)
				}
				cleared++
			}
			skipped++
			continue
		}
		want, err := store.ParseRepoURL(e.RepoURL)
		if err != nil {
			fatal("%s: %v", e.Slug, err)
		}
		if c, ok := current[e.Slug]; ok && c.RepoURL == want.URL && c.RepoHost == e.RepoHost {
			unchanged++
			continue
		}
		if *dryRun {
			fmt.Printf("would set %s -> %s (%s)\n", e.Slug, want.URL, e.RepoHost)
			applied++
			continue
		}
		if _, err := st.SetProjectRepo(ctx, e.Slug, e.RepoURL, e.RepoHost); err != nil {
			fatal("%s: %v", e.Slug, err)
		}
		applied++
	}
	fmt.Printf("applied %d of %d (%d already set, %d cleared, %d skipped)\n",
		applied, len(entries), unchanged, cleared, skipped)
}

func load(path string) ([]entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}
	var out []entry
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(out) == 0 {
		// An empty mapping applied successfully is the worst outcome available: it reports a
		// clean run that did nothing, and every surface downstream still has nothing to show.
		return nil, fmt.Errorf("%s lists no projects; an empty mapping is a refusal, not a sync", path)
	}
	return out, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
