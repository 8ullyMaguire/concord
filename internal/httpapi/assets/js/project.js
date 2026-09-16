// Concord project detail — resolves the slug, then hydrates from the API.
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

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function notFound(slug) {
    return '<div class="empty-state"><div class="empty-state-icon">\u{1F50E}</div>' +
      '<p class="empty-state-title">Project not found</p>' +
      '<p class="empty-state-description">No project with slug &ldquo;' + esc(slug) + '&rdquo; exists on this forge.</p>' +
      '<a class="btn btn-primary" href="/projects">Browse projects</a></div>';
  }

  function render(p, features) {
    var badges = '<div class="pill-row" style="margin-top:0.75rem;">' +
      '<span class="badge badge-indigo">' + esc(p.governance_model || 'governed') + '</span>' +
      (p.license ? '<span class="badge badge-slate">' + esc(p.license) + '</span>' : '') +
      '</div>';

    var meta = '<div class="meta-row">' +
      (p.created_at ? '<span class="meta-item">created ' + esc(fmtDate(p.created_at)) + '</span>' : '') +
      (p.updated_at ? '<span class="meta-item">updated ' + esc(fmtDate(p.updated_at)) + '</span>' : '') +
      '</div>';

    var featureCards = (features || []).map(function (f) {
      return '<div class="card">' +
        '<div class="card-title">' + esc(f.title) + '</div>' +
        (f.body ? '<p class="card-text">' + esc(f.body) + '</p>' : '') +
        '<div class="pill-row"><span class="badge badge-purple">rating ' + Math.round(f.elo_r || 0) + '</span>' +
        '<span class="badge badge-slate">' + esc(f.status || 'proposed') + '</span></div>' +
        '</div>';
    }).join('');

    return '<div class="detail-header">' +
      '<div><h1 class="page-title">' + esc(p.name) + ' <span class="badge badge-slate" style="vertical-align:middle;">' + esc(p.slug) + '</span></h1>' +
      '<p class="page-subtitle">' + esc(p.description || 'No description yet.') + '</p></div>' +
      '<div class="detail-actions">' +
      '<a class="btn btn-primary" href="/projects/' + esc(p.slug) + '/board">Open board</a>' +
      '<a class="btn btn-secondary" href="/projects">Back</a>' +
      '</div></div>' +
      '<div class="card mb-6">' + badges + healthBar(p.health_score) + meta + '</div>' +
      '<div class="section-head"><h2 class="section-title" style="font-size:1.25rem;">Features</h2>' +
      '<p class="section-sub">Proposed solutions ranked by pairwise comparison.</p></div>' +
      (features && features.length
        ? '<div class="grid-responsive-2">' + featureCards + '</div>'
        : '<div class="empty-state"><div class="empty-state-icon">\u2728</div>' +
          '<p class="empty-state-title">No features yet</p>' +
          '<p class="empty-state-description">Features are proposed from validated complaints.</p></div>');
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-detail');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.innerHTML = notFound(''); return; }

    fetch('/api/v1/projects/' + encodeURIComponent(slug))
      .then(function (r) {
        if (r.status === 404) throw { notFound: true };
        return r.ok ? r.json() : Promise.reject({ status: r.status });
      })
      .then(function (p) {
        box.innerHTML = '<div class="loading-spinner"></div>';
        return fetch('/api/v1/projects/' + p.id + '/features')
          .then(function (r) { return r.ok ? r.json() : []; })
          .then(function (features) { box.innerHTML = render(p, features); });
      })
      .catch(function (err) {
        box.innerHTML = err && err.notFound
          ? notFound(slug)
          : '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
            '<p class="empty-state-title">Could not load project</p>' +
            '<p class="empty-state-description">The API did not respond. Try again.</p></div>';
      });
  });
})();
