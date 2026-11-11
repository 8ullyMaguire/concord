// Package ledger is the Go form of the estate's polaris-ledger crate. See
// docs/specs/moderation-spec.md §10: the crate is the shared component and owns the rules,
// the projection, the validation and the cursor; it owns no table, and neither does this.
// Each product supplies its own act vocabulary and its own row.
package ledger
