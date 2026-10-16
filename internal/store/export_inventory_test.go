package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The store's exported methods, classified by who can reach them.
//
// §25 and §26 each found a complete store function that nothing in production called -- and each
// found it by accident, by grepping for it while doing something else. `reader_coverage_test.go`
// closed exactly that hole on the HTTP side (a registered route with no reader in `assets/js` fails
// a test); the store had no equivalent, although both findings lived in it. This is that
// measurement, and the reason it is a test rather than a note is that a hand-kept list of a
// measurable property is wrong the moment the next file lands: `docs/REMAINING.md` §24.7 named
// three dead methods by hand three passes ago.
//
// The classification is by NAME, over the AST of every non-vendored .go file in the tree:
//
//	LIVE      -- at least one call from production code (any non-test .go file)
//	TEST-ONLY -- called only from _test.go files: exercised, so it looks alive in every test run,
//	             and unreachable by anything a user does. Both of the last two passes' findings are
//	             in this class, which is why it is separate from UNCALLED.
//	UNCALLED  -- no call anywhere, not even from a test.
//
// Only SelectorExpr nodes are counted, and that is what makes the measurement correct rather than
// merely plausible: a declaration `func (d *DB) Foo()` holds its name in a FuncDecl, never as a
// selector, so counting selectors counts references and cannot mistake a definition for a caller.
// It also counts calls *within the defining file*, which a path-based scan gets wrong: GetBoard
// calls GetBoardColumns in board.go, and a scan that skipped the defining file called both of them
// dead.

// exportClass is what the measurement found, not what the inventory claims.
type exportClass int

const (
	live exportClass = iota
	testOnly
	uncalled
)

func (c exportClass) String() string {
	switch c {
	case live:
		return "LIVE"
	case testOnly:
		return "TEST-ONLY"
	default:
		return "UNCALLED"
	}
}

// measuredExport is one exported *DB method and what reaches it.
type measuredExport struct {
	name      string
	defFile   string   // where it is defined, relative to the repo root
	prodFiles []string // non-test files that reference it
	testFiles []string // _test.go files that reference it
}

func (m measuredExport) class() exportClass {
	if len(m.prodFiles) > 0 {
		return live
	}
	if len(m.testFiles) > 0 {
		return testOnly
	}
	return uncalled
}

// repoRoot is the tree the measurement walks. The package directory is internal/store, so the
// repository is two levels up.
const repoRoot = "../.."

// skipDir reports whether a directory holds no Go source this measurement cares about.
func skipDir(name string) bool {
	switch name {
	case ".git", "bin", "node_modules", "build", "dist", ".cache":
		return true
	}
	return false
}

// measureStoreExports parses the tree and classifies every exported *DB method.
//
// It fails, rather than degrading, on a file it cannot parse: a walker that silently skips what it
// does not understand reports a smaller tree, and a smaller tree reads as fewer live callers.
func measureStoreExports(t *testing.T) map[string]measuredExport {
	t.Helper()
	fset := token.NewFileSet()

	// Pass 1: the definitions, which are the population being classified. Only the store's own
	// non-test files define them; a same-named method on another type is not this measurement's
	// business.
	defs := map[string]string{}
	storeDir := filepath.Join(repoRoot, "internal", "store")
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("read %s: %v", storeDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(storeDir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			if !isDBReceiver(fn.Recv) {
				continue
			}
			defs[fn.Name.Name] = filepath.ToSlash(filepath.Join("internal", "store", e.Name()))
		}
	}
	if len(defs) == 0 {
		t.Fatalf("no exported *DB methods found under %s: the measurement is looking in the wrong place", storeDir)
	}

	out := map[string]measuredExport{}
	for name, file := range defs {
		out[name] = measuredExport{name: name, defFile: file}
	}

	// Pass 2: every reference to those names, anywhere in the tree.
	root, err := filepath.Abs(filepath.Join(repoRoot))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A parse failure is reported, never skipped: the population would silently shrink.
			return perr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		isTest := strings.HasSuffix(d.Name(), "_test.go")
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			m, ok := out[sel.Sel.Name]
			if !ok {
				return true
			}
			if isTest {
				m.testFiles = append(m.testFiles, rel)
			} else {
				m.prodFiles = append(m.prodFiles, rel)
			}
			out[sel.Sel.Name] = m
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	for name, m := range out {
		sort.Strings(m.prodFiles)
		sort.Strings(m.testFiles)
		out[name] = m
	}
	return out
}

