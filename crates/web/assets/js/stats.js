/* Concord statistics page — renders the instance census from its own API.
 *
 * The page computes nothing. Every figure here is read out of
 * /api/v1/stats/summary, because spec §3.4 is explicit that a page which
 * recomputes an aggregate the API already returns is two answers waiting to
 * disagree, and the page is the one nobody notices is wrong.
 *
 * The provenance split is rendered as three numbers wherever the API carries
 * one, and a total is never shown without it: spec §2 withholds an aggregate
 * that cannot produce the split rather than publishing it with a footnote. */
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function emptyState(title, desc) {
    return '<div class="empty-state"><div class="empty-state-icon">&#128202;</div>' +
      '<p class="empty-state-title">' + esc(title) + '</p>' +
      '<p class="empty-state-description">' + esc(desc) + '</p></div>';
  }

  // A facet row. `split` is a ProvenanceCount (three numbers) or a FacetCount
  // (one), and the branch is on the field's presence rather than on which
  // section we are in -- the API is the contract, not this file's layout.
  function facetRow(row) {
    if (row.organic !== undefined || row.seeded !== undefined || row.unknown !== undefined) {
      return '<tr><td>' + esc(row.value) + '</td>' +
        '<td>' + row.total + '</td>' +
        '<td>' + row.organic + '</td>' +
        '<td>' + row.seeded + '</td>' +
        '<td>' + row.unknown + '</td></tr>';
    }
    return '<tr><td>' + esc(row.value) + '</td><td>' + row.count + '</td></tr>';
  }

  function table(headCells, rows) {
    if (!rows || !rows.length) return '';
    var head = headCells.map(function (h) { return '<th>' + esc(h) + '</th>'; }).join('');
    return '<div class="table-scroll" tabindex="0" role="region" aria-label="tabla"><table class="ranking-table"><thead><tr>' + head +
      '</tr></thead><tbody>' + rows.map(facetRow).join('') + '</tbody></table></div>';
  }

  function card(title, body) {
    return '<div class="card"><div class="card-title">' + esc(title) + '</div>' +
      '<div class="card-text">' + body + '</div></div>';
  }

  // A latency card. The median is shown with its n and its unfinished count,
  // because spec §4 refuses a median without n: the same 216h means something
  // different for 3 episodes than for 300.
  function latencyCard(title, lat) {
    var value = lat.n === 0 ? '—' : lat.median_hours.toFixed(1) + ' h';
    var counts = 'n=' + lat.n + ' · unfinished=' + lat.unfinished;
    if (lat.unmeasured) counts += ' · unmeasured=' + lat.unmeasured;
    return '<div class="card"><div class="card-title">' + esc(title) + '</div>' +
      '<div class="card-text">' + esc(value) + '</div>' +
      '<p class="muted small">' + esc(counts) + '</p></div>';
  }

  function renderLatency(container, latency) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    var parts = [
      latencyCard('Complaint triage', latency.triage),
      latencyCard('Feature delivery', latency.delivery),
      latencyCard('Consensus decision', latency.decision)
    ];
    container.innerHTML = '<div class="grid-responsive-3">' + parts.join('') + '</div>';
  }

  // The split columns are shown only when at least one row carries them, so a
  // plain facet is not padded with three empty columns.
  function hasSplit(rows) {
    return (rows || []).some(function (r) {
      return r.organic !== undefined || r.seeded !== undefined || r.unknown !== undefined;
    });
  }

  // The responsiveness leaderboard (STAT-3.5). Rows are rendered in the order the
  // API returns them and are NOT re-sorted here: the ranking rule lives in the
  // store's ORDER BY, and a page that sorted again would be a second answer.
  // A project under the sample floor is absent from the payload, so it is absent
  // from this table too -- the list is never padded with unmeasured rows.
  function renderResponsiveness(container, rows) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    if (!rows || !rows.length) {
      container.innerHTML = emptyState('No responsiveness published yet',
        'A project appears here once at least five first-time pull requests were answered. ' +
        'Below that it is absent rather than ranked last: a median nobody can act on is not a measurement.');
      return;
    }
    var body = rows.map(function (row, i) {
      return '<tr><td>' + (i + 1) + '</td>' +
        '<td><a href="/projects/' + encodeURIComponent(row.slug) + '">' + esc(row.name) + '</a></td>' +
        '<td>' + row.newcomer_response_hours.toFixed(1) + ' h</td>' +
        '<td>' + row.n + '</td></tr>';
    }).join('');
    container.innerHTML = card('Newcomer responsiveness',
      '<p class="muted small">Median hours from a first-time contributor&#39;s pull request ' +
      'to the first answer by somebody else &mdash; fastest first.</p>' +
      '<div class="table-scroll" tabindex="0" role="region" aria-label="tabla"><table class="ranking-table"><thead><tr>' +
      '<th>#</th><th>project</th><th>median</th><th>n</th></tr></thead><tbody>' +
      body + '</tbody></table></div>');
  }

  // Rising (STAT-3.4). Ranked by organic features shipped per week between the
  // project's own first and last snapshot. The SPAN is printed in every row: a rate over
  // 14 days and one over 90 are different claims, and a reader who is not told which is
  // reading a comparison between incomparable numbers.
  //
  // Health is shown BESIDE the slope, never instead of it -- "rising is a change, health
  // is a level" -- and an unmeasured health renders a dash, never a zero.
  function renderRising(container, rows) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    if (!rows || !rows.length) {
      container.innerHTML = emptyState('No trend yet',
        'A project is ranked here once it has snapshots at least 14 days apart. ' +
        'Below that there is no slope, and a rate over a day or two would be a number about the timer.');
      return;
    }
    var body = rows.map(function (row, i) {
      return '<tr><td>' + (i + 1) + '</td>' +
        '<td><a href="/projects/' + encodeURIComponent(row.slug) + '">' + esc(row.name) + '</a></td>' +
        '<td>' + row.features_per_week.toFixed(2) + '</td>' +
        '<td>' + row.complaints_per_week.toFixed(2) + '</td>' +
        '<td>' + (row.health_score == null ? '\u2014' : Math.round(row.health_score * 100) + '%') + '</td>' +
        '<td>' + row.days + '</td></tr>';
    }).join('');
    container.innerHTML = card('Rising',
      '<p class="muted small">Organic features shipped, then organic complaints filed, ' +
      'per week across each project&#39;s own recorded span &mdash; fastest first. ' +
      'Health is the current level, beside the slope rather than instead of it.</p>' +
      '<div class="table-scroll" tabindex="0" role="region" aria-label="tabla"><table class="ranking-table"><thead><tr>' +
      '<th>#</th><th>project</th><th>features/week</th><th>complaints/week</th>' +
      '<th>health</th><th>days</th></tr></thead><tbody>' +
      body + '</tbody></table></div>');
  }

  function render(container, data) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);

    var parts = [];

    if (hasSplit(data.features_by_status)) {
      parts.push(card('Features by status',
        table(['status', 'total', 'organic', 'seeded', 'unknown'], data.features_by_status)));
      parts.push(card('Features by effort',
        table(['effort', 'total', 'organic', 'seeded', 'unknown'], data.features_by_effort)));
      parts.push(card('Complaints by status',
        table(['status', 'total', 'organic', 'seeded', 'unknown'], data.complaints_by_status)));
    } else {
      parts.push(card('Features by status', table(['status', 'total'], data.features_by_status)));
      parts.push(card('Complaints by status', table(['status', 'total'], data.complaints_by_status)));
    }

    parts.push(card('Projects by language', table(['language', 'projects'], data.projects_by_language)));
    parts.push(card('Projects by tag', table(['tag', 'projects'], data.projects_by_tag)));
    parts.push(card('Projects by license', table(['license', 'projects'], data.projects_by_license)));
    parts.push(card('Projects by governance', table(['model', 'projects'], data.projects_by_governance)));
    parts.push(card('Projects by visibility', table(['visibility', 'projects'], data.projects_by_visibility)));
    parts.push(card('Members by role', table(['role', 'members'], data.members_by_role)));
    parts.push(card('Documents by kind', table(['kind', 'documents'], data.documents_by_kind)));
    parts.push(card('Arenas by type', table(['type', 'arenas'], data.arenas_by_type)));

    container.innerHTML = '<div class="grid-responsive-2">' + parts.join('') + '</div>';
  }

  function authHeaders() {
    return (window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {});
  }

  function getJSON(path) {
    return fetch(path, { headers: authHeaders() })
      .then(function (r) {
        return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status));
      });
  }

  document.addEventListener('DOMContentLoaded', function () {
    var summary = document.getElementById('stats-summary');
    var latency = document.getElementById('stats-latency');
    var responsiveness = document.getElementById('stats-responsiveness');
    var rising = document.getElementById('stats-rising');

    // Its own fetch and its own failure state: the leaderboard is the only
    // section that needs the forge webhook to have ever fired, so a blank one is
    // the normal state of a young instance and must not look like an outage.
    if (responsiveness) {
      responsiveness.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats/responsiveness')
        .then(function (rows) { renderResponsiveness(responsiveness, rows); })
        .catch(function () {
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(responsiveness);
          responsiveness.innerHTML = emptyState('Responsiveness unavailable',
            'The leaderboard did not load. Try again.');
        });
    }

    // The two fetches are independent: a failure in one must not blank the
    // other, and the latency cards are not a footnote to the census.
    if (latency) {
      latency.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats')
        .then(function (data) { renderLatency(latency, data.latency); })
        .catch(function () {
          // Clear aria-busy on the failure path too: aria-busy suppresses
          // live-region updates inside the region, so an error left busy is an
          // error nobody hears.
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(latency);
          latency.innerHTML = emptyState('Latency unavailable',
            'The response times did not load. Try again.');
        });
    }

    // Rising needs the per-project series, so a fresh instance has none and says so.
    if (rising) {
      rising.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats/rising')
        .then(function (rows) { renderRising(rising, rows); })
        .catch(function () {
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(rising);
          rising.innerHTML = emptyState('Trend unavailable',
            'The rising ranking did not load. Try again.');
        });
    }

    if (!summary) return;

    summary.setAttribute('aria-busy', 'true');

    getJSON('/api/v1/stats/summary')
      .then(function (data) { render(summary, data); })
      .catch(function () {
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(summary);
        summary.innerHTML = emptyState('Statistics unavailable',
          'The census did not load. Try again.');
      });
  });
})();
