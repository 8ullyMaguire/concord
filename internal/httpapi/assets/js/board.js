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

    box.innerHTML = '<div class="loading-spinner" role="status" aria-label="Loading board"></div>';

    // The board API is keyed by numeric project id; resolve the slug first.
    fetch('/api/v1/projects/' + encodeURIComponent(slug))
      .then(function (r) {
        if (r.status === 404) throw { notFound: true };
        return r.ok ? r.json() : Promise.reject({ status: r.status });
      })
      .then(function (p) {
        return Promise.all([
          fetch('/api/v1/projects/' + p.id + '/board').then(function (r) { return r.ok ? r.json() : { columns: [], cards: [] }; }),
          fetch('/api/v1/projects/' + p.id + '/features').then(function (r) { return r.ok ? r.json() : []; })
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
          return;
        }

        var html = '<div class="kanban-board">';
        columns.forEach(function (col) {
          var inCol = cards.filter(function (c) { return c.column === col.name; });
          html += '<div class="kanban-column"><div class="kanban-column-header">' +
            '<span class="kanban-column-title">' + esc(col.name) + '</span>' +
            '<span class="kanban-column-count">' + inCol.length + (col.wip ? ' / WIP ' + col.wip : '') + '</span>' +
            '</div>';
          inCol.forEach(function (c) {
            var title = titles[c.feature_id] || ('Feature #' + c.feature_id);
            html += '<div class="kanban-card">' + esc(title) + '</div>';
          });
          html += '</div>';
        });
        html += '</div>';
        box.innerHTML = html;
      })
      .catch(function (err) {
        box.innerHTML = err && err.notFound
          ? '<div class="empty-state"><div class="empty-state-icon">\u{1F50E}</div>' +
            '<p class="empty-state-title">Project not found</p>' +
            '<p class="empty-state-description">No project with slug &ldquo;' + esc(slug) + '&rdquo; exists.</p>' +
            '<a class="btn btn-primary" href="/projects">Browse projects</a></div>'
          : '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
            '<p class="empty-state-title">Could not load board</p>' +
            '<p class="empty-state-description">The API did not respond. Try again.</p></div>';
      });
  });
})();
