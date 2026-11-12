# Plan — the author's inbox (docs/specs/notification-spec.md)

Written before the code, kept as the record of the order the work actually took. Each step is
the size of one commit and says how it is verified, because a plan that says "implement the
inbox" is a wish with a heading.

## M1 — the table and the shape (one migration, no behaviour)

1. `internal/db/migrations/0039_notifications.sql`: the table, two indexes, the CHECK on kind.
   Verify: `make build && ./bin/concord -migrate-only` on a copy, or `go test ./internal/db/` —
   the migrations test runs the chain from empty.
2. `internal/store/notifications.go`: `Notification`, the two kind constants, `notifyTx`,
   `ListNotifications`, `CountUnreadNotifications`, `MarkNotificationRead`,
   `MarkAllNotificationsRead`.
   Verify: `go build ./...` (nothing calls it yet, which is the point — the shape lands first).

## M2 — write the rows where the decision is made

3. `store.RemoveFeatureFromArena`: `notifyTx` before `insertAct`, inside the existing tx.
   Verify: `TestRemovingAFeatureNotifiesItsAuthor`, `TestAModeratorIsNotNotifiedOfTheirOwnDecision`.
4. `store.RejectComplaint`: becomes one transaction (status, reputation penalty, notification)
   and takes `actorID`. Call sites: `handleRejectComplaint`, `complaints_reject_test.go`.
   Verify: `TestRejectingAComplaintNotifiesItsAuthor`; `TestARolledBackRemovalNotifiesNobody`
   proves the same tx by making the act fail after the notification is written.
5. `store/guest.go` `foldColumns`: both columns. The fold gate fails until this is done, which
   is how it was found.
   Verify: `TestEveryColumnThatCarriesAUserIsFolded`.

## M3 — the routes and the page

6. `internal/httpapi/notifications.go`: list / mark one / mark all, all `requireWriteActor`
   (the write-guard gate demands a guard call, and the guard is also the 401).
   Verify: `TestTheInboxIsOnlyEverYourOwn` — stranger's id is 404 and their `read_at` unchanged.
7. `server.go`: four routes, `"notifications"` added to the registered page list. A missing
   entry renders a 500 whose body names the template, which is how the omission surfaced.
8. `templates/notifications.html`, `assets/js/notifications.js`, styles, and the header link in
   `base.html` with the server-rendered count (`pageData.UnreadNotifications`).
   Verify: `TestTheHeaderBadgeIsTheReadersOwnCount`, `TestMarkingAllReadClearsTheBadge`.
9. `reader_coverage_test.go`: one row for the three routes, anchor `data-read-all`.

## M4 — the ledger, then the instance

10. `make verify` (fmt, vet, tests, docs-check, e2e-sync), `make e2e`, `make gates`.
11. Commit, push to forgejo/main + github/master + github/main.
12. `make deploy` to thinkcentre; run a live probe with two probe accounts (author + moderator),
    take a screenshot-free transcript of the header badge, the card, the mark-read and the
    cleared badge; then delete the probe rows and check `PRAGMA integrity_check`.
13. Write the transcript into `docs/REMAINING.md` and move the §8 item in `HANDOFF.md` from
    "owed" to "shipped, with its limit".

## Ordering, and what was rejected

- **The route before the writer** (M3 before M2) was rejected: the page's empty state is
  identical for "nothing happened" and "nothing is ever written", so a page first ships a
  screen that lies quietly.
- **Client-side badge** was rejected: it arrives after a fetch, so the count is absent on the
  first paint and then appears. The consent notice set the precedent that the first paint is
  the server's job.
- **Marking read on GET** was rejected outright: it makes an inbox that clears itself when
  scanned, which is the same as not having one.
