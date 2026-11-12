package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The audit log viewer (idea #25 in the ranked 100).
//
// This reader replaces the old `GetAuditLog`, which had `LIMIT 100` hard-coded, no
// filters and no total, and which nothing in the tree called. Twenty-odd call
// sites write to `audit_log` -- complaints, strategy proposals, emergency holds,
// merges, taxonomy changes, visibility, invites -- and no surface read any of it,
// so a privileged action nobody can read is not auditable, which is the opposite
// of what §9.3 asks for.

// AuditLogEntry represents an entry in the audit log.
//
// It moved here from complaints.go, where it sat next to GetAuditLog. That
// reader is gone (see the note in complaints.go), and the type belongs with the
// code that fills it in rather than across the file from the twenty-odd writers
// that produce its rows.
type AuditLogEntry struct {
	ID        int64   `json:"id"`
	ProjectID *int64  `json:"project_id,omitempty"`
	ActorID   *int64  `json:"actor_id,omitempty"`
	Action    string  `json:"action"`
	Entity    string  `json:"entity"`
	EntityID  *int64  `json:"entity_id,omitempty"`
	Detail    string  `json:"detail"`
	CreatedAt float64 `json:"created_at"`
	// Undoes names the act this entry reverses, when it is a reversal.
	Undoes *int64 `json:"undoes,omitempty"`
	// Undone is the other direction: some later act has reversed THIS one. A viewer that
	// shows neither cannot tell a live act from a reversed one, which is exactly the state
	// the moderation spec's D2 says the log must never be in.
	Undone bool `json:"undone"`
}

// AuditEntry is a row plus the actor's display name.
//
// Display name only, never username or email. The reason is the feeds spec's §5.1
// rule stated for a second surface: `users` has an email column, and an audit row
// is meant to be read by anyone (the admin ledger is deliberately unauthenticated
// for exactly this reason), so an email in one is a permanent exposure. A numeric
// actor id alone is useless to a reader, which is why the name is resolved at all.
//
// The pointer is because `audit_log.actor_id` is nullable, and an entry with no
// actor is a real state -- several writers pass 0, which becomes NULL.
type AuditEntry struct {
	AuditLogEntry
	ActorDisplayName *string `json:"actor_display_name"`
	// CanUndo is a hint for the viewer, filled by the HTTP layer: this is an arena removal that
	// nothing has reversed yet, and this caller may moderate the project. The restore route
	// re-checks, because a flag computed in a read is not a permission.
	CanUndo bool `json:"can_undo"`
}

// AuditFilter narrows the log. Every field is optional and the zero value matches
// everything for the project.
type AuditFilter struct {
	ProjectID int64
	Action    string
	// Actions narrows to a SET of actions at once. It exists for `kind=moderation`, which is a
	// family (arena.remove, arena.restore, feature.reject, feature.unreject) rather than a name:
	// a reader looking for "what did moderation do" should not have to know the act vocabulary,
	// and asking for the family one action at a time is four requests whose union no total
	// describes. Composes with Action by AND, like every other clause here.
	Actions    []string
	ActorID    int64
	EntityType string
	EntityID   int64
	Text       string
	Since      float64
	Offset     int
	Limit      int
}

// AuditKindActions resolves a NAMED family of audit actions to its members.
//
// The alternative -- letting the page translate a kind into action names -- is a second copy of
// the vocabulary in JavaScript, and it drifts the first time an act is added. An unknown kind is
// an error rather than an empty filter: a log that silently ignores `kind=moderaton` shows every
// row, which is indistinguishable from a filter that matched everything.
func AuditKindActions(kind string) ([]string, error) {
	switch kind {
	case "":
		return nil, nil
	case "moderation":
		return ModerationActions(), nil
	}
	return nil, fmt.Errorf("%w: %q is not a kind of audit entry; the kinds are: moderation",
		ErrInvalid, kind)
}

// AuditPage is one page of entries plus the total it was drawn from.
//
// Total is not decoration. A client that renders "50 of ?" is guessing, and a
// client that pages forward has no way to tell "past the end" from "nothing
// matches" without it. It is also the difference between a filter that narrowed
// the log and a filter that was ignored: both return a plausible list, and only the
// total says which happened.
type AuditPage struct {
	Entries []AuditEntry `json:"entries"`
	Total   int          `json:"total"`
	Offset  int          `json:"offset"`
	Limit   int          `json:"limit"`
}

// Audit defaults, stated rather than buried at each use site.
const (
	AuditDefaultLimit = 50
	AuditMaxLimit     = 200
)

// ListAudit returns one page of a project's audit log, newest first, with the
// actors resolved to display names.
//
// The filters compose: an action filter plus a text filter narrows to rows
// matching BOTH. There is no OR form, because "search for this OR that" over an
// audit log is a reporting feature and this is a viewer; adding it later is a
// parameter, removing it is a breaking change.
func (d *DB) ListAudit(ctx context.Context, f AuditFilter) (AuditPage, error) {
	// Two clamps, not one. They look mergeable and must not be: `|| f.Limit > max`
	// folded into the same branch sends a caller that explicitly asked for the
	// ceiling to the DEFAULT instead, so `?limit=200` returns 50 rows and the only
	// symptom is a page that looks like it lost rows. Caught by
	// TestALimitAboveTheCeilingIsRefusedRatherThanHonoured, which failed on first
	// run with "Limit = 50, want 200".
	//
	// Absent limit is the default. A limit above the ceiling is the ceiling --
	// asked-for-and-reduced is a different outcome from unspecified-and-defaulted,
	// and a client paging on `limit` needs to know which size page it got.
	if f.Limit <= 0 {
		f.Limit = AuditDefaultLimit
	}
	if f.Limit > AuditMaxLimit {
		f.Limit = AuditMaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := []string{"project_id = ?"}
	args := []any{f.ProjectID}
	if f.Action != "" {
		where = append(where, "action = ?")
		args = append(args, f.Action)
	}
	// A SET, not a name. The placeholders are built from the length so the count of bind values
	// and the count of `?` cannot disagree -- a mismatch here is a runtime error naming the wrong
	// number of arguments, which reads as a broken endpoint rather than a broken filter.
	if len(f.Actions) > 0 {
		marks := make([]string, len(f.Actions))
		for i, a := range f.Actions {
			marks[i] = "?"
			args = append(args, a)
		}
		where = append(where, "action IN ("+strings.Join(marks, ",")+")")
	}
	if f.ActorID != 0 {
		where = append(where, "actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.EntityType != "" {
		where = append(where, "entity = ?")
		args = append(args, f.EntityType)
	}
	if f.EntityID != 0 {
		where = append(where, "entity_id = ?")
		args = append(args, f.EntityID)
	}
	if f.Since > 0 {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	// An EMPTY text must emit no LIKE at all. `LIKE '%%'` matches every row, so a
	// blanket clause would return exactly the rows the unfiltered query returns --
	// correct output, at the cost of a scan that reads as a slow endpoint. The
	// clause is conditional for that reason and not for tidiness.
	//
	// Wildcards in the user's string are escaped so a search for the literal
	// string "50%" does not return rows that merely contain a 5 followed by any
	// single character (the meaning of "50_" in LIKE) or "50" followed by zero or
	// more characters (the meaning of "50%").
	//
	// ESCAPE IS NOT OPTIONAL, and omitting it is a silent total failure rather
	// than a wrong answer: SQLite reads a backslash in a LIKE pattern as an
	// ordinary character unless the pattern declares ESCAPE, so "\%" is a
	// backslash followed by a live wildcard. Searching for any text the user
	// typed containing % or _ then matches NOTHING AT ALL and returns 200 with
	// an empty list -- indistinguishable from "this project has no such
	// history". Caught by TestASearchTreatsWildcardsAsLiteralCharacters, which
	// failed on first run with "returned 0 entries, want 1" against exactly this
	// code.
	if f.Text != "" {
		// The escape character is doubled FIRST. Doing it last would also double
		// the backslashes this function has just inserted in front of % and _,
		// turning "\%" into "\\%", which ESCAPE reads as a literal backslash
		// followed by a live wildcard -- the exact bug the ESCAPE clause exists
		// to prevent, reintroduced by the fix.
		escaped := strings.ReplaceAll(f.Text, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "%", "\\%")
		escaped = strings.ReplaceAll(escaped, "_", "\\_")
		where = append(where, "(detail LIKE ? ESCAPE '\\' OR action LIKE ? ESCAPE '\\')")
		args = append(args, "%"+escaped+"%", "%"+escaped+"%")
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	// Counted BEFORE the rows are read, never while a cursor is open. The pool is
	// MaxOpenConns(1) (docs/PLAN.md rule 6): a second query issued with a
	// *sql.Rows still open deadlocks the request, and the symptom is a hung page
	// rather than an error. Both queries are single statements and neither holds
	// a cursor while the other runs.
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`+clause,
		args...).Scan(&total); err != nil {
		return AuditPage{}, err
	}

	rows, err := d.QueryContext(ctx, `
		SELECT a.id, a.project_id, a.actor_id, a.action, a.entity, a.entity_id, a.detail,
		       a.created_at, a.undoes,
		       EXISTS (SELECT 1 FROM audit_log u WHERE u.undoes = a.id)
		FROM audit_log a`+clause+`
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return AuditPage{}, err
	}
	var entries []AuditLogEntry
	actorIDs := []int64{}
	for rows.Next() {
		var e AuditLogEntry
		var pid, aid, eid, undoes sql.NullInt64
		var detail sql.NullString
		var undone int
		if err := rows.Scan(&e.ID, &pid, &aid, &e.Action, &e.Entity, &eid,
			&detail, &e.CreatedAt, &undoes, &undone); err != nil {
			rows.Close()
			return AuditPage{}, err
		}
		if pid.Valid {
			e.ProjectID = &pid.Int64
		}
		if aid.Valid {
			v := aid.Int64
			e.ActorID = &v
			actorIDs = append(actorIDs, v)
		}
		if eid.Valid {
			e.EntityID = &eid.Int64
		}
		// `detail` is TEXT with no NOT NULL, and writers pass "" as often as text,
		// so NULL and "" are both live states. Scanning NULL into a bare string
		// fails the whole read, which would take the viewer down over one row.
		e.Detail = detail.String
		if undoes.Valid {
			v := undoes.Int64
			e.Undoes = &v
		}
		e.Undone = undone != 0
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return AuditPage{}, err
	}
	rows.Close()

	// Names resolved after the cursor is closed, in ONE query for the whole page.
	// A per-row lookup would be N+1 on a page an auditor pages through repeatedly.
	names := map[int64]string{}
	if len(actorIDs) > 0 {
		names, err = d.DisplayNamesFor(ctx, actorIDs)
		if err != nil {
			return AuditPage{}, err
		}
	}

	out := make([]AuditEntry, 0, len(entries))
	for _, e := range entries {
		ae := AuditEntry{AuditLogEntry: e}
		if e.ActorID != nil {
			if n, ok := names[*e.ActorID]; ok {
				v := n
				ae.ActorDisplayName = &v
			}
		}
		// An id with no users row leaves the name NULL rather than inventing one.
		// A viewer that showed "user 41" for a deleted account would be telling a
		// reader to go and complain about a person who cannot be complained about.
		out = append(out, ae)
	}
	return AuditPage{Entries: out, Total: total, Offset: f.Offset, Limit: f.Limit}, nil
}

// AuditActions returns the distinct action names present in a project's log, for
// populating a filter control.
//
// Alphabetical, not by frequency: a filter dropdown whose order shifts every time a
// vote lands is one a user cannot learn, and the count of each is available from
// the unfiltered first page anyway.
func (d *DB) AuditActions(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT DISTINCT action FROM audit_log WHERE project_id = ? AND action <> ''
		ORDER BY action`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
