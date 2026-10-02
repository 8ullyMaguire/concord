package store

import (
	"context"
	"testing"
)

// The effort column was unconstrained TEXT that CreateFeature hardcoded to 'M',
// so a caller could pass anything -- or nothing -- and the stored value was
// always 'M' regardless. These tests pin the fixed behaviour.

// scoredFixture creates a project, a user, and one validated complaint, which
// is what CreateFeature requires before it will accept anything at all.
func scoredFixture(t *testing.T) (*DB, int64, int64, int64) {
	t.Helper()
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()
	comp, err := store.CreateComplaint(ctx, pid, uid, "Base pain", "body", 3, 1.0, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := store.ValidateComplaint(ctx, comp.ID); err != nil {
		t.Fatalf("ValidateComplaint: %v", err)
	}
	return store, pid, uid, comp.ID
}

func intp(v int) *int { return &v }

func TestCreateFeatureStoresCallerEffort(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	for _, size := range []string{"S", "M", "L", "XL"} {
		f, err := store.CreateFeature(ctx, pid, uid, "effort "+size, "body", size, nil, nil, []int64{comp})
		if err != nil {
			t.Fatalf("CreateFeature(%s): %v", size, err)
		}
		if f.Effort != size {
			t.Errorf("effort %s: stored %q, want %q", size, f.Effort, size)
		}
		got, err := store.GetFeature(ctx, f.ID)
		if err != nil {
			t.Fatalf("GetFeature: %v", err)
		}
		if got.Effort != size {
			t.Errorf("effort %s: read back %q, want %q", size, got.Effort, size)
		}
	}
}

func TestCreateFeatureRejectsBadEffort(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	// "medium" is the realistic mistake: a human-readable size for a t-shirt
	// scale, and "s" the classic case slip. The schema CHECK catches these too,
	// but the store should refuse them with a clear error rather than let them
	// become a constraint violation surfacing as a 500.
	for _, bad := range []string{"medium", "3", "s", "XXL", " "} {
		if _, err := store.CreateFeature(ctx, pid, uid, "bad", "body", bad, nil, nil, []int64{comp}); err == nil {
			t.Errorf("CreateFeature(effort=%q) succeeded, want rejection", bad)
		}
	}
}

func TestCreateFeatureDefaultsEffortToM(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	f, err := store.CreateFeature(ctx, pid, uid, "no effort", "body", "", nil, nil, []int64{comp})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if f.Effort != "M" {
		t.Errorf("omitted effort: got %q, want %q", f.Effort, "M")
	}
}

func TestCreateFeatureStoresImpactScores(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	f, err := store.CreateFeature(ctx, pid, uid, "scored", "body", "M", intp(8), intp(2), []int64{comp})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if f.Impact == nil || *f.Impact != 8 {
		t.Fatalf("impact: got %v, want 8", f.Impact)
	}
	if f.EffortScore == nil || *f.EffortScore != 2 {
		t.Fatalf("effort_score: got %v, want 2", f.EffortScore)
	}
	// impact_ratio is GENERATED, so it can only be right if it is computed from
	// the two inputs rather than supplied by the caller.
	if f.ImpactRatio == nil {
		t.Fatal("impact_ratio is nil, want 8/2 = 4.0")
	}
	if r := *f.ImpactRatio; r < 3.999 || r > 4.001 {
		t.Errorf("impact_ratio = %v, want 4.0", r)
	}
}

func TestCreateFeatureRejectsOutOfRangeScores(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	cases := []struct {
		name           string
		impact, effort *int
	}{
		{"impact 0", intp(0), intp(5)},
		{"impact 11", intp(11), intp(5)},
		{"impact negative", intp(-3), intp(5)},
		{"effort_score 0", intp(5), intp(0)},
		{"effort_score 11", intp(5), intp(11)},
	}
	for _, tc := range cases {
		if _, err := store.CreateFeature(ctx, pid, uid, tc.name, "body", "M",
			tc.impact, tc.effort, []int64{comp}); err == nil {
			t.Errorf("%s: CreateFeature succeeded, want rejection", tc.name)
		}
	}
}

func TestCreateFeatureUnscoredHasNullRatio(t *testing.T) {
	ctx := context.Background()
	store, pid, uid, comp := scoredFixture(t)

	// Most features predate these columns. They must read NULL, not 0: a zero
	// ratio would sort above every real score and quietly become "the best
	// feature ever proposed".
	f, err := store.CreateFeature(ctx, pid, uid, "unscored", "body", "M", nil, nil, []int64{comp})
	if err != nil {
		t.Fatalf("CreateFeature: %v", err)
	}
	if f.ImpactRatio != nil {
		t.Errorf("impact_ratio = %v, want nil for an unscored feature", *f.ImpactRatio)
	}
	if f.Impact != nil || f.EffortScore != nil {
		t.Errorf("unscored feature got impact=%v effort_score=%v, want nil/nil", f.Impact, f.EffortScore)
	}
}
