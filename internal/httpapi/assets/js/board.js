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
// Concord kanban board — resolves the slug, then hydrates columns + cards.
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
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
      box.innerHTML = '<div class="empty-state"><p class="empty-state-title">No project specified</p></div>';
      return;
    }
    hydrate(slug, box);
  });

  // hydrate fetches the project, its board and its features, then paints.
  //
  // A NAMED function rather than an inline chain, because a card move needs to re-read
  // the board: the move response is {"status":"moved"} and says nothing about whether a
  // phase GATE or a WIP limit refused it, so the server's own order is the only
  // trustworthy one. Optimistically reordering a card would leave the board showing a
  // card in a column it was refused for.
  //
  // silent is set when re-reading after a move: the skeleton would replace the board a
  // reader is looking at, and the announce() live-region message would be wiped with it.
  // RETURNS A PROMISE, because the caller announces after the repaint. A fire-and-
  // forget re-read makes "announce then refresh" silently lose the message.
  function hydrate(slug, box, silent) {
    box.setAttribute('aria-busy', 'true');
    if (!silent) {
      box.innerHTML = (window.ConcordSkeleton ? window.ConcordSkeleton.board(4, 2)
        : '<div class="loading-spinner" role="status" aria-label="Loading board"></div>');
    }

    // The board API is keyed by numeric project id; resolve the slug first.
    //
    // RETURNED, so a caller can wait for the repaint. Without it the chain's result
    // is discarded, `.then(announce)` fires on a promise that has already resolved to
    // undefined, and the confirmation is wiped by the render it was supposed to follow.
    return fetch('/api/v1/projects/' + encodeURIComponent(slug), { headers: apiAuth() })
      .then(function (r) {
        if (r.status === 404) throw { notFound: true };
        return r.ok ? r.json() : Promise.reject({ status: r.status });
      })
      .then(function (p) {
        return Promise.all([
          // By slug, not p.id: the project-scoped routes resolve a slug.
          // Passing p.id 404s, and the fallbacks below turn that into an empty
          // board that looks like a project with nothing on it.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/board', { headers: apiAuth() }).then(function (r) { return r.ok ? r.json() : { columns: [], cards: [] }; }),
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/features', { headers: apiAuth() }).then(function (r) { return r.ok ? r.json() : []; })
        ]).then(function (res) { return { project: p, board: res[0], features: res[1] }; });
      })
      .then(function (ctx) {
        var columns = (ctx.board && ctx.board.columns) || [];
        var cards = (ctx.board && ctx.board.cards) || [];
        var titles = {};
        (ctx.features || []).forEach(function (f) { titles[f.id] = f.title; });

        if (!columns.length) {
          box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u{1F4C5}</div>' +
            '<p class="empty-state-title">No board yet</p>' +
            '<p class="empty-state-description">This project has not set up its phase-gated kanban columns.</p></div>';
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
          return;
        }

        // No cards at all, but the project has columns: say why, rather than
        // rendering nine zeroes that read as "broken" instead of "empty".
        if (!cards.length) {
          var hasWork = (ctx.features || []).length > 0;
          box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u{1F4C5}</div>' +
            '<p class="empty-state-title">Nothing on the board yet</p>' +
            '<p class="empty-state-description">' + (hasWork
              ? 'None of this project\u2019s ' + ctx.features.length +
                ' features have a workflow status that matches a column yet.'
              : 'This project has no features yet, so there is nothing to place.') +
            '</p><p class="empty-state-description">' +
            'A card appears here once a feature carries a status like ' +
            'draft, in_progress or shipped.</p></div>';
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
          return;
        }

        var derivedCount = cards.filter(function (c) { return c.derived; }).length;
        var html = '<div class="kanban-board">';
        columns.forEach(function (col) {
          var inCol = cards.filter(function (c) { return c.column === col.name; });
          // A derived card is sitting in its status column, not placed there by
          // a person. Over-colouring them would imply the board had been curated;
          // a caption below the board states the number instead.
          var atLimit = col.wip && inCol.length >= col.wip;
          html += '<div class="kanban-column' + (atLimit ? ' is-at-wip' : '') + '" ' +
            'data-column="' + esc(col.name) + '"><div class="kanban-column-header">' +
            '<span class="kanban-column-title">' + esc(col.name) + '</span>' +
            '<span class="kanban-column-count">' + inCol.length + (col.wip ? ' / WIP ' + col.wip : '') + '</span>' +
            '</div>';
          inCol.forEach(function (c) {
            // The server sends the title for derived cards; the fetched feature
            // list covers placed ones. Fall back rather than printing "Feature
            // #0" for a card that somehow has no feature id.
            var title = c.title || titles[c.feature_id] ||
              (c.feature_id ? 'Feature #' + c.feature_id : 'Unknown item');
            // A card is a BUTTON, not a div: a draggable div is unusable by
            // keyboard and by screen reader, and a kanban board nobody can operate
            // without a mouse is a broken board.
            //
            // draggable AND focusable, with the column move offered as a control
            // below -- drag is the fast path, the select is the accessible one, and
            // the two post the same request.
            html += '<div class="kanban-card-wrap">' +
              '<button type="button" class="kanban-card" draggable="true" ' +
              // data-card-id is board_cards.id, which is what
              // PUT .../cards/{card_id}/move moves by default. Sending the FEATURE
              // id in that path made every move a no-op -- MoveCard's first branch
              // rejects "both", and with no feature_id query param the default branch
              // ran with a feature id where a card id was expected, so it 404'd and
              // the board silently ignored the drag.
              'data-card-id="' + esc(c.id) + '" ' +
              'data-feature-id="' + esc(c.feature_id) + '" ' +
              'data-column="' + esc(c.column) + '" ' +
              'aria-label="' + esc(title) + ', currently in ' + esc(col.name) + '">' +
              esc(title) + '</button>' +
              '<label class="visually-hidden" for="mv-' + esc(c.id) + '-' +
              esc(col.name) + '">Move ' + esc(title) + ' out of ' + esc(col.name) +
              '</label>' +
              '<select class="kanban-card-move" id="mv-' + esc(c.id) + '-' +
              esc(col.name) + '" data-card-id="' + esc(c.id) + '" ' +
              'aria-label="Move ' + esc(title) + '">' +
              '<option value="">Move to…</option>' +
              columns.filter(function (o) { return o.name !== col.name; })
                .map(function (o) {
                  return '<option value="' + esc(o.name) + '">' + esc(o.name) +
                    (o.wip ? ' (' + o.wip + ')' : '') + '</option>';
                }).join('') +
              '</select></div>';
          });
          html += '</div>';
        });
        html += '</div>';
        if (derivedCount) {
          html += '<p class="kanban-note">' + derivedCount + ' of ' + cards.length +
            ' cards sit here because of the feature\u2019s own status, not because ' +
            'anyone placed them on the board.</p>';
        }
        // aria-busy is cleared BEFORE the DOM swap: a region left busy suppresses its
        // own live-region updates, so announce() after a move would be silent.
        box.removeAttribute('aria-busy');
        box.innerHTML = html;
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        wireMoves(slug, box, columns);
      })
      .catch(function (err) {
        // Cleared here too: the error and empty paths are the ones that most
        // often skip it, and a region left aria-busy suppresses its own live
        // region updates, so the error message would be silent.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        box.innerHTML = err && err.notFound
          ? '<div class="empty-state"><div class="empty-state-icon">\u{1F50E}</div>' +
            '<p class="empty-state-title">Project not found</p>' +
            '<p class="empty-state-description">No project with slug &ldquo;' + esc(slug) + '&rdquo; exists.</p>' +
            '<a class="btn btn-primary" href="/projects">Browse projects</a></div>'
          : '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
            '<p class="empty-state-title">Could not load board</p>' +
            '<p class="empty-state-description">The API did not respond. Try again.</p></div>';
      });
  }
