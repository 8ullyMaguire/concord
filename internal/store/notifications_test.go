package store

// The inbox (docs/specs/notification-spec.md §5). Every test here is a success criterion from
// the spec's §5, named after the rule it guards: "notifications CRUD" would pass against an
// inbox that tells the wrong person.

import (
	"context"
	"errors"
	"testing"
)

// notifFixture: a project, an author who owns a feature, a moderator who does not, and an
// arena holding the feature so it can be removed.
func notifFixture(t *testing.T) (d *DB, author, moderator, pid, fid, arenaID int64) {
	t.Helper()
	d, author, pid = setupWithProject(t)
	ctx := context.Background()
	fid = newFeature(t, d, pid, author, "Make the export complete")
	moderator = createStranger(t, d)
	a, err := d.EnsureArena(ctx, ArenaFeaturePriority, pid, 0, "", "")
	if err != nil {
		t.Fatalf("EnsureArena: %v", err)
	}
	if err := d.UpsertArenaEntry(ctx, a.ID, "feature", fid); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	return d, author, moderator, pid, fid, a.ID
}

// §5.1: removing a feature writes exactly one notification, for the AUTHOR.
func TestRemovingAFeatureNotifiesItsAuthor(t *testing.T) {
	d, author, moderator, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()

	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, moderator, "duplicate of #12"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	list, unread, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the author has %d notifications, want 1", len(list))
	}
	if unread != 1 {
		t.Fatalf("unread = %d, want 1", unread)
	}
	n := list[0]
	if n.Kind != NotificationFeatureRemoved {
		t.Fatalf("kind = %q, want %q", n.Kind, NotificationFeatureRemoved)
	}
	if n.SubjectID != fid || n.ProjectID != pid {
		t.Fatalf("notification addresses feature %d in project %d, want %d / %d", n.SubjectID, n.ProjectID, fid, pid)
	}
	// The TITLE is copied at write time. Asserted here because the copy is the whole reason
	// the column exists: a join would render "(null)" once the feature row is gone.
	if n.Title != "Make the export complete" {
		t.Fatalf("title = %q, want the feature's title", n.Title)
	}
	if n.Reason != "duplicate of #12" {
		t.Fatalf("reason = %q, want the moderator's words", n.Reason)
	}
	if n.ActorName == "" {
		t.Fatal("the notification does not say who decided")
	}
	if n.Read {
		t.Fatal("a fresh notification is already read")
	}

	// And the moderator, who decided, is not told about their own act.
	if theirs, _, _ := d.ListNotifications(ctx, moderator, 50); len(theirs) != 0 {
		t.Fatalf("the moderator was notified of their own decision: %+v", theirs)
	}
}

// §5.2 / §4.2: the notification is in the SAME transaction as the act, so a refused act leaves
// no notification behind. An empty reason makes insertAct fail, which happens AFTER the row is
// written inside the tx -- exactly the window the design exists to close.
func TestARolledBackRemovalNotifiesNobody(t *testing.T) {
	d, author, moderator, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()

	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, moderator, "   "); err == nil {
		t.Fatal("a removal with no reason was accepted")
	}
	list, _, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a rolled-back removal left %d notification(s): %+v", len(list), list)
	}
	// The act itself rolled back too, which is the same transaction proving itself.
	if _, err := d.GetArenaEntry(ctx, arenaID, "feature", fid); err != nil {
		t.Fatalf("the entry was removed by a failed act: %v", err)
	}
}

// §4.2: a moderator acting on their OWN feature is not told about it.
func TestAModeratorIsNotNotifiedOfTheirOwnDecision(t *testing.T) {
	d, author, _, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()

	// The author moderates their own feature.
	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, author, "withdrawing it myself"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, unread, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(list) != 0 || unread != 0 {
		t.Fatalf("a self-decided removal produced %d notifications (unread %d)", len(list), unread)
	}
}

// §5.4: another reader's inbox is unreachable, and the attempt changes nothing.
func TestAnotherReadersInboxIsNotReachable(t *testing.T) {
	d, author, moderator, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()
	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, moderator, "spam"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, _, err := d.ListNotifications(ctx, author, 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("setup: %d notifications, err %v", len(list), err)
	}

	stranger := createStranger(t, d)
	if err := d.MarkNotificationRead(ctx, stranger, list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger marking another reader's notification: %v, want ErrNotFound", err)
	}
	// Not found AND unchanged: a 404 that still wrote would be the worst of both.
	after, unread, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if unread != 1 || after[0].Read {
		t.Fatalf("a stranger's attempt marked it read (unread=%d read=%v)", unread, after[0].Read)
	}
	// The stranger's own inbox stays empty: nothing was created by asking.
	if theirs, _, _ := d.ListNotifications(ctx, stranger, 50); len(theirs) != 0 {
		t.Fatalf("the stranger gained %d notifications", len(theirs))
	}
}

