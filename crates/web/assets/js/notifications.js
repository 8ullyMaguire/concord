// Concord notifications (docs/specs/notification-spec.md §4.4).
//
// Four rules, and each of them is a way this page goes wrong:
//
// 1. Nothing free-text is interpolated unescaped. `title` and `reason` are what a moderator
//    typed, and `actor` is a username: esc() on every one (the house idiom, ranking.js).
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

  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it, so this file cannot disagree with
  // another page about which characters are dangerous.
  var esc = window.ConcordEscape.esc;

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
      root.innerHTML = '<div class="empty-state">' +
        '<p class="empty-state-title">Nothing has been removed from your contributions.</p>' +
        '<p class="muted">When a moderator removes one of your ideas or rejects one of your ' +
        'complaints, the decision appears here with the reason they gave.</p></div>';
      return;
    }
    var out = ['<div class="notif-toolbar">'];
    if (data.unread > 0) {
      out.push('<p class="muted" data-unread>' + esc(data.unread) +
        ' unread</p><button type="button" class="btn" data-read-all>Mark all read</button>');
    }
    out.push('</div><ul class="notif-list">');
    items.forEach(function (n) {
      out.push('<li class="notif-card' + (n.read ? '' : ' notif-unread') + '" data-id="' + esc(n.id) + '">');
      out.push('<div class="notif-head"><span class="notif-kind">' + esc(kindLabel(n.kind)) + '</span>');
      if (!n.read) out.push('<span class="notif-dot" aria-label="unread">●</span>');
      out.push('<span class="notif-when">' + esc(when(n.created_at)) + '</span></div>');
      out.push('<p class="notif-title">' + esc(n.title) + '</p>');
      out.push('<p class="muted">' + esc(n.project_slug ? '/' + n.project_slug : 'project ' + n.project_id) +
        ' · decided by ' + esc(n.actor || 'a moderator') + '</p>');
      if (n.reason) out.push('<p class="notif-reason">“' + esc(n.reason) + '”</p>');
      if (!n.read) out.push('<button type="button" class="btn btn-small" data-read="' + esc(n.id) + '">Mark read</button>');
      out.push('</li>');
    });
    out.push('</ul>');
    root.innerHTML = out.join('');
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
        root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
          esc(e.message) + '</p></div>';
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
