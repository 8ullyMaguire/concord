# The author is told when their contribution is removed or rejected

Status: specified 2026-11-12. Closes the last item of `docs/specs/moderation-spec.md` §8 and
the first of the three owed items in `docs/HANDOFF.md`.

## 1. Problem

A contributor's idea is removed from an arena by a moderator, and the contributor learns it by
finding their own idea missing and a `rejected` status where it used to be. Nothing in the
product says "this happened, here is why, here is who decided". The moderation log exists and
is honest, and it is a public record — a person should not have to read a modlog to be told
about their own contribution.

Measured premise, on the live instance 2026-11-12:

    sqlite3 concord.db "select count(*) from audit_log where action='arena.remove'"
    0
    sqlite3 concord.db "select count(*) from audit_log where action='reject_complaint'"
    0

So the volume today is zero actions, and the design must not depend on volume to justify
itself. What it must not do is *appear* to have a number it does not have: an empty inbox is a
true statement about a quiet instance, not a broken feature, and the page has to say so in a
way a reader can tell apart from "there is nothing here because this shipped yesterday".

## 2. Goals

1. When a moderator removes a feature from an arena, the FEATURE'S AUTHOR has a record of it
   that they can read: what was removed, which project, who did it, when, and the reason if a
   reason was given.
2. When a moderator rejects a complaint as spam, the COMPLAINT'S AUTHOR has the same record.
3. The record is scoped to its reader: nobody sees anyone else's notifications, not even a
   maintainer.
4. A notification can be marked read, and the unread count is visible on every page in the
   header, so "you have something to read" costs one glance.
5. The notification and the decision are written in the SAME transaction. There is no state in
   which the act exists and the author was silently not told, and no state in which the author
   was told about an act that rolled back.

## 3. Non-goals

- **No email.** This instance has no outbound mail configuration and shipping one is a separate
  decision with its own failure modes (deliverability, bounces, unsubscribe).
- **No notification preferences.** One switch to turn off would be a switch that can be left
  off silently; the volume here is decisions about your own contributions, which a person wants.
- **No notifications for votes, comments, or rank changes.** Those are ambient, and an inbox
  that fires on them trains a reader to ignore the inbox.
- **No digest or batching.** Every notification is one decision.
- **No deletion.** A notification is a record of a decision; deleting it hides the decision the
  author is entitled to know about. It can be read or unread, nothing more.

## 4. Design

### 4.1 The table

    CREATE TABLE notifications (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      kind        TEXT    NOT NULL CHECK (kind IN ('feature_removed','complaint_rejected')),
      project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      subject_id  INTEGER NOT NULL,           -- feature id or complaint id, per kind
      title       TEXT    NOT NULL,           -- the subject's title, copied at write time
      reason      TEXT    NOT NULL DEFAULT '',
      actor_id    INTEGER,                    -- who decided; NULL once the account is gone
      created_at  REAL    NOT NULL,
      read_at     REAL
    );
    CREATE INDEX notifications_inbox ON notifications(user_id, created_at DESC);
    CREATE INDEX notifications_unread ON notifications(user_id, read_at);

**Why the title is copied.** A notification is a record of a decision about a thing that may
be deleted afterwards. A join to `features.title` would render "removed: (null)" the moment the
feature row goes, which is exactly the case this feature exists for. The title is the text the
author needs; it is not a second source of truth for the feature.

**Why `actor_id` may be NULL.** `audit_log` keeps the actor id forever; a notification to a
live reader should not fail to render because the moderator's account was deleted. It renders
"a moderator" instead.

### 4.2 Where the rows are written

Two call sites, both already inside a transaction that records the act:

- `store.RemoveFeatureFromArena` — after the `arena_entries` delete and the `features.status`
  update, before the audit insert, with `f.AuthorID`.
- `store.RejectComplaint` (the spam penalty path) — with the complaint's `AuthorID`.

Both use one helper, `notifyTx(ctx, tx, ...)`, so the shape cannot drift between them.

**One exception, and it is deliberate.** A moderator removing THEIR OWN feature gets no
notification. The act is in the modlog, they performed it, and telling somebody what they just
did is noise that makes an inbox look busy.

### 4.3 The routes

    GET  /api/v1/notifications            -> {"notifications":[..], "unread": n}
    POST /api/v1/notifications/{id}/read  -> {"id":.., "read": true}
    POST /api/v1/notifications/read-all   -> {"marked": n}

All three are **self-scoped**: the user comes from the session, never from a parameter or a
body field. There is no route that takes a user id, so there is no route to get wrong.

Marking an id that belongs to somebody else answers 404, the same as an id that does not exist:
a 403 would confirm the id is real, which is a fact about another person's inbox.

### 4.4 The page

`/notifications`, titled **Notifications**, in the header next to the account controls, with the
unread count as a numeral when it is above zero and absent when it is not. The page lists the
newest 50 as cards: kind, title, project, who, when, and the reason in the moderator's words.
Unread cards are flagged with a visible marker, not only by weight or colour.

Reading the page does not mark anything read. A "Mark all read" control does, and each card has
its own. Auto-marking on view means a page you scrolled past looks like a page you read.

### 4.5 What the empty state says

An empty inbox reads: "Nothing has been removed from your contributions." — a statement about
the reader's record, not about the feature. There is no "0 unread" badge: zero is rendered as
absence, because a badge that says zero is a badge nobody needs to read.

## 5. Success criteria

Every one of these is a command with observable output, and each is the kind of thing that can
be false while the suite is green:

1. Removing a feature writes exactly one notification for the author, in the same transaction:
   `go test ./internal/store -run TestRemovingAFeatureNotifiesItsAuthor -v`
2. A rolled-back removal writes none: same test file, `TestARolledBackRemovalNotifiesNobody`.
3. A moderator's own feature produces none: `TestAModeratorIsNotNotifiedOfTheirOwnDecision`.
4. Another reader's inbox is unreachable: `TestAnotherReadersInboxIsNotReachable` — 404 for a
   stranger's id, and the stranger's `read_at` unchanged.
5. Reading the page marks nothing: `TestLoadingTheInboxMarksNothingRead`.
6. The unread count on the page equals the unread count in the response, and both fall to zero
   after "mark all read": `TestTheHeaderCountMatchesTheInbox`.
7. The rejection path notifies too: `TestRejectingAComplaintNotifiesItsAuthor`.
8. Live, on concord.polarisocial.xyz, with a probe account and a real removal: the author's
   `/notifications` shows the card, the header shows the count, marking read clears it — the
   transcript pasted into `docs/REMAINING.md`.

## 6. What this does not fix

The person is told *after* the fact, by a page they have to open. Someone who never signs in
again is still never told. That is the honest limit of an inbox without email, and it is
written here rather than discovered later.
