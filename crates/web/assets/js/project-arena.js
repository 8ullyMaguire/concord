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

function arenaSideHTML(s) {
  return '<div class="pair-side" data-pair-side="' + esc(s.entity_id) + '">' +
    '<span class="pair-label">' + esc(s.label || ('entity ' + s.entity_id)) + '</span>' +
    (s.is_baseline ? '<span class="pair-baseline">do nothing</span>' : '') +
    '<span class="pair-rating">rating ' + esc(Math.round(s.r || 0)) + ' \u00b1 ' + esc(Math.round(s.rd || 0)) + '</span>' +
    '<span class="pair-confidence">' + esc(Math.round(100 * (s.win_probability || 0))) + '% to win</span>' +
    '</div>';
}

// arenaPairForm is the pair AND the control that judges it. Rendering the pair
// without the control would put a voter in front of a question they cannot answer,
// which is how the panel would have looked if the store's NextArenaPair had been
// routed without the vote path already being there.
function arenaPairForm(arenaID, pair) {
  var options = [['a', 'A wins'], ['b', 'B wins'], ['both', 'A draw'],
                 ['neither', 'Neither is worth it'], ['skip', 'I cannot tell']];
  var radios = options.map(function (o, i) {
    return '<label class="pair-choice"><input type="radio" name="outcome" value="' + o[0] + '"' +
      (i === 0 ? ' checked' : '') + '> ' + esc(o[1]) + '</label>';
  }).join('');
  return '<form class="pair-form" data-vote-pair="' + esc(arenaID) + '" ' +
      'data-side-a="' + esc(pair.a.entity_id) + '" data-side-b="' + esc(pair.b.entity_id) + '">' +
    '<p class="pair-question">' + esc(pair.question || 'Which is better?') + '</p>' +
    '<div class="pair-sides">' + arenaSideHTML(pair.a) + arenaSideHTML(pair.b) + '</div>' +
    '<div class="pair-choices">' + radios + '</div>' +
    '<label class="pair-reason">Why (optional)' +
      '<input type="text" name="reason" maxlength="280" placeholder="one line, optional"></label>' +
    '<button type="submit" class="btn btn-primary">Record vote</button>' +
    '<p class="form-status" data-status hidden></p>' +
    '</form>';
}

function arenaLogHTML(votes) {
  if (!votes.length) {
    return '<p class="arena-note">No comparisons recorded yet. The ranking starts at the ' +
      'prior until somebody judges a pair.</p>';
  }
  return '<ul class="arena-log-list">' + votes.map(function (v) {
    var winner = v.outcome === 'a' ? v.a.label : (v.outcome === 'b' ? v.b.label : v.outcome);
    // The WEIGHT is shown, because it is the number that decides how far the vote
    // moved the ranking and §5.2 makes this view the audit trail an appeal is argued
    // from. §30 found the weight being computed, stored, and honoured by nothing;
    // a reader who cannot see it cannot tell a full vote from a ramped-down one, and
    // those two are the difference between a vote and a formality.
    var w = (typeof v.weight === 'number') ? v.weight : Number(v.weight);
    var shown = isFinite(w) ? (w >= 1 ? String(Math.round(w * 100) / 100)
                                     : w.toFixed(4)) : '?';
    return '<li class="arena-log-row">' +
      '<span class="log-winner">' + esc(winner) + '</span>' +
      '<span class="log-pair">' + esc(v.a.label) + ' vs ' + esc(v.b.label) + '</span>' +
      '<span class="log-weight">weight ' + esc(shown) + '</span>' +
      (v.reason ? '<span class="log-reason">' + esc(v.reason) + '</span>' : '') +
      '</li>';
  }).join('') + '</ul>';
}

function arenaVerifyHTML(verdict) {
  // NO ENTRIES IS NOT AGREEMENT. A comparison set that is empty reproduces itself
  // and says nothing about anything, and the live instances are full of arenas in
  // exactly that state: the laptop's database has 55 arenas and no entries at all,
  // and the public one has an arena with two votes and none. `agrees: true` over
  // nothing is the same shape as the 200-of-327 page this pass started with, so the
  // panel states which case it is rather than rendering the reassuring one.
  var n = (verdict.entries || []).length;
  var head = n === 0
    ? '<p class="arena-note arena-empty">This arena has no competitors to verify yet: the log is empty.</p>'
    : verdict.agrees
    ? '<p class="arena-note arena-agrees">The vote log reproduces every rating in this arena.</p>'
    : '<p class="arena-note arena-disagrees">' + esc(verdict.divergent) +
      ' entr' + (verdict.divergent === 1 ? 'y is' : 'ies are') +
      ' not what the vote log produces. The ranking is not derivable from the record.</p>';
  var rows = (verdict.entries || []).filter(function (e) { return !e.agrees; })
    .map(function (e) {
      return '<li class="arena-drift">' +
        '<span>entity ' + esc(e.entity_id) + '</span>' +
        '<span>stored ' + esc(Math.round(e.stored_r)) + '</span>' +
        '<span>from the log ' + esc(Math.round(e.replayed_r)) + '</span>' +
        '</li>';
    }).join('');
  return head + (rows ? '<ul class="arena-drift-list">' + rows + '</ul>' : '') +
    '<button type="button" class="btn btn-quiet" data-arena-recompute="' + esc(verdict.arena_id) +
    '">Recompute from the log</button>' +
    '<p class="form-status" data-status hidden></p>';
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
      .then(function (pair) { out.innerHTML = arenaPairForm(arenaID, pair); })
      .catch(function (err) {
        out.innerHTML = '<p class="arena-note">' + esc(String(err && err.message ? err.message : err)) + '</p>';
      });
  }

  function loadLog(arenaID, out) {
    out.textContent = 'Loading the vote log\u2026';
    window.ConcordApi.get(base + '/arenas/' + encodeURIComponent(arenaID) + '/votes')
      .then(function (log) { out.innerHTML = arenaLogHTML(log.votes || []); })
      .catch(function (err) {
        out.innerHTML = '<p class="arena-note">' + esc(err.message || String(err)) + '</p>';
      });
  }

  function loadVerification(arenaID, out) {
    out.textContent = 'Replaying the vote log\u2026';
    window.ConcordApi.get(base + '/arenas/' + encodeURIComponent(arenaID) + '/verification')
      .then(function (verdict) { out.innerHTML = arenaVerifyHTML(verdict); })
      .catch(function (err) {
        out.innerHTML = '<p class="arena-note">' + esc(err.message || String(err)) + '</p>';
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
          out.innerHTML = '<p class="arena-note">' +
            esc(res.changed === 0
              ? 'The vote log already reproduced every rating: the recompute changed nothing.'
              : res.changed + ' entr' + (res.changed === 1 ? 'y was' : 'ies were') +
                ' repaired. Reload to see the corrected ranking.') + '</p>';
        })
        .catch(function (err) {
          if (out) out.innerHTML = '<p class="arena-note">' + esc(err.message || String(err)) + '</p>';
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
