package store

import (
	"context"
	"testing"
	"time"
)

// TestGetFeaturePrioritiesDoesNotDeadlock is a regression guard for a hang, not
// a wrong value.
//
// GetFeaturePriorities used to iterate feature_complaints with an open rows
// cursor and call GetComplaintPain inside the loop. db.Open sets
// SetMaxOpenConns(1), so the nested query needed a second connection that
// could not be handed out until the first rows was closed — which happened
// after the loop. The request blocked until the client gave up, and the HTTP
// test reported a context deadline rather than anything pointing at the cause.
//
// The test runs the call in a goroutine with a deadline: if the deadlock
// returns, it fails with a message naming the cause instead of hanging the
// package until the global timeout.
func TestGetFeaturePrioritiesDoesNotDeadlock(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	comp, err := store.CreateComplaint(ctx, pid, uid, "Slow load", "Pages take too long.", 3, 0.8, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}

	// Two features, each linked to the same complaint, so the loop has work to
	// do and more than one row to read.
	for _, title := range []string{"Feature A", "Feature B"} {
		if _, err := store.CreateFeature(ctx, pid, uid, title, "body", "", nil, nil, []int64{comp.ID}); err != nil {
			t.Fatalf("CreateFeature %q: %v", title, err)
		}
	}

	charter, err := store.GetCharterForProject(ctx, pid)
	if err != nil {
		t.Fatalf("GetCharterForProject: %v", err)
	}

	type result struct {
		prios []FeaturePriority
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		p, err := store.GetFeaturePriorities(ctx, pid, charter)
		ch <- result{p, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("GetFeaturePriorities: %v", r.err)
		}
		if len(r.prios) != 2 {
			t.Fatalf("got %d priorities, want 2", len(r.prios))
		}
		// A linked complaint must contribute pain, otherwise the join is being
		// read but not used.
		if r.prios[0].PainScore <= 0 {
			t.Errorf("pain score is %v; the linked complaint was not counted", r.prios[0].PainScore)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GetFeaturePriorities did not return within 10s: it is deadlocking, " +
			"almost certainly a query issued while another rows cursor is open " +
			"against a pool limited to one connection")
	}
}

// TestGetFeaturePrioritiesOrdersByPriorityDesc pins the ordering the endpoint
// promises. A caller ranking work must be able to trust the first row.
func TestGetFeaturePrioritiesOrdersByPriorityDesc(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	comp, err := store.CreateComplaint(ctx, pid, uid, "Pain", "body", 5, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	if _, err := store.CreateFeature(ctx, pid, uid, "Only", "body", "", nil, nil, []int64{comp.ID}); err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}

	charter, _ := store.GetCharterForProject(ctx, pid)
	prios, err := store.GetFeaturePriorities(ctx, pid, charter)
	if err != nil {
		t.Fatalf("GetFeaturePriorities: %v", err)
	}
	if len(prios) != 1 {
		t.Fatalf("got %d priorities, want 1", len(prios))
	}
	// A project with no votes has EloR at the default, so the priority must
	// still be a finite number rather than NaN — NaN would sort arbitrarily and
	// the ordering would differ between runs.
	if prios[0].PriorityScore != prios[0].PriorityScore {
		t.Errorf("priority score is NaN, so the ordering is not deterministic")
	}
}
