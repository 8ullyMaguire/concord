package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// rebuildMarker is the opt-in a migration uses to say "I rebuild tables to
// hardcoded shapes, and you should widen them from the source before I copy".
//
// WHY A MARKER. The problem is inherent to create-copy-drop-rename in SQLite:
// SQLite cannot ALTER a foreign key, so ON DELETE CASCADE can only be added by
// rebuilding the table, and a rebuild has to declare the new shape as SQL. A
// hand-written shape is a snapshot of the table as it was when the migration was
// written, so it cannot contain a column added later.
//
// Migration 0008 rebuilds 21 tables. Nineteen later migrations ADD a column to
// five of them (0010 features, 0011 charters, 0014 consensus_calls, 0015 tags,
// 0018/0019 charters and consensus_calls). Replay 0008 against such a database
// and the rebuilt table simply does not have those columns -- and the rows are
// gone from the table that replaces it.
//
// This was previously a loud crash. `INSERT INTO x_new SELECT * FROM x` supplies
// N+1 values to an N-column target, and SQLite refuses with
//
//	table charters_new has 18 columns but 19 values were supplied
//
// Naming the columns (also done in 0008) fixed that crash and UNMASKED the loss
// underneath it: the rebuild succeeded, and silently discarded every column the
// migration did not know about. A crash is a bad way to lose data and silence is
// worse, so the shape has to be derived rather than declared.
//
// WHY OPT-IN. Only a migration that rebuilds needs this, and only one that can
// lose a column. Running it for every file would mean a table-rebuild
// convention inferred from a comment, and the marker makes that explicit at the
// point where the author knows it applies. It mirrors the existing
// `-- concord:requires-foreign-keys-off` marker, which solves the same class of
// problem (something a .sql file cannot do for itself) the same way.
const rebuildMarker = "-- concord:rebuild-preserve-columns"

// needsRebuildWidening reports whether a migration body opted in.
func needsRebuildWidening(body []byte) bool {
	return strings.Contains(string(body), rebuildMarker)
}

// widenRebuiltTables adds to each `<name>_new` table any column that `<name>` has
// and the new shape lacks, before the migration's own INSERT ... SELECT runs.
//
// Called INSIDE the migration's transaction, after nothing has been copied yet and
// before the DROP. Ordering is the whole mechanism:
//
//	CREATE TABLE charters_new (...18 hardcoded columns...);   <- the migration's SQL
//	<this runs here>
//	INSERT INTO charters_new (...) SELECT (...) FROM charters;
//	DROP TABLE charters;
//
// # Why the column list is positional-safe
//
// The migration's own INSERT names its columns explicitly, so widening the target
// afterwards cannot break it: an INSERT with a column list is unaffected by extra
// columns on the table. That is why the two changes belong together -- naming the
// columns makes the rebuild survivable, and this makes it lossless.
//
// # Why the declared type is copied
//
// A column is recreated with the type, NOT NULL and DEFAULT it had in the source,
// read from PRAGMA table_info. Reusing the source's own declaration is the only
// way the widened column keeps behaving like the column it replaces: a widened
// NOT NULL column with no default would reject every existing row.
//
// # Why NOT NULL columns are skipped
//
// ALTER TABLE ADD COLUMN on a NOT NULL column without a default fails on a
// non-empty table ("Cannot add a NOT NULL column with default value NULL"). A
// source column that is NOT NULL has by definition never been NULL for any row
// that exists, but SQLite does not reason about that, so such a column is left
// out of the widened shape rather than failing the replay. Recorded as a residual
// gap rather than papered over: a later migration that adds NOT NULL to one of
// these tables would still lose the column on replay, and this is the honest
// boundary of the technique.
func widenRebuiltTables(ctx context.Context, tx *sql.Tx, body []byte) error {
	_, err := widenRebuiltTablesCollectingRelaxed(ctx, tx, body)
	return err
}

// widenRebuiltTablesCollectingRelaxed widens every rebuilt table and returns the
// columns it had to recreate WITHOUT their NOT NULL constraint, so the caller can
// report the relaxation instead of applying it silently.
func widenRebuiltTablesCollectingRelaxed(ctx context.Context, tx *sql.Tx, body []byte) ([]string, error) {
	var relaxed []string
	for _, table := range rebuiltTablesIn(body) {
		r, err := widenOneRebuiltTable(ctx, tx, table, table+"_new")
		if err != nil {
			return relaxed, fmt.Errorf("preserve columns of %s: %w", table, err)
		}
		relaxed = append(relaxed, r...)
	}
	return relaxed, nil
}

