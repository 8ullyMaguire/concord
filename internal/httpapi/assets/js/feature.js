// Concord feature detail — one page over a feature, its complaints, its solutions
// and its consensus state.
//
// frontend-spec.md 3.4 fixes the content and the order: title and status; I/E score
// and ratio; elo_r with elo_rd as an uncertainty bar; pain term and strategic term
// with their sources; linked complaints; consensus status; kanban card.
//
// Three decisions worth stating, because each is a way this page could be wrong:
//
// 1. elo_rd is drawn as a BAR, not printed as a number. Idea #37: "new and unsure"
//    must not read as "ranked low". A bare 1500 next to a mature 1500 is
//    indistinguishable from a tie, and the reader concludes the new feature lost.
//    The bar's width is elo_rd/350 clamped to 1, so 350+ (a fresh feature) fills
//    the track and the rating visibly means "nothing yet".
//
// 2. A MISSING elo_r is not rendered as 0. A feature that has never been compared
//    has no rating; showing 0 would place it below every real rating, which is a
//    claim the data does not make. It renders as "not yet compared".
//
// 3. Every interpolation goes through esc(). Title and body are authored free text
//    from twenty-odd call sites; impact_ratio and elo_r are numbers but arrive as
//    JSON and are not trusted to be finite.
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function where() {
    // /projects/{slug}/features/{id} splits into FOUR non-empty parts, so the slug
    // is index 1 and the id is index 3 -- NOT index 2.
    //
    // The first version of this read parts[2], which is the literal string
    // "features", so the page requested /features/features/solutions and got a 400
    // on all three fetches. It failed as an unhandled error box rather than as a
    // wrong-feature message, because the 400 was the loudest symptom of a parser
    // that was off by one segment. No test caught it: the Go tests assert on the
    // shell and on project.js, and the e2e suite did not exist yet.
    var parts = window.location.pathname.split('/').filter(Boolean);
    var i = parts.indexOf('features');
    return {
      slug: parts.length >= 2 ? decodeURIComponent(parts[1]) : '',
      id: i > 0 && parts.length > i + 1 ? decodeURIComponent(parts[i + 1]) : ''
    };
  }

  var root = document.getElementById('feature-root');
  if (!root) return;

  function json(path) {
    return fetch(path, { headers: { Accept: 'application/json' } })
      .then(function (r) {
        if (r.status === 404) return null;
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      });
  }

  function done() { root.setAttribute('aria-busy', 'false'); }

  // A rating that has never been compared is not zero. Zero is a real rating that
  // lost every comparison, and the two must not look alike.
  function rating(f) {
    if (f.elo_r == null) {
      return '<p class="muted">Not yet compared. A feature has no rating until it ' +
        'has been through at least one pairwise comparison.</p>';
    }
    var rd = typeof f.elo_rd === 'number' && isFinite(f.elo_rd) ? f.elo_rd : 0;
    // 350 is the starting deviation, so it is the full-width case.
    var pct = Math.max(4, Math.min(100, Math.round((rd / 350) * 100)));
    return '<div class="rating">' +
      '<div class="rating-value">' + esc(Math.round(f.elo_r)) + '</div>' +
      '<div class="rating-bars" role="img" aria-label="rating ' +
      esc(Math.round(f.elo_r)) + ' plus or minus ' + esc(Math.round(rd)) +
      ', ' + esc(pct) + ' percent of the maximum uncertainty">' +
      '<div class="rating-bar-track"><div class="rating-bar-fill" style="width:' +
      pct + '%"></div></div></div>' +
      '<p class="muted">&plusmn;' + esc(Math.round(rd)) +
      ' &mdash; a wider bar means less certainty, not a worse rank.</p>' +
      '</div>';
  }

  // impact_ratio is the number the ranked list sorts on, so it is shown with its
  // inputs. A ratio with no visible numerator reads as a magic number.
  function impactEffort(f) {
    var imp = f.impact, eff = f.effort_score;
    if (imp == null || eff == null) {
      return '<p class="muted">No impact or effort recorded yet, so this feature ' +
        'is not in the ranked order.</p>';
    }
    var ratio = f.impact_ratio;
    var shown = ratio == null ? (imp / eff).toFixed(2) : Number(ratio).toFixed(2);
    return '<div class="ie-score"><span class="ie-value">' + esc(shown) +
      '</span><span class="muted">impact ' + esc(imp) + ' &divide; effort ' +
      esc(eff) + (eff ? ' (' + esc(f.effort || 'unsized') + ')' : '') +
      '</span></div>';
  }

  function complaintRow(c) {
    return '<li class="linked-row">' +
      '<span class="badge badge-slate">#' + esc(c.id) + '</span>' +
      '<span>' + esc(c.title || 'complaint ' + c.id) + '</span>' +
      (c.severity != null
        ? '<span class="badge badge-slate">severity ' + esc(c.severity) + '</span>'
        : '') +
      '<span class="badge badge-slate">' + esc(c.status || 'unknown') + '</span>' +
      '</li>';
  }

  function solutionRow(s) {
    var rd = typeof s.elo_rd === 'number' && isFinite(s.elo_rd) ? s.elo_rd : null;
    return '<li class="linked-row">' +
      '<span class="badge badge-purple">rating ' +
      esc(Math.round(s.elo_r || 0)) +
      (rd != null ? ' &plusmn;' + esc(Math.round(rd)) : '') + '</span>' +
      '<span>' + esc(s.title || 'solution') + '</span>' +
      '<span class="badge badge-slate">' + esc(s.status || 'proposed') + '</span>' +
      '</li>';
  }

  function section(title, body) {
    return '<section class="mb-6"><h2 class="section-title">' + esc(title) +
      '</h2>' + body + '</section>';
  }

  function notFound(slug, id) {
    return '<div class="empty-state"><p class="empty-state-title">No such feature</p>' +
      '<p class="muted">Feature ' + esc(id) + ' does not exist in ' + esc(slug) +
      ', or it belongs to a project you cannot see.</p>' +
      '<p><a class="btn" href="/projects/' + esc(slug) + '">Back to the project</a></p></div>';
  }

  function errorBox(msg) {
    return '<div class="empty-state"><p class="empty-state-title">Could not load this feature</p>' +
      '<p class="muted">' + esc(msg) + '</p></div>';
  }

  var at = where();
  if (!at.slug || !at.id) {
    root.innerHTML = notFound(at.slug, at.id);
    done();
    return;
  }

  var base = '/api/v1/projects/' + encodeURIComponent(at.slug);

  Promise.all([
    json(base + '/features/' + encodeURIComponent(at.id)),
    json(base + '/features/' + encodeURIComponent(at.id) + '/solutions'),
    json(base + '/features/' + encodeURIComponent(at.id) + '/consensus'),
    // Complaints come from their OWN endpoint. linked_complaints is write-only, so
    // the feature object does not carry them and reading f.linked_complaints yields
    // undefined -- which rendered as "no complaints", the exact opposite of the
    // truth for a feature whose whole rank rests on a complaint.
    json(base + '/features/' + encodeURIComponent(at.id) + '/complaints')
  ]).then(function (res) {
    var f = res[0];
    if (!f) {
      root.innerHTML = notFound(at.slug, at.id);
      done();
      return;
    }
    // BOTH sibling endpoints wrap their payload, and getting this wrong is silent:
    // a bare array read off {feature_id, solutions} yields undefined, the solutions
    // section renders "0", and the page looks like a feature nobody proposed
    // anything for. Measured against the live server, not assumed:
    //
    //   GET .../features/{id}             -> the feature object
    //   GET .../features/{id}/solutions   -> {feature_id, solutions:[...]}
    //   GET .../features/{id}/consensus   -> {feature_id, leader_id, runner_up_id,
    //                                        baseline_id, ready, ...}
    var solutions = (res[1] && res[1].solutions) || [];
    if (!Array.isArray(solutions)) solutions = [];
    var call = res[2] || {};
    var complaints = Array.isArray(res[3]) ? res[3] : [];

    var html = '<div class="page-head"><div>' +
      '<p class="breadcrumb"><a href="/projects">Projects</a> / ' +
      '<a href="/projects/' + esc(at.slug) + '">' + esc(at.slug) + '</a> / ' +
      '<span>Feature</span></p>' +
      '<h1>' + esc(f.title || 'Untitled feature') + ' ' +
      '<span class="badge badge-slate">' + esc(f.status || 'proposed') + '</span></h1>' +
      (f.body ? '<p class="lede">' + esc(f.body) + '</p>' : '') +
      '</div></div>';

    html += '<div class="card mb-6">' + rating(f) + impactEffort(f) +
      '<p class="muted">Strategic weight ' +
      esc(f.strategic_weight == null ? 1 : f.strategic_weight) +
      ' &mdash; a multiplier on priority, proposed and ratified rather than set by ' +
      'hand.</p></div>';

    html += section('Solutions (' + solutions.length + ')',
      solutions.length
        ? '<ul class="linked-list">' + solutions.map(solutionRow).join('') + '</ul>'
        : '<p class="muted">No solutions proposed yet.</p>');

    // Consensus here is the SOLUTION ARENA's readiness for this feature, not a call
    // with an open/closed status: the endpoint returns leader_id, runner_up_id,
    // baseline_id and a `ready` flag. Rendering a status string for it would
    // invent a state the API never reported.
    if (Object.keys(call).length) {
      html += section('Consensus',
        (call.ready
          ? '<p class="muted">The solution arena for this feature has a baseline '
            + 'and enough comparisons to call a result.</p>'
          : '<p class="muted">The solution arena for this feature has not called a '
            + 'result yet. A leader and a runner-up mean one comparison is not a '
            + 'verdict.</p>') +
        '<p class="muted">' +
        (call.baseline_id ? 'baseline solution ' + esc(call.baseline_id) : 'no baseline yet') +
        (call.leader_id ? ' \u00b7 leader ' + esc(call.leader_id) : '') +
        (call.runner_up_id ? ' \u00b7 runner-up ' + esc(call.runner_up_id) : '') +
        '</p>');
    }

    // The complaints section, always rendered: an empty list must SAY it has none,
    // because "this feature has no validated complaints" is the answer to "why is
    // this ranked here?" for most features and the reader cannot guess it.
    html += section('Complaints (' + complaints.length + ')',
      complaints.length
        ? '<ul class="linked-list">' + complaints.map(complaintRow).join('') + '</ul>'
        : '<p class="muted">No validated complaint is linked to this feature, so ' +
          'its rank rests on comparison alone rather than on reported pain.</p>');

    root.innerHTML = html;
    done();
  }).catch(function (err) {
    // aria-busy is cleared on the error path too: a skeleton that spins forever
    // over an error message reads as "still loading".
    root.innerHTML = errorBox(err && err.message ? err.message : String(err));
    done();
  });
})();