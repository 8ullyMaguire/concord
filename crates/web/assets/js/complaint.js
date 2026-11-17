// Concord complaint detail — one complaint, its impact meter, and the work built to
// answer it.
//
// frontend-spec.md's page ranking, item 4: "Carries impact meter, linked features,
// status."
//
// TWO SHAPES, MEASURED AGAINST A LIVE SERVER RATHER THAN INFERRED. They are not
// symmetric, which is exactly the kind of thing that produces a silently empty page:
//
//   GET .../complaints/{id}             -> the complaint object
//   GET .../complaints/{id}/features   -> a BARE ARRAY of features
//
// The sibling endpoint on the feature page (.../features/{id}/solutions) is a
// WRAPPED object {feature_id, solutions:[...]}, so the habit formed there — read an
// array — is wrong here, and Array.isArray({feature_id:1, solutions:[]}) is false.
//
// THERE IS NO IMPACT READ ENDPOINT. The complaint object carries severity,
// frequency and strategic_multiplier — the three numbers an impact meter needs — so
// the meter is computed here from what the response already has.
//
// Rendering (step 2b): `el()` nodes only. Titles, bodies, slugs, linked feature titles
// and error messages are text or attributes; there is no HTML string sink here.

(function () {
  'use strict';

  var el = window.ConcordDom.el;

  function where() {
    // /projects/{slug}/complaints/{id} splits into FOUR non-empty parts, so the slug
    // is index 1 and the id is index 3. Reading parts[2] yields the literal
    // "complaints" and every fetch 400s.
    var parts = window.location.pathname.split('/').filter(Boolean);
    var i = parts.indexOf('complaints');
    return {
      slug: parts.length >= 2 ? decodeURIComponent(parts[1]) : '',
      id: i > 0 && parts.length > i + 1 ? decodeURIComponent(parts[i + 1]) : ''
    };
  }

  var root = document.getElementById('complaint-root');
  if (!root) return;

  // A 404 here is `null` — "no such complaint" is a state this page renders, not a
  // failure — and it is `ApiError.status` that says so.
  function json(path) {
    return window.ConcordApi.get(path).catch(function (error) {
      if (error && error.status === 404) return null;
      throw error;
    });
  }

  // Through the house helper, not by setting the attribute: done() is the one place
  // that knows what else must change when a region stops loading.
  function done() {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    else root.setAttribute('aria-busy', 'false');
  }

  // Three inputs, one number, and the inputs shown beside it. A bare score is
  // unreadable: 12 out of what? Severity is a 1..5 judgement and frequency is a
  // report count, so they are NOT on one scale and are weighted separately.
  function impact(c) {
    var sev = typeof c.severity === 'number' ? c.severity : null;
    var freq = typeof c.frequency === 'number' ? c.frequency : null;
    var mult = typeof c.strategic_multiplier === 'number' ? c.strategic_multiplier : 1;

    if (sev == null) {
      return el('p', { class: 'muted' }, 'No severity recorded, so there is no impact score.');
    }
    // Severity weighted twice as heavily as frequency: a severe bug reported twice
    // outranks a cosmetic annoyance reported forty times.
    var score = sev * 2 + Math.min(freq || 0, 10);
    var pct = Math.max(3, Math.min(100, Math.round((score / 20) * 100)));
    return el('div', { class: 'impact' }, [
      el('div', { class: 'impact-value' }, score),
      el('div', {
        class: 'impact-track',
        role: 'img',
        'aria-label': 'impact ' + score + ' out of 20, ' + pct + ' percent of the maximum'
      }, el('div', { class: 'impact-fill', style: 'width:' + pct + '%' })),
      el('p', { class: 'muted' },
        'severity ' + sev + ' of 5 × 2' +
        (freq != null ? ' plus ' + freq + ' report' + (freq === 1 ? '' : 's') + ' (capped at 10)' : '') +
        ' · strategic multiplier ' + mult +
        (mult !== 1 ? ' (a project can weight this pain up or down)' : ''))
    ]);
  }

  var currentSlug = '';

  function featureRow(f) {
    var rd = typeof f.elo_rd === 'number' && isFinite(f.elo_rd) ? f.elo_rd : null;
    return el('li', { class: 'linked-row' }, [
      el('a', {
        href: '/projects/' + (f.project_slug || currentSlug) + '/features/' + f.id
      }, f.title || 'feature'),
      el('span', { class: 'badge badge-purple' },
        f.elo_r == null ? 'not rated' : 'rating ' + Math.round(f.elo_r) +
          (rd != null ? ' ±' + Math.round(rd) : '')),
      el('span', { class: 'badge badge-slate' }, f.status || 'proposed')
    ]);
  }

  function section(title, body) {
    return el('section', { class: 'mb-6' }, [
      el('h2', { class: 'section-title' }, title),
      body
    ]);
  }

  function notFound(slug, id) {
    return el('div', { class: 'empty-state' }, [
      el('p', { class: 'empty-state-title' }, 'No such complaint'),
      el('p', { class: 'muted' }, 'Complaint ' + id + ' does not exist in ' + slug +
        ', or it belongs to a project you cannot see.'),
      el('p', {}, el('a', { class: 'btn', href: '/projects/' + slug }, 'Back to the project'))
    ]);
  }

  function errorBox(msg) {
    return el('div', { class: 'empty-state' }, [
      el('p', { class: 'empty-state-title' }, 'Could not load this complaint'),
      el('p', { class: 'muted' }, msg)
    ]);
  }

  var at = where();
  if (!at.slug || !at.id) {
    root.replaceChildren(notFound(at.slug, at.id));
    done();
    return;
  }
  currentSlug = at.slug;

  var base = '/api/v1/projects/' + encodeURIComponent(at.slug);

  Promise.all([
    json(base + '/complaints/' + encodeURIComponent(at.id)),
    json(base + '/complaints/' + encodeURIComponent(at.id) + '/features')
  ]).then(function (res) {
    var c = res[0];
    if (!c) {
      root.replaceChildren(notFound(at.slug, at.id));
      done();
      return;
    }
    // A bare array here, NOT a wrapper. Guarded anyway: if this ever becomes
    // {features:[...]}, the page degrades to "none linked" rather than throwing.
    var features = Array.isArray(res[1]) ? res[1]
      : (res[1] && Array.isArray(res[1].features) ? res[1].features : []);

    var parts = [
      el('div', { class: 'page-head' },
        el('div', {}, [
          el('p', { class: 'breadcrumb' }, [
            el('a', { href: '/projects' }, 'Projects'),
            ' / ',
            el('a', { href: '/projects/' + at.slug }, at.slug),
            ' / ',
            el('span', {}, 'Complaint')
          ]),
          el('h1', {}, [
            c.title || 'Untitled complaint', ' ',
            el('span', { class: 'badge badge-slate' }, c.status || 'open')
          ]),
          c.body ? el('p', { class: 'lede' }, c.body) : null
        ])),
      el('div', { class: 'card mb-6' }, [
        impact(c),
        c.merged_into
          ? el('p', { class: 'muted' }, 'Merged into complaint ' + c.merged_into +
            '. This one is kept for the record and is no longer counted separately.')
          : null,
        c.status !== 'validated'
          ? el('p', { class: 'muted' }, 'Not yet validated. §6.2 requires a validated complaint ' +
            'before a feature can be built from it, so until this reaches validated it ranks nothing.')
          : null
      ]),
      section('Features answering this (' + features.length + ')',
        features.length
          ? el('ul', { class: 'linked-list' }, features.map(featureRow))
          : el('p', { class: 'muted' }, 'No feature is linked to this complaint yet. That is the ' +
            'usual state for a validated complaint: the complaint is the evidence, and ' +
            'the work is proposed separately.'))
    ];
    root.replaceChildren.apply(root, parts);
    done();
  }).catch(function (err) {
    // The error path clears aria-busy too: a spinning skeleton over an error reads
    // as "still loading".
    root.replaceChildren(errorBox(err && err.message ? err.message : String(err)));
    done();
  });
})();
