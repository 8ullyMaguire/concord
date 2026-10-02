// Command granttrust assigns a trust level to an account.
//
// It exists because SetTrustLevel has no HTTP route: without it, no account
// can ever reach the configured minimum, and every trust-gated endpoint --
// feature status changes above all -- is unreachable for every user including
// the operator. This calls the same store function the application would, so
// the audit row in trust_grants and the value in users.trust_level stay
// consistent with each other.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: granttrust <user-id> <level>")
		os.Exit(2)
	}
	dbPath := os.Getenv("CONCORD_DB")
	if dbPath == "" {
		dbPath = os.ExpandEnv("$HOME/.local/share/concord/concord.db")
	}
	// db.Open is the same constructor cmd/concord uses, so the connection has
	// the same pragmas (foreign_keys, busy_timeout, WAL) the server relies on.
	sqlDB, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer sqlDB.Close()
	st := store.New(sqlDB)

	target, err := strconv.ParseInt(os.Args[1], 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad user id:", err)
		os.Exit(2)
	}
	level, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad level:", err)
		os.Exit(2)
	}

	// There is no admin role to check against on a local instance: reaching
	// this database at all is the authorisation. granted_by is the target
	// account, which keeps the audit trail meaningful for a self-grant.
	if err := st.SetTrustLevel(context.Background(), target, level, target,
		"operator account: runs the concord portfolio import"); err != nil {
		fmt.Fprintln(os.Stderr, "SetTrustLevel:", err)
		os.Exit(1)
	}
	cur, _ := st.GetTrustLevel(context.Background(), target)
	ceil, _ := st.TrustCeiling(context.Background())
	fmt.Printf("user %d trust_level=%d ceiling=%d\n", target, cur, ceil)
}