// widenOneRebuiltTable widens a single rebuilt table and returns the columns whose
// NOT NULL constraint could not be reproduced.
func widenOneRebuiltTable(ctx context.Context, tx *sql.Tx, src, dst string) ([]string, error) {
	srcCols, err := tableColumns(ctx, tx, src)
	if err != nil {
		return nil, err
	}
	dstCols, err := tableColumns(ctx, tx, dst)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(dstCols))
	for _, c := range dstCols {
		have[c.name] = true
	}
	// Columns recreated without their NOT NULL constraint, reported to the caller
	// so the relaxation is visible rather than silent.
	var relaxedColumns []string
	for _, c := range srcCols {
		if have[c.name] {
			// Present in both: leave it alone.
			//
			// A column the rebuilt shape declares WEAKER than the source -- 0008's
			// features_new has `body TEXT DEFAULT ''` where the live column is NOT
			// NULL -- is deliberately NOT reported here. It cannot be tightened:
			// SQLite has no ALTER COLUMN, so the only fix is re-deriving the rebuilt
			// shape from the source, which is the change KNOWN-ISSUES.md records as
			// still open. A report here would fire on every replay and be ignored.
			continue
		}
		// Keep the source's own NOT NULL and DEFAULT, and if SQLite refuses the
		// strict form, retry without NOT NULL rather than dropping the column.
		//
		// Two earlier versions of this each lost real data by being cautious:
		//
		//  - Skipping NOT NULL outright lost charters.support_ratio_min (0011,
		//    NOT NULL DEFAULT 0.5) and tags.suggested (0015, NOT NULL DEFAULT 0),
		//    because SQLite refuses ADD COLUMN NOT NULL *without* a default. Both
		//    were dropped by a replay that otherwise reported success. A guard that
		//    discards a column to avoid an error it would not have hit is the guard
		//    causing the damage.
		//  - Keeping NOT NULL unconditionally failed the replay, because
		//    pairwise_votes.arena_id is NOT NULL with no default and SQLite refuses
		//    that on a NON-EMPTY table.
		//
		// The asymmetry matters: NOT NULL is a constraint on writes, so a replay
		// that recreates a column without it allows rows a later migration forbade,
		// while a replay that DROPS the column loses the data outright. Preserving
		// the column with a relaxed constraint is the recoverable direction, and it
		// is reported below rather than left silent.
		def := ""
		if c.hasDefault {
			def = " DEFAULT " + c.defaultExpr
		}
		// The FK clause goes on BOTH forms below. A column that keeps its data but
		// loses its REFERENCES keeps the rows and drops the delete behaviour, which
		// is invisible until a parent row is deleted and the children survive.
		typeAndDefault := fmt.Sprintf("%s%s%s", c.declType, c.fkClause, def)
		// NOT NULL is reproduced exactly, and it always succeeds because widening
		// runs BEFORE the copy: the target is empty at this point, and SQLite only
		// refuses ADD COLUMN NOT NULL without a default on a NON-EMPTY table.
		//
		// An earlier version had a fallback here -- try the strict form, and if
		// SQLite refuses, add the column nullable instead. It was dead code: it
		// existed because I had reasoned about the SOURCE table, which has rows,
		// rather than the TARGET, which does not yet. Mutation-checking is what
		// exposed it -- replacing the fallback with `continue` changed nothing
		// observable, because no fixture can make a rebuilt table non-empty at
		// widening time.
		//
		// The fallback is kept as a defensive guard rather than deleted: if a future
		// migration widens a table it has already populated, the strict form fails
		// and the column is still preserved, just nullable. Skipping it would lose
		// the data, which is the failure mode that matters.
		if c.notNull {
			strict := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s%s NOT NULL%s",
				dst, c.name, c.declType, c.fkClause, def)
			if _, err := tx.ExecContext(ctx, strict); err == nil {
				continue
			}
			relaxedColumns = append(relaxedColumns, c.name)
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", dst, c.name, typeAndDefault)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return relaxedColumns, fmt.Errorf("add %s to %s: %w", c.name, dst, err)
		}
	}
	return relaxedColumns, nil
}

