// Concord notifications (docs/specs/notification-spec.md §4.4).
//
// Four rules, and each of them is a way this page goes wrong:
//
// 1. Nothing free-text becomes markup. `title`, `reason` and `actor` are moderator prose or usernames, and this page builds
//    them with `el()` (text nodes and setAttribute only), so there is no innerHTML sink and no esc() call to forget.
// 2. Loading the page marks NOTHING read. A page you scrolled past must not look like a page
//    you read, so "Mark read" is a control the reader presses.
// 3. The unread count on the page comes from the response, not from counting rendered cards:
//    the response measures all of the reader's rows, the cards are the newest page of them.
// 4. The empty state is a statement about the reader's record ("Nothing has been removed from
//    your contributions"), not about the feature ("No notifications"). Zero is rendered as
//    absence, never as a badge reading 0.
(function () {
  'use strict';

  var root = document.getElementById('notifications-root');
  if (!root) return;

  // dom.js is loaded with the other globals before any page script. It has no HTML-parsing API.
  var el = window.ConcordDom.el;

  function when(ts) {
    if (!ts) return '';
    var d = new Date(ts * 1000);
    return d.toISOString().slice(0, 16).replace('T', ' ');
  }

  function kindLabel(kind) {
    if (kind === 'feature_removed') return 'Idea removed';
    if (kind === 'complaint_rejected') return 'Complaint rejected';
    return kind;
  }

  function render(data) {
    var items = data.notifications || [];
    // 'false', not removeAttribute: the rest of the site publishes aria-busy="false" when a
    // panel is done (ranking.js, board.js), and a suite -- or a screen reader -- that waits for
    // =="false" never sees an absent attribute. Measured: the browser suite hung on exactly this.
    root.setAttribute('aria-busy', 'false');
    if (!items.length) {
      root.replaceChildren(el('div', { class: 'empty-state' }, [
        el('p', { class: 'empty-state-title' }, 'Nothing has been removed from your contributions.'),
        el('p', { class: 'muted' }, 'When a moderator removes one of your ideas or rejects one of your ' +
          'complaints, the decision appears here with the reason they gave.')
      ]));
      return;
    }

    var parts = [];
    if (data.unread > 0) {
      parts.push(el('div', { class: 'notif-toolbar' }, [
        el('p', { class: 'muted', 'data-unread': '' }, String(data.unread) + ' unread'),
        el('button', { type: 'button', class: 'btn', 'data-read-all': '' }, 'Mark all read')
      ]));
    }

    var list = el('ul', { class: 'notif-list' });
    items.forEach(function (n) {
      list.appendChild(el('li', {
        class: 'notif-card' + (n.read ? '' : ' notif-unread'),
        'data-id': n.id
      }, [
        el('div', { class: 'notif-head' }, [
          el('span', { class: 'notif-kind' }, kindLabel(n.kind)),
          n.read ? null : el('span', { class: 'notif-dot', 'aria-label': 'unread' }, '●'),
          el('span', { class: 'notif-when' }, when(n.created_at))
        ]),
        el('p', { class: 'notif-title' }, n.title),
        el('p', { class: 'muted' }, (n.project_slug ? '/' + n.project_slug : 'project ' + n.project_id) +
          ' · decided by ' + (n.actor || 'a moderator')),
        n.reason ? el('p', { class: 'notif-reason' }, '“' + n.reason + '”') : null,
        n.read ? null : el('button', { type: 'button', class: 'btn btn-small', 'data-read': n.id }, 'Mark read')
      ]));
    });
    parts.push(list);
    root.replaceChildren.apply(root, parts);
    wire();
  }

  function wire() {
    var all = root.querySelector('[data-read-all]');
    if (all) all.addEventListener('click', function () { markAll(all); });
    Array.prototype.forEach.call(root.querySelectorAll('[data-read]'), function (btn) {
      btn.addEventListener('click', function () { markOne(btn); });
    });
  }

  function markOne(btn) {
    var id = btn.getAttribute('data-read');
    if (!id) return;
    btn.disabled = true;
    window.ConcordApi.post('/api/v1/notifications/' + encodeURIComponent(id) + '/read', {})
      .then(function () { return load(); })
      .catch(function (e) {
        btn.disabled = false;
        btn.textContent = 'Could not mark read (' + (e && e.message ? e.message : 'no answer') + ')';
      });
  }

  function markAll(btn) {
    btn.disabled = true;
    window.ConcordApi.post('/api/v1/notifications/read-all', {})
      .then(function () { return load(); })
      .catch(function (e) {
        btn.disabled = false;
        btn.textContent = 'Could not mark read (' + (e && e.message ? e.message : 'no answer') + ')';
      });
  }

  function load() {
    root.setAttribute('aria-busy', 'true');
    // `ApiError` already says "Sign in first — this needs an account." for a 401; the sentence this page used to
    // substitute said the same thing in its own words, which is how a product ends up with four of them.
    return window.ConcordApi.get('/api/v1/notifications')
      .then(render)
      .catch(function (e) {
        root.setAttribute('aria-busy', 'false');
        root.replaceChildren(el('div', { class: 'empty-state' }, [
          el('p', { class: 'empty-state-title' }, e.message)
        ]));
      });
  }

  // WAIT FOR THE SESSION SCRIPT. The header is assembled when the request is made, not at parse time, but a request
  // fired before `session.js` has run still goes out anonymous — the signed-in page then telling a signed-in reader to
  // sign in. Proved by the browser suite once; the wait is the fix and it still applies with `api.js` doing the work.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', load);
  } else {
    load();
  }
})();
