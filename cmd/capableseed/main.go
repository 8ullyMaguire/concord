// Command capableseed registers a capability set and (optionally) asserts some
// values against projects.
//
//	capableseed -list
//	capableseed -apply capabilities.json
//	capableseed -apply capabilities.json -assert
//
// The seed exists because a capability matrix nobody has asserted anything into
// carries no information, and Finder's question selector picks questions by
// information gain over the candidate set. With every capability absent from
// every project there is nothing to gain and the engine correctly declines to
// ask anything — which is honest, and also useless. Seeding is what turns the
// matrix from schema into questions.
//
// Assertions are seeded as `asserted`, not `confirmed`: seeding is a claim by
// the instance operator, and letting a seed file manufacture a quorum would let
// anyone who can write the file promote their own assertions to confirmed
// without a second person ever agreeing. Confirmation happens when two real
// accounts confirm, through the same endpoint the UI uses.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// seedCapability is one capability definition plus the values seeded for
// specific projects. Assertions are keyed by project slug so the file stays
// readable and does not depend on autoincrement ids.
type seedCapability struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Kind     string   `json:"kind"`
	Values   []string `json:"values,omitempty"`
	Assert   map[string]string `json:"assert,omitempty"`
}

type seedFile struct {
	Capabilities []seedCapability `json:"capabilities"`
}

func main() {
	list := flag.Bool("list", false, "list registered capabilities and exit")
	apply := flag.String("apply", "", "path to a capabilities JSON file")
	assertIt := flag.Bool("assert", false, "also write the assert map (needs a --as user)")
	asUser := flag.String("as", "", "username to attribute seeded assertions to")
	dbFlag := flag.String("db", "", "sqlite path (defaults to $CONCORD_DB)")
	flag.Parse()

	// Same resolution order as cmd/embedbackfill: an explicit flag, then the
	// environment, then the documented default. Reading it a third way here
	// would mean three places that can disagree about which database is live.
	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("CONCORD_DB")
	}
	if dbPath == "" {
		dbPath = os.ExpandEnv("$HOME/.local/share/concord/concord.db")
	}

	raw, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer raw.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx, raw); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	s := store.New(raw)

	if *list {
		caps, err := s.ListCapabilities(ctx, "")
		if err != nil {
			log.Fatalf("list: %v", err)
		}
		for _, c := range caps {
			fmt.Printf("%-24s %-20s %-8s %v\n", c.Key, c.Category, c.Kind, c.Values)
		}
		return
	}

	if *apply == "" {
		flag.Usage()
		os.Exit(2)
	}

	raw2, err := os.ReadFile(*apply)
	if err != nil {
		log.Fatalf("read %s: %v", *apply, err)
	}
	var sf seedFile
	if err := json.Unmarshal(raw2, &sf); err != nil {
		log.Fatalf("parse %s: %v", *apply, err)
	}
	if len(sf.Capabilities) == 0 {
		log.Fatalf("%s declares no capabilities", *apply)
	}

	var author int64
	if *assertIt {
		if *asUser == "" {
			log.Fatal("-assert requires -as <username>")
		}
		u, err := s.GetUser(ctx, *asUser)
		if err != nil {
			log.Fatalf("get user %q: %v", *asUser, err)
		}
		author = u.ID
	}

	categories := map[string]int{}
	defined, asserted := 0, 0
	for _, c := range sf.Capabilities {
		kind := c.Kind
		if kind == "" {
			kind = store.CapBoolean
		}
		if err := s.EnsureCapability(ctx, store.Capability{
			Key: c.Key, Label: c.Label, Category: c.Category,
			Kind: kind, Values: c.Values,
		}); err != nil {
			log.Fatalf("ensure %q: %v", c.Key, err)
		}
		defined++
		categories[c.Category]++

		for slug, value := range c.Assert {
			if !*assertIt {
				log.Fatalf("%s declares assertions but -assert was not given", c.Key)
			}
			if _, err := s.AssertCapability(ctx, c.Key, slug, value, "seed file", author); err != nil {
				log.Fatalf("assert %s=%s for %s: %v", c.Key, value, slug, err)
			}
			asserted++
		}
	}

	fmt.Printf("registered %d capabilities across %d categories", defined, len(categories))
	if *assertIt {
		fmt.Printf(", %d assertions", asserted)
	}
	fmt.Println()

	// The seed's purpose is to make questions askable, so report whether it
	// actually achieved that rather than just reporting that it ran. A seed that
	// registers definitions and no assertions has built a matrix with nothing in
	// it, and the engine will correctly decline to ask anything.
	if *assertIt && asserted == 0 {
		fmt.Println("WARNING: no assertions were written; every capability is unasserted and " +
			"Finder will have no capability questions to ask.")
	}
	cats := make([]string, 0, len(categories))
	for c := range categories {
		cats = append(cats, c)
	}
	fmt.Println("categories: " + strings.Join(cats, ", "))
}