// isDBReceiver reports whether a func receiver is `d *DB` (or the value form).
func isDBReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	switch t := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && id.Name == "DB"
	case *ast.Ident:
		return t.Name == "DB"
	}
	return false
}

// anchors are methods whose reachability is asserted before anything else is believed. The first
// attempt at this measurement was a text scan that called 249 of 254 methods uncalled, among them
// CreateFeature and GetBoard; a classification is only worth reporting if a broken walker is
// caught by it, so these three have to come back LIVE with a production caller.
var anchors = map[string]string{
	"CreateFeature": "internal/httpapi/handlers.go",
	"GetBoard":      "internal/httpapi/handlers.go",
	"LinkComplaint": "internal/httpapi/complaint_links.go",
	"ListFeatures":  "internal/httpapi/handlers.go",
	"AddAudit":      "internal/store/complaints.go",
}

// exportNote is what the inventory says about a method the measurement cannot vouch for.
//
// Only two classes need an entry: a method reached solely from tests, and one reached by nothing.
// A method with a production caller needs no entry -- the caller is the proof, and a declaration
// that could rot independently of it would be a second thing to keep true.
type exportNote struct {
	class  exportClass
	reason string
}

// storeExportInventory names every exported *DB method that production cannot reach, with the
// reason it is still there. The reasons are the deliverable: an unreachable function without a
// reason is what this pass exists to end, and "someone might need it" is not a reason.
//
// The entries are grouped by the surface they belong to, because that is what makes them
// actionable: a whole group cycles a feature whose store half landed and whose route did not.
var storeExportInventory = map[string]exportNote{
	// --- Deleted in §27, by hand, one at a time: ApplyProjectTag, GetBoardCard and GetMember were
	// the three UNCALLED methods, and each had a named replacement (ApplyExistingProjectTag,
	// GetBoardCards via GetBoard, GetRoleForProject plus ListMembers). Their entries are gone
	// from this map because the methods are gone from the tree, which is what the ghost check
	// above is for. What remains below is the class that CANNOT be fixed by deletion: a method
	// the tests exercise and no user can reach.

	// --- The ILLUSTRATED-BY-TEST half of a surface with no route. This is the §25/§26 class at
	// its largest: the store is complete, the tests exercise it, and nothing a user does reaches it.
	//
	// §29 took the three arena-loop reads and writes out of this list: the pair to
	// judge, the log, and the replay that proves the ranking all have routes now.
	//
	// §28 took the two biggest out of this list by building their routes (CreateFieldReport and
	// RespondToFieldReport -- field reports had a READ route and no write, so the panel's "No field
	// reports yet" was the state the code guaranteed). What is left below is the same class, named
	// the same way: each entry says which surface it belongs to and what is missing above it.
	"ConfirmCapability":         {testOnly, "no route records an independent confirmation or dispute of a capability claim (internal/httpapi/capabilities.go only lists claims)"},
	"ConsentRecords":            {testOnly, "no route reads the consent ledger; the guest status route answers from GuestStatusFor"},
	"RemoveFieldReport":         {testOnly, "as CreateFieldReport: no write route"},
	"EditFieldOwnerResponse":    {testOnly, "as RespondToFieldReport: only tests edit a response"},
	"DeleteFieldOwnerResponse":  {testOnly, "as RespondToFieldReport: only tests delete one"},
	"ExpireHolds":               {testOnly, "the expiry sweep (confirms the vote for a hold that has lapsed) has no caller: nothing schedules it"},
	"FindSimilar":               {testOnly, "the entity-level twin of FindSimilarField, which is what production reads through"},
	"UpsertEmbedding":           {testOnly, "the twin of UpsertEmbeddingField, as FindSimilar is of FindSimilarField"},
	"DeleteEmbedding":           {testOnly, "nothing removes a vector; the reindex path only writes"},
	"GetFeatureVotes":           {testOnly, "no route reads a feature's pairwise votes; the consensus page reads positions"},
	"GetListEntryVotes":         {testOnly, "VoteListEntry is routed and its read is not: a reader cannot see who voted on an entry"},
	"HasQualifyingContribution": {testOnly, "IsEligibleCollaborator without the role check; the live eligibility paths call the role-checking form"},
	"ListArases":                {testOnly, "the unscoped, pre-project variant (name included) of ListProjectArases, which is what the routes call; one store test still reads through it"},
	"GetListsByProject":         {testOnly, "the scoped read that GetListsByProjectForDisplay wraps; kept as the narrow form the scoping invariants are tested against (lists_test.go)"},
	"RemoveArenaEntry":          {testOnly, "no route removes a competitor"},
	"ListCriterionRatings":      {testOnly, "the criteria ratings are read through AllCriterionRatings (CompositeRank's path), not per criterion"},
	"RankByCriterion":           {testOnly, "running a rating period over a project's unrated games has no route and no scheduler"},
	"CreateUser":                {testOnly, "a fixture helper: production registers through RegisterUser (password hashing and the claim flow live there)"},
	"SetPassword":               {testOnly, "there is no password-change route; only tests set one"},
}

