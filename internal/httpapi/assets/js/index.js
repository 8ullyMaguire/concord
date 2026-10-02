// Concord homepage stats — rendered from the public read API.
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  window.concordEsc = esc;

  function setText(id, value) {
    var el = document.getElementById(id);
    if (el) el.textContent = value;
  }

  document.addEventListener('DOMContentLoaded', function () {
    var projects = fetch('/api/v1/projects').then(function (r) { return r.ok ? r.json() : []; });
    var search = fetch('/api/v1/search?sort=updated').then(function (r) { return r.ok ? r.json() : { facets: {} }; });

    projects.then(function (list) {
      setText('stat-projects', Array.isArray(list) ? list.length : 0);
      statsSettled();
    }).catch(function () { setText('stat-projects', 0); statsSettled(); });

    // aria-busy is cleared only once BOTH fetches have settled, not on the
    // first one to land. The stats row is one region; clearing it when only the
    // projects count had arrived would announce "not busy" while two of the
    // three figures were still placeholders.
    var settled = 0;
    function statsSettled() {
      if (++settled < 2) return;
      var el = document.getElementById('stats-grid');
      if (el && el.setAttribute) el.setAttribute('aria-busy', 'false');
    }

    search.then(function (data) {
      var facets = data && data.facets ? data.facets : {};
      setText('stat-tags', (facets.tags || []).length);
      setText('stat-languages', (facets.languages || []).length);
      statsSettled();
    }).catch(function () {
      setText('stat-tags', 0);
      setText('stat-languages', 0);
      statsSettled();
    });
  });
})();
