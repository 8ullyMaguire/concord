/* apiAuth: the session's Authorization header.
 *
 * Inlined at every call site rather than wrapped one level away, because
 * assets_auth_reads_test.go reads the CALL SITES: a helper that hides the header shipped a
 * member-only read as anonymous, and the audit could not see it. Two lines here, twice, is
 * the price of a gate that can see the credential.
 */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}

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

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

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
    fetch('/api/v1/notifications/' + encodeURIComponent(id) + '/read', {
      method: 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' })
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return load();
    }).catch(function (e) {
      btn.disabled = false;
      btn.textContent = 'Could not mark read (' + e.message + ')';
    });
  }

  function markAll(btn) {
    btn.disabled = true;
    fetch('/api/v1/notifications/read-all', {
      method: 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' })
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return load();
    }).catch(function (e) {
      btn.disabled = false;
      btn.textContent = 'Could not mark read (' + e.message + ')';
    });
  }

  function load() {
    root.setAttribute('aria-busy', 'true');
    return fetch('/api/v1/notifications', { headers: apiAuth() })
      .then(function (r) {
        if (r.status === 401) throw new Error('sign in to see your notifications');
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      })
      .then(render)
      .catch(function (e) {
        root.setAttribute('aria-busy', 'false');
        root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
          esc(e.message) + '</p></div>';
      });
  }

  // WAIT FOR THE SESSION SCRIPT. base.html loads session.js at the END of <body>, after the
  // page's own content, so a script here that calls window.ConcordSession at parse time finds
  // it undefined -- apiAuth() then returns {} and every read answers 401 "sign in to see your
  // notifications" for a reader who IS signed in. Proved by the browser suite: the server
  // rendered the signed-in page and the page still asked the reader to sign in.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', load);
  } else {
    load();
  }
})();
