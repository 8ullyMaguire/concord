package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

type List struct {
	ID int64 `json:"id"`
	// A POINTER, because lists.project_id is NULLABLE -- migration 0001 says so on
	// the column ("NULL = instance-wide") and §16 makes it a first-class scope.
	//
	// As an int64 this was the reason an instance-wide list was unreachable: the
	// SELECT returned NULL and the scan failed, so handleGetList answered 500 and
	// handleGetLists -- which filters `project_id=?` and so never even selected one --
	// looked like it was working. The failure was invisible from both ends: no index
	// listed it, and the detail route blamed the database.
	//
	// *int64 reads "no project", 0 never occurs, and the JSON stays null for the page
	// to label.
	ProjectID   *int64  `json:"project_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

type ListEntry struct {
	ID          int64   `json:"id"`
	ListID      int64   `json:"list_id"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Category    *string `json:"category"`
	Status      string  `json:"status"`
	ER          float64 `json:"elo_r"`
	RD          float64 `json:"elo_rd"`
	Vol         float64 `json:"elo_vol"`
	ProposedBy  int64   `json:"proposed_by"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

// CreateList inserts a list, project-scoped or instance-wide.
//
// projectID 0 means INSTANCE-WIDE, and that is what it must mean: 16 says a list is
// "attachable to a project or instance-wide", the column is nullable, and migration
// 0001 annotates the NULL "NULL = instance-wide". So 0 is translated to NULL here.
//
// Without the translation, projectID 0 went to the FOREIGN KEY, which referenced
// projects(id) and had no row 0, and every attempt to create the spec's own default
// scope failed with "FOREIGN KEY constraint failed". A feature that cannot be created
// cannot be tested, and the constraint error named the wrong problem entirely.
//
// created_by was also hardcoded to user 1, so every list in the instance claimed the
// same author. It is now the caller's, which is what makes a list attributable.
func (d *DB) CreateList(ctx context.Context, projectID int64, createdBy int64, slug, title, description string) (List, error) {
	now := float64(time.Now().Unix())

	// A typed nil, not an untyped 0: database/sql needs a driver.Value, and a bare nil
	// fails to convert. (*int64)(nil) is the shape SQLite reads as SQL NULL.
	var project any = projectID
	if projectID == 0 {
		project = (*int64)(nil)
	}

	res, err := d.ExecContext(ctx, `
		INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'open', ?, ?, ?)`, project, slug, title, description, createdBy, now, now)
	if err != nil {
		return List{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetList(ctx, id)
}

func (d *DB) GetList(ctx context.Context, id int64) (List, error) {
	var l List
	err := d.QueryRowContext(ctx, `SELECT id, project_id, slug, title, description, status, created_at, updated_at FROM lists WHERE id=?`, id).Scan(
		&l.ID, &l.ProjectID, &l.Slug, &l.Name, &l.Description, &l.Status, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return List{}, err
	}
	return l, nil
}

// GetListsByProject returns ONE project's lists and nothing else.
//
// Not the page's source: handleGetLists uses GetListsByProjectForDisplay so the
// instance-wide lists are included, because §16 makes them the scope every project
// shares. This method stays for callers that must not see them.
func (d *DB) GetListsByProject(ctx context.Context, projectID int64) ([]List, error) {
	return d.getListsWhere(ctx, `project_id=?`, projectID)
}

func (d *DB) CreateListEntry(ctx context.Context, listID int64, url, title, body string) (ListEntry, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO list_entries (list_id, url, title, description, status, proposed_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'proposed', 1, ?, ?)`, listID, url, title, body, now, now)
	if err != nil {
		return ListEntry{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetListEntry(ctx, id)
}

func (d *DB) GetListEntry(ctx context.Context, id int64) (ListEntry, error) {
	var le ListEntry
	err := d.QueryRowContext(ctx, `SELECT id, list_id, url, title, description, category, status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at FROM list_entries WHERE id=?`, id).Scan(
		&le.ID, &le.ListID, &le.URL, &le.Title, &le.Description, &le.Category, &le.Status, &le.ER, &le.RD, &le.Vol, &le.ProposedBy, &le.CreatedAt, &le.UpdatedAt)
	if err != nil {
		return ListEntry{}, err
	}
	return le, nil
}

// GetListEntries returns all entries for a list.
func (d *DB) GetListEntries(ctx context.Context, listID int64) ([]ListEntry, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, list_id, url, title, description, category, status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at
		FROM list_entries WHERE list_id=? AND status != 'removed' ORDER BY elo_r DESC`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Empty, never nil: nil marshals to null, and the entries page would render
	// "null entries" instead of an empty list.
	entries := []ListEntry{}
	for rows.Next() {
		var le ListEntry
		if err := rows.Scan(&le.ID, &le.ListID, &le.URL, &le.Title, &le.Description, &le.Category, &le.Status, &le.ER, &le.RD, &le.Vol, &le.ProposedBy, &le.CreatedAt, &le.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, le)
	}
	return entries, rows.Err()
}

// VoteListEntry records a PAIRWISE comparison between two entries of the same list.
//
// §16: "Entries are ranked, not just listed. Entries compete in pairwise comparisons
// (the same Glicko-2 engine as feature priority), so 'best first' reflects community
// judgment instead of alphabetical accident."
//
// The schema already said all of this -- list_entry_votes is
// (list_id, entry_a, entry_b, voter_id, outcome, weight) with the same five-outcome
// CHECK as every other pairwise table, and list_entries carries elo_r/elo_rd/elo_vol.
// What did not exist was ANY Go code: no method wrote the table, no route called one,
// and every entry therefore read back at its 1500 seed forever.
//
// So a list was exactly what §16 says it must not be -- alphabetical.
//
// Mirrors RecordVote (features) and VoteAnswer (request answers), including the
// checks that were missing from both before this session:
//
//   - §5.1: you cannot vote on an entry you proposed
//   - both entries must belong to THIS list, or a vote spends this list's tally on a
//     comparison happening somewhere else
//   - UNIQUE (voter_id, entry_a, entry_b) means one vote per pair, which the INSERT
//     enforces; the error is translated so the page can say "you already compared
//     these" instead of surfacing a constraint name
func (d *DB) VoteListEntry(ctx context.Context, listID, entryA, entryB, voterID int64, outcome string, weight float64, charter governance.Charter) error {
	switch outcome {
	case "a", "b", "both", "neither", "skip":
	default:
		return fmt.Errorf("%w: outcome must be a, b, both, neither or skip", ErrInvalid)
	}
	if entryA == entryB {
		return fmt.Errorf("%w: cannot compare an entry with itself", ErrInvalid)
	}

	var authorA, authorB int64
	for _, pair := range []struct {
		id     int64
		author *int64
	}{{entryA, &authorA}, {entryB, &authorB}} {
		var list int64
		var status string
		if err := d.QueryRowContext(ctx,
			`SELECT proposed_by, list_id, status FROM list_entries WHERE id=?`, pair.id).
			Scan(pair.author, &list, &status); err != nil {
			return fmt.Errorf("fetch entry %d: %w", pair.id, err)
		}
		if list != listID {
			return fmt.Errorf("%w: entry %d belongs to list %d, not %d",
				ErrInvalid, pair.id, list, listID)
		}
		// A removed entry is not a candidate for "which of these two is better".
		if status == "removed" {
			return fmt.Errorf("%w: entry %d has been removed", ErrInvalid, pair.id)
		}
	}
	if voterID == authorA || voterID == authorB {
		return fmt.Errorf("%w: you cannot vote on an entry you proposed", ErrPerm)
	}

	var fa, fb ranking.Feature
	load := func(id int64, out *ranking.Feature) error {
		return d.QueryRowContext(ctx,
			`SELECT elo_r, elo_rd, elo_vol FROM list_entries WHERE id=?`, id).
			Scan(&out.R, &out.RD, &out.Vol)
	}
	if err := load(entryA, &fa); err != nil {
		return fmt.Errorf("fetch entry A rating: %w", err)
	}
	if err := load(entryB, &fb); err != nil {
		return fmt.Errorf("fetch entry B rating: %w", err)
	}

	out := ranking.Outcome(outcome)
	a2, b2 := ranking.ApplyPairwiseVote(fa, fb, out, weight, charter.GlickoTau)

	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx, `
		INSERT INTO list_entry_votes
			(list_id, entry_a, entry_b, voter_id, outcome, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		listID, entryA, entryB, voterID, outcome, weight, now); err != nil {
		// UNIQUE (voter_id, entry_a, entry_b). Translated because "you already
		// compared these two" is a thing a page can say and a constraint name is not.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: you have already compared these two entries", ErrPerm)
		}
		return err
	}

	if _, err := d.ExecContext(ctx,
		`UPDATE list_entries SET elo_r=?, elo_rd=?, elo_vol=?, updated_at=? WHERE id=?`,
		a2.R, a2.RD, a2.Vol, now, entryA); err != nil {
		return fmt.Errorf("update entry A rating: %w", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE list_entries SET elo_r=?, elo_rd=?, elo_vol=?, updated_at=? WHERE id=?`,
		b2.R, b2.RD, b2.Vol, now, entryB); err != nil {
		return fmt.Errorf("update entry B rating: %w", err)
	}
	return nil
}

// GetListEntryVotes returns how many comparisons a list's entries have received.
//
// A count is what makes the ranking legible: §16's claim is that order reflects
// community judgment, and "3 comparisons" beside an entry is the evidence for that.
// Without it a 1610 and a 1500 look like the same number of opinions.
func (d *DB) GetListEntryVotes(ctx context.Context, listID int64) (map[int64]int, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT entry_a, entry_b FROM list_entry_votes WHERE list_id = ?`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		counts[a]++
		counts[b]++
	}
	return counts, rows.Err()
}

// GetInstanceLists returns the lists that belong to no project.
//
// §16: a list is "attachable to a project or instance-wide". lists.project_id is
// nullable for exactly that reason and migration 0001 says so on the column
// ("NULL = instance-wide") -- but nothing could ever RETURN one: GetListsByProject
// filters `project_id=?`, which never matches NULL, and handleGetList requires
// `l.ProjectID == projectID`, which is 0 != anything.
//
// So the schema's own instance-wide option was unreachable, and a list created
// instance-wide was invisible forever: no index returned it, no detail route could
// serve it, and nothing errored. A feature that cannot be observed cannot be tested,
// and a reader who created one sees a success message and an empty page.
func (d *DB) GetInstanceLists(ctx context.Context) ([]List, error) {
	return d.getListsWhere(ctx, `project_id IS NULL`)
}

// GetListsByProjectForDisplay returns a project's lists PLUS the instance-wide ones.
//
// The instance-wide ones are included because they are the spec's DEFAULT scope: a
// "good resources" list is most useful to a reader precisely because every project can
// see it, and a project page that hid them would be hiding the lists that apply to it.
// Filtered in Go rather than SQL so the two queries stay legible; the second is
// constant-size and cached by nothing, which is the same trade every other
// instance-wide read in this codebase makes.
func (d *DB) GetListsByProjectForDisplay(ctx context.Context, projectID int64) ([]List, error) {
	scoped, err := d.getListsWhere(ctx, `project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	global, err := d.GetInstanceLists(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]List, 0, len(scoped)+len(global))
	out = append(out, scoped...)
	return append(out, global...), nil
}

func (d *DB) getListsWhere(ctx context.Context, where string, args ...any) ([]List, error) {
	q := `SELECT id, project_id, slug, title, description, status, created_at, updated_at
		FROM lists WHERE ` + where + ` ORDER BY updated_at DESC`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lists := []List{}
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Slug, &l.Name, &l.Description,
			&l.Status, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	return lists, rows.Err()
}
