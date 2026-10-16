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

  // aria-busy is cleared through the house helper, not by setting the attribute.
  //
  // The a11y suite rejects a page that does the latter
  // (TestAriaBusyIsClearedWhenContentArrives): done() is the single place that knows
  // what else has to change when a region stops loading, and a page that sets the
  // attribute itself announces results while still looking unfinished. It also keeps
  // this page correct if the helper grows -- the first version here set the attribute
  // and failed the gate.
  function done() {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    else root.setAttribute('aria-busy', 'false');
  }

  // wireContests attaches §6.4's challenge flow: open the reason field, send the
  // contest, and report the SERVER's refusal in the row it belongs to.
  //
  // The form is inline and per-claim rather than a modal, because a modal would
  // have to carry the claim whose text it is about -- and the reason someone
  // contests a claim is read off the claim.
  function wireContests(base, featureID) {
    var contests = root.querySelectorAll('[data-contest]');
    for (var i = 0; i < contests.length; i++) {
      (function (btn) {
        btn.addEventListener('click', function () {
          var row = btn.closest('.coverage-claim');
          var form = row && row.querySelector('.coverage-contest-form');
          if (form) form.hidden = !form.hidden;
        });
      })(contests[i]);
    }
    var sends = root.querySelectorAll('[data-contest-send]');
    for (var j = 0; j < sends.length; j++) {
      (function (btn) {
        btn.addEventListener('click', function () {
          var row = btn.closest('.coverage-claim');
          if (!row) return;
          var input = row.querySelector('[data-contest-reason]');
          var err = row.querySelector('[data-contest-error]');
          var reason = input ? String(input.value || '').trim() : '';
          if (!reason) {
            if (err) { err.textContent = 'A challenge needs its reason: the claim\u2019s author has to know what to answer.'; err.hidden = false; }
            return;
          }
          btn.disabled = true;
          var path = base + '/features/' + encodeURIComponent(featureID) + '/solutions/' +
            encodeURIComponent(btn.getAttribute('data-contest-send')) + '/coverage/' +
            encodeURIComponent(btn.getAttribute('data-contest-send-complaint')) + '/contest';
          // authHeaders() from session.js, the one place that knows the key: reaching
          // into localStorage here would be a second copy of the token's spelling.
          var headers = { 'Content-Type': 'application/json' };
          var sess = window.ConcordSession && window.ConcordSession.instance();
          if (sess) {
            var auth = sess.authHeaders();
            for (var k in auth) {
              if (Object.prototype.hasOwnProperty.call(auth, k)) headers[k] = auth[k];
            }
          }
          fetch(path, {
            method: 'POST',
            headers: headers,
            body: JSON.stringify({ reason: reason })
          }).then(function (r) {
            return r.text().then(function (txt) {
              var body = {};
              try { body = txt ? JSON.parse(txt) : {}; } catch (e) { body = {}; }
              if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
              return body;
            });
          }).then(function () {
            // Re-read: coverage is recomputed server-side and the contested claim
            // changes that number, so a locally patched row would show the old one.
            window.location.reload();
          }).catch(function (e) {
            btn.disabled = false;
            if (err) { err.textContent = e.message; err.hidden = false; }
          });
        });
      })(sends[j]);
    }
  }

  // Adding and removing a feature's tags. Both are contributor writes, and both re-read the
  // page: the tag changes which themes apply, so the priority the page is explaining has
  // just changed, and a locally patched list would show the old one.
  //
  // The refusal is reported in the server's words, because the interesting refusals here are
  // specific: an unknown tag names the route that proposes one, and a non-contributor is
  // told which role is needed.
  function wireTags(base, featureID) {
    var err = root.querySelector('[data-tag-error]');
    function fail(msg) {
      if (err) { err.textContent = msg; err.hidden = false; }
    }
    function headers() {
      // authHeaders() from session.js, the one place that knows the key.
      var h = { 'Content-Type': 'application/json' };
      var sess = window.ConcordSession && window.ConcordSession.instance();
      if (sess) {
        var auth = sess.authHeaders();
        for (var k in auth) {
          if (Object.prototype.hasOwnProperty.call(auth, k)) h[k] = auth[k];
        }
      }
      return h;
    }
    function send(path, payload, btn) {
      if (btn) btn.disabled = true;
      fetch(path, { method: 'POST', headers: headers(), body: JSON.stringify(payload) })
        .then(function (r) {
          return r.text().then(function (txt) {
            var body = {};
            try { body = txt ? JSON.parse(txt) : {}; } catch (e) { body = {}; }
            if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
            window.location.reload();
          });
        })
        .catch(function (e) {
          if (btn) btn.disabled = false;
          fail(e.message || String(e));
        });
    }
    var form = document.getElementById('add-tag-form');
    if (form) {
      form.addEventListener('submit', function (ev) {
        ev.preventDefault();
        var input = form.querySelector('#add-tag');
        var tag = input ? String(input.value || '').trim() : '';
        if (!tag) { fail('Type a tag first: an empty tag steers nothing.'); return; }
        send(base + '/features/' + encodeURIComponent(featureID) + '/tags', { tag: tag },
          form.querySelector('button[type=submit]'));
      });
    }
    var removes = root.querySelectorAll('[data-tag-remove]');
    for (var i = 0; i < removes.length; i++) {
      (function (btn) {
        btn.addEventListener('click', function () {
          send(base + '/features/' + encodeURIComponent(featureID) + '/tags/remove',
            { tag: btn.getAttribute('data-tag-remove') }, btn);
        });
      })(removes[i]);
    }
  }

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

  // §6.4's coverage claims, one line each, with the challenge beside them.
  //
  // The contest endpoint has existed with a handler, a self-vote rule and a store
  // test, and there was NO WAY TO REACH IT: a challenge names a claim, and no read
  // exposed which claims a solution makes. The claims now arrive with the solution
  // (the same endpoint), which is what makes the control possible at all.
  //
  // The page does not decide who may contest one. The store refuses the claimant's
  // own challenge (§5.2's self-vote rule), and the refusal is what the page shows.
  function claimRows(s) {
    var claims = Array.isArray(s.claims) ? s.claims : [];
    if (!claims.length) return '';
    return '<ul class="coverage-claims" data-coverage-claims>' + claims.map(function (c) {
      var contested = c.contested
        ? ' <span class="badge badge-amber" title="' + esc(c.contest_reason || '') +
          '">contested</span>'
        : '';
      return '<li class="coverage-claim" data-claim="' + esc(c.complaint_id) + '">' +
        '<span class="coverage-claim-text">' +
        esc(c.claim === 'leaves-unresolved' ? 'leaves complaint #' : 'resolves complaint #') +
        esc(c.complaint_id) + '</span>' + contested +
        '<button type="button" class="btn btn-secondary btn-sm" data-contest="' +
        esc(s.solution_id) + '" data-contest-complaint="' + esc(c.complaint_id) + '">' +
        'Contest</button>' +
        '<span class="coverage-contest-form" hidden>' +
        '<label class="field-label" for="contest-reason-' + esc(s.solution_id) + '-' +
        esc(c.complaint_id) + '">Why the claim does not stand</label>' +
        '<input type="text" class="input" id="contest-reason-' + esc(s.solution_id) + '-' +
        esc(c.complaint_id) + '" data-contest-reason placeholder="e.g. it only fixes the ' +
        'first page of results">' +
        '<button type="button" class="btn" data-contest-send="' + esc(s.solution_id) +
        '" data-contest-send-complaint="' + esc(c.complaint_id) + '">Record the challenge</button>' +
        '</span>' +
        '<span class="coverage-contest-error" role="alert" data-contest-error hidden></span>' +
        '</li>';
    }).join('') + '</ul>';
  }

  function solutionRow(s) {
    var rd = typeof s.elo_rd === 'number' && isFinite(s.elo_rd) ? s.elo_rd : null;
    return '<li class="linked-row">' +
      '<span class="badge badge-purple">rating ' +
      esc(Math.round(s.elo_r || 0)) +
      (rd != null ? ' &plusmn;' + esc(Math.round(rd)) : '') + '</span>' +
      '<span>' + esc(s.title || 'solution') + '</span>' +
      '<span class="badge badge-slate">' + esc(s.status || 'proposed') + '</span>' +
      claimRows(s) +
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
    json(base + '/features/' + encodeURIComponent(at.id) + '/complaints'),
    // §6.2's steering input, read on the page it steers: a feature's tags decide which
    // ratified themes count toward its priority. The read half was missing too -- the
    // table that stores them had no writer AND no reader.
    json(base + '/features/' + encodeURIComponent(at.id) + '/tags')
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

    // The tags section, always rendered: a feature with no tags scores with no theme at
    // all, and that is the answer to "why does this not move with the theme we ratified?".
    var tagList = (res[4] && res[4].tags) || [];
    html += section('Tags (' + tagList.length + ')',
      (tagList.length
        ? '<ul class="tag-list" data-feature-tags>' + tagList.map(function (tg) {
            return '<li class="tag-pill">' + esc(tg) +
              ' <button type="button" class="btn btn-quiet" data-tag-remove="' + esc(tg) +
              '">Remove</button></li>';
          }).join('') + '</ul>'
        : '<p class="muted">No tags. A tag is what connects this feature to a ratified ' +
          'theme, so a theme only steers a feature that carries its tag.</p>') +
      '<form id="add-tag-form" class="inline-form">' +
      '<label class="form-label" for="add-tag">Add a tag</label>' +
      '<input id="add-tag" name="tag" maxlength="60" placeholder="an existing tag">' +
      '<button type="submit" class="btn">Add</button>' +
      '</form>' +
      '<p class="form-status" data-tag-error hidden></p>');

    root.innerHTML = html;
    done();
    wireContests(base, at.id);
    wireTags(base, at.id);
  }).catch(function (err) {
    // aria-busy is cleared on the error path too: a skeleton that spins forever
    // over an error message reads as "still loading".
    root.innerHTML = errorBox(err && err.message ? err.message : String(err));
    done();
  });
})();