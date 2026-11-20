// Concord homepage stats — rendered from the public read API.
(function () {
  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it, so this file cannot disagree with
  // another page about which characters are dangerous.
  var esc = window.ConcordEscape.esc;
  window.concordEsc = esc;

  function setText(id, value) {
    var el = document.getElementById(id);
    if (el) el.textContent = value;
  }

  document.addEventListener('DOMContentLoaded', function () {
    // One client (review item 9b): the search response carries the TRUE total. The
    // projects endpoint is paged, so counting its 50 items printed 50 on a 73-project
    // instance (D-214). Reuse the same response for the facets and the count.
    var search = window.ConcordApi.get('/api/v1/search?sort=updated')
      .catch(function () { return { facets: {}, total: 0 }; });
    var projects = search.then(function (data) {
      return typeof data.total === 'number'
        ? data.total
        : window.ConcordApi.list(data).length;
    }).catch(function () { return 0; });

    projects.then(function (total) {
      setText('stat-projects', total);
      statsSettled();
    });

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