// TestStoreExportInventory classifies every exported *DB method and refuses an undeclared hole.
func TestStoreExportInventory(t *testing.T) {
	measured := measureStoreExports(t)

	// (1) Soundness first: a walker that finds no callers would otherwise report a clean, wrong,
	// all-dead tree, which is exactly what the text-scan attempt did.
	for name, wantFile := range anchors {
		m, ok := measured[name]
		if !ok {
			t.Fatalf("anchor %s is not among the measured methods: the definition walker missed it", name)
		}
		if m.class() != live {
			t.Fatalf("anchor %s was classified %s with no production caller: the reference walker is broken (prod=%v test=%v)",
				name, m.class(), m.prodFiles, m.testFiles)
		}
		if wantFile != "" {
			found := false
			for _, f := range m.prodFiles {
				if f == wantFile {
					found = true
				}
			}
			if !found {
				t.Fatalf("anchor %s is LIVE but not from %s (prod=%v): the walker is counting the wrong files",
					name, wantFile, m.prodFiles)
			}
		}
	}

	// (2) The table, printed so the landscape is readable in an ordinary test run.
	names := make([]string, 0, len(measured))
	for name := range measured {
		names = append(names, name)
	}
	sort.Strings(names)
	byClass := map[exportClass][]string{}
	for _, name := range names {
		m := measured[name]
		byClass[m.class()] = append(byClass[m.class()], name)
		note, declared := storeExportInventory[name]
		switch m.class() {
		case live:
			if declared {
				t.Errorf("%s is LIVE (called from %s) but the inventory gives it a reason for being unreachable: drop the entry",
					name, m.prodFiles[0])
			}
		case testOnly, uncalled:
			if !declared {
				t.Errorf("%s is %s and the inventory does not name it. Wire it, delete it, or name it with the reason it stays.",
					name, m.class())
				continue
			}
			if note.class != m.class() {
				t.Errorf("%s is %s but the inventory declares it %s: the declaration has rotted",
					name, m.class(), note.class)
			}
			if strings.TrimSpace(note.reason) == "" {
				t.Errorf("%s is declared %s with no reason: an unreachable method without a reason is the state this test exists to end",
					name, m.class())
			}
		}
	}
	// (3) A ghost entry names something that is not an exported *DB method any more.
	for name := range storeExportInventory {
		if _, ok := measured[name]; !ok {
			t.Errorf("the inventory names %s, which is no longer an exported *DB method: drop the entry", name)
		}
	}
	for _, c := range []exportClass{live, testOnly, uncalled} {
		t.Logf("%-9s %3d", c, len(byClass[c]))
	}
}
