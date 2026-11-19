// Concord kanban board — resolves the slug, then hydrates columns + cards.
// Rendering (step 2b): el() trees only; hostile titles and column names are text
// or setAttribute values.
(function () {
  'use strict';
  var el = window.ConcordDom.el;

  // The board filter: ONE board, narrowed. A board per milestone would multiply
  // the WIP limits by the number of milestones.
  function boardQuery() {
    var ms = new URLSearchParams(window.location.search).get('milestone');
    return ms ? '?milestone=' + encodeURIComponent(ms) : '';
  }

  function emptyState(icon, title, description) {
    return el('div', { class: 'empty-state' }, [
      icon ? el('div', { class: 'empty-state-icon' }, icon) : null,
      el('p', { class: 'empty-state-title' }, title),
      description ? el('p', { class: 'empty-state-description' }, description) : null
    ]);
  }

  function paintFilter(box, milestones) {
    if (!milestones.length) return;
    var page = box.parentNode;
    if (!page || page.querySelector('[data-board-filter]')) return;
    var cur = new URLSearchParams(window.location.search).get('milestone') || '';
    var opts = [el('option', { value: '' }, 'Whole board')].concat(milestones.map(function (m) {
      return el('option', { value: m.id, selected: String(m.id) === cur ? true : null },
        m.title + ' (' + (m.open || 0) + ' open)');
    }));
    var select = el('select', { id: 'board-milestone' }, opts);
    var wrap = el('div', { class: 'board-filter', 'data-board-filter': '' }, [
      el('label', { class: 'form-label', for: 'board-milestone' }, 'Milestone'),
      select
    ]);
    page.insertBefore(wrap, box);
    select.addEventListener('change', function (ev) {
      var url = new URL(window.location.href);
      if (ev.target.value) url.searchParams.set('milestone', ev.target.value);
      else url.searchParams.delete('milestone');
      window.location.href = url.toString();
    });
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('kanban-board');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) {
      box.replaceChildren(emptyState(null, 'No project specified', null));
      return;
    }
    hydrate(slug, box);
  });

  // hydrate fetches the project, its board and its features, then paints. A NAMED
  // function rather than an inline chain, because a card move needs to re-read the
  // board: the move response says nothing about a phase gate or WIP refusal.
  // RETURNS A PROMISE so the caller can announce after the repaint.
  function hydrate(slug, box, silent) {
    box.setAttribute('aria-busy', 'true');
    if (!silent) {
      box.replaceChildren(window.ConcordSkeleton ? loadingBoard()
        : el('div', { class: 'loading-spinner', role: 'status', 'aria-label': 'Loading board' }));
    }

    var api = window.ConcordApi;
    return api.get('/api/v1/projects/' + encodeURIComponent(slug))
      .catch(function (error) {
        if (error && error.status === 404) throw { notFound: true };
        throw { status: error && error.status };
      })
      .then(function (p) {
        return Promise.all([
          api.get('/api/v1/projects/' + encodeURIComponent(slug) + '/board' + boardQuery())
            .catch(function () { return { columns: [], cards: [] }; }),
          api.get('/api/v1/projects/' + encodeURIComponent(slug) + '/features')
            .then(window.ConcordApi.list)
            .catch(function () { return []; }),
          api.get('/api/v1/projects/' + encodeURIComponent(slug) + '/milestones')
            .then(window.ConcordApi.list)
            .catch(function () { return []; })
        ]).then(function (res) { return { project: p, board: res[0], features: res[1], milestones: res[2] }; });
      })
      .then(function (ctx) {
        paintFilter(box, ctx.milestones || []);
        var columns = (ctx.board && ctx.board.columns) || [];
        var cards = (ctx.board && ctx.board.cards) || [];
        var titles = {};
        (ctx.features || []).forEach(function (f) { titles[f.id] = f.title; });

        if (!columns.length) {
          box.replaceChildren(emptyState('📅', 'No board yet',
            'This project has not set up its phase-gated kanban columns.'));
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
          return;
        }
        if (!cards.length) {
          var hasWork = (ctx.features || []).length > 0;
          box.replaceChildren(emptyState('📅', 'Nothing on the board yet', [
            hasWork
              ? 'None of this project’s ' + ctx.features.length +
                ' features have a workflow status that matches a column yet.'
              : 'This project has no features yet, so there is nothing to place.',
            el('p', { class: 'empty-state-description' },
              'A card appears here once a feature carries a status like draft, in_progress or shipped.')
          ]));
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
          return;
        }

        var derivedCount = cards.filter(function (c) { return c.derived; }).length;
        var nodes = columns.map(function (col) {
          var inCol = cards.filter(function (c) { return c.column === col.name; });
          // A derived card is sitting in its status column, not placed by a person.
          var atLimit = col.wip && inCol.length >= col.wip;
          return el('div', {
            class: 'kanban-column' + (atLimit ? ' is-at-wip' : ''), 'data-column': col.name
          }, [
            el('div', { class: 'kanban-column-header' }, [
              el('span', { class: 'kanban-column-title' }, col.name),
              el('span', { class: 'kanban-column-count' },
                inCol.length + (col.wip ? ' / WIP ' + col.wip : ''))
            ]),
            inCol.map(function (c) {
              // The server sends the title for derived cards; the fetched feature
              // list covers placed ones.
              var title = c.title || titles[c.feature_id] ||
                (c.feature_id ? 'Feature #' + c.feature_id : 'Unknown item');
              var moveID = 'mv-' + c.id + '-' + col.name;
              // A card is a BUTTON, not a div: a draggable div is unusable by
              // keyboard and by screen reader.
              return el('div', { class: 'kanban-card-wrap' }, [
                el('button', {
                  type: 'button', class: 'kanban-card', draggable: 'true',
                  'data-card-id': c.id, 'data-feature-id': c.feature_id,
                  'data-column': c.column,
                  'aria-label': title + ', currently in ' + col.name
                }, title),
                el('label', { class: 'visually-hidden', for: moveID },
                  'Move ' + title + ' out of ' + col.name),
                el('select', {
                  class: 'kanban-card-move', id: moveID, 'data-card-id': c.id,
                  'aria-label': 'Move ' + title
                }, [
                  el('option', { value: '' }, 'Move to…')
                ].concat(columns.filter(function (o) { return o.name !== col.name; })
                  .map(function (o) {
                    return el('option', { value: o.name }, o.name + (o.wip ? ' (' + o.wip + ')' : ''));
                  })))
              ]);
            })
          ]);
        });
        var children = [el('div', { class: 'kanban-board' }, nodes)];
        if (derivedCount) {
          children.push(el('p', { class: 'kanban-note' },
            derivedCount + ' of ' + cards.length +
            ' cards sit here because of the feature’s own status, not because ' +
            'anyone placed them on the board.'));
        }
        // aria-busy is cleared BEFORE the DOM swap: a busy region suppresses its
        // own live-region updates, so announce() after a move would be silent.
        box.removeAttribute('aria-busy');
        box.replaceChildren.apply(box, children);
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        wireMoves(slug, box, columns);
      })
      .catch(function (err) {
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        box.replaceChildren(err && err.notFound
          ? emptyState('🔎', 'Project not found', [
              'No project with slug “' + slug + '” exists.',
              el('a', { class: 'btn btn-primary', href: '/projects' }, 'Browse projects')
            ])
          : emptyState('⚠️', 'Could not load board', 'The API did not respond. Try again.'));
      });
  }

  // A local skeleton with the same classes as skeleton.js's board(), but built
  // with el() so the loading state needs no HTML string.
  function loadingBoard() {
    return el('div', { class: 'grid-responsive-2' }, [0, 1, 2, 3].map(function () {
      return el('div', { class: 'card' }, [
        el('div', { class: 'skeleton skeleton-title', style: 'width:40%' }),
        el('div', { class: 'skeleton skeleton-text' }),
        el('div', { class: 'skeleton skeleton-text' })
      ]);
    }));
  }

  // announce reports a move's outcome in a live region, never window.alert.
  function announce(box, msg, kind) {
    var live = box.querySelector('[data-board-live]');
    if (!live) {
      live = el('p', {
        class: kind === 'error' ? 'notice notice-error' : 'visually-hidden',
        role: kind === 'error' ? 'alert' : 'status',
        'aria-live': kind === 'error' ? 'assertive' : 'polite',
        'data-board-live': ''
      });
      box.insertBefore(live, box.firstChild);
    }
    live.textContent = msg;
  }

  function moveCard(slug, cardID, to) {
    return window.ConcordApi
      .put('/api/v1/projects/' + encodeURIComponent(slug) + '/board/cards/' +
        encodeURIComponent(cardID) + '/move?to=' + encodeURIComponent(to), {})
      .then(function (body) { return { ok: true, body: body || {} }; })
      .catch(function (error) { return { ok: false, body: { error: error && error.message } }; });
  }

  function commit(slug, box, cardID, to, sourceCard) {
    moveCard(slug, cardID, to).then(function (res) {
      if (!res.ok) {
        announce(box, (res.body && res.body.error) ? res.body.error
          : 'That move was refused.', 'error');
        if (sourceCard) sourceCard.focus();
        return;
      }
      hydrate(slug, box, true).then(function () {
        announce(box, 'Moved to ' + to + '.');
      });
    }).catch(function () {
      announce(box, 'Could not reach the server.', 'error');
    });
  }

  function wireMoves(slug, box, columns) {
    // THE ACCESSIBLE PATH: a select on every card, same request as the drag.
    box.querySelectorAll('.kanban-card-move').forEach(function (sel) {
      sel.addEventListener('change', function () {
        var to = sel.value;
        if (!to) return;
        sel.value = '';
        commit(slug, box, sel.getAttribute('data-card-id'), to);
      });
    });

    var dragging = null;
    box.querySelectorAll('.kanban-card').forEach(function (card) {
      card.addEventListener('dragstart', function (ev) {
        dragging = card;
        card.classList.add('is-dragging');
        ev.dataTransfer.setData('text/plain', card.getAttribute('data-card-id'));
        ev.dataTransfer.effectAllowed = 'move';
      });
      card.addEventListener('dragend', function () {
        card.classList.remove('is-dragging');
        dragging = null;
      });
      card.addEventListener('keydown', function (ev) {
        var names = columns.map(function (c) { return c.name; });
        var i = names.indexOf(card.getAttribute('data-column'));
        if (i < 0) return;
        var j = null;
        if (ev.key === 'ArrowRight' || ev.key === 'ArrowDown') j = i + 1;
        if (ev.key === 'ArrowLeft' || ev.key === 'ArrowUp') j = i - 1;
        if (j === null || j < 0 || j >= names.length) return;
        ev.preventDefault();
        commit(slug, box, card.getAttribute('data-card-id'), names[j], card);
      });
    });

    box.querySelectorAll('.kanban-column').forEach(function (col) {
      // dragover MUST preventDefault or the drop never fires.
      col.addEventListener('dragover', function (ev) {
        ev.preventDefault();
        ev.dataTransfer.dropEffect = 'move';
        col.classList.add('is-drop-target');
      });
      col.addEventListener('dragleave', function () {
        col.classList.remove('is-drop-target');
      });
      col.addEventListener('drop', function (ev) {
        ev.preventDefault();
        col.classList.remove('is-drop-target');
        var id = ev.dataTransfer.getData('text/plain');
        if (!id && dragging) id = dragging.getAttribute('data-card-id');
        if (!id) return;
        commit(slug, box, id, col.getAttribute('data-column'));
      });
    });
  }
})();
