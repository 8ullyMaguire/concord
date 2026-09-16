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
    }).catch(function () { setText('stat-projects', 0); });

    search.then(function (data) {
      var facets = data && data.facets ? data.facets : {};
      setText('stat-tags', (facets.tags || []).length);
      setText('stat-languages', (facets.languages || []).length);
    }).catch(function () {
      setText('stat-tags', 0);
      setText('stat-languages', 0);
    });
  });
})();
