package store

import (
	"context"
	"testing"
)

// STAT-3.4: rising, from the per-project series 0033 records.
//
// The rule that carries the milestone is the SPAN. With a daily timer, two snapshots one
// day apart turn a single shipped feature into "seven per week", and a ranking built on
// that is a ranking of the timer rather than of the projects. Below RisingMinDays there
// is no slope and the project is absent.

// seedProject builds a public project and returns its id.
func seedProject(t *testing.T, d *DB, uid int64, slug string) int64 {
	t.Helper()
	p, err := d.CreateProject(context.Background(), uid, slug, slug, "d", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
	return p.ID
}

func writeDays(t *testing.T, d *DB, days []string, rows []ProjectSnapshot) {
	t.Helper()
	for _, day := range days {
		if err := d.WriteProjectSnapshots(context.Background(), day, rows); err != nil {
			t.Fatalf("WriteProjectSnapshots(%s): %v", day, err)
		}
	}
}

func TestRisingRanksByRatePerWeekAcrossTheProjectsOwnSpan(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	fast := seedProject(t, d, uid, "fast-riser")
	slow := seedProject(t, d, uid, "slow-riser")

	// fast: 0 -> 14 shipped over 28 days = 3.5/week. slow: 0 -> 6 over 28 days.
	if err := d.WriteProjectSnapshots(ctx, "2026-01-01", []ProjectSnapshot{
		{ProjectID: fast}, {ProjectID: slow},
	}); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-01-29", []ProjectSnapshot{
		{ProjectID: fast, FeaturesShipped: 14}, {ProjectID: slow, FeaturesShipped: 6},
	}); err != nil {
		t.Fatalf("day 29: %v", err)
	}

	rows, err := d.RisingProjects(ctx)
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both projects", rows)
	}
	if rows[0].Slug != "fast-riser" {
		t.Fatalf("first = %s, want fast-riser (3.5/week beats 1.5/week)", rows[0].Slug)
	}
	if rows[0].Days != 28 {
		t.Fatalf("days = %d, want 28 (the span is published, not implied)", rows[0].Days)
	}
	if rows[0].FeaturesPerWeek != 3.5 {
		t.Fatalf("features/week = %v, want 3.5", rows[0].FeaturesPerWeek)
	}
}

func TestRisingOmitsASpanUnderTheFloor(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	id := seedProject(t, d, uid, "two-days-apart")

	// Two snapshots one day apart. A single shipped feature would read as 7/week.
	if err := d.WriteProjectSnapshots(ctx, "2026-02-01", []ProjectSnapshot{{ProjectID: id}}); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-02-02", []ProjectSnapshot{
		{ProjectID: id, FeaturesShipped: 1},
	}); err != nil {
		t.Fatalf("day 2: %v", err)
	}

	rows, err := d.RisingProjects(ctx)
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none: %d days is under the %d-day floor",
			rows, 1, RisingMinDays)
	}
}

func TestRisingOmitsAProjectWithOneSnapshot(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	only := seedProject(t, d, uid, "one-snapshot")
	spanning := seedProject(t, d, uid, "spanning-project")

	if err := d.WriteProjectSnapshots(ctx, "2026-03-01", []ProjectSnapshot{
		{ProjectID: only}, {ProjectID: spanning},
	}); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-03-20", []ProjectSnapshot{
		{ProjectID: spanning, FeaturesShipped: 2},
	}); err != nil {
		t.Fatalf("day 20: %v", err)
	}

	rows, err := d.RisingProjects(ctx)
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	for _, r := range rows {
		if r.Slug == "one-snapshot" {
			t.Fatalf("a project with a single snapshot was ranked: %+v", r)
		}
	}
	if len(rows) != 1 || rows[0].Slug != "spanning-project" {
		t.Fatalf("rows = %+v, want only spanning-project", rows)
	}
}

func TestRisingKeepsASignedDeltaRatherThanClamping(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	id := seedProject(t, d, uid, "went-backwards")

	// A feature was reopened, or a complaint deleted: the series really can fall, and a
	// clamped 0 would be a measurement nobody took.
	if err := d.WriteProjectSnapshots(ctx, "2026-04-01", []ProjectSnapshot{
		{ProjectID: id, FeaturesShipped: 5, ComplaintsFiled: 4},
	}); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-04-15", []ProjectSnapshot{
		{ProjectID: id, FeaturesShipped: 3, ComplaintsFiled: 1},
	}); err != nil {
		t.Fatalf("day 15: %v", err)
	}

	rows, err := d.RisingProjects(ctx)
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the falling project present", rows)
	}
	if rows[0].FeaturesShipped != -2 || rows[0].ComplaintsFiled != -3 {
		t.Fatalf("deltas = %d/%d, want -2/-3 (signed, not clamped)",
			rows[0].FeaturesShipped, rows[0].ComplaintsFiled)
	}
}

