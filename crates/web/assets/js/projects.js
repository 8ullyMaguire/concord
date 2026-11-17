// Concord projects listing — hydrates the grid from the public API.
//
// Rendering (step 2b): every builder is an `el()` tree. Names, slugs, descriptions,
// governance models and licenses are authored free text; no HTML string sink.
(function () {
  var el = window.ConcordDom.el;

  function healthBar(score) {
    if (score == null) {
      return el('p', { class: 'health-label' }, [
        el('span', {}, 'health'), el('span', {}, 'no metrics yet')
      ]);
    }
    var pct = Math.round(score * 100);
    var cls = pct >= 70 ? 'good' : pct >= 40 ? 'mid' : 'poor';
    return [
      el('div', { class: 'health-bar' },
        el('div', { class: 'health-fill health-fill-' + cls, style: 'width:' + pct + '%' })),
      el('p', { class: 'health-label' }, [
        el('span', {}, 'health'), el('span', {}, pct + '%')
      ])
    ];
  }

  function fmtDate(unix) {
    if (!unix) return '';
    try { return new Date(unix * 1000).toLocaleDateString(); } catch (e) { return ''; }
  }

  function card(p) {
    var pills = [el('span', { class: 'badge badge-indigo' }, p.governance_model || 'governed')];
    if (p.license) pills.push(el('span', { class: 'badge badge-slate' }, p.license));
    if (p.updated_at) pills.push(el('span', { class: 'badge badge-slate' }, 'updated ' + fmtDate(p.updated_at)));
    return el('a', { class: 'card fade-in', href: '/projects/' + p.slug }, [
      el('div', { class: 'card-title' }, [
        p.name + ' ',
        el('span', { class: 'badge badge-slate', style: 'margin-left:0.375rem;' }, p.slug)
      ]),
      el('p', { class: 'card-text' }, p.description || 'No description yet.'),
      el('div', { class: 'pill-row' }, pills),
      healthBar(p.health_score)
    ]);
  }

  document.addEventListener('DOMContentLoaded', function () {
    var grid = document.getElementById('projects-grid');
    if (!grid) return;

    function clearBusy() {
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(grid);
    }

    window.ConcordApi.get('/api/v1/projects')
      .catch(function () { return []; })
      .then(function (list) {
        if (!Array.isArray(list) || !list.length) {
          grid.replaceChildren(el('div', { class: 'empty-state' }, [
            el('div', { class: 'empty-state-icon' }, '📦'),
            el('p', { class: 'empty-state-title' }, 'No projects yet'),
            el('p', { class: 'empty-state-description' }, 'Be the first: POST a project to /api/v1/projects.')
          ]));
          clearBusy();
          return;
        }
        grid.replaceChildren(el('div', { class: 'grid-responsive-2' }, list.map(card)));
        clearBusy();
      })
      .catch(function () {
        grid.replaceChildren(el('div', { class: 'empty-state' }, [
          el('div', { class: 'empty-state-icon' }, '⚠️'),
          el('p', { class: 'empty-state-title' }, 'Could not load projects'),
          el('p', { class: 'empty-state-description' }, 'The API did not respond. Try again.')
        ]));
        // Cleared on the failure path too: an error left busy is an error a screen
        // reader never hears about.
        clearBusy();
      });
  });
})();