// Card moving on the kanban board.
//
// THE MISSING WRITE PATH. handleMoveCard, MoveCard, PUT .../board/cards/{id}/move and
// the phase-gate and WIP-limit refusals all existed and were tested at the store layer.
// The board rendered columns and cards and could not be moved at all: a kanban board
// nobody can move a card on is a read-only list wearing a board's layout.
//
// THREE WAYS TO MOVE, because drag alone is not an interface:
//
//  1. DRAG, the fast path.
//  2. A SELECT on every card, the accessible path.
//  3. ARROW KEYS on a focused card, so the board is operable with no pointer at all.
//
// All three post the same request. (1) alone would be a board that requires a mouse,
// which is the defect the draggable-button rule exists to prevent.

// announce reports a move's outcome in a live region.
//
// NOT window.alert: a card move is an inline, non-blocking event, and a modal dialog
// per failed drag trains people to dismiss dialogs without reading them. A live region
// also means a screen reader announces a refusal without stealing focus, which is the
// point of having made the card a button.
function announce(box, msg, kind) {
  var live = box.querySelector('[data-board-live]');
  if (!live) {
    live = document.createElement('p');
    live.className = kind === 'error' ? 'notice notice-error' : 'visually-hidden';
    live.setAttribute('role', kind === 'error' ? 'alert' : 'status');
    live.setAttribute('aria-live', kind === 'error' ? 'assertive' : 'polite');
    live.setAttribute('data-board-live', '');
    box.insertBefore(live, box.firstChild);
  }
  live.textContent = msg;
}

function moveCard(slug, cardID, to) {

  return fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/board/cards/' +
      encodeURIComponent(cardID) + '/move?to=' + encodeURIComponent(to),
    { method: 'PUT', headers: apiAuth({ 'Content-Type': 'application/json' }) })
    .then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        return { ok: r.ok, body: j };
      });
    });
}

function commit(slug, box, cardID, to, sourceCard) {
  moveCard(slug, cardID, to).then(function (res) {
    if (!res.ok) {
      // "that phase has a gate" and "WIP limit reached" are refusals the reader can act
      // on, so the store's message is shown rather than a generic failure.
      announce(box, (res.body && res.body.error) ? res.body.error
        : 'That move was refused.', 'error');
      if (sourceCard) sourceCard.focus();
      return;
    }
    // The board is RE-READ, not reordered locally: the server decides whether a gate
    // or a WIP limit refused the move, and a local reorder would show a card in a
    // column it was refused for.
    //
    // announce comes AFTER hydrate, because hydrate replaces box.innerHTML and the
    // live region lives inside box -- announcing first meant the confirmation was
    // wiped by the very render that confirmed it. Announced first in the first
    // version and the accessible path was silent on every successful move.
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
      // setData is NOT optional: without it the drop handler's getData is empty and
      // the drop silently does nothing.
      ev.dataTransfer.setData('text/plain', card.getAttribute('data-card-id'));
      ev.dataTransfer.effectAllowed = 'move';
    });
    card.addEventListener('dragend', function () {
      card.classList.remove('is-dragging');
      dragging = null;
    });

    // THE NO-POINTER PATH: arrows move a focused card to the adjacent column. With
    // drag as the only affordance, the board is unusable by keyboard, and a
    // draggable <div> with a tabindex is still unusable by screen reader because the
    // drop target is never announced.
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
    // dragover MUST preventDefault or the drop never fires -- and it fails silently,
    // presenting as "dragging does nothing".
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
