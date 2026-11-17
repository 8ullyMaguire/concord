// Concord pairwise ranking.
//
// One comparison at a time, five possible answers, and the fifth matters most:
// "no preference" is real information about a pair, not a dismissal. The
// rating maths already distinguishes a skip from a loss, and folding them
// together would make the site unable to say "nobody wanted this" as opposed to
// "nobody had an opinion".
//
// Rendering (step 2b): domEl() nodes only. Feature titles/bodies, refusal
// messages and all fallback states are text nodes or setAttribute values.
(function () {
  'use strict';
  var domEl = window.ConcordDom.el;

  // (No `token()` helper: the session is an HttpOnly cookie now, so a same-origin request authenticates without
  // JavaScript reading anything. The helper that used to sit here was never called.)

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

  function signInPrompt() {
    return domEl('div', { class: 'empty-state' }, [
      domEl('div', { class: 'empty-state-icon' }, '🔑'),
      domEl('p', { class: 'empty-state-title' }, 'Sign in to rank'),
      domEl('p', { class: 'empty-state-description' }, [
        'Votes are weighted by demonstrated contribution, so an account is what gives yours weight. ',
        domEl('a', { href: '/login?next=' + encodeURIComponent(window.location.pathname) }, 'Sign in'),
        ' or ', domEl('a', { href: '/register' }, 'create one'),
        ' — registration takes a moment.'
      ])
    ]);
  }

  // An empty state is not an error: it is the ordinary answer for a voter who
  // has nothing left to do, and it gets a forward link instead of a retry.
  function emptyState(icon, title, description) {
    return domEl('div', { class: 'empty-state' }, [
      domEl('div', { class: 'empty-state-icon' }, icon),
      domEl('p', { class: 'empty-state-title' }, title),
      domEl('p', { class: 'empty-state-description' }, description),
      domEl('a', { class: 'btn btn-primary', href: '/projects/' + encodeURIComponent(slug) + '/ranking' },
        'See the ranking anyway')
    ]);
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
    return domEl('div', { class: 'empty-state error-state' }, [
      domEl('div', { class: 'empty-state-icon' }, '⚠'),
      domEl('p', { class: 'empty-state-title' }, 'Could not load a comparison'),
      domEl('p', { class: 'empty-state-description' }, message),
      retriable === false ? null : domEl('button', { class: 'btn btn-primary', id: 'rank-retry' }, 'Try again')
    ]);
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
      return emptyState('📋',
        'Nothing to compare yet',
        'Ranking works by showing two features side by side. This project has ' +
        'fewer than two, so there is no pair to vote on. Propose a second ' +
        'feature and this page will have something to ask you.');
    }
    return emptyState('🙇',
      'You have voted on every pair here',
      'Nothing left to compare for this project. New features will appear on ' +
      'this page as they are proposed.');
  }

  // A card names the idea it shows and links to it. The link is in the card HEAD, above the
  // body: the whole point (spec §7) is that the pairwise page gains no destructive control and no
  // click target near the vote buttons, because a button that acts on the option under the
  // reader's cursor is a mis-click generator. The head is the far end of the card from
  // #rank-actions, and the href carries THIS card's id -- a shared or hardcoded href is how both
  // cards end up opening the same idea.
  function featureCard(f, side) {
    return domEl('article', {
      class: 'rank-card', 'data-side': side, 'data-feature': f.id
    }, [
      domEl('header', { class: 'rank-card-head' }, [
        domEl('span', { class: 'rank-label' }, 'Option ' + side),
        domEl('a', {
          class: 'rank-open',
          href: '/projects/' + encodeURIComponent(slug) + '/features/' + f.id
        }, 'Open the idea')
      ]),
      domEl('h2', { class: 'rank-title' }, f.title),
      domEl('p', { class: 'rank-body' }, f.body || ''),
      domEl('p', { class: 'rank-meta' }, [
        'rating ' + Math.round(f.elo_r) + ' ',
        domEl('span', { class: 'dim' }, '(±' + Math.round(f.elo_rd) + ')')
      ])
    ]);
  }

  function keyHint() {
    return domEl('p', { class: 'rank-keys' }, [
      'Keyboard: ', domEl('kbd', {}, '←'), ' A, ', domEl('kbd', {}, '→'), ' B, ',
      domEl('kbd', {}, '↑'), ' both, ', domEl('kbd', {}, '↓'), ' neither, ',
      domEl('kbd', {}, 'S'), ' skip.'
    ]);
  }

  function renderPair(pair) {
    if (!pair || !pair.feature_a || !pair.feature_b) {
      root.replaceChildren(domEl('div', { class: 'empty-state' }, [
        domEl('div', { class: 'empty-state-icon' }, '🏁'),
        domEl('p', { class: 'empty-state-title' }, 'Nothing left to compare'),
        domEl('p', { class: 'empty-state-description' },
          'You have been shown every pair. That is the whole sample, so the ranking ' +
          'reflects your answers and nothing more.'),
        domEl('a', { class: 'btn btn-primary', href: '/projects/' + encodeURIComponent(slug) + '/ranking' },
          'See the ranking')
      ]));
      var rp = document.getElementById('rank-progress');
      if (rp) rp.hidden = true;
      return;
    }
    var a = pair.feature_a, b = pair.feature_b;
    var actions = domEl('div', { class: 'rank-actions', id: 'rank-actions' }, OUTCOMES.map(function (o) {
      var labels = {
        a: 'A is better', b: 'B is better', both: 'Both equally',
        neither: 'Neither', skip: 'No preference'
      };
      var cls = o === 'skip' ? 'btn btn-quiet'
        : (o === 'both' || o === 'neither' ? 'btn' : 'btn btn-primary');
      return domEl('button', { class: cls, 'data-outcome': o }, labels[o]);
    }));
    root.replaceChildren(
      domEl('div', { class: 'rank-pair' }, [featureCard(a, 'A'), featureCard(b, 'B')]),
      actions,
      keyHint(),
      domEl('p', { class: 'rank-note' },
        'A rating of 1500 ± 350 has barely been compared against anything. The ± is ' +
        'the uncertainty, and it is shown because a bare number would present noise ' +
        'as a measurement.')
    );

    var buttons = actions.querySelectorAll('button');
    Array.prototype.forEach.call(buttons, function (btn) {
      btn.addEventListener('click', function () { submit(btn.getAttribute('data-outcome')); });
    });
  }

  // Keyboard shortcuts. Every key routes through submit(), the same function the
  // buttons call, so a key and a click cannot diverge in behaviour.
  var KEY_OUTCOMES = {
    ArrowLeft: 'a', ArrowRight: 'b', ArrowUp: 'both', ArrowDown: 'neither',
    s: 'skip', S: 'skip'
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

  function submit(outcome) {
    if (OUTCOMES.indexOf(outcome) === -1) return;
    var pair = window.__pair;
    if (!pair) return;
    var actions = document.getElementById('rank-actions');
    if (actions) actions.classList.add('rank-actions-busy');

    // One client (review item 9b). The three status branches this page distinguishes are still distinguished — 401 is
    // "you are anonymous", 403 is "the server refused you for a reason it will say", anything else is the vote not
    // landing — but they are read off `ApiError` instead of a raw response.
    window.ConcordApi.post('/api/v1/projects/' + encodeURIComponent(slug) + '/features/' +
          pair.feature_a.id + '/vote',
      { feature_a: pair.feature_a.id, feature_b: pair.feature_b.id, outcome: outcome })
      .then(function () {
        cast += 1;
        updateProgress();
        load();
      })
      .catch(function (err) {
        var status = err && err.status ? err.status : 0;
        if (status === 401) { renderAnonymous(); return; }
        if (status === 403) { renderRefused({ error: err.message }); return; }
        if (status) {
          root.replaceChildren(errorState('The vote was not recorded (HTTP ' + status + '). ' + err.message));
          return;
        }
        root.replaceChildren(errorState(err && err.message ? err.message : String(err)));
      });
  }

  function renderRefused(body) {
    var why = (body && (body.error || body.detail)) || 'That vote was refused.';
    var extra = (body && body.detail && body.detail !== body.error) ? body.detail : '';
    root.replaceChildren(domEl('div', { class: 'empty-state error-state' }, [
      domEl('div', { class: 'empty-state-icon' }, '🚫'),
      domEl('p', { class: 'empty-state-title' }, why),
      domEl('p', { class: 'empty-state-description' }, extra),
      domEl('a', { class: 'btn btn-primary', href: '/projects/' + encodeURIComponent(slug) + '/ranking' },
        'See the ranking anyway')
    ]));
  }

  function renderAnonymous() {
    root.replaceChildren(signInPrompt());
  }

  function updateProgress() {
    var el = document.querySelector('[data-cast]');
    if (el) el.textContent = String(cast);
    var box = document.getElementById('rank-progress');
    if (box) box.hidden = false;
  }

  function load() {
    window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/votes/next')
      // The refusals this page tells apart, mapped onto the same `null` the success path uses so the render below runs
      // once. `unretriable()` reads the status and the body, both of which `ApiError` carries.
      .catch(function (err) {
        var status = err && err.status ? err.status : 0;
        if (status === 401 || status === 403) { renderAnonymous(); return null; }
        if (status === 404) {
          root.replaceChildren(errorState('No project with that slug.'));
          return null;
        }
        if (status) {
          // A 409/422 body names why ("at least 2 features", "every pair voted"); a body that does not parse stays a
          // visible error, which is the safe direction.
          var body = null;
          try { body = JSON.parse(err.message); } catch (e) { body = { detail: err.message }; }
          var friendly = unretriable({ status: status }, body);
          root.replaceChildren(friendly || errorState('HTTP ' + status + ': ' + err.message));
          return null;
        }
        throw err;
      })
      .then(function (pair) {
        // Every outcome above funnels through here: a pair, a 404, a 500, an
        // anonymous prompt. Clearing aria-busy in one place rather than at each
        // render means the error and empty paths cannot be the two that were
        // forgotten -- and an error left aria-busy is an error a screen reader
        // never announces.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
        if (pair === null) return;
        window.__pair = pair && pair.feature_a ? pair : null;
        renderPair(window.__pair);
      }).catch(function (err) {
        root.replaceChildren(errorState(err && err.message ? err.message : String(err)));
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
