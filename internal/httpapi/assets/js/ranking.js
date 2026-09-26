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

  function num(n, dp) {
    if (n == null || isNaN(n)) return '—';
    return Number(n).toFixed(dp == null ? 2 : dp);
  }

  // A deviation above this means the rating is not yet worth much. The Glicko-2
  // default initial RD is 350, so anything at or above it has barely been
  // compared.
  var UNTESTED_RD = 300;

  var root = document.getElementById('ranking-root');
  var slug = slugFromPath();

  function errorState(message) {
    return '<div class="empty-state error-state">' +
      '<div class="empty-state-icon">\u26A0</div>' +
      '<p class="empty-state-title">Could not load the ranking</p>' +
      '<p class="empty-state-description">' + esc(message) + '</p>' +
      '<button class="btn btn-primary" id="ranking-retry">Try again</button></div>';
  }

  function row(p, i, tally) {
    var rd = p.elo_rd == null ? 0 : p.elo_rd;
    var untested = rd >= UNTESTED_RD;
    var participation = tally
      ? tally.votes_cast + (tally.skips ? ' (' + tally.skips + ' skipped)' : '')
      : '0';
    return '<tr' + (untested ? ' class="row-untested"' : '') + '>' +
      '<td class="cell-rank">' + (i + 1) + '</td>' +
      '<td class="cell-title"><strong>' + esc(p.title) + '</strong>' +
        (untested ? ' <span class="badge badge-slate">under-tested</span>' : '') +
        '<p class="cell-note">' + esc(p.body || '') + '</p></td>' +
      '<td class="cell-num">' + Math.round(p.elo_r) +
        (untested ? '' : ' <span class="dim">±' + Math.round(rd) + '</span>') + '</td>' +
      '<td class="cell-num">' + num(p.pain_score) + '</td>' +
      '<td class="cell-num">' + num(p.strategic_weight) + '</td>' +
      '<td class="cell-num">' + num(p.priority_score) + '</td>' +
      '<td class="cell-num">' + esc(participation) + '</td>' +
      '</tr>';
  }

  function render(priorities, tallies) {
    if (!priorities || priorities.length === 0) {
      root.innerHTML = '<div class="empty-state">' +
        '<div class="empty-state-icon">\u{1F4CB}</div>' +
        '<p class="empty-state-title">Nothing to rank yet</p>' +
        '<p class="empty-state-description">A feature needs at least one ' +
        'validated complaint behind it before it is a candidate, and it ' +
        'needs comparisons before it can be ordered.</p>' +
        '<a class="btn btn-primary" href="/projects/' + encodeURIComponent(slug) + '/rank">Rank the first pair</a>' +
        '</div>';
      return;
    }
    var byId = {};
    (tallies || []).forEach(function (t) { byId[t.feature_id] = t; });

    var underTested = priorities.filter(function (p) {
      return p.elo_rd != null && p.elo_rd >= UNTESTED_RD;
    }).length;

    root.innerHTML =
      '<div class="ranking-meta">' +
        '<p><strong>' + priorities.length + '</strong> feature' +
          (priorities.length === 1 ? '' : 's') + ' in order. ' +
          (underTested > 0
            ? '<strong>' + underTested + '</strong> marked under-tested: the ' +
              'uncertainty is still wider than the difference between them.'
            : 'All ratings have narrowed enough to mean something.') + '</p>' +
      '</div>' +
      '<div class="table-scroll"><table class="ranking-table">' +
        '<thead><tr>' +
          '<th>#</th><th>Feature</th><th>Rating</th><th>Pain</th>' +
          '<th>Weight</th><th>Priority</th><th>Votes</th>' +
        '</tr></thead>' +
        '<tbody>' +
        priorities.map(function (p, i) { return row(p, i, byId[p.id]); }).join('') +
        '</tbody></table></div>' +
      '<p class="rank-note">Order is the priority score the forge computed, ' +
      'not the raw rating: pain and strategic weight are part of the score. ' +
      'Pain is the sum over a feature\'s validated complaints, so a feature ' +
      'proposed without a problem behind it is not competing for anything.</p>';
  }

  function load() {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);
    Promise.all([
      fetch(base + '/priorities').then(function (r) {
        if (!r.ok) throw new Error('priorities: HTTP ' + r.status);
        return r.json();
      }),
      // Tallies are supplementary: a ranking without participation counts is
      // less honest, but its absence must not blank the page.
      fetch(base + '/tallies').then(function (r) { return r.ok ? r.json() : []; })
    ]).then(function (res) {
      render(res[0], res[1]);
    }).catch(function (err) {
      root.innerHTML = errorState(err && err.message ? err.message : String(err));
    });
  }

  if (root) {
    load();
    document.addEventListener('click', function (e) {
      if (e.target && e.target.id === 'ranking-retry') load();
    });
  }
})();