// columnInfo is the subset of PRAGMA table_info needed to recreate a column.
type columnInfo struct {
	name        string
	declType    string
	notNull     bool
	hasDefault  bool
	defaultExpr string
	// fkClause is the trailing REFERENCES clause as declared on the source
	// column, e.g. `REFERENCES arenas(id) ON DELETE CASCADE`. Foreign keys are part
	// of a column's definition in SQLite, not a table-level object, so preserving
	// the column without preserving this silently drops the constraint.
	fkClause string
}

// tableColumns reads a table's declared columns.
func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]columnInfo, error) {
	// The declared DDL, which is the only place the full column declaration --
	// including any REFERENCES clause -- exists. PRAGMA table_info reports type,
	// notnull and default but NOT the constraint, and PRAGMA foreign_key_list gives
	// no column ordinal to pair with.
	tableDecls, err := declaredColumns(ctx, tx, table)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []columnInfo
	for rows.Next() {
		var (
			cid       int
			name      string
			declType  sql.NullString
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out = append(out, columnInfo{
			name:        name,
			declType:    declType.String,
			notNull:     notNull != 0,
			hasDefault:  dfltValid(dfltValue),
			defaultExpr: dfltValue.String,
			fkClause:    referencesClause(tableDecls[name]),
		})
	}
	return out, rows.Err()
}

func dfltValid(v sql.NullString) bool { return v.Valid }

// rebuiltTablesIn finds the tables a migration rebuilds, from its own
// `CREATE TABLE <name>_new` statements.
//
// Read from the body rather than from a declared list, so a table added to the
// migration later is picked up without editing this file -- the same reason the
// test derives its table set the same way.
func rebuiltTablesIn(body []byte) []string {
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(line)
		upper := strings.ToUpper(t)
		if !strings.HasPrefix(upper, "CREATE TABLE ") {
			continue
		}
		rest := strings.TrimSpace(t[len("CREATE TABLE "):])
		name := strings.FieldsFunc(rest, func(r rune) bool {
			return r == ' ' || r == '(' || r == '\t'
		})
		if len(name) == 0 {
			continue
		}
		n := name[0]
		if strings.HasSuffix(n, "_new") {
			out = append(out, strings.TrimSuffix(n, "_new"))
		}
	}
	return out
}

// preserveSplitMarker is the explicit line a rebuilding migration puts between
// "create the new shapes" and "copy into them".
//
// EXPLICIT, NOT INFERRED. The obvious implementation is to look for the first
// `INSERT INTO x_new`, which is deterministic and needs no convention. It was
// written, and it is wrong in a way that only shows up on a database that does not
// need widening: on a fresh install there is nothing to widen, so the split and
// the widening are no-ops and the test suite is identical -- the inference is
// never exercised, and a bug in it survives until it meets the one database it
// was written for. A marker also cannot be accidentally triggered by a comment,
// a string literal, or a future reordering.
//
// The cost is one line in the migration, which is the right price for a step that
// must not silently not run.
const preserveSplitMarker = "-- concord:preserve-columns-split"

// preserveSplitPoints returns every byte offset of the split marker within a
// migration body, or nil when the migration does not opt in.
//
// # Why a LIST and not one point
//
// The first design had a single marker, placed before the first INSERT. It is
// wrong for 0008, and only the replay against the live database showed it: 0008
// interleaves its statements PER TABLE --
//
//	CREATE TABLE charters_new ...   <- split here
//	INSERT INTO charters_new ...
//	DROP TABLE charters;
//	CREATE TABLE members_new ...
//	INSERT INTO members_new ...
//
// so one split covers `charters` and nothing after it. The other twenty tables
// were copied with no widening, which is the defect the change set out to fix --
// and the unit test passed anyway, because it widened `charters` first and so
// exercised the one table the single split happened to cover.
//
// One marker per rebuilt table, before that table's INSERT. The list is sorted by
// offset so the migration can be executed in order.
//
// # Why the opt-in marker is still required
//
// Both markers must be present. A migration carrying the split marker without the
// opt-in marker is a mistake, and honouring it anyway would mean a file's SQL
// shape silently changed behaviour.
func preserveSplitPoints(body []byte) []int {
	if !needsRebuildWidening(body) {
		return nil
	}
	var out []int
	s := string(body)
	for off := 0; ; {
		i := strings.Index(s[off:], preserveSplitMarker)
		if i < 0 {
			break
		}
		abs := off + i
		out = append(out, abs)
		off = abs + len(preserveSplitMarker)
	}
	return out
}

