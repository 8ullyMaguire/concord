/* apiAuth: the session's Authorization header, for API reads.
 *
 * Every read on this page used to go out anonymous -- harmless while the project is
 * public, and fatal for the projects whose settings most need changing: a member
 * reaches a protected project (the navigation cookie opened the page) and then every
 * panel 404s, because the page's own reads carried no credential. session.js has
 * exported authHeaders() all along; this is the two lines that use it, and for a
 * signed-out visitor the request is identical to before. */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}
// Concord audit log viewer — one page over ListAudit.
//
// Three things this deliberately does not do:
//
// 1. Never write `detail` or an actor name into innerHTML unescaped. Both are
//    free text from twenty-odd AddAudit call sites, and `detail` in particular
//    is whatever string a handler decided to pass. esc() is the house idiom
//    (ranking.js) and this file uses it on every interpolation.
// 2. Never hide the total. "Showing 50 of 4,000" is what tells a reader their
//    filter excluded 3,950 rows rather than matched them, and a viewer that
//    omits it cannot be told apart from one whose filter is broken.
// 3. Never clear aria-busy on only the happy path. It is cleared in the error
//    branch too, because a skeleton that spins forever over an error message
//    reads as "still loading".
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  var root = document.getElementById('audit-root');
  var slug = slugFromPath();

  // The filter state lives here rather than in the URL. It is deliberately not
  // pushed into the query string: nothing on this site makes a filter shareable,
  // and a filter in the URL is a second source of truth to keep in step with
  // the controls.
  // `kind` is a NAMED family of actions ("moderation"), resolved by the SERVER. The page never
  // translates a kind into action names: that would be a second copy of the act vocabulary, and
  // it would drift the first time an act is added to the spec.
  var state = { kind: '', action: '', q: '' };

  function whenUnix(seconds) {
    if (seconds == null || isNaN(seconds)) return '—';
    return new Date(seconds * 1000).toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
  }

  // The actor column renders a deleted account as a dash, never as a number the
  // reader could go looking for. An id is not a person, and "user 41" on a
  // deleted row invites a reader to complain about someone who cannot be
  // complained about.
  function actor(e) {
    if (e.actor_display_name) return esc(e.actor_display_name);
    if (e.actor_id == null) return '<span class="dim">system</span>';
    return '<span class="dim">deleted account</span>';
  }

  // An act's `detail` is JSON — `{"reason": "...", "prev": {...}}` — and a viewer that prints it
  // raw shows a reader punctuation. The reason is the sentence a person wrote; the rest is the
  // numbers the reversal needs and belongs in the log, not on the screen.
  function detailText(e) {
    var d = e.detail || '';
    if (!d) return '';
    try {
      var parsed = JSON.parse(d);
      if (parsed && typeof parsed === 'object' && typeof parsed.reason === 'string') {
        return parsed.reason;
      }
    } catch (err) { /* a plain-text detail, which most writers produce */ }
    return d;
  }

  // Both directions of a reversal in one place: an act that was reversed, and the act that did
  // it. A log showing only one of them makes the reader guess which removal is still in force.
  function marks(e) {
    var out = '';
    if (e.undone) out += '<span class="audit-mark">undone</span>';
    if (e.undoes) out += '<span class="audit-mark">reversal of #' + esc(String(e.undoes)) + '</span>';
    return out;
  }

  function undoControl(e) {
    if (!e.can_undo) return '';
    var id = esc(String(e.id));
    return '<div class="audit-undo" data-undo="' + id + '">' +
      '<button type="button" class="btn btn-secondary" data-undo-open="' + id + '">Undo</button>' +
      '<div class="audit-undo-form" hidden>' +
        '<input type="text" data-undo-reason="' + id + '" ' +
          'placeholder="Why? The log records this." ' +
          'aria-label="Reason for reversing this removal">' +
        '<button type="button" class="btn btn-secondary" data-undo-confirm="' + id + '">Reverse it</button>' +
        '<button type="button" class="btn btn-secondary" data-undo-cancel="' + id + '">Cancel</button>' +
      '</div></div>';
  }

  function row(e) {
    var why = detailText(e);
    return '<tr>' +
      '<td class="cell-num"><span class="dim">' + esc(whenUnix(e.created_at)) + '</span></td>' +
      '<td class="cell-title"><code>' + esc(e.action) + '</code>' + marks(e) +
        (why ? '<p class="cell-note">' + esc(why) + '</p>' : '') + undoControl(e) + '</td>' +
      '<td>' + actor(e) + '</td>' +
      '<td class="cell-num"><span class="dim">' + esc(e.entity || '—') + '</span></td>' +
      '</tr>';
  }

  function controls(actions) {
    // One option, because one family exists today. A kind the server does not know answers 400
    // with the sentence naming the kinds, so this list is a convenience, not the contract.
    var kinds = '<option value="">All entries</option>' +
      '<option value="moderation"' + (state.kind === 'moderation' ? ' selected' : '') +
      '>Moderation</option>';
    var opts = '<option value="">All actions</option>';
    (actions || []).forEach(function (a) {
      opts += '<option value="' + esc(a) + '"' + (a === state.action ? ' selected' : '') + '>' +
        esc(a) + '</option>';
    });
    return '<div class="audit-controls">' +
      '<label for="audit-kind">Kind</label>' +
      '<select id="audit-kind">' + kinds + '</select>' +
      '<label for="audit-action">Action</label>' +
      '<select id="audit-action">' + opts + '</select>' +
      '<label for="audit-q">Search</label>' +
      '<input id="audit-q" type="search" placeholder="detail or action" value="' + esc(state.q) + '">' +
      '<button class="btn btn-primary" id="audit-apply">Apply</button>' +
      '</div>';
  }

  // The count line has to compare the SHOWN rows against the UNFILTERED total,
  // not against the filtered total the API returns.
  //
  // `page.total` is COUNT(*) over the FILTERED set (spec §4), so when a filter is
  // applied it equals the number of rows on screen and the line reads
  // "Showing all 5 entries" for a list of 1 -- telling the reader their filter
  // excluded nothing when it excluded four. The unfiltered total is fetched
  // alongside it, and only when a filter is active, so the common case costs one
  // request and the misleading one is impossible.
  function countLine(page, unfilteredTotal, filtered) {
    var shown = (page.entries || []).length;
    var of = filtered ? (unfilteredTotal != null ? unfilteredTotal : page.total) : page.total;
    var what = shown === 1 ? 'entry' : 'entries';
    if (of === shown) {
      return 'Showing all <strong>' + of + '</strong> ' + what + '.';
    }
    return 'Showing <strong>' + shown + '</strong> of <strong>' + of +
      '</strong> ' + what + ' \\u2014 the filter excluded ' + (of - shown) + '.';
  }

  function emptyState(page, actions, unfilteredTotal, filtered) {
    var found = page.total != null ? page.total : 0;
    return controls(actions) +
      '<div class="empty-state">' +
      '<div class="empty-state-icon">' + (filtered ? '\u{1F50D}' : '\u{1F4CB}') + '</div>' +
      '<p class="empty-state-title">' +
        (filtered ? 'Nothing matches this filter' : 'No audit activity yet') + '</p>' +
      '<p class="empty-state-description">' +
        (filtered
          ? 'The project has ' + (unfilteredTotal != null ? unfilteredTotal : found) +
            ' entr' + ((unfilteredTotal != null ? unfilteredTotal : found) === 1 ? 'y' : 'ies') +
            ', and none of them match what you asked for.'
          : 'Privileged actions are recorded here as they happen \\u2014 visibility changes, ' +
            'strategy weight, invitations, merges. Nothing has happened yet.') +
      '</p></div>';
  }

  function render(page, actions, unfilteredTotal) {
    var filtered = state.kind !== '' || state.action !== '' || state.q !== '';
    if (!page || !page.entries || page.entries.length === 0) {
      root.innerHTML = emptyState(page || { total: 0 }, actions, unfilteredTotal, filtered);
      return;
    }
    root.innerHTML = controls(actions) +
      '<p class="note" data-audit-note role="status" aria-live="polite"></p>' +
      '<p class="audit-count" data-audit-count>' + countLine(page, unfilteredTotal, filtered) + '</p>' +
      '<div class="table-scroll" tabindex="0" role="region" aria-label="tabla"><table class="ranking-table">' +
        '<thead><tr><th>When</th><th>Action</th><th>Who</th><th>Entity</th></tr></thead>' +
        '<tbody>' + page.entries.map(row).join('') + '</tbody>' +
      '</table></div>';
  }

  function say(msg) {
    var el = root.querySelector('[data-audit-note]');
    if (el) el.textContent = msg || '';
  }

  // reverse posts the restore act for one audit id. It asks for a reason because the route
  // refuses an empty one, and it reports the refusal where the reader is looking.
  function reverse(id, reason, btn) {
    if (!reason) {
      say('A reversal needs a reason: the log keeps it.');
      return;
    }
    if (btn) btn.disabled = true;
    say('Reversing\u2026');
    fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/moderation/arena/restore', {
      method: 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' }),
      body: JSON.stringify({ undoes: Number(id), reason: reason, confirm: true })
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        if (!r.ok) {
          say((data && data.error) || 'Could not reverse that.');
          if (btn) btn.disabled = false;
          return;
        }
        load('Reversed. The restore act is in this log with that reason on it.');
      });
    }).catch(function () {
      say('Could not reach the server.');
      if (btn) btn.disabled = false;
    });
  }

  function query() {
    var params = [];
    if (state.kind) params.push('kind=' + encodeURIComponent(state.kind));
    if (state.action) params.push('action=' + encodeURIComponent(state.action));
    // `q`, matching the API. Sending `text` here would return every row and
    // look exactly like a filter that matched too much.
    if (state.q) params.push('q=' + encodeURIComponent(state.q));
    return params.length ? '?' + params.join('&') : '';
  }

  function errorState(message) {
    return '<div class="empty-state error-state">' +
      '<div class="empty-state-icon">⚠</div>' +
      '<p class="empty-state-title">Could not load the audit log</p>' +
      '<p class="empty-state-description">' + esc(message) + '</p>' +
      '<button class="btn btn-primary" id="audit-retry">Try again</button></div>';
  }

  function load(note) {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);
    var filtered = state.kind !== '' || state.action !== '' || state.q !== '';
    var fetches = [
      fetch(base + '/audit' + query(), { headers: apiAuth() }).then(function (r) {
        if (!r.ok) throw new Error('audit: HTTP ' + r.status);
        return r.json();
      }),
      // The action list is supplementary: a log that cannot be filtered by
      // action is less usable, but its absence must not blank the page.
      fetch(base + '/audit/actions', { headers: apiAuth() }).then(function (r) { return r.ok ? r.json() : []; })
    ];
    if (filtered) {
      // Only when a filter is active, and only to make the count line honest.
      // Fetching this on every load would be a request whose result is never
      // read, and a second failure would have to be handled for no benefit.
      fetches.push(fetch(base + '/audit?limit=1', { headers: apiAuth() }).then(function (r) {
        return r.ok ? r.json().then(function (p) { return p.total; }) : null;
      }));
    }
    Promise.all(fetches).then(function (res) {
      render(res[0], res[1], res[2]);
      // The note is set AFTER the render, because render() replaces the node it lives in. A
      // message written before the reload is a message the reload deletes -- which is how the
      // "Reversed." confirmation came out as an empty line on the first try.
      if (note) say(note);
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    }).catch(function (err) {
      root.innerHTML = errorState(err && err.message ? err.message : String(err));
      // Cleared on the error path too. A skeleton left spinning over an error
      // message reads as "still loading", which is a different bug report from
      // the one that is true.
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    });
  }

  if (root) {
    // AFTER the document, not at parse time. base.html loads session.js at the end of <body>,
    // so a read started here goes out without a token -- and an audit read that arrives
    // anonymous cannot be told apart from one by a reader with no standing: `can_undo` comes
    // back false, so a moderator sees a log with no Undo on it and no explanation. The same
    // timing bug was found and fixed on the curation page; this file had it too.
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', function () { load(); });
    } else {
      load();
    }
    document.addEventListener('click', function (e) {
      if (!e.target) return;
      if (e.target.id === 'audit-retry') { load(); return; }
      if (e.target.id === 'audit-apply') {
        var kind = document.getElementById('audit-kind');
        var sel = document.getElementById('audit-action');
        var box = document.getElementById('audit-q');
        state.kind = kind ? kind.value : '';
        state.action = sel ? sel.value : '';
        state.q = box ? box.value.trim() : '';
        load();
        return;
      }

      // The Undo control, delegated rather than bound per button: the table is re-rendered from
      // HTML on every load, so a listener attached to a button dies with the row it was on.
      var open = e.target.getAttribute && e.target.getAttribute('data-undo-open');
      if (open) {
        var box2 = root.querySelector('[data-undo="' + open + '"] .audit-undo-form');
        if (box2) {
          box2.hidden = false;
          var field = root.querySelector('[data-undo-reason="' + open + '"]');
          if (field) field.focus();
        }
        return;
      }
      var cancel = e.target.getAttribute && e.target.getAttribute('data-undo-cancel');
      if (cancel) {
        var box3 = root.querySelector('[data-undo="' + cancel + '"] .audit-undo-form');
        if (box3) box3.hidden = true;
        return;
      }
      var confirm = e.target.getAttribute && e.target.getAttribute('data-undo-confirm');
      if (confirm) {
        var field2 = root.querySelector('[data-undo-reason="' + confirm + '"]');
        reverse(confirm, field2 ? field2.value.trim() : '', e.target);
      }
    });
    // Enter in the search box applies, because a reader who has typed a filter
    // and pressed Enter did not mean to do nothing.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && e.target && e.target.id === 'audit-q') {
        state.q = e.target.value.trim();
        load();
      }
    });
  }
})();