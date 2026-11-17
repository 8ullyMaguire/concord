// The alternatives arena's loop: the pair to judge, the log the ranking is derived from, the replay that proves it.
//
// Split out of project.js (review item 9's "split by capability"): these 220 lines share nothing with the rest of the
// overview beyond `esc`, the API client and the two helpers the caller hands in — `setStatus` and `postJSON` keep their one
// definition in project.js rather than being copied here, which is the whole reason this is a split and not a fork.
//
// Loaded before project.js; registers itself on window, like every other asset here.
(function () {
  var esc = window.ConcordEscape.esc;

// --- §5's arena loop, per arena -------------------------------------------
//
// Before §29 this panel could only READ a ranking. The API had no route that
// would hand a voter the comparison to judge, none for the append-only log the
// ranking is computed from, and none that could check the result, so an
// alternatives arena was a leaderboard that nothing on the page could move. The
// three controls here are the visible half of that loop:
//
//   - the pair to judge, with the ratings it is being judged on,
//   - the log the ranking is derived from (§5.2's audit view),
//   - the replay that proves it (§5.1's "Anyone can verify a ranking"), and the
//     repair, which reports what it changed rather than quietly rewriting.
function arenaControls(a) {
  // `a.arena_id`, not `a.id`: the alternatives response spells it the way the
  // route does, and the first version read `a.id` -- which is undefined, so the
  // control posted to /arenas/undefined/... and the server answered 400. The
  // browser test caught it; every API test passed.
  var id = esc(a.arena_id);
  // One line, marker and root together: the panel root-element tests read a
  // state from its own return line, and a version split across lines reported
  // "no return produces this state" for a state that existed.
  return '<div class="arena-loop">' +
    '<button type="button" class="btn btn-quiet" data-arena-pair="' + id + '">Judge the next pair</button>' +
    '<button type="button" class="btn btn-quiet" data-arena-log="' + id + '">Vote log</button>' +
    '<button type="button" class="btn btn-quiet" data-arena-verify="' + id + '">Verify ranking</button>' +
    '</div>' +
    '<div class="arena-pair" data-arena-pair-out="' + id + '" role="status" aria-live="polite"></div>' +
    '<div class="arena-log" data-arena-log-out="' + id + '"></div>' +
    '<div class="arena-verify" data-arena-verify-out="' + id + '"></div>';
}

// The three panels below are rendered with domEl(), never with an HTML string.
// They are the ONLY renderers of their data: the old *HTML builders duplicated
// this markup as a string, and the string and DOM versions drifted (the DOM one
// lost the vote weight, then the string one lost the reason). A single tree
// builder per panel is the fix.
function note(msg) {
  return window.ConcordDom.el('p', { class: 'arena-note' }, msg);
}

function arenaSide(s) {
  var el = window.ConcordDom.el;
  return el('div', { class: 'pair-side', 'data-pair-side': s.entity_id }, [
    el('span', { class: 'pair-label' }, s.label || ('entity ' + s.entity_id)),
    s.is_baseline ? el('span', { class: 'pair-baseline' }, 'do nothing') : null,
    el('span', { class: 'pair-rating' },
      'rating ' + Math.round(s.r || 0) + ' ± ' + Math.round(s.rd || 0)),
    el('span', { class: 'pair-confidence' },
      Math.round(100 * (s.win_probability || 0)) + '% to win')
  ]);
}

// arenaPairForm is the pair AND the control that judges it. Rendering the pair
// without the control would put a voter in front of a question they cannot answer.
function arenaPairForm(arenaID, pair) {
  var el = window.ConcordDom.el;
  var options = [['a', 'A wins'], ['b', 'B wins'], ['both', 'A draw'],
                 ['neither', 'Neither is worth it'], ['skip', 'I cannot tell']];
  var radios = options.map(function (o, i) {
    return el('label', { class: 'pair-choice' }, [
      el('input', { type: 'radio', name: 'outcome', value: o[0], checked: i === 0 ? true : null }),
      ' ' + o[1]
    ]);
  });
  return el('form', {
    class: 'pair-form', 'data-vote-pair': arenaID,
    'data-side-a': pair.a.entity_id, 'data-side-b': pair.b.entity_id
  }, [
    el('p', { class: 'pair-question' }, pair.question || 'Which is better?'),
    el('div', { class: 'pair-sides' }, [arenaSide(pair.a), arenaSide(pair.b)]),
    el('div', { class: 'pair-choices' }, radios),
    el('label', { class: 'pair-reason' }, [
      'Why (optional)',
      el('input', { type: 'text', name: 'reason', maxlength: '280',
        placeholder: 'one line, optional' })
    ]),
    el('button', { type: 'submit', class: 'btn btn-primary' }, 'Record vote'),
    el('p', { class: 'form-status', 'data-status': '', hidden: '' })
  ]);
}

function arenaLog(votes) {
  var el = window.ConcordDom.el;
  if (!votes.length) {
    return note('No comparisons recorded yet. The ranking starts at the prior until somebody judges a pair.');
  }
  return el('ul', { class: 'arena-log-list' }, votes.map(function (v) {
    var winner = v.outcome === 'a' ? v.a.label : (v.outcome === 'b' ? v.b.label : v.outcome);
    // The WEIGHT is shown, because it is the number that decides how far the vote
    // moved the ranking and §5.2 makes this view the audit trail an appeal is argued
    // from. §30 found the weight being computed, stored, and honoured by nothing.
    var w = (typeof v.weight === 'number') ? v.weight : Number(v.weight);
    var shown = isFinite(w) ? (w >= 1 ? String(Math.round(w * 100) / 100) : w.toFixed(4)) : '?';
    var rows = [
      el('span', { class: 'log-winner' }, winner),
      el('span', { class: 'log-pair' }, v.a.label + ' vs ' + v.b.label),
      el('span', { class: 'log-weight' }, 'weight ' + shown)
    ];
    if (v.reason) rows.push(el('span', { class: 'log-reason' }, v.reason));
    return el('li', { class: 'arena-log-row' }, rows);
  }));
}

function arenaVerify(verdict) {
  var el = window.ConcordDom.el;
  // NO ENTRIES IS NOT AGREEMENT. An empty comparison set reproduces itself and
  // says nothing; the panel states which case it is rather than rendering the
  // reassuring one.
  var n = (verdict.entries || []).length;
  var head = n === 0
    ? el('p', { class: 'arena-note arena-empty' },
        'This arena has no competitors to verify yet: the log is empty.')
    : verdict.agrees
    ? el('p', { class: 'arena-note arena-agrees' },
        'The vote log reproduces every rating in this arena.')
    : el('p', { class: 'arena-note arena-disagrees' },
        verdict.divergent + ' entr' + (verdict.divergent === 1 ? 'y is' : 'ies are') +
        ' not what the vote log produces. The ranking is not derivable from the record.');
  var rows = (verdict.entries || []).filter(function (e) { return !e.agrees; })
    .map(function (e) {
      return el('li', { class: 'arena-drift' }, [
        el('span', {}, 'entity ' + e.entity_id),
        el('span', {}, 'stored ' + Math.round(e.stored_r)),
        el('span', {}, 'from the log ' + Math.round(e.replayed_r))
      ]);
    });
  return [head,
    rows.length ? el('ul', { class: 'arena-drift-list' }, rows) : null,
    el('button', { type: 'button', class: 'btn btn-quiet', 'data-arena-recompute': verdict.arena_id },
      'Recompute from the log'),
    el('p', { class: 'form-status', 'data-status': '', hidden: '' })
  ].filter(Boolean);
}
function wireArenaControls(box, slug, helpers) {
    var setStatus = helpers.setStatus;
    var postJSON = helpers.postJSON;
  var base = '/api/v1/projects/' + encodeURIComponent(slug);

  function loadPair(arenaID, out) {
    out.textContent = 'Finding the comparison that teaches the most\u2026';
    // The store's "this voter has judged every pair" is the ordinary end state of an arena, not an error, and it says
    // so in its message — which the one error model carries, so the refusal and the network failure are the same
    // three lines instead of a response-shaped branch and a separate catch.
    window.ConcordApi.get(base + '/arenas/' + encodeURIComponent(arenaID) + '/next-pair')
      .then(function (pair) { out.replaceChildren(arenaPairForm(arenaID, pair)); })
      .catch(function (err) {
        out.replaceChildren(note(String(err && err.message ? err.message : err)));
      });
  }

  function loadLog(arenaID, out) {
    out.textContent = 'Loading the vote log\u2026';
    window.ConcordApi.get(base + '/arenas/' + encodeURIComponent(arenaID) + '/votes')
      .then(function (log) { out.replaceChildren(arenaLog(log.votes || [])); })
      .catch(function (err) {
        out.replaceChildren(note(err.message || String(err)));
      });
  }

  function loadVerification(arenaID, out) {
    out.textContent = 'Replaying the vote log\u2026';
    window.ConcordApi.get(base + '/arenas/' + encodeURIComponent(arenaID) + '/verification')
      .then(function (verdict) { out.replaceChildren.apply(out, arenaVerify(verdict)); })
      .catch(function (err) {
        out.replaceChildren(note(err.message || String(err)));
      });
  }

  Array.prototype.forEach.call(box.querySelectorAll('[data-arena-pair]'), function (btn) {
    btn.addEventListener('click', function () {
      var id = btn.getAttribute('data-arena-pair');
      var out = box.querySelector('[data-arena-pair-out="' + id + '"]');
      if (out) loadPair(id, out);
    });
  });
  Array.prototype.forEach.call(box.querySelectorAll('[data-arena-log]'), function (btn) {
    btn.addEventListener('click', function () {
      var id = btn.getAttribute('data-arena-log');
      var out = box.querySelector('[data-arena-log-out="' + id + '"]');
      if (out) loadLog(id, out);
    });
  });
  Array.prototype.forEach.call(box.querySelectorAll('[data-arena-verify]'), function (btn) {
    btn.addEventListener('click', function () {
      var id = btn.getAttribute('data-arena-verify');
      var out = box.querySelector('[data-arena-verify-out="' + id + '"]');
      if (out) loadVerification(id, out);
    });
  });

  // DELEGATED, and it has to be: the Recompute control is rendered with the
  // verification verdict, which arrives AFTER this runs. Attaching the listener
  // here meant it was attached to nothing -- the button appeared, and clicking it
  // did nothing at all, which the browser test caught (the verdict text stayed put).
  box.addEventListener('click', function (ev) {
    var btn = ev.target && ev.target.closest ? ev.target.closest('[data-arena-recompute]') : null;
    if (!btn) return;
    {
      var id = btn.getAttribute('data-arena-recompute');
      var out = box.querySelector('[data-arena-verify-out="' + id + '"]');
      btn.disabled = true;
      postJSON(base + '/arenas/' + encodeURIComponent(id) + '/recompute', {})
        .then(function (res) {
          if (!out) return;
          out.replaceChildren(note(res.changed === 0
            ? 'The vote log already reproduced every rating: the recompute changed nothing.'
            : res.changed + ' entr' + (res.changed === 1 ? 'y was' : 'ies were') +
              ' repaired. Reload to see the corrected ranking.'));
        })
        .catch(function (err) {
          if (out) out.replaceChildren(note(err.message || String(err)));
        });
    }
  });

  // Delegated, because the vote form is rendered by loadPair AFTER this runs:
  // a listener attached at wiring time would be attached to nothing.
  box.addEventListener('submit', function (ev) {
    var form = ev.target;
    if (!form || !form.getAttribute || form.getAttribute('data-vote-pair') === null) return;
    ev.preventDefault();
    var arenaID = form.getAttribute('data-vote-pair');
    var fd = new FormData(form);
    setStatus(form, 'Recording\u2026', false);
    // The vote goes to the arena's OWN vote route. Judging a pair and casting a
    // comparison are the same act, and a second write path for the same act is
    // how two rules for one thing start.
    postJSON(base + '/alternatives/' + encodeURIComponent(arenaID) + '/vote', {
      a_project_id: Number(form.getAttribute('data-side-a')),
      b_project_id: Number(form.getAttribute('data-side-b')),
      outcome: fd.get('outcome'),
      reason: fd.get('reason') || ''
    })
      .then(function () {
        // The vote moved two ratings and possibly the order, and the panel the
        // reader is looking at is the leaderboard. Reload, as the field-report
        // panel does for the same reason.
        window.location.reload();
      })
      .catch(function (err) { setStatus(form, err.message || String(err), true); });
  });
}
  window.ConcordArena = { controls: arenaControls, wire: wireArenaControls };
})();
