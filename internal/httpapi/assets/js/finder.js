// Concord Finder: the question-at-a-time discovery flow.
//
// Vanilla JS, no build step, matching assets/js/rank.js. All discovery logic is
// server-side in internal/finder; this file renders what it is told and posts
// answers back. It deliberately computes nothing about ranking -- a client-side
// re-ranking would be a second implementation of the same rule, and the two
// would disagree the moment a weight changed.
//
// Three states share one state object: seed, question, results. They are not
// three pages because the answers live in a process-local session that a page
// navigation would abandon.
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

  function headers() {
    var h = { 'Content-Type': 'application/json' };
    var t = token();
    if (t) h.Authorization = 'Bearer ' + t;
    return h;
  }

  var root = document.getElementById('finder-root');
  if (!root) return;

  // State is the single source of truth for what is on screen. Every render
  // reads it and only it, so there is no way for the DOM and the session to
  // disagree -- which is the usual bug in a hand-written flow like this.
  var state = { view: 'seed', session: null, question: null, selected: null, lastError: null };

  // Keyboard: 1-9 select, Enter confirms, S skips, Esc/Backspace goes back,
  // R reveals why, V jumps to results. Finder §4.2.
  var KEYMAP = { '1': 0, '2': 1, '3': 2, '4': 3, '5': 4, '6': 5, '7': 6, '8': 7, '9': 8 };

  function api(method, path, body) {
    return fetch(path, {
      method: method,
      headers: headers(),
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (r) {
      return r.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = { error: txt }; }
        if (!r.ok) {
          var err = new Error(data.error || ('HTTP ' + r.status));
          err.status = r.status;
          throw err;
        }
        return data;
      });
    });
  }

  function el(id) { return document.getElementById(id); }

  function show(view) {
    state.view = view;
    el('finder-seed').hidden = view !== 'seed';
    el('finder-flow').hidden = view !== 'question' && view !== 'stop';
    el('finder-results').hidden = view !== 'results';
    root.setAttribute('aria-busy', 'false');
  }

  function fail(err) {
    state.lastError = err;
    var box = el('finder-error');
    box.hidden = false;
    box.textContent = 'Finder could not continue: ' + (err && err.message ? err.message : err);
    if (err && err.status === 401) {
      box.innerHTML += ' <a href="/login">Sign in</a>';
    }
  }

  // ------------------------------------------------------------------ render

  function render() {
    if (state.view === 'seed') return renderSeed();
    if (state.view === 'question' || state.view === 'stop') return renderQuestion();
    if (state.view === 'results') return renderResults();
  }

  function renderSeed() {
    var box = el('finder-categories');
    // Categories come from the seed input, not from a hardcoded list, so a new
    // category in the catalog appears here without a code change. Until the
    // endpoint that returns them exists the list is the tag namespaces already
    // in use, which is what §3's picker is.
    var cats = ['productivity', 'developer-tools', 'media', 'self-hosted',
      'communication', 'data', 'security', 'infrastructure'];
    box.innerHTML = cats.map(function (c) {
      return '<button type="button" class="chip" data-category="' + esc(c) + '">' +
        esc(c) + '</button>';
    }).join('');
  }

  function renderQuestion() {
    var s = state.session;
    if (!s) return;
    var q = state.question;

    // Progress: "Question 3 of ~8". The total is deliberately approximate -- it
    // is the cap, not a promise, and printing an exact-looking number that the
    // engine then exceeds is how a flow loses trust.
    var asked = s.questions_asked || 0;
    el('finder-progress-text').textContent =
      asked === 0 ? 'Getting started' : 'Question ' + (asked + 1);
    var bar = el('finder-progress-bar');
    var pct = Math.min(100, Math.round((asked / 10) * 100));
    bar.setAttribute('aria-valuenow', String(pct));
    bar.style.width = pct + '%';

    if (!q) {
      el('finder-question-text').textContent = 'Nothing left to narrow down.';
      el('finder-options').innerHTML = '';
      el('finder-why').hidden = true;
      showResults();
      return;
    }

    el('finder-question-text').textContent = q.text || q.key;
    el('finder-why-text').textContent = q.why_asked || '';
    el('finder-why').hidden = !q.why_asked;

    // "Doesn't matter" (option id "any") is not a real option: it is the
    // non-filtering mode, and it is rendered as a separate button so nobody
    // reads it as "a project where this doesn't matter".
    var options = (q.options || []).filter(function (o) { return o.id !== 'any'; });
    el('finder-options').innerHTML = options.map(function (o, i) {
      var sel = state.selected === o.id ? ' selected' : '';
      return '<button type="button" class="finder-option' + sel + '" role="radio"' +
        ' aria-checked="' + (state.selected === o.id ? 'true' : 'false') + '"' +
        ' data-option="' + esc(o.id) + '">' +
        '<span class="finder-option-key">' + (i + 1) + '</span> ' +
        esc(o.label || o.id) +
        '<span class="finder-option-impact"></span></button>';
    }).join('') +
      '<button type="button" class="finder-option finder-option-any" data-option="any">' +
      "Doesn't matter</button>";

    el('finder-back').disabled = asked === 0;

    var stopping = state.view === 'stop' || s.should_suggest_stop;
    el('finder-stop-prompt').hidden = !stopping;
    if (stopping) {
      el('finder-stop-reason').textContent = stopReasonText(s.stop_reason);
      el('finder-stop-top').innerHTML = (s.top_candidates || []).slice(0, 3).map(function (c) {
        return '<li><strong>' + esc(c.name || c.slug) + '</strong> ' +
          Math.round((c.fit_score || 0) * 100) + '% fit</li>';
      }).join('');
    }

    renderShortList(s);
  }

  // "fit" is a share of AVAILABLE evidence, so it must say how much evidence
  // that was. Without the qualifier a 100% match on one known field looks
  // identical to a 100% match backed by the whole catalog.
  function fitLabel(c) {
    var pct = Math.round((c.fit_score || 0) * 100);
    var cov = typeof c.evidence_coverage === 'number' ? c.evidence_coverage : null;
    if (cov === null || cov >= 0.99) return pct + '% fit';
    return pct + '% fit <span class="finder-thin" title="only ' +
      Math.round(cov * 100) + '% of the scoring signals had evidence behind them">' +
      '(' + Math.round(cov * 100) + '% evidence)</span>';
  }

  function stopReasonText(reason) {
    switch (reason) {
      case 'SATURATED': return 'Only a handful of candidates are left.';
      case 'HIGH_CONFIDENCE': return 'The leader is well clear of the field.';
      case 'LOW_GAIN': return 'The next question would only slightly change this.';
      case 'QUESTION_CAP': return "That is as far as this round of questions goes.";
      case 'NO_MATCHES': return 'Nothing matches every answer so far.';
      default: return 'We could usefully ask more questions.';
    }
  }

  function renderShortList(s) {
    var was = s.initial_candidate_count || 0;
    var now = s.candidate_count || 0;
    el('finder-shortlist-summary').textContent =
      now + (was > now ? ' matches (was ' + was + ')' : ' matches');

    el('finder-shortlist-items').innerHTML = (s.top_candidates || []).map(function (c, i) {
      var warn = (c.unknown && c.unknown.length)
        ? '<span class="finder-warn" title="we have no data here">unknown: ' +
          esc(c.unknown.join(', ')) + '</span>'
        : '';
      return '<li class="finder-shortlist-item">' +
        '<span class="finder-rank">#' + (i + 1) + '</span>' +
        '<a href="/projects/' + esc(c.slug) + '">' + esc(c.name || c.slug) + '</a>' +
        '<span class="finder-fit">' + fitLabel(c) + '</span>' +
        warn + '</li>';
    }).join('');

    el('finder-answers').innerHTML = (s.answers || []).map(function (a) {
      return '<li>' + esc(a.dimension_key) + ': <strong>' + esc(a.option_id) +
        '</strong> <span class="finder-mode">(' + esc(a.mode) + ')</span></li>';
    }).join('');
  }

  function renderResults() {
    var s = state.session;
    if (!s) return;
    show('results');

    el('finder-results-answers').innerHTML =
      '<h3>Your answers</h3><ul>' + (s.answers || []).map(function (a) {
        return '<li>' + esc(a.dimension_key) + ': <strong>' + esc(a.option_id) +
          '</strong> (' + esc(a.mode) + ')</li>';
      }).join('') + '</ul>';

    var ranked = s.candidates || s.top_candidates || [];
    el('finder-ranked-heading').textContent = 'Ranked matches (' + ranked.length + ')';
    el('finder-ranked').innerHTML = ranked.map(function (c, i) {
      return '<li class="finder-result">' +
        '<span class="finder-rank">#' + (i + 1) + '</span> ' +
        '<a href="/projects/' + esc(c.slug) + '">' + esc(c.name || c.slug) + '</a> ' +
        '<span class="finder-fit">' + fitLabel(c) + '</span>' +
        '<button type="button" class="btn btn-ghost" data-why="' + esc(c.slug) + '">' +
        'Why this rank?</button>' +
        '</li>';
    }).join('');

    var unknown = s.unknown_fit || [];
    el('finder-unknown').hidden = unknown.length === 0;
    el('finder-unknown-items').innerHTML = unknown.map(function (c) {
      return '<li><a href="/projects/' + esc(c.slug) + '">' + esc(c.name || c.slug) + '</a> — missing: ' +
        esc((c.unknown || []).join(', ')) + '</li>';
    }).join('');

    var out = s.filtered_out || [];
    el('finder-filtered-out').hidden = out.length === 0;
    el('finder-filtered-items').innerHTML = out.map(function (f) {
      return '<li>filtered by: ' + esc((f.filters || []).join(', ')) +
        ' <button type="button" class="btn btn-ghost" data-lift="' + esc((f.filters || [])[0] || '') +
        '">Lift →</button></li>';
    }).join('');

    var nothing = (s.candidate_count || 0) === 0;
    el('finder-nothing').hidden = !nothing;
    if (nothing) {
      el('finder-nothing-detail').textContent =
        'The strictest filters right now are the ones above. Lifting one will ' +
        'bring projects back.';
    }

    el('finder-export-json').href = '/api/v1/finder/sessions/' + s.session_id + '/results';
  }

  // ------------------------------------------------------------------ actions

  function startSession(seed, category) {
    el('finder-error').hidden = true;
    return api('POST', '/api/v1/finder/sessions', {
      text: seed || '',
      category: category || '',
      seed_candidates: []
    }).then(function (s) {
      state.session = s;
      state.question = s.question;
      state.selected = null;
      show(s.question ? 'question' : 'results');
      if (!s.question) showResults();
      render();
    }).catch(fail);
  }

  function answer(mode, optionID) {
    var s = state.session;
    if (!s || !state.question) return;
    el('finder-error').hidden = true;
    return api('POST', '/api/v1/finder/sessions/' + s.session_id + '/answers', {
      question_key: state.question.key,
      option_id: optionID || '',
      mode: mode
    }).then(function (next) {
      applyState(next);
    }).catch(fail);
  }

  function applyState(next) {
    var prevCount = state.session ? state.session.candidate_count : null;
    state.session = next;
    state.question = next.question;
    state.selected = null;

    // §4.3 step 1: animate the change. The list is not re-rendered when the
    // count is unchanged, because re-rendering identical DOM under a CSS
    // transition looks like a flicker and reads as a bug.
    if (prevCount !== null && next.candidate_count !== prevCount) {
      var list = el('finder-shortlist-items');
      list.classList.remove('finder-changed');
      // Force a reflow so the class re-applies on consecutive answers.
      void list.offsetWidth;
      list.classList.add('finder-changed');
    }

    if (next.candidate_count === 0) showResults();
    else show(next.question ? 'question' : 'results');
    render();
    if (!next.question) showResults();
  }

  function goBack() {
    var s = state.session;
    if (!s || !(s.questions_asked > 0)) return;
    return api('POST', '/api/v1/finder/sessions/' + s.session_id + '/back', {})
      .then(applyState).catch(fail);
  }

  function lift(questionKey) {
    var s = state.session;
    if (!s || !questionKey) return;
    return api('POST', '/api/v1/finder/sessions/' + s.session_id + '/lift',
      { question_key: questionKey }).then(applyState).catch(fail);
  }

  function showResults() {
    var s = state.session;
    if (!s) return;
    return api('GET', '/api/v1/finder/sessions/' + s.session_id + '/results')
      .then(function (r) {
        state.session = Object.assign({}, s, {
          candidates: r.candidates,
          unknown_fit: r.unknown_fit,
          filtered_out: r.filtered_out,
          weights: r.weights,
          gaps: r.gaps
        });
        state.view = 'results';
        render();
      }).catch(function (err) {
        // Falling back to what the session already returned beats showing
        // nothing: the ranked short-list is still valid data.
        state.view = 'results';
        render();
        if (err && err.status === 404) return;
        fail(err);
      });
  }

  // ------------------------------------------------------------------ events

  root.addEventListener('click', function (ev) {
    var t = ev.target;

    if (t.id === 'finder-seed-form' || (t.tagName === 'FORM' && t.id === 'finder-seed-form')) {
      ev.preventDefault();
      startSession(el('finder-seed-text').value, state.category || '');
      return;
    }
    var cat = t.closest ? t.closest('[data-category]') : null;
    if (cat) {
      state.category = cat.getAttribute('data-category');
      var all = document.querySelectorAll('#finder-categories .chip');
      for (var i = 0; i < all.length; i++) {
        all[i].classList.toggle('chip-on', all[i] === cat);
      }
      return;
    }
    var opt = t.closest ? t.closest('[data-option]') : null;
    if (opt) {
      var id = opt.getAttribute('data-option');
      if (id === 'any') { answer('doesnt-matter', ''); return; }
      state.selected = id;
      renderQuestion();
      answer('required', id);
      return;
    }
    if (t.closest && t.closest('[data-mode]')) {
      answer(t.closest('[data-mode]').getAttribute('data-mode'), '');
      return;
    }
    if (t.closest && t.closest('[data-lift]')) {
      lift(t.closest('[data-lift]').getAttribute('data-lift'));
      return;
    }
    if (t.id === 'finder-back') { goBack(); return; }
    if (t.id === 'finder-stop' || t.id === 'finder-stop-go') { showResults(); return; }
    if (t.id === 'finder-stop-keep') {
      state.view = 'question';
      render();
      return;
    }
    if (t.id === 'finder-view-all') { showResults(); return; }
  });

  root.addEventListener('submit', function (ev) {
    if (ev.target.id === 'finder-seed-form') {
      ev.preventDefault();
      startSession(el('finder-seed-text').value, state.category || '');
    }
  });

  document.addEventListener('keydown', function (ev) {
    if (state.view === 'seed') return;
    var tag = (ev.target && ev.target.tagName) || '';
    if (tag === 'INPUT' || tag === 'TEXTAREA') return;

    if (ev.key in KEYMAP) {
      var options = Array.prototype.slice.call(
        document.querySelectorAll('#finder-options [data-option]'));
      var pick = options[KEYMAP[ev.key]];
      if (pick) {
        ev.preventDefault();
        pick.click();
      }
      return;
    }
    switch (ev.key) {
      case 'Enter':
        if (state.selected) { ev.preventDefault(); answer('required', state.selected); }
        break;
      case 's': case 'S':
        ev.preventDefault(); answer('skip', ''); break;
      case 'Escape': case 'Backspace':
        ev.preventDefault(); goBack(); break;
      case 'r': case 'R':
        ev.preventDefault();
        var why = el('finder-why');
        if (why) why.open = !why.open;
        break;
      case 'v': case 'V':
        ev.preventDefault(); showResults(); break;
    }
  });

  // Deep link: /finder?seed=note-taking pre-fills and starts (§3).
  var params = new URLSearchParams(window.location.search);
  var seedParam = params.get('seed');
  el('finder-seed-text').value = seedParam || '';
  show('seed');
  // render(), not just show(): the seed view is the one whose contents are
  // built by script, and calling only show() left the category row empty --
  // a page that looked loaded and offered nothing to click.
  render();
  if (seedParam) startSession(seedParam, '');
})();