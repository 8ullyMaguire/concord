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
    return '<div class="table-scroll"><table class="ranking-table"><thead><tr>' + head +
      '</tr></thead><tbody>' + rows.map(facetRow).join('') + '</tbody></table></div>';
  }

  function card(title, body) {
    return '<div class="card"><div class="card-title">' + esc(title) + '</div>' +
      '<div class="card-text">' + body + '</div></div>';
  }

  // The split columns are shown only when at least one row carries them, so a
  // plain facet is not padded with three empty columns.
  function hasSplit(rows) {
    return (rows || []).some(function (r) {
      return r.organic !== undefined || r.seeded !== undefined || r.unknown !== undefined;
    });
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

  document.addEventListener('DOMContentLoaded', function () {
    var container = document.getElementById('stats-summary');
    if (!container) return;

    container.setAttribute('aria-busy', 'true');

    fetch('/api/v1/stats/summary', {
      headers: (window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {})
    })
      .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
      .then(function (data) { render(container, data); })
      .catch(function () {
        // Clear aria-busy on the failure path too: aria-busy suppresses
        // live-region updates inside the region, so an error left busy is an
        // error nobody hears.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
        container.innerHTML = emptyState('Statistics unavailable',
          'The census did not load. Try again.');
      });
  });
})();
