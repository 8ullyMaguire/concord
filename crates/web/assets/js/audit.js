
// Concord audit log viewer — one page over ListAudit.
//
// Three things this deliberately does not do:
//
// 1. Never write `detail` or an actor name into HTML markup. Both are free text from twenty-odd
//    AddAudit call sites; the renderer builds text nodes and attributes only.
// 2. Never hide the total. "Showing 50 of 4,000" tells a reader their filter excluded 3,950 rows
//    rather than matched them.
// 3. Never clear aria-busy on only the happy path. It is cleared in the error branch too.
//
// Rendering (step 2b): every HTML builder in this file was replaced by an `el()` tree.

(function () {
  'use strict';

  var el = window.ConcordDom.el;

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  var root = document.getElementById('audit-root');
  var slug = slugFromPath();

  // The filter state lives here rather than in the URL. `kind` is a NAMED family of actions
  // ("moderation"), resolved by the SERVER; the page never translates it into action names.
  var state = { kind: '', action: '', q: '' };

  function whenUnix(seconds) {
    if (seconds == null || isNaN(seconds)) return '—';
    return new Date(seconds * 1000).toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
  }

  // A deleted account renders as a dash, never as a number the reader could go looking for.
  function actor(e) {
    if (e.actor_display_name) return e.actor_display_name;
    return el('span', { class: 'dim' }, e.actor_id == null ? 'system' : 'deleted account');
  }

  // An act's `detail` is JSON. The reason is the sentence a person wrote; the rest is the
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

  // Both directions of a reversal in one place: an act that was reversed, and the act
  // that did it.
  function marks(e) {
    var nodes = [];
    if (e.undone) nodes.push(el('span', { class: 'audit-mark' }, 'undone'));
    if (e.undoes) nodes.push(el('span', { class: 'audit-mark' }, 'reversal of #' + e.undoes));
    return nodes;
  }

  function undoControl(e) {
    if (!e.can_undo) return null;
    var id = String(e.id);
    return el('div', { class: 'audit-undo', 'data-undo': id }, [
      el('button', { type: 'button', class: 'btn btn-secondary', 'data-undo-open': id }, 'Undo'),
      el('div', { class: 'audit-undo-form', hidden: true }, [
        el('input', {
          type: 'text', 'data-undo-reason': id,
          placeholder: 'Why? The log records this.',
          'aria-label': 'Reason for reversing this removal'
        }),
        el('button', { type: 'button', class: 'btn btn-secondary', 'data-undo-confirm': id }, 'Reverse it'),
        el('button', { type: 'button', class: 'btn btn-secondary', 'data-undo-cancel': id }, 'Cancel')
      ])
    ]);
  }

  function row(e) {
    var why = detailText(e);
    return el('tr', {}, [
      el('td', { class: 'cell-num' }, el('span', { class: 'dim' }, whenUnix(e.created_at))),
      el('td', { class: 'cell-title' }, [
        el('code', {}, e.action)
      ].concat(marks(e), [
        why ? el('p', { class: 'cell-note' }, why) : null,
        undoControl(e)
      ])),
      el('td', {}, actor(e)),
      el('td', { class: 'cell-num' }, el('span', { class: 'dim' }, e.entity || '—'))
    ]);
  }

  function controlsNode(actions) {
    // One option, because one family exists today. A kind the server does not know answers 400.
    return el('div', { class: 'audit-controls' }, [
      el('label', { for: 'audit-kind' }, 'Kind'),
      el('select', { id: 'audit-kind' }, [
        el('option', { value: '' }, 'All entries'),
        el('option', { value: 'moderation', selected: state.kind === 'moderation' }, 'Moderation')
      ]),
      el('label', { for: 'audit-action' }, 'Action'),
      el('select', { id: 'audit-action' }, [el('option', { value: '' }, 'All actions')]
        .concat((actions || []).map(function (a) {
          return el('option', { value: a, selected: a === state.action }, a);
        }))),
      el('label', { for: 'audit-q' }, 'Search'),
      el('input', { id: 'audit-q', type: 'search', placeholder: 'detail or action', value: state.q }),
      el('button', { class: 'btn btn-primary', id: 'audit-apply' }, 'Apply')
    ]);
  }

  // The count line compares SHOWN rows against the UNFILTERED total. page.total is
  // COUNT(*) over the FILTERED set, so the unfiltered total is fetched only when a
  // filter is active.
  function countNodes(page, unfilteredTotal, filtered) {
    var shown = (page.entries || []).length;
    var of = filtered ? (unfilteredTotal != null ? unfilteredTotal : page.total) : page.total;
    var what = shown === 1 ? 'entry' : 'entries';
    if (of === shown) {
      return ['Showing all ', el('strong', {}, of), ' ', what + '.'];
    }
    return [
      'Showing ', el('strong', {}, shown), ' of ', el('strong', {}, of), ' ', what,
      ' — the filter excluded ' + (of - shown) + '.'
    ];
  }

  function emptyNodes(page, actions, unfilteredTotal, filtered) {
    var found = page.total != null ? page.total : 0;
    var total = unfilteredTotal != null ? unfilteredTotal : found;
    return [
      controlsNode(actions),
      el('div', { class: 'empty-state' }, [
        el('div', { class: 'empty-state-icon' }, filtered ? '🔍' : '📋'),
        el('p', { class: 'empty-state-title' }, filtered ? 'Nothing matches this filter' : 'No audit activity yet'),
        el('p', { class: 'empty-state-description' }, filtered
          ? 'The project has ' + total + ' entr' + (total === 1 ? 'y' : 'ies') +
            ', and none of them match what you asked for.'
          : 'Privileged actions are recorded here as they happen — visibility changes, ' +
            'strategy weight, invitations, merges. Nothing has happened yet.')
      ])
    ];
  }

  function flatten(nodes) {
    return nodes.reduce(function (out, node) {
      return out.concat(Array.isArray(node) ? flatten(node) : [node]);
    }, []);
  }

  function render(page, actions, unfilteredTotal) {
    var filtered = state.kind !== '' || state.action !== '' || state.q !== '';
    if (!page || !page.entries || page.entries.length === 0) {
      root.replaceChildren.apply(root, flatten(emptyNodes(page || { total: 0 }, actions, unfilteredTotal, filtered)));
      return;
    }
    root.replaceChildren.apply(root, flatten([
      controlsNode(actions),
      el('p', { class: 'note', 'data-audit-note': '', role: 'status', 'aria-live': 'polite' }),
      el('p', { class: 'audit-count', 'data-audit-count': '' }, countNodes(page, unfilteredTotal, filtered)),
      el('div', { class: 'table-scroll', tabindex: '0', role: 'region', 'aria-label': 'tabla' },
        el('table', { class: 'ranking-table' }, [
          el('thead', {}, el('tr', {}, [
            el('th', {}, 'When'), el('th', {}, 'Action'), el('th', {}, 'Who'), el('th', {}, 'Entity')
          ])),
          el('tbody', {}, page.entries.map(row))
        ]))
    ]));
  }

  function say(msg) {
    var note = root.querySelector('[data-audit-note]');
    if (note) note.textContent = msg || '';
  }

  // reverse posts the restore act for one audit id. It asks for a reason because the route
  // refuses an empty one, and it reports the refusal where the reader is looking.
  function reverse(id, reason, btn) {
    if (!reason) {
      say('A reversal needs a reason: the log keeps it.');
      return;
    }
    if (btn) btn.disabled = true;
    say('Reversing…');
    window.ConcordApi
      .post('/api/v1/projects/' + encodeURIComponent(slug) + '/moderation/arena/restore',
        { undoes: Number(id), reason: reason, confirm: true })
      .then(function () {
        load('Reversed. The restore act is in this log with that reason on it.');
      })
      .catch(function (error) {
        say(error && error.message ? error.message : 'Could not reach the server.');
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

  function errorNode(message) {
    return el('div', { class: 'empty-state error-state' }, [
      el('div', { class: 'empty-state-icon' }, '⚠'),
      el('p', { class: 'empty-state-title' }, 'Could not load the audit log'),
      el('p', { class: 'empty-state-description' }, message),
      el('button', { class: 'btn btn-primary', id: 'audit-retry' }, 'Try again')
    ]);
  }

  function load(note) {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);
    var filtered = state.kind !== '' || state.action !== '' || state.q !== '';
    var fetches = [
      window.ConcordApi.get(base + '/audit' + query()),
      // The action list is supplementary: a log that cannot be filtered by
      // action is less usable, but its absence must not blank the page.
      window.ConcordApi.get(base + '/audit/actions').then(window.ConcordApi.list).catch(function () { return []; })
    ];
    if (filtered) {
      // Only when a filter is active, and only to make the count line honest.
      fetches.push(window.ConcordApi.get(base + '/audit?limit=1')
        .then(function (page) { return page && page.total !== undefined ? page.total : null; })
        .catch(function () { return null; }));
    }
    Promise.all(fetches).then(function (res) {
      // ConcordApi.list accepts the Step-6 {items,next} envelope, the legacy {entries} audit page and a bare array.
      render(res[0], res[1], res[2]);
      // The note is set AFTER the render, because render() replaces the node it lives in.
      if (note) say(note);
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    }).catch(function (err) {
      root.replaceChildren(errorNode(err && err.message ? err.message : String(err)));
      // Cleared on the error path too. A skeleton left spinning over an error
      // message reads as "still loading".
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    });
  }

  if (root) {
    // AFTER the document, not at parse time: a read started at parse time goes out
    // without a token and an anonymous audit read cannot be told apart from one by a
    // reader with no standing.
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

      // The Undo control is delegated: the table is re-rendered on every load, so a
      // listener attached to a button dies with the row it was on.
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
    // Enter in the search box applies, because a reader who typed a filter and
    // pressed Enter did not mean to do nothing.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && e.target && e.target.id === 'audit-q') {
        state.q = e.target.value.trim();
        load();
      }
    });
  }
})();
