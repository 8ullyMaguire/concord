package store

// Visibility: the four levels, and the access rule each one implies.
//
// The behaviour worth testing is not "the column round-trips" but the refusals:
// an anonymous caller, a signed-in non-member, and a member must get three
// different answers for a private project, and only the member may change the
// setting. Those are the cases that leak a project by accident.

import (
	"context"
	"errors"
	"testing"
)

func mkProject(t *testing.T, store *DB, uid int64, slug string) Project {
	t.Helper()
	p, err := store.CreateProject(context.Background(), uid, slug, slug, "desc", "collective", "MIT")
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", slug, err)
	}
	return p
}

// TestProjectDefaultsToPublic guards the migration's backfill: an existing
// project must not silently become private, which would break every existing
// link on the instance.
func TestProjectDefaultsToPublic(t *testing.T) {
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()

	p, err := store.GetProject(ctx, "test-project")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Visibility != VisibilityPublic {
		t.Errorf("default visibility = %q, want %q", p.Visibility, VisibilityPublic)
	}
	if !p.IsListed() {
		t.Error("a public project must be listed")
	}
	_ = uid
}

// TestUnlistedIsReadableByAnyone is the deliberate statement that unlisted is
// obscurity, not access control. If this ever becomes false, the level's
// documentation is a lie.
func TestUnlistedIsReadableByAnyone(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	if _, err := store.SetProjectVisibility(ctx, "test-project", VisibilityUnlisted); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}

	ok, err := store.CanAccessProject(ctx, Project{ID: pid, Visibility: VisibilityUnlisted}, 0)
	if err != nil {
		t.Fatalf("CanAccessProject: %v", err)
	}
	if !ok {
		t.Error("unlisted must be readable anonymously: the URL is the capability")
	}

	// ...but it must still be absent from listings.
	listed, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range listed {
		if p.ID == pid {
			t.Error("an unlisted project must not appear in the public list")
		}
	}
	_ = uid
}

// TestPrivateRefusesNonMember covers the matrix that matters: anonymous and
// signed-in-non-member are both refused, a member is allowed.
func TestPrivateRefusesNonMember(t *testing.T) {
	store, uid, pid := setupWithProject(t)
	ctx := context.Background()

	proj := mkProject(t, store, uid, "secret")
	if _, err := store.SetProjectVisibility(ctx, "secret", VisibilityPrivate); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}

	cases := []struct {
		name  string
		actor int64
		proj  Project
		allow bool
	}{
		{"anonymous", 0, Project{ID: proj.ID, Visibility: VisibilityPrivate}, false},
		{"non-member", 999, Project{ID: proj.ID, Visibility: VisibilityPrivate}, false},
		{"member", uid, Project{ID: proj.ID, Visibility: VisibilityPrivate}, true},
		{"anonymous on public", 0, Project{ID: proj.ID, Visibility: VisibilityPublic}, true},
		{"anonymous on unlisted", 0, Project{ID: proj.ID, Visibility: VisibilityUnlisted}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.CanAccessProject(ctx, tc.proj, tc.actor)
			if err != nil {
				t.Fatalf("CanAccessProject: %v", err)
			}
			if got != tc.allow {
				t.Errorf("CanAccessProject(%s, actor=%d) = %v, want %v", tc.proj.Visibility, tc.actor, got, tc.allow)
			}
		})
	}
	_ = pid
}

