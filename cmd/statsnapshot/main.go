// Command statsnapshot records one day's instance statistics (spec §4.13 STAT-3).
//
// A one-shot command under a systemd timer rather than a goroutine in the server: the
// server restarts on every deploy, and a loop inside it would lose its schedule and
// re-run the pass on each restart. A gap in the series is also VISIBLE as a missing day;
// the goroutine alternative hides it, because a loop that dies with the process leaves
// no row and no error anywhere.
//
// Two rules:
//
//  1. ONE COMPUTATION. The frame is store.ComputeSnapshot, the same latency builders
//     GET /api/v1/stats uses. A snapshot that recomputed the medians its own way would
//     be a second answer to the same question, and the two would drift.
//  2. A SECOND RUN CORRECTS. ON CONFLICT(day) DO UPDATE, so re-running after a fix
//     replaces the row instead of adding a second point at the same x.
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
		dbPath = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		dryRun = flag.Bool("dry-run", false, "compute and print, write nothing")
		day    = flag.String("day", "", "which day to snapshot, YYYY-MM-DD (default today UTC)")
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

	d := *day
	if d == "" {
		d = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", d); err != nil {
		fatal("bad -day %q: want YYYY-MM-DD", d)
	}

	s, err := st.ComputeSnapshot(context.Background(), d)
	if err != nil {
		fatal("compute: %v", err)
	}
	s.ComputedAt = float64(time.Now().Unix())

	if *dryRun {
		fmt.Printf("day=%s projects=%d measured=%d features=%d seed=%d complaints=%d cseed=%d "+
			"no_origin=%d triage=%s(n=%d) delivery=%s(n=%d) decision=%s(n=%d)\n",
			s.Day, s.Projects, s.ProjectsMeasured, s.FeaturesTotal, s.FeaturesSeed,
			s.ComplaintsTotal, s.ComplaintsSeed, s.NoOrigin,
			median(s.TriageMedianH), s.TriageN,
			median(s.DeliveryMedianH), s.DeliveryN,
			median(s.DecisionMedianH), s.DecisionN)
		return
	}
	if err := st.WriteSnapshot(context.Background(), s); err != nil {
		fatal("write: %v", err)
	}
	fmt.Printf("recorded %s\n", d)
}

// median renders a nullable median for the operator's console. An unmeasured median is
// "n/a" and never "0.0" -- the same distinction the badge carries, because the console
// is where an operator first sees the number.
func median(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", *v)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "statsnapshot: "+format+"\n", args...)
	os.Exit(1)
}
