// Command proposalsweep marks proposals that nobody decided as stale (spec §8.6).
//
// A one-shot command under a systemd timer, for the same reasons statsnapshot is one: the server
// restarts on every deploy, so a loop inside it would lose its schedule and re-run the pass on each
// restart; and a pass that dies with the process leaves nothing in the journal, where a timer
// failure is visible in `systemctl --user status`.
//
// Three rules, in the order an operator meets them:
//
//  1. DRY RUN FIRST. `-dry-run` prints the same list the sweep would mark. The first run against a
//     live database is the only run that can surprise anyone, and it is the one an operator should
//     be able to rehearse.
//  2. THE CUTOFF IS AN OPERATOR DECISION. `-ttl-days` overrides $CONCORD_PROPOSAL_TTL_DAYS; 0
//     disables the sweep entirely, which is a legitimate setting for a quiet instance and not an
//     error.
//  3. ONE LINE ON SUCCESS. A timer's journal is read by `grep`, so the pass prints one line with the
//     count and exits 0; silence would be indistinguishable from a timer that never fired.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

func main() {
	var (
		dbPath  = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		dryRun  = flag.Bool("dry-run", false, "list what would be marked stale, write nothing")
		ttlDays = flag.Int("ttl-days", -1, "proposal lifetime in days (overrides $CONCORD_PROPOSAL_TTL_DAYS)")
		verbose = flag.Bool("v", false, "print one line per expired proposal")
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

	days := *ttlDays
	if days < 0 {
		days = config.ProposalTTLDays()
	}
	if days == 0 {
		fmt.Println("proposalsweep: disabled (ttl 0 days); nothing to do")
		return
	}

	cutoff := float64(time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix())
	rep, err := store.New(sqlDB).ExpireProposals(context.Background(), cutoff, *dryRun)
	if err != nil {
		fatal("sweep: %v", err)
	}

	if *verbose || *dryRun {
		for _, e := range rep.Expired {
			fmt.Printf("%s\t%s\t%d\tproject=%d\tproposer=%d\tconsents=%d\n",
				e.Kind, "stale", e.ID, e.ProjectID, e.ProposedBy, e.Consents)
		}
	}
	verb := "marked"
	if *dryRun {
		verb = "would mark"
	}
	fmt.Printf("proposalsweep: %s %d proposal(s) older than %d days as stale\n", verb, rep.Total, days)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "proposalsweep: "+format+"\n", args...)
	os.Exit(1)
}
