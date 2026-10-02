// Concord pairwise ranking.
//
// One comparison at a time, five possible answers, and the fifth matters most:
// "no preference" is real information about a pair, not a dismissal. The
// rating maths already distinguishes a skip from a loss, and folding them
// together would make the site unable to say "nobody wanted this" as opposed to
// "nobody had an opinion".
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function token() {
    try { return window.localStorage.getItem('concord.token') || ''; } catch (e) { return ''; }
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    // /projects/{slug}/rank
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  var root = document.getElementById('rank-root');
  var slug = slugFromPath();
  var cast = 0;

  // Outcome strings must match internal/ranking exactly:
  // OutcomeA "a", OutcomeB "b", OutcomeBoth "both", OutcomeNeither "neither",
  // OutcomeSkip "skip". TestAllFiveOutcomesAreAccepted pins the other end.
  var OUTCOMES = ['a', 'b', 'both', 'neither', 'skip'];

  function authHeaders() {
    var h = { 'Content-Type': 'application/json' };
    var t = token();
    if (t) h.Authorization = 'Bearer ' + t;
    return h;
  }

  function signInPrompt() {
    return '<div class="empty-state">' +
      '<div class="empty-state-icon">\u{1F511}</div>' +
      '<p class="empty-state-title">Sign in to rank</p>' +
      '<p class="empty-state-description">Votes are weighted by demonstrated ' +
      'contribution, so an account is what gives yours weight. ' +
      '<a href="/login?next=' + encodeURIComponent(window.location.pathname) + '">Sign in</a> ' +
      'or <a href="/register">create one</a> — registration takes a moment.</p></div>';
  }

  function errorState(message) {
    // A failure is rendered as a failure. Mapping a non-OK response to an empty
    // list is what made this site claim "No features yet" for a project with
    // three of them: the 404 looked like a valid empty answer, so nothing
    // prompted anyone to look.
    return '<div class="empty-state error-state">' +
      '<div class="empty-state-icon">\u26A0</div>' +
      '<p class="empty-state-title">Could not load a comparison</p>' +
      '<p class="empty-state-description">' + esc(message) + '</p>' +
      '<button class="btn btn-primary" id="rank-retry">Try again</button></div>';
  }

  function featureCard(f, side) {
    return '<article class="rank-card" data-side="' + side + '">' +
      '<header class="rank-card-head">' +
        '<span class="rank-label">Option ' + side + '</span>' +
      '</header>' +
      '<h2 class="rank-title">' + esc(f.title) + '</h2>' +
      '<p class="rank-body">' + esc(f.body || '') + '</p>' +
      '<p class="rank-meta">rating ' + Math.round(f.elo_r) +
        ' <span class="dim">(±' + Math.round(f.elo_rd) + ')</span></p>' +
      '</article>';
  }

  function renderPair(pair) {
    if (!pair || !pair.feature_a || !pair.feature_b) {
      root.innerHTML = '<div class="empty-state">' +
        '<div class="empty-state-icon">\u{1F3C1}</div>' +
        '<p class="empty-state-title">Nothing left to compare</p>' +
        '<p class="empty-state-description">You have been shown every pair. ' +
        'That is the whole sample, so the ranking reflects your answers and ' +
        'nothing more.</p>' +
        '<a class="btn btn-primary" href="/projects/' + encodeURIComponent(slug) + '/ranking">See the ranking</a>' +
        '</div>';
      var rp = document.getElementById('rank-progress');
      if (rp) rp.hidden = true;
      return;
    }
    var a = pair.feature_a, b = pair.feature_b;
    root.innerHTML =
      '<div class="rank-pair">' +
        featureCard(a, 'A') +
        featureCard(b, 'B') +
      '</div>' +
      '<div class="rank-actions" id="rank-actions">' +
        '<button class="btn btn-primary" data-outcome="a">A is better</button>' +
        '<button class="btn btn-primary" data-outcome="b">B is better</button>' +
        '<button class="btn" data-outcome="both">Both equally</button>' +
        '<button class="btn" data-outcome="neither">Neither</button>' +
        '<button class="btn btn-quiet" data-outcome="skip">No preference</button>' +
      '</div>' +
      '<p class="rank-note">A rating of 1500 ± 350 has barely been compared ' +
      'against anything. The ± is the uncertainty, and it is shown because ' +
      'a bare number would present noise as a measurement.</p>';

    var actions = document.getElementById('rank-actions');
    Array.prototype.forEach.call(actions.querySelectorAll('button'), function (btn) {
      btn.addEventListener('click', function () { submit(btn.getAttribute('data-outcome')); });
    });
  }

  function submit(outcome) {
    if (OUTCOMES.indexOf(outcome) === -1) return;
    var pair = window.__pair;
    if (!pair) return;
    var actions = document.getElementById('rank-actions');
    if (actions) actions.classList.add('rank-actions-busy');

    fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/features/' +
          pair.feature_a.id + '/vote', {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({
        feature_a: pair.feature_a.id,
        feature_b: pair.feature_b.id,
        outcome: outcome
      })
    }).then(function (res) {
      if (res.status === 401) { renderAnonymous(); return; }
      if (res.status === 403) {
        return res.json().then(function (b) {
          renderRefused(b);
        });
      }
      if (!res.ok) {
        return res.text().then(function (txt) {
          root.innerHTML = errorState('The vote was not recorded (HTTP ' + res.status + '). ' + txt);
        });
      }
      cast += 1;
      updateProgress();
      load();
    }).catch(function (err) {
      root.innerHTML = errorState(err && err.message ? err.message : String(err));
    });
  }

  function renderRefused(body) {
    var why = (body && (body.error || body.detail)) || 'That vote was refused.';
    var extra = (body && body.detail && body.detail !== body.error) ? body.detail : '';
    root.innerHTML = '<div class="empty-state error-state">' +
      '<div class="empty-state-icon">\u{1F6AB}</div>' +
      '<p class="empty-state-title">' + esc(why) + '</p>' +
      '<p class="empty-state-description">' + esc(extra) + '</p>' +
      '<a class="btn btn-primary" href="/projects/' + encodeURIComponent(slug) + '/ranking">See the ranking anyway</a>' +
      '</div>';
  }

  function renderAnonymous() {
    root.innerHTML = signInPrompt();
  }

  function updateProgress() {
    var el = document.querySelector('[data-cast]');
    if (el) el.textContent = String(cast);
    var box = document.getElementById('rank-progress');
    if (box) box.hidden = false;
  }

  function load() {
    fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/votes/next', {
      headers: authHeaders()
    }).then(function (res) {
      if (res.status === 401) { renderAnonymous(); return null; }
      if (res.status === 403) { renderAnonymous(); return null; }
      if (res.status === 404) {
        root.innerHTML = errorState('No project with that slug.');
        return null;
      }
      if (!res.ok) {
        return res.text().then(function (txt) {
          root.innerHTML = errorState('HTTP ' + res.status + ': ' + txt);
          return null;
        });
      }
      return res.json();
    }).then(function (pair) {
      // Every outcome above funnels through here: a pair, a 404, a 500, an
      // anonymous prompt. Clearing aria-busy in one place rather than at each
      // innerHTML assignment means the error and empty paths cannot be the two
      // that were forgotten -- and an error left aria-busy is an error a screen
      // reader never announces, because aria-busy suppresses live-region
      // updates inside the region it marks.
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
      if (pair === null) return;
      window.__pair = pair && pair.feature_a ? pair : null;
      renderPair(window.__pair);
    }).catch(function (err) {
      root.innerHTML = errorState(err && err.message ? err.message : String(err));
      if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    });
  }

  if (root) {
    load();
    document.addEventListener('click', function (e) {
      if (e.target && e.target.id === 'rank-retry') { window.__pair = null; load(); }
    });
  }
})();