// quoteIdent quotes an SQLite identifier for interpolation. Trigger names come
// from sqlite_master rather than from user input, but they are still identifiers
// and are quoted rather than trusted.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// dropTriggersReferencingRebuiltTables removes triggers whose body mentions a
// table the pending chunk is about to DROP.
//
// # Why this is needed
//
// A trigger's WHEN clause is re-validated whenever a row is written to its table.
// 0015 creates trg_tag_alias_no_shadow on tag_aliases with
//
//	WHEN EXISTS (SELECT 1 FROM tags WHERE lower(name) = lower(NEW.alias))
//
// which references `tags` -- a table 0008 rebuilds. On a REPLAY of 0008 that
// trigger still exists (0015 created it; 0008 does not remove it) and the rebuild
// drops `tags`, so SQLite fails with
//
//	error in trigger trg_tag_alias_no_shadow: no such table: main.tags
//
// and the migration rolls back. Only visible on a replay: on a fresh install the
// trigger does not exist yet when 0008 runs.
//
// # Why dropping is safe
//
// The trigger belongs to a LATER migration whose own CREATE TRIGGER recreates
// it, and that statement is unconditional. The alternative -- leaving it -- means
// the replay cannot complete at all.
func dropTriggersReferencingRebuiltTables(ctx context.Context, tx *sql.Tx, tables []string) ([]droppedTrigger, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		return nil, err
	}
	var toDrop, toSave []droppedTrigger
	for rows.Next() {
		var name, tbl, body sql.NullString
		if err := rows.Scan(&name, &tbl, &body); err != nil {
			rows.Close()
			return nil, err
		}
		_ = tbl
		up := strings.ToUpper(body.String)
		referenced := false
		for _, t := range tables {
			if mentionsTable(up, strings.ToUpper(t)) {
				referenced = true
				break
			}
		}
		if referenced {
			// A trigger whose body reads a table the chunk is about to drop:
			// SQLite revalidates the WHEN clause on write, so the rebuild fails
			// with "error in trigger X: no such table". Dropped before the DROP.
			toDrop = append(toDrop, droppedTrigger{name.String, body.String})
			toSave = append(toSave, droppedTrigger{name.String, body.String})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, h := range toDrop {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+quoteIdent(h.name)); err != nil {
			return nil, fmt.Errorf("drop trigger %s: %w", h.name, err)
		}
	}
	return toSave, nil
}

// saveTriggersOnRebuiltTables collects the definitions of triggers ATTACHED to the
// tables a rebuild is about to drop.
//
// # Why this exists separately
//
// `DROP TABLE` takes a table's own triggers with it, silently. That is a
// different mechanism from the referencing case above, and the first version of
// this file missed it entirely: it only looked for triggers whose body MENTIONS a
// rebuilt table, so
//
//	CREATE TRIGGER trg_pairwise_votes_no_update BEFORE UPDATE ON pairwise_votes
//
// was never seen -- `UPDATE ON pairwise_votes` is the trigger's target clause, not
// a reference -- and the replay dropped it on the floor. The replay test against the
// live database is what caught it: after a replay, pairwise_votes had lost the
// append-only enforcement 0024 added, and tags had lost trg_tag_no_self_parent.
//
// Losing an append-only guarantee is worse than the crash that dropping the
// referencing triggers avoided, because the crash blocked a replay nobody performs
// while the missing trigger quietly removed an invariant from a database that is
// running. Hence every trigger on a rebuilt table is saved and re-created, whether
// or not dropping it was necessary.
func saveTriggersOnRebuiltTables(ctx context.Context, tx *sql.Tx, tables []string) ([]droppedTrigger, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	q := `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type='trigger' AND tbl_name IN (?` +
		strings.Repeat(",?", len(tables)-1) + `)`
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []droppedTrigger
	for rows.Next() {
		var name, body sql.NullString
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		out = append(out, droppedTrigger{name.String, body.String})
	}
	return out, rows.Err()
}