// §5.5: loading the inbox marks nothing read -- only the reader's explicit action does.
func TestLoadingTheInboxMarksNothingRead(t *testing.T) {
	d, author, moderator, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()
	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, moderator, "off scope"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	for i := 0; i < 3; i++ {
		list, unread, err := d.ListNotifications(ctx, author, 50)
		if err != nil {
			t.Fatalf("ListNotifications: %v", err)
		}
		if len(list) != 1 || unread != 1 || list[0].Read {
			t.Fatalf("read %d after %d loads (unread=%d read=%v)", i+1, i+1, unread, list[0].Read)
		}
	}

	// And the explicit action does mark it, idempotently.
	if err := d.MarkNotificationRead(ctx, author, list0(t, d, author)); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if err := d.MarkNotificationRead(ctx, author, list0(t, d, author)); err != nil {
		t.Fatalf("marking an already-read notification: %v", err)
	}
	if _, unread, _ := d.ListNotifications(ctx, author, 50); unread != 0 {
		t.Fatalf("unread = %d after marking read", unread)
	}
}

func list0(t *testing.T, d *DB, uid int64) int64 {
	t.Helper()
	list, _, err := d.ListNotifications(context.Background(), uid, 50)
	if err != nil || len(list) == 0 {
		t.Fatalf("no notification to mark: %v", err)
	}
	return list[0].ID
}

// §5.6: the badge count and the inbox agree, and both fall to zero.
func TestTheBadgeCountMatchesTheInbox(t *testing.T) {
	d, author, moderator, pid, fid, arenaID := notifFixture(t)
	ctx := context.Background()
	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, fid, moderator, "duplicate"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// A second decision about the same author, so the count is not trivially 1.
	second := newFeature(t, d, pid, author, "Second idea")
	if err := d.UpsertArenaEntry(ctx, arenaID, "feature", second); err != nil {
		t.Fatalf("seed second entry: %v", err)
	}
	if _, err := d.RemoveFeatureFromArena(ctx, pid, arenaID, second, moderator, "also duplicate"); err != nil {
		t.Fatalf("remove second: %v", err)
	}

	badge, err := d.CountUnreadNotifications(ctx, author)
	if err != nil {
		t.Fatalf("CountUnreadNotifications: %v", err)
	}
	list, unread, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if badge != 2 || unread != 2 || len(list) != 2 {
		t.Fatalf("badge %d, unread %d, rows %d -- want 2/2/2", badge, unread, len(list))
	}
	// Newest first: the second removal is the first card.
	if list[0].Title != "Second idea" {
		t.Fatalf("newest first is broken: %q came first", list[0].Title)
	}

	marked, err := d.MarkAllNotificationsRead(ctx, author)
	if err != nil {
		t.Fatalf("mark all: %v", err)
	}
	if marked != 2 {
		t.Fatalf("marked %d, want 2", marked)
	}
	if badge, _ := d.CountUnreadNotifications(ctx, author); badge != 0 {
		t.Fatalf("the badge still reads %d", badge)
	}
	// Marking again clears nothing, and says so honestly.
	if again, _ := d.MarkAllNotificationsRead(ctx, author); again != 0 {
		t.Fatalf("a second mark-all cleared %d rows", again)
	}
}

// §5.7: rejecting a complaint notifies ITS author, in the same transaction as the penalty.
func TestRejectingAComplaintNotifiesItsAuthor(t *testing.T) {
	d, author, moderator, pid := func() (*DB, int64, int64, int64) {
		db, uid, pid := setupWithProject(t)
		return db, uid, createStranger(t, db), pid
	}()
	ctx := context.Background()
	c, err := d.CreateComplaint(ctx, pid, author, "Buy my course", "spam body", 1, 0.5, 1.0)
	if err != nil {
		t.Fatalf("CreateComplaint: %v", err)
	}
	if err := d.RejectComplaint(ctx, c.ID, moderator); err != nil {
		t.Fatalf("RejectComplaint: %v", err)
	}

	list, unread, err := d.ListNotifications(ctx, author, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(list) != 1 || unread != 1 {
		t.Fatalf("the complaint's author has %d notifications (unread %d), want 1", len(list), unread)
	}
	if list[0].Kind != NotificationComplaintRejected {
		t.Fatalf("kind = %q, want %q", list[0].Kind, NotificationComplaintRejected)
	}
	if list[0].SubjectID != c.ID {
		t.Fatalf("subject = %d, want the complaint %d", list[0].SubjectID, c.ID)
	}
	if list[0].Title != "Buy my course" {
		t.Fatalf("title = %q, want the complaint's title", list[0].Title)
	}
	// The rejection itself still happened exactly once.
	if got, _ := d.GetComplaint(ctx, c.ID); got.Status != "rejected" {
		t.Fatalf("complaint status = %q, want rejected", got.Status)
	}
	if theirs, _, _ := d.ListNotifications(ctx, moderator, 50); len(theirs) != 0 {
		t.Fatalf("the moderator was notified of their own decision")
	}
}
