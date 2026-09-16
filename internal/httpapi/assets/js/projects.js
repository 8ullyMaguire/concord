// Concord projects listing — hydrates the grid from the public API.
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

  function fmtDate(unix) {
    if (!unix) return '';
    try { return new Date(unix * 1000).toLocaleDateString(); } catch (e) { return ''; }
  }

  function card(p) {
    return '<a class="card fade-in" href="/projects/' + esc(p.slug) + '">' +
      '<div class="card-title">' + esc(p.name) + ' <span class="badge badge-slate" style="margin-left:0.375rem;">' + esc(p.slug) + '</span></div>' +
      '<p class="card-text">' + esc(p.description || 'No description yet.') + '</p>' +
      '<div class="pill-row">' +
      '<span class="badge badge-indigo">' + esc(p.governance_model || 'governed') + '</span>' +
      (p.license ? '<span class="badge badge-slate">' + esc(p.license) + '</span>' : '') +
      (p.updated_at ? '<span class="badge badge-slate">updated ' + esc(fmtDate(p.updated_at)) + '</span>' : '') +
      '</div>' +
      healthBar(p.health_score) +
      '</a>';
  }

  document.addEventListener('DOMContentLoaded', function () {
    var grid = document.getElementById('projects-grid');
    if (!grid) return;

    fetch('/api/v1/projects')
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        if (!Array.isArray(list) || !list.length) {
          grid.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u{1F4E6}</div>' +
            '<p class="empty-state-title">No projects yet</p>' +
            '<p class="empty-state-description">Be the first: POST a project to /api/v1/projects.</p></div>';
          return;
        }
        grid.innerHTML = '<div class="grid-responsive-2">' + list.map(card).join('') + '</div>';
      })
      .catch(function () {
        grid.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
          '<p class="empty-state-title">Could not load projects</p>' +
          '<p class="empty-state-description">The API did not respond. Try again.</p></div>';
      });
  });
})();
