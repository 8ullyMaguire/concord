package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A feature's tags are the input §6.2's themes steer by, and until this pass nothing could
// write one: `feature_tags` has existed since 0001 with no INSERT outside a test, and the
// live instance held 537 features and 0 rows. So the tests here are about the writer, and
// about the rule that makes the tags meaningful rather than free text.

func TestFeatureTagsCanBeAddedAndRead(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()

	tags, err := store.GetFeatureTags(ctx, featureID)
	if err != nil {
		t.Fatalf("GetFeatureTags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("a new feature has tags %v, want none", tags)
	}
	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	tags, err = store.GetFeatureTags(ctx, featureID)
	if err != nil {
		t.Fatalf("GetFeatureTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != tag {
		t.Errorf("tags = %v, want [%s]", tags, tag)
	}
	// Adding the same tag twice is a no-op rather than a 500: the primary key is the
	// enforcement point and two contributors tagging the same thing is ordinary.
	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Errorf("re-adding the same tag: %v", err)
	}
	// An uppercase spelling attaches the SAME tag, and the assertion is that it attached at
	// all, not merely that a count did not change. Written the weaker way first: the store
	// used to re-select the tag by exact name, so "SECURITY-HARDENING" matched nothing, the
	// INSERT wrote zero rows, and the call returned nil -- a silent no-op that a
	// count-based assertion cannot see. The mutation run is what surfaced it.
	if err := store.RemoveFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("RemoveFeatureTag: %v", err)
	}
	if err := store.AddFeatureTag(ctx, pid, featureID, uid, strings.ToUpper(tag)); err != nil {
		t.Fatalf("AddFeatureTag (uppercase): %v", err)
	}
	tags, _ = store.GetFeatureTags(ctx, featureID)
	if len(tags) != 1 || tags[0] != tag {
		t.Errorf("adding %q left tags = %v; want the tag attached in its canonical "+
			"spelling", strings.ToUpper(tag), tags)
	}
}

func TestAddFeatureTagRefusesATagThatDoesNotExist(t *testing.T) {
	store, pid, uid, featureID, _ := themeFixture(t)
	ctx := context.Background()
	err := store.AddFeatureTag(ctx, pid, featureID, uid, "invented-just-now")
	if err == nil {
		t.Fatal("an unknown tag: got nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "taxonomy/proposals") {
		t.Errorf("the refusal does not name the route that creates tags: %q", err.Error())
	}
}

func TestAddFeatureTagRefusesAFeatureFromAnotherProject(t *testing.T) {
	store, _, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()
	// A second project with its own slug: setupWithProject always creates the same slug,
	// so calling it twice returns the project the fixture already made and the test would
	// prove nothing.
	otherProj, err := store.CreateProject(ctx, uid, "other-project", "Other", "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	otherPID := otherProj.ID
	// The feature belongs to pid, not otherPID. Feature ids are global, so a store write
	// that trusted the id alone would let one project's URL tag another project's feature.
	err = store.AddFeatureTag(ctx, otherPID, featureID, uid, tag)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("tagging a feature through an unrelated project: err = %v, want ErrNotFound", err)
	}
}

func TestRemoveFeatureTagDetachesIt(t *testing.T) {
	store, pid, uid, featureID, tag := themeFixture(t)
	ctx := context.Background()
	if err := store.AddFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("AddFeatureTag: %v", err)
	}
	if err := store.RemoveFeatureTag(ctx, pid, featureID, uid, tag); err != nil {
		t.Fatalf("RemoveFeatureTag: %v", err)
	}
	tags, err := store.GetFeatureTags(ctx, featureID)
	if err != nil {
		t.Fatalf("GetFeatureTags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("tags = %v after removal, want none", tags)
	}
	// Removing a tag the feature does not carry is a 404, not a silent success: a client
	// told "removed" when nothing was removed cannot tell a typo from a completed action.
	if err := store.RemoveFeatureTag(ctx, pid, featureID, uid, tag); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a tag that is not there: err = %v, want ErrNotFound", err)
	}
}