func TestRisingOmitsPrivateProjects(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	pub := seedProject(t, d, uid, "public-riser")
	priv := seedProject(t, d, uid, "private-riser")
	if _, err := d.ExecContext(ctx, `UPDATE projects SET visibility='private' WHERE id=?`, priv); err != nil {
		t.Fatalf("set visibility: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-05-01", []ProjectSnapshot{
		{ProjectID: pub}, {ProjectID: priv},
	}); err != nil {
		t.Fatalf("day 1: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-05-20", []ProjectSnapshot{
		{ProjectID: pub, FeaturesShipped: 2}, {ProjectID: priv, FeaturesShipped: 9},
	}); err != nil {
		t.Fatalf("day 20: %v", err)
	}

	rows, err := d.RisingProjects(ctx)
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	if len(rows) != 1 || rows[0].Slug != "public-riser" {
		t.Fatalf("rows = %+v, want only public-riser", rows)
	}
}

func TestRisingWithNoSeriesIsEmptyNotNil(t *testing.T) {
	d, _ := setupWithUser(t)
	rows, err := d.RisingProjects(context.Background())
	if err != nil {
		t.Fatalf("RisingProjects: %v", err)
	}
	if rows == nil {
		t.Fatal("empty ranking is nil; the JSON contract for a list route is []")
	}
}

// A second run on the same day CORRECTS the row rather than appending a second point at
// the same x -- the same rule as instance_stats, and the one a duplicate day breaks.
func TestWriteProjectSnapshotsIsIdempotentPerDay(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	id := seedProject(t, d, uid, "corrected-twice")

	if err := d.WriteProjectSnapshots(ctx, "2026-06-01", []ProjectSnapshot{
		{ProjectID: id, FeaturesShipped: 1},
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := d.WriteProjectSnapshots(ctx, "2026-06-01", []ProjectSnapshot{
		{ProjectID: id, FeaturesShipped: 4},
	}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	var rows, shipped int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(organic_features_shipped), 0) FROM project_stats
		  WHERE day = '2026-06-01' AND project_id = ?`, id).Scan(&rows, &shipped); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d rows for one day, want 1: a second run must CORRECT, not append", rows)
	}
	if shipped != 4 {
		t.Fatalf("count = %d, want the corrected 4", shipped)
	}
}

// The counts are ORGANIC only. Seeded and imported rows would make the slope a chart of
// an import script, which is the defect STAT-1 exists to prevent.
func TestComputeProjectSnapshotsCountsOrganicOnly(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	id := seedProject(t, d, uid, "mixed-origins")

	for _, row := range []struct {
		origin string
		count  int
	}{
		{"api", 2}, {"ui", 1}, {"seed", 7}, {"import", 3}, {"unknown", 5},
	} {
		for i := 0; i < row.count; i++ {
			if _, err := d.ExecContext(ctx, `
				INSERT INTO features (project_id, author_id, title, status, origin, created_at, updated_at)
				VALUES (?, ?, 'f', 'shipped', ?, 1, 1)`, id, uid, row.origin); err != nil {
				t.Fatalf("insert feature (%s): %v", row.origin, err)
			}
		}
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO complaints (project_id, author_id, title, origin, created_at, updated_at)
		VALUES (?, ?, 'c', 'seed', 1, 1), (?, ?, 'c', 'api', 1, 1)`,
		id, uid, id, uid); err != nil {
		t.Fatalf("insert complaints: %v", err)
	}

	rows, err := d.ComputeProjectSnapshots(ctx)
	if err != nil {
		t.Fatalf("ComputeProjectSnapshots: %v", err)
	}
	var got *ProjectSnapshot
	for i := range rows {
		if rows[i].ProjectID == id {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("project is absent from the snapshot: %+v", rows)
	}
	if got.FeaturesShipped != 3 {
		t.Fatalf("shipped = %d, want 3 (api 2 + ui 1; seed/import/unknown excluded)", got.FeaturesShipped)
	}
	if got.ComplaintsFiled != 1 {
		t.Fatalf("filed = %d, want 1 (api only)", got.ComplaintsFiled)
	}
}

// Every project is recorded, including one with no organic work at all.
//
// Without this row, "absent from the series" means two different things -- "no organic
// work yet" and "created after the first snapshot" -- and the reader cannot tell a new
// project from a flat one. The zero is the record that the project existed on that day.
func TestComputeProjectSnapshotsRecordsEveryProjectIncludingEmpty(t *testing.T) {
	d, uid := setupWithUser(t)
	ctx := context.Background()
	busy := seedProject(t, d, uid, "busy-project")
	idle := seedProject(t, d, uid, "idle-project")
	if _, err := d.ExecContext(ctx, `
		INSERT INTO features (project_id, author_id, title, status, origin, created_at, updated_at)
		VALUES (?, ?, 'f', 'shipped', 'api', 1, 1)`, busy, uid); err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	rows, err := d.ComputeProjectSnapshots(ctx)
	if err != nil {
		t.Fatalf("ComputeProjectSnapshots: %v", err)
	}
	seen := map[int64]ProjectSnapshot{}
	for _, r := range rows {
		seen[r.ProjectID] = r
	}
	if got, ok := seen[idle]; !ok {
		t.Fatalf("idle project absent from the series: %+v", rows)
	} else if got.FeaturesShipped != 0 || got.ComplaintsFiled != 0 {
		t.Fatalf("idle project = %+v, want 0/0", got)
	}
	if seen[busy].FeaturesShipped != 1 {
		t.Fatalf("busy project = %+v, want 1 shipped", seen[busy])
	}

	// And the zero survives the write, so the day it joined the series is the day the
	// project existed rather than the day it first shipped something.
	if err := d.WriteProjectSnapshots(ctx, "2026-07-01", rows); err != nil {
		t.Fatalf("WriteProjectSnapshots: %v", err)
	}
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_stats WHERE day='2026-07-01' AND project_id=?`, idle).Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 1 {
		t.Fatalf("idle project has %d rows for the day, want 1 (recorded at zero)", n)
	}
}
