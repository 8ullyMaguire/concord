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
  var state = { action: '', q: '' };

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

  function row(e) {
    return '<tr>' +
      '<td class="cell-num"><span class="dim">' + esc(whenUnix(e.created_at)) + '</span></td>' +
      '<td class="cell-title"><code>' + esc(e.action) + '</code>' +
        (e.detail ? '<p class="cell-note">' + esc(e.detail) + '</p>' : '') + '</td>' +
      '<td>' + actor(e) + '</td>' +
      '<td class="cell-num"><span class="dim">' + esc(e.entity || '—') + '</span></td>' +
      '</tr>';
  }

  function controls(actions) {
    var opts = '<option value="">All actions</option>';
    (actions || []).forEach(function (a) {
      opts += '<option value="' + esc(a) + '"' + (a === state.action ? ' selected' : '') + '>' +
        esc(a) + '</option>';
    });
    return '<div class="audit-controls">' +
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
    var filtered = state.action !== '' || state.q !== '';
    if (!page || !page.entries || page.entries.length === 0) {
      root.innerHTML = emptyState(page || { total: 0 }, actions, unfilteredTotal, filtered);
      return;
    }
    root.innerHTML = controls(actions) +
      '<p class="audit-count" data-audit-count>' + countLine(page, unfilteredTotal, filtered) + '</p>' +
      '<div class="table-scroll"><table class="ranking-table">' +
        '<thead><tr><th>When</th><th>Action</th><th>Who</th><th>Entity</th></tr></thead>' +
        '<tbody>' + page.entries.map(row).join('') + '</tbody>' +
      '</table></div>';
  }

  function query() {
    var params = [];
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

  function load() {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);
    var filtered = state.action !== '' || state.q !== '';
    var fetches = [
      fetch(base + '/audit' + query()).then(function (r) {
        if (!r.ok) throw new Error('audit: HTTP ' + r.status);
        return r.json();
      }),
      // The action list is supplementary: a log that cannot be filtered by
      // action is less usable, but its absence must not blank the page.
      fetch(base + '/audit/actions').then(function (r) { return r.ok ? r.json() : []; })
    ];
    if (filtered) {
      // Only when a filter is active, and only to make the count line honest.
      // Fetching this on every load would be a request whose result is never
      // read, and a second failure would have to be handled for no benefit.
      fetches.push(fetch(base + '/audit?limit=1').then(function (r) {
        return r.ok ? r.json().then(function (p) { return p.total; }) : null;
      }));
    }
    Promise.all(fetches).then(function (res) {
      render(res[0], res[1], res[2]);
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
    load();
    document.addEventListener('click', function (e) {
      if (!e.target) return;
      if (e.target.id === 'audit-retry') { load(); return; }
      if (e.target.id === 'audit-apply') {
        var sel = document.getElementById('audit-action');
        var box = document.getElementById('audit-q');
        state.action = sel ? sel.value : '';
        state.q = box ? box.value.trim() : '';
        load();
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