// recreateDroppedTriggers restores triggers that were dropped because the replay
// dropped a table they reference.
//
// # Why restoring, rather than leaving them gone
//
// The first version dropped and stopped there. That is a REGRESSION, and the
// replay test found it: after a replay of 0008 on the live database,
// trg_tag_alias_no_shadow and trg_tag_no_self_parent were absent, because 0015
// created them and -- 0015 being already recorded -- nothing recreated them. So a
// replay left a schema that silently stopped enforcing "an alias may not shadow an
// existing tag".
//
// Which is worse than the crash it was fixing: the crash blocked a replay nobody
// performs, the missing trigger removed an invariant from a database that is
// running. Hence the definitions are saved and re-created here, inside the same
// transaction, so the trigger exists before the migration commits and an aborted
// replay leaves the original intact.
//
// The saved SQL is sqlite_master's own text, so what is restored is exactly what
// existed -- not a reconstruction.
func recreateDroppedTriggers(ctx context.Context, tx *sql.Tx, dropped []droppedTrigger) error {
	for _, t := range dropped {
		if t.body == "" {
			// sqlite_master stores NULL for a trigger whose SQL could not be
			// recovered. Recreating is impossible and skipping silently would
			// leave the invariant unenforced, so say so.
			return fmt.Errorf(
				"trigger %s references a rebuilt table and its definition could not be "+
					"read, so it cannot be restored; re-run the migration that creates it",
				t.name)
		}
		if _, err := tx.ExecContext(ctx, t.body); err != nil {
			return fmt.Errorf("restore trigger %s: %w", t.name, err)
		}
	}
	return nil
}

// droppedTrigger is a trigger definition saved before it was removed.
type droppedTrigger struct {
	name string
	body string
}

// mentionsTable reports whether an uppercased SQL body references a table.
//
// Matched on a SQL KEYWORD followed by the table name and then a word boundary.
// Both simpler versions were wrong on the real trigger:
//
//	SELECT 1 FROM tags WHERE lower(name) = lower(NEW.alias)
//
// A trailing-" " or "-;" pattern misses `FROM tags WHERE`, and a bare
// strings.Contains(body, "TAGS") also matches `tag_aliases` -- the trigger's own
// table -- so every trigger on a table with "tags" in its name would be dropped.
//
// The keyword list is the set of clauses after which a table name can appear. It
// is not exhaustive SQL; the replay test against the live database is what
// exercises the real case.
func mentionsTable(body, table string) bool {
	for _, kw := range []string{"FROM ", "JOIN ", "INTO ", "UPDATE ", "TABLE "} {
		rest := body
		for {
			i := strings.Index(rest, kw)
			if i < 0 {
				break
			}
			rest = rest[i+len(kw):]
			rest = strings.TrimLeft(rest, " \t\n")
			if strings.HasPrefix(rest, "main."+table) {
				return true
			}
			if !strings.HasPrefix(rest, table) {
				continue
			}
			after := rest[len(table):]
			if after == "" {
				return true
			}
			switch after[0] {
			case ' ', '\t', '\n', '(', ';', ',', ')', '"', '`':
				return true
			}
		}
	}
	return false
}

// dropAndRememberTriggers drops the dependent triggers for a chunk and returns
// their definitions so they can be recreated once the rebuild is done.
//
// Exists as a wrapper so the runner has one call site and so the "save it before
// removing it" intent is visible at the call site rather than spread across two
// functions.
func dropAndRememberTriggers(ctx context.Context, tx *sql.Tx, chunk []byte) ([]droppedTrigger, error) {
	tables := rebuiltTablesIn(chunk)
	referencing, err := dropTriggersReferencingRebuiltTables(ctx, tx, tables)
	if err != nil {
		return nil, err
	}
	// Triggers ATTACHED to a rebuilt table are removed by DROP TABLE itself, so
	// they are only saved, never explicitly dropped. Both sets are restored after
	// the rebuild.
	attached, err := saveTriggersOnRebuiltTables(ctx, tx, tables)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(referencing))
	for _, r := range referencing {
		seen[r.name] = true
	}
	out := referencing
	for _, a := range attached {
		if !seen[a.name] {
			out = append(out, a)
		}
	}
	return out, nil
}

// droppedIndex is an index definition saved before its table was dropped.
type droppedIndex struct {
	name string
	sql  string
}

