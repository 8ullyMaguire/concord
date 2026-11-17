
// Concord search page — hydrates results and tag facets from the search API.
// Rendering (step 2b): el() trees only; hostile result fields and tag values are
// text nodes or setAttribute values.
(function () {
  'use strict';
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
      el('p', { class: 'health-label' }, [el('span', {}, 'health'), el('span', {}, pct + '%')])
    ];
  }

  function resultCard(p) {
    var badges = el('div', { class: 'pill-row', style: 'margin-top:0.5rem;' }, [
      el('span', { class: 'badge badge-indigo' }, p.governance_model || 'governed'),
      p.license ? el('span', { class: 'badge badge-slate' }, p.license) : null
    ]);
    return el('a', { class: 'card fade-in', href: '/projects/' + p.slug }, [
      el('div', { class: 'card-title' }, [
        p.name + ' ',
        el('span', { class: 'badge badge-slate', style: 'margin-left:0.375rem;' }, p.slug)
      ]),
      el('p', { class: 'card-text' }, p.description || 'No description yet.'),
      badges,
      healthBar(p.health_score)
    ]);
  }

  function emptyState(icon, title, desc) {
    return el('div', { class: 'empty-state' }, [
      el('div', { class: 'empty-state-icon' }, icon),
      el('p', { class: 'empty-state-title' }, title),
      el('p', { class: 'empty-state-description' }, desc)
    ]);
  }

  function renderResults(container, data) {
    var results = (data && data.results) || [];
    if (!results.length) {
      container.replaceChildren(emptyState('🔍', 'No matches',
        'Nothing matched that query. Try fewer or different words.'));
      return;
    }
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    container.replaceChildren(
      el('p', { class: 'meta-item mb-4' },
        results.length + ' of ' + (data.total || results.length) + ' projects'),
      el('div', { class: 'grid-responsive-2' }, results.map(resultCard))
    );
  }

  function renderChips(container, facets, activeTags, q) {
    if (!container) return;
    var tags = (facets && facets.tags) || [];
    if (!tags.length) return;
    container.replaceChildren.apply(container, tags.slice(0, 8).map(function (t) {
      var active = activeTags.indexOf(t.value) !== -1;
      var href = '/search?q=' + encodeURIComponent(q);
      activeTags.forEach(function (t2) {
        if (t2 !== t.value) href += '&tag=' + encodeURIComponent(t2);
      });
      if (!active) href += '&tag=' + encodeURIComponent(t.value);
      return el('a', { class: 'filter-chip' + (active ? ' active' : ''), href: href },
        t.value + ' (' + t.count + ')');
    }));
  }

  // A local skeleton with skeleton.js's row classes, built as nodes.
  function loadingRows(n) {
    return el('div', {}, Array.apply(null, Array(n)).map(function () {
      return el('div', { class: 'skeleton-card skeleton-row' }, [
        el('span', { class: 'skeleton', style: 'width:2rem;height:2rem;border-radius:6px' }),
        el('div', { class: 'skeleton-col' }, [
          el('div', { class: 'skeleton skeleton-title' }),
          el('div', { class: 'skeleton skeleton-text' })
        ])
      ]);
    }));
  }

  document.addEventListener('DOMContentLoaded', function () {
    var container = document.getElementById('search-results');
    var chipsBox = document.getElementById('tag-chips');
    if (!container) return;

    var params = new URLSearchParams(window.location.search);
    var q = params.get('q') || '';
    var activeTags = params.getAll('tag');
    if (!q && !activeTags.length) return; // server-rendered empty state stays

    container.setAttribute('aria-busy', 'true');
    container.replaceChildren(window.ConcordSkeleton ? loadingRows(4)
      : el('div', { class: 'loading-spinner', role: 'status', 'aria-label': 'Searching' }));

    var qs = new URLSearchParams();
    if (q) qs.set('q', q);
    activeTags.forEach(function (t) { qs.append('tag', t); });

    window.ConcordApi.get('/api/v1/search?' + qs.toString())
      .catch(function () { return { results: [] }; })
      .then(function (data) {
        renderResults(container, data);
        renderChips(chipsBox, data.facets, activeTags, q);
        // A 404 or 401 resolves as SUCCESS with zero results, so the clear has
        // to be here as well or those statuses leave the region busy forever.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
      })
      .catch(function () {
        container.replaceChildren(emptyState('⚠️', 'Search failed',
          'The search service did not respond. Try again.'));
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
      });
  });
})();