// TestUnknownVisibilityFailsClosed pins the direction of the fallback. A value
// the store does not recognise must deny, because the alternative is that a
// future or corrupted row is readable by everyone.
func TestUnknownVisibilityFailsClosed(t *testing.T) {
	store, _, _ := setupWithProject(t)
	ctx := context.Background()

	ok, err := store.CanAccessProject(ctx, Project{ID: 1, Visibility: "wide-open"}, 0)
	if err == nil {
		t.Fatal("expected an error for an unknown visibility")
	}
	if ok {
		t.Error("an unknown visibility must not grant access")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

// TestSetProjectVisibilityRejectsGarbage keeps a typo from storing a level that
// no read path can classify.
func TestSetProjectVisibilityRejectsGarbage(t *testing.T) {
	store, _, _ := setupWithProject(t)
	ctx := context.Background()

	if _, err := store.SetProjectVisibility(ctx, "test-project", "secret-but-not-a-level"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	p, err := store.GetProject(ctx, "test-project")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Visibility != VisibilityPublic {
		t.Errorf("a rejected write must not change the stored value; got %q", p.Visibility)
	}
}

// TestListProjectsHidesNonPublic is the discovery guarantee: the anonymous list
// is exactly the public set.
func TestListProjectsHidesNonPublic(t *testing.T) {
	store, uid, _ := setupWithProject(t)
	ctx := context.Background()

	priv := mkProject(t, store, uid, "priv")
	prot := mkProject(t, store, uid, "prot")
	unl := mkProject(t, store, uid, "unl")
	for slug, vis := range map[string]string{
		"priv": VisibilityPrivate,
		"prot": VisibilityProtected,
		"unl":  VisibilityUnlisted,
	} {
		if _, err := store.SetProjectVisibility(ctx, slug, vis); err != nil {
			t.Fatalf("SetProjectVisibility(%s): %v", slug, err)
		}
	}

	anon, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range anon {
		switch p.ID {
		case priv.ID, prot.ID, unl.ID:
			t.Errorf("anonymous list leaked %s at %q", p.Slug, p.Visibility)
		}
	}

	// The owner must still see their own private project, or a private project
	// would be unreachable from the UI.
	mine, err := store.ListProjectsVisibleTo(ctx, uid)
	if err != nil {
		t.Fatalf("ListProjectsVisibleTo: %v", err)
	}
	found := map[string]bool{}
	for _, p := range mine {
		found[p.Slug] = true
	}
	for _, slug := range []string{"priv", "prot", "unl", "test-project"} {
		if !found[slug] {
			t.Errorf("owner cannot see their own %s", slug)
		}
	}

	// Someone else must not.
	theirs, err := store.ListProjectsVisibleTo(ctx, 4242)
	if err != nil {
		t.Fatalf("ListProjectsVisibleTo: %v", err)
	}
	for _, p := range theirs {
		if p.ID == priv.ID {
			t.Error("a stranger's visible list leaked the private project")
		}
	}
}

// ---------------------------------------------------------------- invites

func TestInviteRedeemGrantsProtectedAccess(t *testing.T) {
	store, owner, _ := setupWithProject(t)
	ctx := context.Background()

	if _, err := store.SetProjectVisibility(ctx, "test-project", VisibilityProtected); err != nil {
		t.Fatalf("SetProjectVisibility: %v", err)
	}
	inv, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 0)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if len(inv.Token) < 32 {
		t.Errorf("token is %d chars; too short to be unguessable", len(inv.Token))
	}

	guest, err := store.CreateUser(ctx, "guest", "Guest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Before redeeming, a protected project is closed to a signed-in stranger.
	proj := Project{ID: mustProjectID(t, store, "test-project"), Visibility: VisibilityProtected}
	ok, err := store.CanAccessProject(ctx, proj, guest.ID)
	if err != nil {
		t.Fatalf("CanAccessProject: %v", err)
	}
	if ok {
		t.Error("protected must be closed before the invite is redeemed")
	}

	if _, err := store.RedeemInvite(ctx, inv.Token, guest.ID); err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}

	ok, err = store.CanAccessProject(ctx, proj, guest.ID)
	if err != nil {
		t.Fatalf("CanAccessProject: %v", err)
	}
	if !ok {
		t.Error("redeeming the invite must grant access")
	}
}

// TestRedeemInviteIsIdempotent covers the real-world case: the link is opened
// twice, or the browser retries. The use counter must advance once, or a
// max_uses=1 invite is exhausted by one person clicking twice.
func TestRedeemInviteIsIdempotent(t *testing.T) {
	store, owner, pid := setupWithProject(t)
	ctx := context.Background()
	_ = pid

	inv, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 1)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	guest, err := store.CreateUser(ctx, "guest", "Guest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.RedeemInvite(ctx, inv.Token, guest.ID); err != nil {
			t.Fatalf("RedeemInvite attempt %d: %v", i+1, err)
		}
	}
	got, err := store.GetInvite(ctx, inv.ID)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if got.Uses != 1 {
		t.Errorf("uses = %d after three redemptions by one user, want 1", got.Uses)
	}
}

// TestRedeemInviteRejectsExpiredRevokedExhausted pins all three refusal modes to
// the same error, so the endpoint cannot be used to probe which tokens exist.
func TestRedeemInviteRejectsExpiredRevokedExhausted(t *testing.T) {
	store, owner, pid := setupWithProject(t)
	ctx := context.Background()
	_ = pid

	guest, err := store.CreateUser(ctx, "guest", "Guest")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	other, err := store.CreateUser(ctx, "other", "Other")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Expired: a negative TTL is stored as an absolute time in the past by
	// clamping here, because CreateInvite treats <= 0 as "no expiry". Insert
	// directly so the row under test is unambiguous.
	expiredTok := "expired-token-value-for-test"
	_, err = store.ExecContext(ctx, `
		INSERT INTO project_invites (project_id, token, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, mustProjectID(t, store, "test-project"), expiredTok,
		owner, float64(1), float64(2)) // epoch 2 is in the past
	if err != nil {
		t.Fatalf("insert expired invite: %v", err)
	}

	revoked, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 0)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := store.RevokeInvite(ctx, revoked.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	exhausted, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 1)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := store.RedeemInvite(ctx, exhausted.Token, guest.ID); err != nil {
		t.Fatalf("RedeemInvite: %v", err)
	}

	for name, tok := range map[string]string{
		"expired":     expiredTok,
		"revoked":     revoked.Token,
		"exhausted":   exhausted.Token,
		"nonexistent": "no-such-token-at-all",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.RedeemInvite(ctx, tok, other.ID)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("error = %v, want ErrNotFound (all failure modes must be indistinguishable)", err)
			}
		})
	}
}

// TestAnonymousCannotRedeem: an invite is redeemed by a signed-in user, since
// the grant it creates is a membership.
func TestAnonymousCannotRedeem(t *testing.T) {
	store, owner, _ := setupWithProject(t)
	ctx := context.Background()

	inv, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 0)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := store.RedeemInvite(ctx, inv.Token, 0); !errors.Is(err, ErrAuth) {
		t.Errorf("error = %v, want ErrAuth", err)
	}
}

// TestRevokeInviteTwiceIsRejected: revoking twice should say so rather than
// silently report success on a no-op UPDATE.
func TestRevokeInviteTwiceIsRejected(t *testing.T) {
	store, owner, _ := setupWithProject(t)
	ctx := context.Background()

	inv, err := store.CreateInvite(ctx, mustProjectID(t, store, "test-project"), owner, 0, 0)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := store.RevokeInvite(ctx, inv.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if err := store.RevokeInvite(ctx, inv.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("second revoke error = %v, want ErrInvalid", err)
	}
	if err := store.RevokeInvite(ctx, inv.ID+9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a missing invite error = %v, want ErrNotFound", err)
	}
}

func mustProjectID(t *testing.T, store *DB, slug string) int64 {
	t.Helper()
	p, err := store.GetProject(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetProject(%s): %v", slug, err)
	}
	return p.ID
}
