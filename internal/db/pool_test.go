package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOpenAllowsOnlyOneConnection is the precondition for the deadlock guard
// below. It is written as a test rather than a comment because the whole
// failure mode depends on it: if this ever changes, the nested-query analysis
// stops being valid and the guard needs revisiting.
func TestOpenAllowsOnlyOneConnection(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if got := d.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1. With more than one connection "+
			"nested queries stop deadlocking, so the guard in TestNoNestedQueriesInStore "+
			"may be relaxed deliberately rather than accidentally", got)
	}
}

// TestNoNestedQueriesInStore is a static guard against the deadlock that cost
// the most time to find.
//
// db.Open sets SetMaxOpenConns(1). A store method that issues a query while a
// rows cursor from the same *DB is still open therefore waits forever for a
// connection that cannot be handed out until the cursor closes. Two such sites
// existed: GetFeaturePriorities and GetComplaintPain, both reached from the
// feature-ranking endpoint, both presenting as an HTTP timeout rather than as
// a deadlock.
//
// A deadlock in Go is a goroutine that never finishes, so a test that triggers
// one reports a timeout with no indication of the cause. This check is static
// instead: it reads the store source and fails at review time, naming the file
// and line, which is far cheaper than a 4-minute package timeout.
func TestNoNestedQueriesInStore(t *testing.T) {
	storeDir := filepath.Join("..", "store")
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}

	// A store call that would need a second connection while a cursor is open.
	nested := regexp.MustCompile(`d\.(Get|List|Count|Create|Add|Validate|Merge|Vote|Update|Delete)[A-Z]\w*\(ctx`)
	cursorOpen := regexp.MustCompile(`rows\w*, err :?= d\.QueryContext`)

	problems := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(storeDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")

		openAt := -1
		for i, line := range lines {
			if cursorOpen.MatchString(line) {
				openAt = i
				continue
			}
			if openAt < 0 {
				continue
			}
			if strings.Contains(line, "rows.Close()") {
				openAt = -1
				continue
			}
			if nested.MatchString(line) && !strings.Contains(line, "QueryRow") {
				t.Errorf("%s:%d: query issued while a rows cursor is open (cursor "+
					"opened at line %d): %s",
					name, i+1, openAt+1, strings.TrimSpace(line))
				problems++
			}
		}
	}
	if problems > 0 {
		t.Errorf("found %d nested quer%s; with MaxOpenConns(1) these deadlock. "+
			"Collect the ids into a slice, close the cursor, then query.",
			problems, plural(problems))
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
