// Concord search page — hydrates results and tag facets from the search API.
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function healthBar(score) {
    if (score == null) return '<p class="health-label"><span>health</span><span>no metrics yet</span></p>';
    var pct = Math.round(score * 100);
    var cls = pct >= 70 ? 'good' : pct >= 40 ? 'mid' : 'poor';
    return '<div class="health-bar"><div class="health-fill health-fill-' + cls + '" style="width:' + pct + '%"></div></div>' +
      '<p class="health-label"><span>health</span><span>' + pct + '%</span></p>';
  }

  function resultCard(p) {
    var badges = '<div class="pill-row" style="margin-top:0.5rem;">' +
      '<span class="badge badge-indigo">' + esc(p.governance_model || 'governed') + '</span>' +
      (p.license ? '<span class="badge badge-slate">' + esc(p.license) + '</span>' : '') +
      '</div>';
    return '<a class="card fade-in" href="/projects/' + esc(p.slug) + '">' +
      '<div class="card-title">' + esc(p.name) + ' <span class="badge badge-slate" style="margin-left:0.375rem;">' + esc(p.slug) + '</span></div>' +
      '<p class="card-text">' + esc(p.description || 'No description yet.') + '</p>' +
      badges + healthBar(p.health_score) +
      '</a>';
  }

  function emptyState(icon, title, desc) {
    return '<div class="empty-state"><div class="empty-state-icon">' + icon + '</div>' +
      '<p class="empty-state-title">' + esc(title) + '</p>' +
      '<p class="empty-state-description">' + esc(desc) + '</p></div>';
  }

  function renderResults(container, data) {
    var results = (data && data.results) || [];
    if (!results.length) {
      container.innerHTML = emptyState('\u{1F50D}', 'No matches', 'Nothing matched that query. Try fewer or different words.');
      return;
    }
    container.innerHTML = '<p class="meta-item mb-4">' + results.length + ' of ' + (data.total || results.length) + ' projects</p>' +
      '<div class="grid-responsive-2">' + results.map(resultCard).join('') + '</div>';
  }

  function renderChips(container, facets, activeTags, q) {
    var tags = (facets && facets.tags) || [];
    if (!tags.length) return;
    var chips = tags.slice(0, 8).map(function (t) {
      var active = activeTags.indexOf(t.value) !== -1;
      var href = '/search?q=' + encodeURIComponent(q);
      activeTags.forEach(function (t2) {
        if (t2 !== t.value) href += '&tag=' + encodeURIComponent(t2);
      });
      if (!active) href += '&tag=' + encodeURIComponent(t.value);
      return '<a class="filter-chip' + (active ? ' active' : '') + '" href="' + esc(href) + '">' + esc(t.value) + ' (' + t.count + ')</a>';
    }).join('');
    container.innerHTML = chips;
  }

  document.addEventListener('DOMContentLoaded', function () {
    var container = document.getElementById('search-results');
    var chipsBox = document.getElementById('tag-chips');
    if (!container) return;

    var params = new URLSearchParams(window.location.search);
    var q = params.get('q') || '';
    var activeTags = params.getAll('tag');

    if (!q && !activeTags.length) return; // server-rendered empty state stays

    container.innerHTML = '<div class="loading-spinner" role="status" aria-label="Searching"></div>';

    var qs = new URLSearchParams();
    if (q) qs.set('q', q);
    activeTags.forEach(function (t) { qs.append('tag', t); });

    fetch('/api/v1/search?' + qs.toString())
      .then(function (r) { return r.ok ? r.json() : { results: [] }; })
      .then(function (data) {
        renderResults(container, data);
        renderChips(chipsBox, data.facets, activeTags, q);
      })
      .catch(function () {
        container.innerHTML = emptyState('\u26A0\uFE0F', 'Search failed', 'The search service did not respond. Try again.');
      });
  });
})();