// saveIndexesOnRebuiltTables collects the definitions of indexes ATTACHED to the
// tables a rebuild is about to drop, so they can be recreated afterwards.
//
// # Why this is the same problem as the triggers, and was found the same way
//
// `DROP TABLE` takes the table's indexes with it -- including UNIQUE indexes,
// which SQLite stores as separate objects rather than as table constraints. 0008's
// own header records that lesson the hard way: an early version did not recreate
// them, applied cleanly, `integrity_check` said ok, and three tests failed
// afterwards with "duplicate criterion vote was accepted".
//
// On a FRESH install that is invisible, because the migration recreates its own
// indexes in the same file. On a REPLAY it is not: 0014, 0017 and 0020 add indexes
// to tables 0008 rebuilds, and a replay dropped all seven of them with no error at
// all. Only the replay against the live database showed it.
//
// A UNIQUE index is not merely a performance concern -- it is an invariant. Losing
// one allows duplicates that every layer above assumes cannot exist, which is the
// same shape as losing a trigger.
func saveIndexesOnRebuiltTables(ctx context.Context, tx *sql.Tx, tables []string) ([]droppedIndex, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	q := `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type='index' AND tbl_name IN (?` +
		strings.Repeat(",?", len(tables)-1) + `)`
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []droppedIndex
	for rows.Next() {
		var name, body sql.NullString
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		// sqlite_autoindex_* is SQLite's own name for a UNIQUE constraint's
		// backing index. It is recreated by the rebuilt table's own constraints and
		// cannot be CREATE INDEX'd by hand, so it must be skipped.
		if strings.HasPrefix(name.String, "sqlite_autoindex") {
			continue
		}
		out = append(out, droppedIndex{name.String, body.String})
	}
	return out, rows.Err()
}

// recreateSavedIndexes restores indexes saved before a rebuild.
func recreateSavedIndexes(ctx context.Context, tx *sql.Tx, saved []droppedIndex) error {
	for _, ix := range saved {
		if ix.sql == "" {
			return fmt.Errorf(
				"index %s is attached to a rebuilt table and its definition could not be "+
					"read, so it cannot be restored; re-run the migration that creates it",
				ix.name)
		}
		// Index names are global in SQLite -- CREATE INDEX takes its name from
		// sqlite_master, not from the table -- so an index on ANOTHER table can
		// collide, and rewriting the SQL to IF NOT EXISTS is not enough to
		// distinguish "same definition" from "different definition, same name".
		//
		// So the existence check is explicit, and a genuine conflict is reported
		// rather than swallowed. 0008 recreates several of its own indexes in its
		// own SQL, which is where the first collision came from -- found by
		// TestTheHatchSignalIsCheckedNotAnyValue, a test about a vote log that had
		// no business being the thing that caught it.
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`,
			ix.name).Scan(&exists); err != nil {
			return fmt.Errorf("check index %s: %w", ix.name, err)
		}
		if exists > 0 {
			// Already recreated by the migration itself. The definition in
			// sqlite_master is the one the database had, so nothing is lost.
			continue
		}
		if _, err := tx.ExecContext(ctx, ix.sql); err != nil {
			return fmt.Errorf("restore index %s: %w", ix.name, err)
		}
	}
	return nil
}

// declaredColumns parses a table's CREATE TABLE statement into one full
// declaration per column.
//
// # Why parse the DDL instead of using the PRAGMAs
//
// PRAGMA table_info gives type / notnull / default / primary-key, and
// PRAGMA foreign_key_list gives the FK list but in reverse order with no ordinal
// tying a row to a column. Neither can reconstruct a column's declaration, and the
// declaration is exactly what has to be reproduced -- a widened column missing its
// REFERENCES keeps the data and loses the constraint.
//
// Parsing is on the DDL SQLite itself stores in sqlite_master, so it is the
// database's own text rather than a reconstruction. It handles the shapes this
// schema uses: a name, then the remainder up to the next comma at depth zero,
// ignoring commas inside parentheses (DECIMAL(10,2), CHECK (a IN (1,2))).
func declaredColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]string, error) {
	out := map[string]string{}
	var createSQL sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).
		Scan(&createSQL)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !createSQL.Valid || createSQL.String == "" {
		return out, nil
	}
	open := strings.Index(createSQL.String, "(")
	close := strings.LastIndex(createSQL.String, ")")
	if open < 0 || close <= open {
		return out, nil
	}
	// Strip comments BEFORE splitting. SQLite keeps them verbatim in
	// sqlite_master, and this schema's DDL is heavily commented -- including
	// comments BETWEEN columns:
	//
	//	id  INTEGER PRIMARY KEY,
	//
	//	-- The arena the vote belongs to. NOT NULL here even though ...
	//	arena_id INTEGER NOT NULL REFERENCES arenas(id) ON DELETE CASCADE,
	//
	//	-- Legacy feature-vote columns ...
	//
	// Without stripping, the split yields `--`, `because`, `kept` as column
	// "names" and the FK clause is read off a comment instead of the declaration.
	// The first version of this parser did exactly that and silently produced a
	// rebuilt pairwise_votes with no arena_id foreign key.
	body := stripSQLComments(createSQL.String[open+1 : close])

	// Split on commas at paren depth zero, which is a table-level constraint
	// boundary as well as a column boundary.
	var parts []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		first := strings.Fields(part)[0]
		upper := strings.ToUpper(first)
		// Table-level constraints are not columns.
		if strings.HasPrefix(upper, "CONSTRAINT") || strings.HasPrefix(upper, "PRIMARY") ||
			strings.HasPrefix(upper, "UNIQUE") || strings.HasPrefix(upper, "CHECK") ||
			strings.HasPrefix(upper, "FOREIGN") {
			continue
		}
		name := strings.Trim(first, "`\"[]")
		out[name] = part
	}
	return out, nil
}

