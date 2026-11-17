// Concord feature ranking — the public result of the pairwise comparisons.
//
// Two things this deliberately does not do:
//
// 1. Re-sort by Glicko rating. The API returns a priority order that already
//    folds in pain and strategic weight; sorting by rating client-side would
//    silently discard both, and produce an order the forge never concluded.
// 2. Hide the deviation. A rating of 1500 ± 350 has barely been compared to
//    anything. Printing the bare number would present noise as a measurement,
//    so every rating is shown with its uncertainty and a high-deviation
//    feature is labelled as under-tested.
//
// Rendering (step 2b): `el()` nodes only. Titles, bodies, participation strings
// and error messages are text nodes; hrefs are set verbatim as attributes.
(function () {
  'use strict';

  var el = window.ConcordDom.el;

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function num(n, dp) {
    if (n == null || isNaN(n)) return '—';
    return Number(n).toFixed(dp == null ? 2 : dp);
  }

  // A deviation above this means the rating is not yet worth much. The Glicko-2
  // default initial RD is 350, so anything at or above it has barely been compared.
  var UNTESTED_RD = 300;

  var root = document.getElementById('ranking-root');
  var slug = slugFromPath();

  // Whether this reader may moderate, decided by the SERVER from the same permission
  // pair the moderation routes enforce and handed over as an attribute on the root.
  // An attribute is zero requests; the panel on the feature page remains the authority.
  var CAN_MODERATE = (root && root.getAttribute('data-can-moderate')) === 'true';

  function errorNode(message) {
    return el('div', { class: 'empty-state error-state' }, [
      el('div', { class: 'empty-state-icon' }, '⚠'),
      el('p', { class: 'empty-state-title' }, 'Could not load the ranking'),
      el('p', { class: 'empty-state-description' }, message),
      el('button', { class: 'btn btn-primary', id: 'ranking-retry' }, 'Try again')
    ]);
  }

  // The moderation cell: a link to the feature page's panel, never a second implementation.
  // A reader without it gets no cell at all -- not a disabled control, not a lock icon.
  function modCell(p) {
    if (!CAN_MODERATE) return null;
    var href = '/projects/' + encodeURIComponent(slug) + '/features/' + p.id + '#moderation-panel';
    return el('td', { class: 'cell-mod' },
      el('a', { class: 'rank-moderate', href: href }, 'Moderate'));
  }

  function modHead() {
    return CAN_MODERATE ? el('th', {}, el('span', { class: 'dim' }, 'Moderate')) : null;
  }

  function row(p, i, tally) {
    var rd = p.elo_rd == null ? 0 : p.elo_rd;
    var untested = rd >= UNTESTED_RD;
    var participation = tally
      ? tally.votes_cast + (tally.skips ? ' (' + tally.skips + ' skipped)' : '')
      : '0';
    var rating = el('td', { class: 'cell-num' }, [
      Math.round(p.elo_r),
      untested ? null : el('span', { class: 'dim' }, '±' + Math.round(rd))
    ]);
    return el('tr', untested ? { class: 'row-untested' } : {}, [
      el('td', { class: 'cell-rank' }, i + 1),
      el('td', { class: 'cell-title' }, [
        el('strong', {}, p.title),
        untested ? ' ' : null,
        untested ? el('span', { class: 'badge badge-slate' }, 'under-tested') : null,
        el('p', { class: 'cell-note' }, p.body || '')
      ]),
      rating,
      el('td', { class: 'cell-num' }, num(p.pain_score)),
      el('td', { class: 'cell-num' }, num(p.strategic_weight)),
      el('td', { class: 'cell-num' }, num(p.priority_score)),
      el('td', { class: 'cell-num' }, participation),
      modCell(p)
    ]);
  }

  function render(priorities, tallies) {
    if (!priorities || priorities.length === 0) {
      root.replaceChildren(el('div', { class: 'empty-state' }, [
        el('div', { class: 'empty-state-icon' }, '📋'),
        el('p', { class: 'empty-state-title' }, 'Nothing to rank yet'),
        el('p', { class: 'empty-state-description' }, 'A feature needs at least one ' +
          'validated complaint behind it before it is a candidate, and it ' +
          'needs comparisons before it can be ordered.'),
        el('a', {
          class: 'btn btn-primary', href: '/projects/' + encodeURIComponent(slug) + '/rank'
        }, 'Rank the first pair')
      ]));
      return;
    }
    var byId = {};
    (tallies || []).forEach(function (t) { byId[t.feature_id] = t; });

    var underTested = priorities.filter(function (p) {
      return p.elo_rd != null && p.elo_rd >= UNTESTED_RD;
    }).length;

    root.replaceChildren(
      el('div', { class: 'ranking-meta' },
        el('p', {}, [
          el('strong', {}, priorities.length), ' feature' + (priorities.length === 1 ? '' : 's') +
          ' in order. ',
          underTested > 0
            ? el('span', {}, [
                el('strong', {}, underTested),
                ' marked under-tested: the uncertainty is still wider than the difference between them.'
              ])
            : 'All ratings have narrowed enough to mean something.'
        ])),
      el('div', { class: 'table-scroll', tabindex: '0', role: 'region', 'aria-label': 'tabla' },
        el('table', { class: 'ranking-table' }, [
          el('thead', {}, el('tr', {}, [
            el('th', {}, '#'), el('th', {}, 'Feature'), el('th', {}, 'Rating'), el('th', {}, 'Pain'),
            el('th', {}, 'Weight'), el('th', {}, 'Priority'), el('th', {}, 'Votes'), modHead()
          ])),
          el('tbody', {}, priorities.map(function (p, i) { return row(p, i, byId[p.id]); }))
        ])),
      el('p', { class: 'rank-note' }, 'Order is the priority score the forge computed, ' +
        'not the raw rating: pain and strategic weight are part of the score. ' +
        'Pain is the sum over a feature\'s validated complaints, so a feature ' +
        'proposed without a problem behind it is not competing for anything.')
    );
  }

  function load() {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);
    Promise.all([
      window.ConcordApi.get(base + '/priorities'),
      // Tallies are supplementary: a ranking without participation counts is
      // less honest, but its absence must not blank the page.
      window.ConcordApi.get(base + '/tallies').catch(function () { return []; })
    ]).then(function (res) {
      render(res[0], res[1]);
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    }).catch(function (err) {
      root.replaceChildren(errorNode(err && err.message ? err.message : String(err)));
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    });
  }

  if (root) {
    load();
    document.addEventListener('click', function (e) {
      if (e.target && e.target.id === 'ranking-retry') load();
    });
  }
})();
