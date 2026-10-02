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

  // An empty state is not an error: it is the ordinary answer for a voter who
  // has nothing left to do, and it gets a forward link instead of a retry.
  function emptyState(icon, title, description) {
    return '<div class="empty-state">' +
      '<div class="empty-state-icon">' + icon + '</div>' +
      '<p class="empty-state-title">' + esc(title) + '</p>' +
      '<p class="empty-state-description">' + esc(description) + '</p>' +
      '<a class="btn btn-primary" href="/projects/' + encodeURIComponent(slug) +
      '/ranking">See the ranking anyway</a></div>';
  }

  function errorState(message, retriable) {
    // A failure is rendered as a failure. Mapping a non-OK response to an empty
    // list is what made this site claim "No features yet" for a project with
    // three of them: the 404 looked like a valid empty answer, so nothing
    // prompted anyone to look.
    //
    // `retriable` defaults to true, so an unexpected failure keeps its Try again
    // button. It is false for the states that retrying cannot fix -- a project
    // with fewer than two features, a voter with nothing left to compare --
    // because offering a button that fails identically is worse than no button.
    return '<div class="empty-state error-state">' +
      '<div class="empty-state-icon">\u26A0</div>' +
      '<p class="empty-state-title">Could not load a comparison</p>' +
      '<p class="empty-state-description">' + esc(message) + '</p>' +
      (retriable === false ? '' :
        '<button class="btn btn-primary" id="rank-retry">Try again</button>') + '</div>';
  }

  // 409 is the store's "a valid request the current state refuses". For this
  // endpoint that means one of two unretriable states: not enough features to
  // compare, or this voter has exhausted the pairs. Both are reported as an
  // empty state with a way forward rather than as an error with a dead button.
  function unretriable(res, body) {
    if (res.status !== 409 && res.status !== 422) return null;
    var why = (body && (body.error || body.detail)) || '';
    var few = /at least 2 features/i.test(why);
    if (few) {
      return emptyState('\u{1F4CB}',
        'Nothing to compare yet',
        'Ranking works by showing two features side by side. This project has ' +
        'fewer than two, so there is no pair to vote on. Propose a second ' +
        'feature and this page will have something to ask you.');
    }
    return emptyState('\u{1F64F}',
      'You have voted on every pair here',
      'Nothing left to compare for this project. New features will appear on ' +
      'this page as they are proposed.');
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
      keyHint() +
      '<p class="rank-note">A rating of 1500 ± 350 has barely been compared ' +
      'against anything. The ± is the uncertainty, and it is shown because ' +
      'a bare number would present noise as a measurement.</p>';

    var actions = document.getElementById('rank-actions');
    Array.prototype.forEach.call(actions.querySelectorAll('button'), function (btn) {
      btn.addEventListener('click', function () { submit(btn.getAttribute('data-outcome')); });
    });
  }

  // Keyboard shortcuts. Every key routes through submit(), the same function the
  // buttons call, so a key and a click cannot diverge in behaviour.
  //
  // The mapping is the one the frontend spec promises (7.2): arrows for the four
  // real outcomes plus skip. ArrowLeft means A and ArrowRight means B, which
  // reads left-to-right across the pair rather than encoding a preference
  // direction -- so it does not invert if the pair is ever presented B first.
  //
  // Guards, each for a concrete failure:
  //   - a pair must be on screen, so a stray arrow on the empty or signed-out
  //     states does nothing;
  //   - a key held down must not fire repeatedly, which would spend a voter's
  //     whole queue on one keypress;
  //   - keys are ignored while focus is in a text field or other editable
  //     context, where an arrow means cursor movement;
  //   - modified keystrokes are ignored, so a browser shortcut or an Alt+Left
  //     back-navigation is not swallowed.
  var KEY_OUTCOMES = {
    ArrowLeft: 'a',
    ArrowRight: 'b',
    ArrowUp: 'both',
    ArrowDown: 'neither',
    s: 'skip',
    S: 'skip'
  };

  function isTypingContext(el) {
    if (!el) return false;
    var tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' ||
      el.isContentEditable === true;
  }

  function onKeyDown(ev) {
    if (ev.defaultPrevented || ev.metaKey || ev.ctrlKey || ev.altKey) return;
    if (ev.repeat) return;                      // held key: one vote, not a queue
    if (isTypingContext(ev.target)) return;     // arrows still mean caret movement
    if (!window.__pair) return;                 // nothing to vote on
    var outcome = KEY_OUTCOMES[ev.key];
    if (!outcome) return;
    ev.preventDefault();
    submit(outcome);
  }

  function keyHint() {
    return '<p class="rank-keys">Keyboard: <kbd>&larr;</kbd> A, <kbd>&rarr;</kbd> B, ' +
      '<kbd>&uarr;</kbd> both, <kbd>&darr;</kbd> neither, <kbd>S</kbd> skip.</p>';
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
          // Parse the body so the 409 can be recognised. A malformed body falls
          // back to the old rendering, which is the safe direction: an
          // unrecognised failure stays a visible error.
          var body = null;
          try { body = JSON.parse(txt); } catch (e) { /* not JSON */ }
          var friendly = unretriable(res, body);
          root.innerHTML = friendly ||
            errorState('HTTP ' + res.status + ': ' + txt);
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
    // One listener for the page's life, rather than one per rendered pair: the
    // handler reads window.__pair, so a newly rendered pair is picked up without
    // rebinding, and re-binding on every render would leak one listener per vote.
    document.addEventListener('keydown', onKeyDown);
  }
})();