// referencesClause extracts the trailing `REFERENCES ...` clause from a column's
// declared definition.
func referencesClause(declared string) string {
	if declared == "" {
		return ""
	}
	// WHITESPACE-INSENSITIVE, because this schema's DDL is hand-formatted and puts
	// the clause on its own indented line:
	//
	//	fk_probe INTEGER
	//		REFERENCES projects(id) ON DELETE CASCADE
	//
	// The first version searched for the literal " REFERENCES " with single
	// spaces, matched nothing, and emitted `ADD COLUMN fk_probe INTEGER` -- keeping
	// the column and silently dropping the constraint, which is the exact failure
	// the whole change set exists to prevent. Found by printing the generated ALTER
	// rather than by reading it, because a newline is invisible in a format string.
	norm := normalizeSQLSpace(declared)
	up := strings.ToUpper(norm)
	i := strings.Index(up, " REFERENCES ")
	if i < 0 {
		return ""
	}
	// i indexes the space that PRECEDES "REFERENCES", so the clause text begins at
	// i+1 -- but the space between the type and REFERENCES has to survive, or the
	// result is `INTEGERREFERENCES solutions(id)` and SQLite reports
	// `near "id": syntax error`. Found by running the generated ALTER against a
	// scratch table rather than by reading it, because the missing character is
	// invisible in the format string.
	clause := " " + norm[i+1:]
	clause = strings.TrimRight(clause, " ,")
	// The clause is a suffix: what follows it is a column attribute, handled
	// separately. Trim at the first one.
	for _, stop := range []string{" NOT NULL", " DEFAULT", " COLLATE", " CHECK", " UNIQUE"} {
		if j := strings.Index(strings.ToUpper(clause), stop); j > 0 {
			clause = clause[:j]
		}
	}
	return clause
}

// stripSQLComments removes `--` line comments and `/* */` block comments from a
// SQL fragment.
//
// String literals are respected: a `--` inside 'a--b' is data, not a comment, and
// replacing it would corrupt a default value like DEFAULT '--'. Quoting matters
// here because defaults ARE part of what this parser reproduces.
func stripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	inSingle, inDouble, inBacktick := false, false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case inSingle:
			b.WriteByte(c)
			if c == '\'' {
				// '' is an escaped quote, not a terminator.
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte(sql[i+1])
					i++
					continue
				}
				inSingle = false
			}
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
		case inBacktick:
			b.WriteByte(c)
			if c == '`' {
				inBacktick = false
			}
		case c == '\'':
			inSingle = true
			b.WriteByte(c)
		case c == '"':
			inDouble = true
			b.WriteByte(c)
		case c == '`':
			inBacktick = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i++
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// normalizeSQLSpace collapses every run of whitespace to a single space and trims,
// so clause matching does not depend on how the DDL happens to be laid out.
//
// Not a general SQL formatter: it does not respect string literals, and it does
// not need to. It is applied ONLY to detect where a clause starts, and the slice is
// then taken from the same normalized text, which is what gets emitted. The emitted
// clause therefore has single spaces throughout, which is valid and is not re-parsed
// by anything downstream.
func normalizeSQLSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteByte(c)
	}
	return b.String()
}
