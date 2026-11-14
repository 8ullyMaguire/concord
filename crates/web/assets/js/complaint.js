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
// array — is wrong here, and Array.isArray({feature_id:1, solutions:[]}) is false in
// the same way. Both directions are in the file's sibling; neither is guessable.
//
// THERE IS NO IMPACT READ ENDPOINT. The complaint object carries severity,
// frequency and strategic_multiplier — the three numbers an impact meter needs — so
// the meter is computed here from what the response already has. The
// POST .../impact route writes reports; nothing reads them back on this page. If a
// read endpoint is added later it should replace the computation, not sit beside
// it, or the page will show two numbers that disagree.

(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function where() {
    // /projects/{slug}/complaints/{id} splits into FOUR non-empty parts, so the slug
    // is index 1 and the id is index 3. See feature.js: reading parts[2] yields the
    // literal "complaints" and every fetch 400s.
    var parts = window.location.pathname.split('/').filter(Boolean);
    var i = parts.indexOf('complaints');
    return {
      slug: parts.length >= 2 ? decodeURIComponent(parts[1]) : '',
      id: i > 0 && parts.length > i + 1 ? decodeURIComponent(parts[i + 1]) : ''
    };
  }

  var root = document.getElementById('complaint-root');
  if (!root) return;

  function json(path) {
    return fetch(path, { headers: { Accept: 'application/json' } })
      .then(function (r) {
        if (r.status === 404) return null;
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      });
  }

  // Through the house helper, not by setting the attribute: a11y_test.go rejects a
  // page that does the latter, because done() is the one place that knows what else
  // must change when a region stops loading.
  function done() {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    else root.setAttribute('aria-busy', 'false');
  }

  // The impact meter.
  //
  // Three inputs, one number, and the inputs shown beside it. A bare score is
  // unreadable: 12 out of what? The meter is the visual, the arithmetic is printed
  // underneath, so a reader can check the claim rather than trust it.
  //
  // severity is a 1..5 judgement and frequency is a report count, so they are NOT on
  // one scale and must not be drawn as if they were. They are weighted separately and
  // their ranges are labelled.
  function impact(c) {
    var sev = typeof c.severity === 'number' ? c.severity : null;
    var freq = typeof c.frequency === 'number' ? c.frequency : null;
    var mult = typeof c.strategic_multiplier === 'number' ? c.strategic_multiplier : 1;

    if (sev == null) {
      return '<p class="muted">No severity recorded, so there is no impact score.</p>';
    }
    // Severity weighted twice as heavily as frequency: a severe bug reported twice
    // outranks a cosmetic annoyance reported forty times, which is the judgement
    // strategic_multiplier exists to override.
    var score = sev * 2 + Math.min(freq || 0, 10);
    var pct = Math.max(3, Math.min(100, Math.round((score / 20) * 100)));

    return '<div class="impact">' +
      '<div class="impact-value">' + esc(score) + '</div>' +
      '<div class="impact-track" role="img" aria-label="impact ' + esc(score) +
      ' out of 20, ' + esc(pct) + ' percent of the maximum">' +
      '<div class="impact-fill" style="width:' + pct + '%"></div></div>' +
      '<p class="muted">severity ' + esc(sev) + ' of 5 &times; 2' +
      (freq != null ? ' plus ' + esc(freq) + ' report' + (freq === 1 ? '' : 's') + ' (capped at 10)' : '') +
      ' &middot; strategic multiplier ' + esc(mult) +
      (mult !== 1 ? ' (a project can weight this pain up or down)' : '') +
      '</p></div>';
  }

  function featureRow(f) {
    var rd = typeof f.elo_rd === 'number' && isFinite(f.elo_rd) ? f.elo_rd : null;
    return '<li class="linked-row">' +
      '<a href="/projects/' + esc(f.project_slug || currentSlug) + '/features/' + esc(f.id) + '">' +
      esc(f.title || 'feature') + '</a>' +
      '<span class="badge badge-purple">' +
      (f.elo_r == null ? 'not rated' : 'rating ' + Math.round(f.elo_r) +
        (rd != null ? ' &plusmn;' + Math.round(rd) : '')) + '</span>' +
      '<span class="badge badge-slate">' + esc(f.status || 'proposed') + '</span>' +
      '</li>';
  }

  var currentSlug = '';

  function section(title, body) {
    return '<section class="mb-6"><h2 class="section-title">' + esc(title) +
      '</h2>' + body + '</section>';
  }

  function notFound(slug, id) {
    return '<div class="empty-state"><p class="empty-state-title">No such complaint</p>' +
      '<p class="muted">Complaint ' + esc(id) + ' does not exist in ' + esc(slug) +
      ', or it belongs to a project you cannot see.</p>' +
      '<p><a class="btn" href="/projects/' + esc(slug) + '">Back to the project</a></p></div>';
  }

  function errorBox(msg) {
    return '<div class="empty-state"><p class="empty-state-title">Could not load this complaint</p>' +
      '<p class="muted">' + esc(msg) + '</p></div>';
  }

  var at = where();
  if (!at.slug || !at.id) {
    root.innerHTML = notFound(at.slug, at.id);
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
      root.innerHTML = notFound(at.slug, at.id);
      done();
      return;
    }
    // A bare array here, NOT a wrapper. Guarded anyway: if this ever becomes
    // {features:[...]}, the page degrades to "none linked" rather than throwing.
    var features = Array.isArray(res[1]) ? res[1]
      : (res[1] && Array.isArray(res[1].features) ? res[1].features : []);

    var html = '<div class="page-head"><div>' +
      '<p class="breadcrumb"><a href="/projects">Projects</a> / ' +
      '<a href="/projects/' + esc(at.slug) + '">' + esc(at.slug) + '</a> / ' +
      '<span>Complaint</span></p>' +
      '<h1>' + esc(c.title || 'Untitled complaint') + ' ' +
      '<span class="badge badge-slate">' + esc(c.status || 'open') + '</span></h1>' +
      (c.body ? '<p class="lede">' + esc(c.body) + '</p>' : '') +
      '</div></div>';

    html += '<div class="card mb-6">' + impact(c) +
      (c.merged_into
        ? '<p class="muted">Merged into complaint ' + esc(c.merged_into) +
          '. This one is kept for the record and is no longer counted separately.</p>'
        : '') +
      (c.status !== 'validated'
        ? '<p class="muted">Not yet validated. §6.2 requires a validated complaint ' +
          'before a feature can be built from it, so until this reaches validated it ' +
          'ranks nothing.</p>'
        : '') +
      '</div>';

    html += section('Features answering this (' + features.length + ')',
      features.length
        ? '<ul class="linked-list">' + features.map(featureRow).join('') + '</ul>'
        : '<p class="muted">No feature is linked to this complaint yet. That is the ' +
          'usual state for a validated complaint: the complaint is the evidence, and ' +
          'the work is proposed separately.</p>');

    root.innerHTML = html;
    done();
  }).catch(function (err) {
    // aria-busy is cleared on the error path too: a skeleton that spins forever over
    // an error message reads as "still loading".
    root.innerHTML = errorBox(err && err.message ? err.message : String(err));
    done();
  });
})();