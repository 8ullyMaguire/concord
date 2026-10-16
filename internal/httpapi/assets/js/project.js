/* apiAuth: the session's Authorization header, for API reads.
 *
 * Every read on this page used to go out anonymous -- harmless while the project is
 * public, and fatal for the projects whose settings most need changing: a member
 * reaches a protected project (the navigation cookie opened the page) and then every
 * panel 404s, because the page's own reads carried no credential. session.js has
 * exported authHeaders() all along; this is the two lines that use it, and for a
 * signed-out visitor the request is identical to before. */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}
// Concord project detail — resolves the slug, then hydrates from the API.
(function () {
  function esc(s) {
      return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
      });
    }

    // ------------------------------------------------------- the §4.10 panels
    //
    // Capabilities, field reports and solutions. Three read-only panels added
    // 2026-10-03; their stores shipped long before with full test coverage and no
    // way for a browser to see any of it.
    //
    // Each is fetched separately and each degrades on its own, following the rule
    // the complaints fetch above already follows: a panel that fails must not cost
    // the panels that worked. A failure renders as an explicit "could not load"
    // rather than as an empty state, because "no capabilities" and "we could not
    // ask" look identical when both are an empty div -- and the second one is a
    // lie about the project.

    // capabilityValue is what a row says about a capability.
    //
    //   no claim yet  -> nobody has asserted this capability for this project at all
    //   unknown       -> somebody DID assert it, valued "unknown", i.e. recorded
    //                    that nobody knows
    //   yes/no/partial-> somebody claimed it
    //
    // A disputed claim prints its value WITH the dispute beside it. It never prints
      // as settled, and it is never flattened to "no" -- "people disagree" and "this
      // is false" are different claims about a project.
      function capabilityValue(c) {
        if (!c.asserted) {
          // "no claim yet", not "not reported": "report" is field-report vocabulary
          // one screen below, and "not reported" reads as "someone reported that it
          // is unknown" -- which is the other state.
          return '<span class="cap-unknown">no claim yet</span>';
        }
        var val = c.value || 'unknown';
        var mark = '';
        if (c.state === 'disputed') {
          mark = ' <span class="badge badge-amber" title="People disagree about this. ' +
            esc(c.disputes || 0) + ' contested it; the original claim is not rewritten.">disputed</span>';
        } else if (c.state === 'confirmed') {
          mark = ' <span class="badge badge-green" title="Confirmed by ' +
            esc(c.confirms || 0) + ' independent confirmations.">confirmed</span>';
        } else {
          // The third state, and the only one of the three with no store-side
          // confirmation behind it: one claim, nobody has agreed or disagreed yet.
          mark = ' <span class="badge badge-slate">asserted, unconfirmed</span>';
        }
        return '<span class="cap-value">' + esc(val) + '</span>' + mark;
      }

    function capabilitiesPanel(data) {
      if (!data) {
        return panelError('Capabilities', 'The capability matrix could not be loaded.');
      }
      var caps = data.capabilities || [];
      // The root is the wrapper <div class="panel-capabilities"> opened at the end of
      // this function, NOT the section-head inside it. Marking both made
      // `.panel-capabilities` match two elements and every strict-mode locator
      // in the e2e suite fail on ambiguity rather than on a real defect.
      var head = '<div class="section-head"><h2 class="section-title" style="font-size:1.25rem;">Capabilities</h2>' +
        '<p class="section-sub">What this project claims about itself, and what nobody has ' +
        'claimed yet. ' + (data.unknown_count > 0
          ? esc(data.unknown_count) + ' of ' + caps.length + ' have no claim from anyone.'
          : 'Every capability in the catalog has a claim.') + '</p></div>';

      if (!caps.length) {
        return '<div class="panel-capabilities">' + head + panelEmpty('No capability matrix yet',
          'This instance has no capability catalog, so there is nothing for a ' +
          'project to claim. Capabilities are created instance-wide and then ' +
          'asserted per project.') + '</div>';
      }
      var rows = caps.map(function (c) {
        // The evidence goes in the row. The API has been returning it since the
        // panel's endpoint was written and this table dropped it, which is the
        // whole substance of a claim: "yes" from a project that enforces WIP
        // limits and "yes" from one that heard of them are different claims, and
        // the evidence is what tells them apart. Authored text, so escaped.
        var evidence = c.evidence
          ? '<div class="cap-evidence">' + esc(c.evidence) + '</div>'
          : '';
        return '<tr><td>' + esc(c.label || c.key) +
          '<div class="cap-key">' + esc(c.key) + '</div></td>' +
          '<td class="cap-cat">' + esc(c.category || '') + '</td>' +
          '<td>' + capabilityValue(c) + evidence + '</td></tr>';
      }).join('');
      return '<div class="panel-capabilities">' + head +
        '<div class="card"><table class="cap-table"><thead><tr>' +
        '<th>Capability</th><th>Category</th><th>Claim</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table></div></div>';
    }

    // A rate is never printed without its denominator. "worked for 83% of
    // reporters" and "83%" are different claims, and a project nobody has tried has
    // no rate at all rather than a rate of zero.
    function fieldReportsPanel(data) {
      if (!data) {
        return panelError('Field reports', 'Field reports could not be loaded.');
      }
      var reports = data.reports || [];
      var head = '<div class="section-head" style="margin-top:2rem;">' +
        '<h2 class="section-title" style="font-size:1.25rem;">Field reports</h2>' +
        '<p class="section-sub">Structured experience records: version, environment, and ' +
        'what actually happened.</p></div>';

      // The summary is computed BEFORE the empty check and included in both
      // returns, so the `outcome_rate != null` guard below is the thing that
      // decides whether a rate appears.
      //
      // It used to sit after an early return on `!reports.length`, which made
      // the guard unreachable in its false branch: no reports implies a nil
      // rate, so `if (true)` was indistinguishable from `if (rate != null)` and
      // a mutation run reported it as SURVIVED. The observable behaviour was not
      // wrong, but the guard was decorative -- it could not fail -- and a
      // decorative guard protects nothing when the store's rule changes.
      // The write half. The panel has rendered a rate and a corpus since phase3 and nothing
      // could ever add to either: this form is what makes "No field reports yet" a state the
      // instance can leave. The outcome is a SELECT of §4.7's four values rather than a text
      // input, because the store refuses anything else and a free-text box would make the
      // refusal a discovery instead of a rule the page states.
      var fileForm = '<form class="card" id="field-report-form" style="margin-top:1rem;">' +
        '<div class="card-title">File a field report</div>' +
        '<p class="card-text">What happened when you used this, in the terms the outcome rate ' +
        'counts. A version, an environment, and an outcome are what make one report ' +
        'comparable with the next.</p>' +
        '<div class="form-row">' +
        '<label class="form-label" for="fr-outcome">Outcome</label>' +
        '<select class="input" id="fr-outcome" name="outcome">' +
        '<option value="worked">worked</option>' +
        '<option value="worked-with-caveats">worked, with caveats</option>' +
        '<option value="abandoned">abandoned</option>' +
        '<option value="migrated-away">migrated away</option>' +
        '</select></div>' +
        '<div class="form-row"><label class="form-label" for="fr-version">Version</label>' +
        '<input class="input" id="fr-version" name="version" placeholder="1.4.2"></div>' +
        '<div class="form-row"><label class="form-label" for="fr-environment">Environment</label>' +
        '<input class="input" id="fr-environment" name="environment" placeholder="arm64, docker, bare metal"></div>' +
        '<div class="form-row"><label class="form-label" for="fr-use-case">Use case</label>' +
        '<input class="input" id="fr-use-case" name="use_case" placeholder="what you were trying to do"></div>' +
        // Named as required-for-one-outcome in the label, so the server's refusal is a rule the
        // page already stated rather than a surprise.
        '<div class="form-row"><label class="form-label" for="fr-migrated-to">Migrated to ' +
        '<span class="muted">(required only for the migrated-away outcome)</span></label>' +
        '<input class="input" id="fr-migrated-to" name="migrated_to" placeholder="the successor"></div>' +
        '<div class="form-row"><label class="form-label" for="fr-caveats">What happened</label>' +
        '<textarea class="input" id="fr-caveats" name="caveats" rows="3"></textarea></div>' +
        '<div class="form-row"><label class="form-label" for="fr-advice">Advice for the next ' +
        'person</label><textarea class="input" id="fr-advice" name="advice" rows="2"></textarea></div>' +
        '<button type="submit" class="btn" data-file-field-report>File this report</button>' +
        '<p class="form-status" data-fr-error hidden></p>' +
        '</form>';

      var summary = '';
      if (data.outcome_rate != null) {
        var pct = Math.round(data.outcome_rate * 100);
        summary = '<div class="fr-summary"><strong>' + pct + '%</strong> of ' +
          esc(data.sample_size) + ' reporter' + (data.sample_size === 1 ? '' : 's') +
          ' report this working, weighted by reporter reputation.</div>';
      }
      var env = '';
      var envs = data.by_environment || {};
      var envKeys = Object.keys(envs);
      if (envKeys.length) {
        env = '<div class="pill-row" style="margin-top:0.5rem;">' + envKeys.map(function (k) {
          return '<span class="badge badge-slate">' + esc(k) + ' ' +
            Math.round(envs[k] * 100) + '%</span>';
        }).join('') + '</div>';
      }

      if (!reports.length) {
        // summary and env are still emitted, and both are empty for this state --
        // but they are emitted by the same guard that governs the populated
        // path, so a rate cannot appear here without the store having sent one.
        return '<div class="panel-field-reports">' + head + summary + env + panelEmpty('No field reports yet',
          'Nobody has recorded using this. Until someone does, there is no evidence ' +
          'here to weigh against the project\'s claims.') + fileForm + '</div>';
      }

      var cards = reports.map(function (r) {
        var body = '';
        if (r.caveats) body += '<p class="card-text">' + esc(r.caveats) + '</p>';
        if (r.advice) body += '<p class="card-text"><strong>Advice:</strong> ' + esc(r.advice) + '</p>';
        var responses = (r.responses || []).map(function (resp) {
          return '<div class="fr-response"><span class="badge ' +
            (resp.is_owner ? 'badge-indigo' : 'badge-slate') + '">' +
            (resp.is_owner ? 'owner' : 'reply') + '</span> ' + esc(resp.body) + '</div>';
        }).join('');
        // The reply form is keyed by report id: the page renders up to ten reports, so a form
        // found by id alone would post every reply to the first one shown.
        responses += '<form class="fr-reply-form" data-respond-report="' + esc(r.id) + '">' +
          '<label class="form-label" for="fr-reply-' + esc(r.id) + '">Reply</label>' +
          '<input class="input" id="fr-reply-' + esc(r.id) + '" data-response-body ' +
          'placeholder="the owner can answer here; anyone signed in can say they saw it too">' +
          '<button type="submit" class="btn" data-submit-response>Reply</button>' +
          '<p class="form-status" data-fr-error hidden></p>' +
          '</form>';
        return '<div class="card fr-card"><div class="card-title">' +
          esc(r.use_case || 'field report') + '</div>' +
          '<div class="pill-row">' +
          '<span class="badge badge-purple">' + esc(r.outcome) + '</span>' +
          (r.version ? '<span class="badge badge-slate">' + esc(r.version) + '</span>' : '') +
          (r.environment ? '<span class="badge badge-slate">' + esc(r.environment) + '</span>' : '') +
          '</div>' + body + responses + '</div>';
      }).join('');

      var omitted = data.omitted > 0
        ? '<p class="section-sub">Showing the newest ' + esc(data.shown) +
          '; ' + esc(data.omitted) + ' older report' + (data.omitted === 1 ? '' : 's') +
          ' not shown.</p>'
        : '';

      return '<div class="panel-field-reports">' + head + summary + env + omitted +
        '<div class="grid-responsive-2" style="margin-top:1rem;">' + cards + '</div>' +
        fileForm + '</div>';
    }

    function solutionsPanel(data) {
      if (!data) {
        return panelError('Solutions', 'Solution standings could not be loaded.');
      }
      var groups = data.features || [];
      // No panel class on the head: the wrapper opened by the return below is the
      // root. The capabilities panel had the class on both and every strict-mode
      // browser locator then failed on ambiguity rather than on a real defect.
      var head = '<div class="section-head" style="margin-top:2rem;">' +
        '<h2 class="section-title" style="font-size:1.25rem;">Solution standings</h2>' +
        '<p class="section-sub">Ranked proposals per feature. Scores are computed ' +
        'against the other proposals for that feature, so they are not comparable ' +
        'across features.</p></div>';

      // Three states, and the counts are what separate them. A project with no
      // features has no roadmap; a project with features nobody has answered has
      // a roadmap with nothing on it. Rendering both as "no solutions" states
      // something untrue about one of them.
      if (!groups.length) {
        return '<div class="panel-solutions">' + head + panelEmpty('No roadmap yet',
          'This project has no features, so there is nothing for solutions to ' +
          'address. Features come from validated complaints.') + '</div>';
      }

      var withSol = data.features_with_solutions || 0;
      var without = data.features_without || 0;
      var tally = '<div class="fr-summary" style="border-left-color: var(--slate-400);">' +
        '<strong>' + esc(withSol) + '</strong> of ' + esc(groups.length) +
        ' feature' + (groups.length === 1 ? '' : 's') + ' ' +
        (withSol === 1 ? 'has' : 'have') + ' proposals' +
        (without > 0 ? '; ' + esc(without) + ' ' + (without === 1 ? 'has' : 'have') +
          ' none.' : '.') + '</div>';

      var blocks = groups.map(function (g) {
        var sols = g.solutions || [];
        if (!sols.length) {
          return '<div class="sol-group sol-empty">' +
            '<div class="sol-feature">' + esc(g.feature_title) + '</div>' +
            '<p class="card-text">No proposals yet. ' +
            (g.feature_id ? 'Feature ' + esc(g.feature_id) + ' is open for one.' : '') +
            '</p></div>';
        }
        var rows = sols.map(function (sol, i) {
          // The rank is rendered, not implied by list order alone: a reader
          // comparing two features needs to see that these are separate
          // contests, not positions in one.
          var rank = '<span class="sol-rank">' + (i + 1) + '</span>';
          var cls = sol.is_baseline ? 'sol-row sol-baseline' : 'sol-row';
          // The title is the point of the row. A standings list showing scores
          // with no names is a ranking nobody can act on, and the API omitted the
          // title until `SolutionScore.Title` was added for exactly this panel.
          return '<li class="' + cls + '">' + rank +
            '<span class="sol-title">' +
            esc(sol.title || 'proposal ' + esc(sol.solution_id)) + '</span>' +
            '<span class="sol-coverage" title="Coverage against this feature\'s ' +
            'criteria">' + Math.round((sol.coverage || 0) * 100) + '%</span>' +
            esc(sol.rating != null ? sol.rating.toFixed(2) : '—') +
            (sol.is_baseline ? ' <span class="badge badge-slate">do nothing</span>' : '') +
            '</li>';
        }).join('');
        var omitted = g.omitted > 0
          ? '<p class="section-sub">' + esc(g.omitted) + ' more proposal' +
            (g.omitted === 1 ? '' : 's') + ' not shown.</p>'
          : '';
        return '<div class="sol-group"><div class="sol-feature">' +
          esc(g.feature_title) + '</div>' +
          '<ul class="sol-list">' + rows + '</ul>' + omitted + '</div>';
      }).join('');

      // The link to the Consensus page, once there is a call to look at. Only
      // rendered when the API reports one, because the page takes ?call=<id> and
      // there is no endpoint that lists calls -- so a link without an id would go
      // nowhere. A page linked from another page must exist; a link to a call that
      // does not would be worse than no link.
      var consensusLink = (data.consensus_calls && data.consensus_calls.length)
        ? '<p class="panel-consensus-link"><a href="/projects/' +
          encodeURIComponent(data.slug || '') + '/consensus?call=' +
          encodeURIComponent(data.consensus_calls[0].id) + '">Open the call</a></p>'
        : '';

      return '<div class="panel-solutions">' + head + tally + consensusLink +
        '<div class="sol-groups">' + blocks + '</div></div>';
    }

  // The fourth §4.10 panel: alternatives arenas (S2,
  // docs/specs/alternatives-panel-spec.md).
  //
  // Two states, and the distinction is the panel's whole point: a project that has
  // never posed the question says so, and a project that posed it and got no
  // entries also says so — in different words. Merging them reads as "nothing to
  // do here" when the truth is "you asked and nobody answered".
  function alternativesPanel(data) {
    if (!data) {
      return panelError('Alternatives', 'Alternative project rankings could not be loaded.');
    }
    var arenas = data.arenas || [];
    // No panel class on the head: the wrapper the return opens is the root, and
    // marking both is what made every strict-mode browser locator ambiguous.
    var head = '<div class="section-head" style="margin-top:2rem;">' +
      '<h2 class="section-title" style="font-size:1.25rem;">Alternatives</h2>' +
      '<p class="section-sub">Competing projects, ranked for a use case you name. ' +
      'Each use case is ranked on its own, so scores are not comparable between ' +
      'them.</p></div>';

    // The marker and the root are deliberately on ONE line, as in the other three
    // panels. The root-element test requires that a state be identifiable from its
    // own `return` line, and a version with the title on the following line broke
    // that — the test reported "no return produces this state" for a state that
    // existed and rendered correctly.
    if (!arenas.length) {
      return '<div class="panel-alternatives">' + head + panelEmpty('No use cases compared yet',
        'This project has not compared itself against other projects for a ' +
        'particular use case. Those comparisons are added per use case, and each ' +
        'one is ranked separately.') + '</div>';
    }

    var blocks = arenas.map(function (a) {
      var rows = (a.entries || []).map(function (e) {
        return '<li class="alt-row">' +
          '<span class="alt-rank">' + esc(e.rank) + '</span>' +
          '<a class="alt-title" href="/projects/' + esc(e.slug) + '">' +
          esc(e.title) + '</a>' +
          '<span class="alt-rating">' + esc(e.rating.toFixed ? e.rating.toFixed(0) : e.rating) + '</span>' +
          '</li>';
      }).join('');
      var list = rows
        ? '<ul class="alt-list">' + rows + '</ul>'
        : '<p class="card-text">No projects entered yet. ' +
          'This use case has been posed and nothing has been compared against it.</p>';
      // §7.3's better-for / worse-for pair, stated once per use case rather than
      // per entry: repeating it on every row is noise, and it is a property of the
      // arena, not of any one competitor.
      // better_for / worse_for are per-ENTRY fields in the response, and every
      // entry of one arena carries the arena's own use case — so the pair is read
      // off the first entry and stated once. If the arena has no entries there is
      // no pair to state, which is why `entries` is checked first.
      var pair = '';
      if (a.entries && a.entries.length) {
        pair = '<p class="alt-pair"><span>Better for: ' +
          esc(a.entries[0].better_for) + '</span>' +
          (a.entries[0].worse_for ? '<span>' + esc(a.entries[0].worse_for) + '</span>' : '') +
          '</p>';
      }
      return '<div class="alt-arena">' +
        '<div class="alt-usecase">' + esc(a.use_case) + '</div>' +
        pair + list + arenaControls(a) + '</div>';
    }).join('');

    return '<div class="panel-alternatives">' + head + blocks + '</div>';
  }

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
      return '<li class="arena-log-row">' +
        '<span class="log-winner">' + esc(winner) + '</span>' +
        '<span class="log-pair">' + esc(v.a.label) + ' vs ' + esc(v.b.label) + '</span>' +
        (v.reason ? '<span class="log-reason">' + esc(v.reason) + '</span>' : '') +
        '</li>';
    }).join('') + '</ul>';
  }

  function arenaVerifyHTML(verdict) {
    var head = verdict.agrees
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

  function wireArenaControls(box, slug) {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);

    function loadPair(arenaID, out) {
      out.textContent = 'Finding the comparison that teaches the most\u2026';
      fetch(base + '/arenas/' + encodeURIComponent(arenaID) + '/next-pair', { headers: apiAuth() })
        .then(function (r) {
          return r.json().catch(function () { return {}; }).then(function (p) {
            return { ok: r.ok, status: r.status, body: p };
          });
        })
        .then(function (res) {
          if (!res.ok) {
            // The store's "this voter has judged every pair" is the ordinary end
            // state of an arena, not an error, and it says so in its message.
            out.innerHTML = '<p class="arena-note">' + esc(res.body.error || ('HTTP ' + res.status)) + '</p>';
            return;
          }
          out.innerHTML = arenaPairForm(arenaID, res.body);
        })
        .catch(function (err) {
          out.innerHTML = '<p class="arena-note">' + esc(String(err && err.message ? err.message : err)) + '</p>';
        });
    }

    function loadLog(arenaID, out) {
      out.textContent = 'Loading the vote log\u2026';
      fetch(base + '/arenas/' + encodeURIComponent(arenaID) + '/votes', { headers: apiAuth() })
        .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
        .then(function (log) { out.innerHTML = arenaLogHTML(log.votes || []); })
        .catch(function (err) {
          out.innerHTML = '<p class="arena-note">' + esc(err.message || String(err)) + '</p>';
        });
    }

    function loadVerification(arenaID, out) {
      out.textContent = 'Replaying the vote log\u2026';
      fetch(base + '/arenas/' + encodeURIComponent(arenaID) + '/verification', { headers: apiAuth() })
        .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
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

  function panelEmpty(title, description) {
      return '<div class="empty-state"><p class="empty-state-title">' + esc(title) + '</p>' +
        '<p class="empty-state-description">' + esc(description) + '</p></div>';
    }

    // A failed fetch renders differently from an empty one, on purpose. Both being
    // "nothing here" is how a broken endpoint passes for a project with no data.
    function panelError(what, message) {
      // `what` is the panel's title, mapped to its root class -- a lookup, not a
      // ternary chain. The first version was
      // `what === 'Capabilities' ? 'panel-capabilities' : 'panel-field-reports'`,
      // which silently filed the SOLUTIONS panel's failures under
      // panel-field-reports when the third panel was added. An unmapped title
      // yields no root class, so a future panel is visibly unclassed rather than
      // invisibly misfiled.
      //
      // Declared INSIDE the function: the first version put the map just above
      // the declaration, and a test that slices the source from
      // `function panelError` onward therefore read an empty tail and reported
      // all three panels as unmapped.
      var roots = {
        'Capabilities': 'panel-capabilities',
        'Field reports': 'panel-field-reports',
        'Solutions': 'panel-solutions',
        'Alternatives': 'panel-alternatives'
      };
      return '<div class="empty-state panel-error ' + (roots[what] || '') + '">' +
        '<p class="empty-state-title">' +
        esc(what) + ' unavailable</p>' +
        '<p class="empty-state-description">' + esc(message) + '</p></div>';
    }

  function healthBar(score) {
    if (score == null) return '<p class="health-label"><span>health</span><span>no metrics yet</span></p>';
    var pct = Math.round(score * 100);
    var cls = pct >= 70 ? 'good' : pct >= 40 ? 'mid' : 'poor';
    return '<div class="health-bar"><div class="health-fill health-fill-' + cls + '" style="width:' + pct + '%"></div></div>' +
      '<p class="health-label"><span>health</span><span>' + pct + '%</span></p>';
  }

  function fmtDate(unix) {
    if (!unix) return '';
    try { return new Date(unix * 1000).toLocaleDateString(); } catch (e) { return ''; }
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function notFound(slug) {
    return '<div class="empty-state"><div class="empty-state-icon">\u{1F50E}</div>' +
      '<p class="empty-state-title">Project not found</p>' +
      '<p class="empty-state-description">No project with slug &ldquo;' + esc(slug) + '&rdquo; exists on this forge.</p>' +
      '<a class="btn btn-primary" href="/projects">Browse projects</a></div>';
  }

  // complaintCards renders one card per complaint, the title linked to its detail
  // page.
  //
  // The title was a plain div, which is the same defect the feature cards had: the
  // route and the template existed and nothing pointed at them. It is also the most
  // natural place a reader looks for the detail.
  //
  // The card is NOT the link. There is a Validate BUTTON in the pill row, and nesting
  // an interactive element inside an <a> is invalid HTML and breaks keyboard and
  // screen-reader behaviour in ways the a11y suite would catch late.
  // requestSection renders §17's request board for one project.
  //
  // The link is built from BOTH the slug and the request id, so the reachability gate
  // in requests_page_test.go cannot be satisfied by a hardcoded path. That gate is
  // asserted on THIS FILE rather than the server's HTML on purpose: the cards render
  // client-side, so a server-HTML assertion would pass because the script's own source
  // contains the path and would keep passing if the link were deleted here.
  // listSection renders §16's lists for one project, linking each to its detail page.
  //
  // The link is built from BOTH the slug and the list id.
  function listSection(lists, p) {
    var head =
      '<div class="section-head" style="margin-top:2rem;">' +
      '<h2 class="section-title" style="font-size:1.25rem;">Lists</h2>' +
      '<p class="section-sub">Curated collections whose entries are ranked by ' +
      'pairwise comparison, not by who edited the markdown last.</p></div>';

    if (!lists || !lists.length) {
      return head +
        '<div class="empty-state"><div class="empty-state-icon">\u{1F4CB}</div>' +
        '<p class="empty-state-title">No lists yet</p>' +
        '<p class="empty-state-description">Nothing has been curated here.</p></div>';
    }

    return head + '<div class="grid-responsive-2">' + lists.map(function (l) {
      // `p` is the project, a parameter of render(); `l` is this list. Both in scope
      // HERE, which is the whole lesson of complaintCards(list, slug).
      var href = '/projects/' + encodeURIComponent(p.slug) + '/lists/' +
        encodeURIComponent(l.id);
      // A LIST has `name`; an entry has `title`. One letter apart, and reading the
      // wrong one renders a card with an empty heading and no error.
      return '<article class="card">' +
        '<h3><a href="' + href + '">' + esc(l.name) + '</a></h3>' +
        (l.description ? '<p class="card-text">' + esc(l.description) + '</p>' : '') +
        '<p class="card-meta"><span class="badge ' +
        (l.status === 'open' ? 'badge-blue' : 'badge-slate') + '">' +
        esc(l.status) + '</span></p></article>';
    }).join('') + '</div>';
  }

  function requestSection(requests, p) {
    var head =
      '<div class="section-head" style="margin-top:2rem;">' +
      '<h2 class="section-title" style="font-size:1.25rem;">Requests</h2>' +
      '<p class="section-sub">Structured questions, not threads. Projects answer ' +
      'them with a fit rationale, and the community ranks those answers against ' +
      'each other.</p></div>';

    if (!requests || !requests.length) {
      return head +
        '<div class="empty-state"><div class="empty-state-icon">\u{1F4DC}</div>' +
        '<p class="empty-state-title">No open requests</p>' +
        '<p class="empty-state-description">Nothing has been asked here yet.</p>' +
        '</div>';
    }

    return head + '<div class="grid-responsive-2">' + requests.map(function (r) {
      // `p` is the project, a parameter of render(), and `r` is this request. Both
      // are in scope HERE, which is the whole lesson of complaintCards(list, slug):
      // an identifier that is not a parameter of the enclosing function throws at
      // runtime and blanks the entire page, while a source-level gate stays green.
      var href = '/projects/' + encodeURIComponent(p.slug) + '/requests/' +
        encodeURIComponent(r.id);
      return '<article class="card">' +
        '<h3><a href="' + href + '">' + esc(r.title) + '</a></h3>' +
        (r.body ? '<p class="card-text">' + esc(r.body) + '</p>' : '') +
        '<p class="card-meta"><span class="badge ' +
          (r.status === 'open' ? 'badge-blue' : 'badge-slate') + '">' +
          esc(r.status || 'open') + '</span></p>' +
        '</article>';
    }).join('') + '</div>';
  }

  function complaintCards(list, slug) {
    return (list || []).map(function (c) {
      return '<div class="card">' +
        '<div class="card-title"><a href="/projects/' + esc(slug) + '/complaints/' +
          esc(c.id) + '">' + esc(c.title) + '</a></div>' +
        (c.body ? '<p class="card-text">' + esc(c.body) + '</p>' : '') +
        '<div class="pill-row">' +
        '<span class="badge ' + (c.status === 'validated' ? 'badge-green' : 'badge-slate') + '">' +
          esc(c.status || 'open') + '</span>' +
        '<span class="badge badge-slate">severity ' + esc(c.severity) + '</span>' +
        (c.status === 'open'
          ? '<button class="btn btn-quiet" data-validate="' + esc(c.id) + '">Validate</button>' +
            '<button class="btn btn-quiet" data-reject="' + esc(c.id) + '">Reject as spam</button>'
          : '') +
        '</div></div>';
    }).join('');
  }

  // forms renders the two ways to add to the project.
  //
  // Both are real <form> elements with ids, and both reload the page on
  // success rather than splicing a row into the DOM. The page is
  // server-rendered state: a complaint that appeared without a validation
  // round-trip would be shown as filed even when the server rejected it, and
  // the author would believe they had reported something.
  function forms(p) {
    if (!(window.ConcordSession && window.ConcordSession.instance().signedIn())) {
      return '<div class="empty-state"><p class="empty-state-title">Sign in to take part</p>' +
        '<p class="empty-state-description">Filing a complaint, proposing a feature ' +
        'and ranking all need an account. ' +
        '<a href="/login?next=' + encodeURIComponent(window.location.pathname) + '">Sign in</a> ' +
        'or <a href="/register">register</a>.</p></div>';
    }
    // method="post" on every form here matters: without an explicit method a
    // form defaults to GET, so if this script fails to load, is blocked, or errors
    // before it attaches a submit handler, the browser submits natively and puts
    // the field values -- including a password on the auth pages -- into the URL,
    // where they land in browser history and server access logs.
    return '<div class="form-grid">' +
      '<form class="card" id="complaint-form" method="post" action="#">' +
        '<h3 class="form-title">File a complaint</h3>' +
        '<p class="form-note">A specific problem, with the harm it causes. ' +
        'Vague complaints cannot be validated and therefore cannot be solved.</p>' +
        '<label class="field"><span>Title</span>' +
        '<input name="title" required maxlength="200" placeholder="Search returns nothing for an empty query"></label>' +
        dupPanel('complaint') +
        '<label class="field"><span>What happens, and to whom</span>' +
        '<textarea name="body" rows="3" required placeholder="I searched for… and got…"></textarea></label>' +
        '<div class="field-row">' +
          '<label class="field"><span>Severity 1-5</span>' +
          '<input name="severity" type="number" min="1" max="5" value="3"></label>' +
          '<label class="field"><span>How often</span>' +
          '<input name="frequency" type="number" min="0" step="0.1" value="1"></label>' +
        '</div>' +
        '<button class="btn btn-primary" type="submit">File it</button>' +
        '<p class="form-status" data-status hidden></p>' +
      '</form>' +

      '<form class="card" id="feature-form" method="post" action="#">' +
        '<h3 class="form-title">Propose a feature</h3>' +
        '<p class="form-note">A solution to one of the complaints above. Pick which ' +
        'it answers — an unlinked feature has no pain score and never ranks.</p>' +
        '<label class="field"><span>Title</span>' +
        '<input name="title" required maxlength="200" placeholder="Search falls back to substring matching"></label>' +
        dupPanel('feature') +
        '<label class="field"><span>What it would do</span>' +
        '<textarea name="body" rows="3" required></textarea></label>' +
        '<label class="field"><span>Answers complaint</span>' +
        '<select name="complaint_id"><option value="">Choose…</option>' +
          (window.__complaints || []).map(function (c) {
            return '<option value="' + esc(c.id) + '">' + esc(c.title) + '</option>';
          }).join('') +
        '</select></label>' +
        '<button class="btn btn-primary" type="submit">Propose it</button>' +
        '<p class="form-status" data-status hidden></p>' +
      '</form>' +
    '</div>';
  }


  // wire attaches behaviour to the rendered forms and the validate buttons.
  // Called after every render, and it must be idempotent-safe: the DOM is
  // replaced wholesale each time, so there is nothing to detach.
  function wire(box, slug, projectID) {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);

    // --- join ----------------------------------------------------------------
    //
    // The button is offered only when a session exists; without one the page
    // leaves it hidden and the visitor is not told about an action they cannot
    // take. On success it RELOADS rather than patching the page: the role the
    // server granted is what every panel's visibility depends on, and a page
    // that kept its old render would show a member the controls of a guest.
    var joinBtn = document.getElementById('project-join');
    if (joinBtn && window.ConcordSession && window.ConcordSession.instance()) {
      var joinHeaders = window.ConcordSession.instance().authHeaders();
      var hasToken = false;
      for (var hk in joinHeaders) {
        if (Object.prototype.hasOwnProperty.call(joinHeaders, hk) &&
            hk.toLowerCase() === 'authorization') hasToken = true;
      }
      if (hasToken) {
        joinBtn.hidden = false;
        joinBtn.addEventListener('click', function () {
          joinBtn.disabled = true;
          joinBtn.textContent = 'Joining\u2026';
          fetch(base + '/join', { method: 'POST', headers: apiAuth({ 'Content-Type': 'application/json' }), body: '{}' })
            .then(function (r) {
              return r.json().catch(function () { return {}; }).then(function (payload) {
                if (!r.ok) throw new Error(payload.error || ('HTTP ' + r.status));
                return payload;
              });
            })
            .then(function () {
              window.location.reload();
            })
            .catch(function (err) {
              joinBtn.disabled = false;
              joinBtn.hidden = true;
              var note = document.createElement('p');
              note.className = 'form-status form-status-error';
              note.textContent = 'Could not join: ' + (err.message || String(err));
              joinBtn.parentNode.appendChild(note);
            });
        });
      }
    }

    var complaintForm = document.getElementById('complaint-form');
    if (complaintForm) {
      wireDupPanel(complaintForm, 'complaint', projectID);
      complaintForm.addEventListener('submit', function (ev) {
        ev.preventDefault();
        var fd = new FormData(complaintForm);
        setStatus(complaintForm, 'Filing…', false);
        postJSON(base + '/complaints', {
          project_id: projectID,
          title: String(fd.get('title') || '').trim(),
          body: String(fd.get('body') || '').trim(),
          severity: Number(fd.get('severity') || 3),
          frequency: Number(fd.get('frequency') || 1)
        }).then(function () {
          // Reload rather than insert. The server owns the validation and the
          // id, and a row shown without a round-trip would tell the author
          // their complaint was filed when it may have been rejected.
          window.location.reload();
        }).catch(function (err) {
          setStatus(complaintForm, err.message || String(err), true);
        });
      });
    }

    var featureForm = document.getElementById('feature-form');
    if (featureForm) {
      wireDupPanel(featureForm, 'feature', projectID);
      featureForm.addEventListener('submit', function (ev) {
        ev.preventDefault();
        var fd = new FormData(featureForm);
        var complaintId = Number(fd.get('complaint_id') || 0);
        if (!complaintId) {
          setStatus(featureForm, 'Pick the complaint this feature answers. An unlinked feature has no pain score, so it never ranks.', true);
          return;
        }
        setStatus(featureForm, 'Proposing…', false);
        postJSON(base + '/features', {
          project_id: projectID,
          title: String(fd.get('title') || '').trim(),
          body: String(fd.get('body') || '').trim(),
          linked_complaints: [complaintId]
        }).then(function () {
          window.location.reload();
        }).catch(function (err) {
          setStatus(featureForm, err.message || String(err), true);
        });
      });
    }

    // The two verdicts on a complaint, wired the same way: both reload the page on
    // success, because the status badge is server-rendered and a complaint that
    // looked rejected without a round trip would be shown as something the server
    // never wrote. The label says which of the two acts it is -- "reject as spam",
    // not "reject" -- because the store's rejection carries the M1 reputation
    // penalty and a control that hides that is a control that surprises.
    [['data-validate', 'validate'], ['data-reject', 'reject']].forEach(function (pair) {
      Array.prototype.forEach.call(box.querySelectorAll('[' + pair[0] + ']'), function (btn) {
        btn.addEventListener('click', function () {
          btn.disabled = true;
          postJSON(base + '/complaints/' +
            encodeURIComponent(btn.getAttribute(pair[0])) + '/' + pair[1], {})
            .then(function () { window.location.reload(); })
            .catch(function (err) {
              btn.disabled = false;
              // ON THE CARD, not in a form's status line. `complaintForm || featureForm`
              // is null on a page that renders neither form, and the old path then
              // reached setStatus(null, ...) -> form.querySelector, which threw a
              // TypeError: the refusal was invisible AND the console carried an
              // exception. A refusal that renders nowhere is the worst outcome a
              // refusal can have, so it is shown next to the control that produced it,
              // where it exists in every page state.
              showCardRefusal(btn, err.message || String(err));
            });
        });
      });
    });

    // The field-reports panel's two write controls (§28). Called here, at the end of wire(),
    // because the panel is rendered before the forms above and the DOM is replaced wholesale on
    // every render -- there is nothing to detach and no order that matters beyond "after render".
    wireFieldReports(slug);
    // §5's arena loop, wired after every render like the rest.
    wireArenaControls(box, slug);
  }

  // ------------------------------------------------- §6.1 duplicate detection
  //
  // "Duplicate detection at filing time uses semantic similarity BEFORE submit,
  // and also searches other projects." The server half shipped with the store:
  // GET /api/v1/similar/{kind}/ was complete, measured (coverage, per-model
  // thresholds, cross-project scope) and called by NOTHING. `grep -rin similar`
  // over assets/js, templates/ and tests/ returned the route registration and
  // nothing else, so a filer could not see what they were about to duplicate --
  // only be refused after pressing submit, which is the wrong end of the form.
  //
  // Two forms use it: the complaint form and the feature form, both of which ask
  // for a title first, which is exactly what the endpoint scores.
  function dupPanel(kind) {
    // aria-live: the panel's whole content arrives without a keystroke, so a
    // screen reader has to be told. `polite` because the filer is typing and an
    // assertive announcement would interrupt them mid-word.
    return '<div class="dup-panel" data-dup-panel="' + esc(kind) + '" role="status" aria-live="polite"></div>';
  }

  // dupSeq is the last lookup started. A response whose sequence is not current
  // is dropped: the panel is debounced, so typing "search returns", then "search
  // returns nothing" leaves two requests in flight, and without this the FIRST
  // one's answer can land last and describe a title the filer has moved on from.
  var dupSeq = 0;

  function wireDupPanel(form, kind, projectID) {
    var title = form.querySelector('input[name="title"]');
    var panel = form.querySelector('[data-dup-panel]');
    if (!title || !panel) return;
    var timer = null;

    title.addEventListener('input', function () {
      var text = title.value.trim();
      if (timer) clearTimeout(timer);
      if (text.length < 8) {
        // Below eight characters the vector is mostly noise, and the answer
        // would be "nothing similar" for a title that is not yet a title. Saying
        // nothing is more honest than saying that.
        panel.innerHTML = '';
        return;
      }
      timer = setTimeout(function () { dupLookup(kind, projectID, text, panel); }, 350);
    });
  }

  function dupLookup(kind, projectID, text, panel) {
    var mine = ++dupSeq;
    panel.innerHTML = '<p class="dup-note dup-checking">Checking for existing ' + esc(kind) + 's…</p>';
    var url = '/api/v1/similar/' + encodeURIComponent(kind) + '/?text=' +
      encodeURIComponent(text) + '&project_id=' + encodeURIComponent(projectID);
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (mine !== dupSeq) return;
        panel.innerHTML = dupMarkup(kind, data);
      })
      .catch(function () {
        if (mine !== dupSeq) return;
        // Never an empty panel: "we could not ask" and "nothing similar exists"
        // are different statements, and the second one is a lie if the first is
        // what happened.
        panel.innerHTML = '<p class="dup-note">The duplicate check could not run. ' +
          'Filing still works.</p>';
      });
  }

  function dupMarkup(kind, data) {
    if (!data || data.available === false) {
      return '<p class="dup-note">' + esc((data && data.note) ||
        'Duplicate detection is not configured on this instance.') + '</p>';
    }
    var matches = data.similar || [];
    // The coverage line is not decoration. §6.1's promise is "nothing similar
    // exists on the parts of the instance we indexed", and a filer told "no
    // duplicates" by an instance that has indexed a twelfth of its corpus has
    // been told nothing. The endpoint returns the number; the panel says it.
    var cov = data.coverage || null;
    var covNote = '';
    if (cov && cov.coverage != null && cov.coverage < 1) {
      covNote = ' Only ' + Math.round(cov.coverage * 100) + '% of ' + esc(kind) +
        's are indexed, so this is not a complete answer.';
    }
    if (!matches.length) {
      return '<p class="dup-note">No similar ' + esc(kind) + ' found.' + covNote + '</p>';
    }
    var head = data.has_duplicate === true
      ? '<p class="dup-note dup-strong">This looks like an existing ' + esc(kind) +
        '. Read it before filing — if it is the same problem, adding to it is worth ' +
        'more than a second report.</p>'
      : '<p class="dup-note">Similar ' + esc(kind) + 's already exist:</p>';
    var rows = matches.slice(0, 5).map(function (m) {
      var label = esc(m.title || (kind + ' #' + m.entity_id));
      // The href is the API's own `url`, not a path reassembled here. The
      // endpoint used to hand out `/api/v1/features/222`, which is a 404 -- and
      // it did so for every match, because nothing ever followed one. Using the
      // server's value means a wrong URL fails a test instead of being written
      // correctly a second time in this file.
      var link = m.url ? '<a href="' + esc(m.url) + '">' + label + '</a>' : label;
      return '<li>' + link + ' <span class="dup-score">' +
        Math.round((m.score || 0) * 100) + '%</span></li>';
    }).join('');
    return head + '<ul class="dup-list">' + rows + '</ul>' + covNote;
  }

  // showCardRefusal puts a refusal beside the control that produced it, creating the
  // slot on first use. The pill row is the buttons' own parent, so the text lands where
  // the click did rather than at the bottom of a form the reader may not have.
  function showCardRefusal(btn, message) {
    var host = btn.parentNode;
    if (!host) return;
    var el = host.querySelector('[data-card-status]');
    if (!el) {
      el = document.createElement('p');
      el.setAttribute('data-card-status', '');
      el.className = 'form-status form-status-error';
      host.appendChild(el);
    }
    el.hidden = false;
    el.textContent = '';
    el.appendChild(document.createTextNode(message));
  }

  function setStatus(form, message, isError) {
    // A missing form is not an error to throw on: this helper is called from paths that
    // also have a card-level fallback, and the project page renders no complaint form
    // at all in several states. The guard is here rather than at each call site because
    // the call sites are many and the invariant is one.
    if (!form) return;
    var el = form.querySelector('[data-status]');
    if (!el) return;
    el.hidden = false;
    el.textContent = message;
    el.className = 'form-status' + (isError ? ' form-status-error' : '');
  }

  function postJSON(path, body) {
    // session.js owns the token, so it is read from one place rather than
    // reaching into localStorage again with a second set of key literals.
    var session = window.ConcordSession && window.ConcordSession.instance();
    var headers = { 'Content-Type': 'application/json' };
    if (session) {
      var h = session.authHeaders();
      for (var k in h) { if (Object.prototype.hasOwnProperty.call(h, k)) headers[k] = h[k]; }
    }
    return fetch(path, { method: 'POST', headers: headers, body: JSON.stringify(body) })
      .then(function (r) {
        if (r.status === 401) throw new Error('Sign in first — votes and filings need an account.');
        return r.json().catch(function () { return {}; }).then(function (payload) {
          if (!r.ok) throw new Error(payload.error || ('HTTP ' + r.status));
          return payload;
        });
      });
  }

  // wireFieldReports attaches the panel's two write controls.
  //
  // Both write and then RELOAD: a new report moves the outcome rate, the denominator and the
  // per-environment breakdown at once, and a new reply can change which badge the exchange
  // carries -- so the page the reader is looking at is the only thing that can be trusted to
  // show the result. Both render their refusal beside the control that caused it.
  function wireFieldReports(slug) {
    var base = '/api/v1/projects/' + encodeURIComponent(slug);

    var fileForm = document.getElementById('field-report-form');
    if (fileForm) {
      fileForm.addEventListener('submit', function (ev) {
        ev.preventDefault();
        var err = fileForm.querySelector('[data-fr-error]');
        var btn = fileForm.querySelector('[data-file-field-report]');
        function val(id) {
          var el = document.getElementById(id);
          return el ? el.value.trim() : '';
        }
        // Caught here rather than sent: the store refuses migrated-away with no successor, and a
        // request that cannot succeed is not worth a round trip. The label says the rule; this
        // makes it a refusal instead of an error message from the server.
        var outcome = val('fr-outcome');
        if (outcome === 'migrated-away' && !val('fr-migrated-to')) {
          if (err) { err.textContent = 'A migrated-away report has to name what you moved to.'; err.hidden = false; }
          return;
        }
        btn.disabled = true;
        postJSON(base + '/field-reports', {
          outcome: outcome,
          version: val('fr-version'),
          environment: val('fr-environment'),
          use_case: val('fr-use-case'),
          migrated_to: val('fr-migrated-to'),
          caveats: val('fr-caveats'),
          advice: val('fr-advice'),
        }).then(function () {
          window.location.reload();
        }).catch(function (e) {
          btn.disabled = false;
          if (err) { err.textContent = e.message || String(e); err.hidden = false; }
        });
      });
    }

    var replyForms = document.querySelectorAll('form[data-respond-report]');
    for (var i = 0; i < replyForms.length; i++) {
      (function (form) {
        form.addEventListener('submit', function (ev) {
          ev.preventDefault();
          var err = form.querySelector('[data-fr-error]');
          var btn = form.querySelector('[data-submit-response]');
          var field = form.querySelector('[data-response-body]');
          var body = field ? field.value.trim() : '';
          if (!body) {
            if (err) { err.textContent = 'A reply needs something in it.'; err.hidden = false; }
            return;
          }
          var reportID = form.getAttribute('data-respond-report');
          btn.disabled = true;
          postJSON(base + '/field-reports/' + encodeURIComponent(reportID) + '/responses',
            { body: body }).then(function () {
            window.location.reload();
          }).catch(function (e) {
            btn.disabled = false;
            if (err) { err.textContent = e.message || String(e); err.hidden = false; }
          });
        });
      })(replyForms[i]);
    }
  }

  function render(p, features, complaints, documents, panels, requests, lists) {
    panels = panels || {};
    var badges = '<div class="pill-row" style="margin-top:0.75rem;">' +
      '<span class="badge badge-indigo">' + esc(p.governance_model || 'governed') + '</span>' +
      (p.license ? '<span class="badge badge-slate">' + esc(p.license) + '</span>' : '') +
      '</div>';

    var meta = '<div class="meta-row">' +
      (p.created_at ? '<span class="meta-item">created ' + esc(fmtDate(p.created_at)) + '</span>' : '') +
      (p.updated_at ? '<span class="meta-item">updated ' + esc(fmtDate(p.updated_at)) + '</span>' : '') +
      '</div>';

    // The title is a LINK to the feature's own page.
    //
    // It was a plain div, which is what made the feature detail page unreachable
    // by clicking -- the same defect as Finder, Scout and the audit log: a route
    // and a template existed, and nothing pointed at them. A card whose title is
    // not a link is the most natural place a reader looks for the detail.
    //
    // The whole card is not the link: nested interactive elements inside an <a> are
    // invalid, and the pills below are not actions.
    var featureCards = (features || []).map(function (f) {
      return '<div class="card">' +
        '<div class="card-title"><a href="/projects/' + esc(p.slug) + '/features/' +
          esc(f.id) + '">' + esc(f.title) + '</a></div>' +
        (f.body ? '<p class="card-text">' + esc(f.body) + '</p>' : '') +
        '<div class="pill-row"><span class="badge badge-purple">rating ' +
          // A feature that has never been compared has no rating, not a rating of
          // zero; Math.round(f.elo_r || 0) printed 0 for it, which reads as "lost
          // every comparison" rather than "not compared yet".
          (f.elo_r == null ? 'not compared' : 'rating ' + Math.round(f.elo_r)) +
          (f.elo_rd != null && f.elo_r != null ? ' \u00b1' + Math.round(f.elo_rd) : '') + '</span>' +
        '<span class="badge badge-slate">' + esc(f.status || 'proposed') + '</span></div>' +
        '</div>';
    }).join('');

    return '<div class="detail-header">' +
      '<div><h1 class="page-title">' + esc(p.name) + ' <span class="badge badge-slate" style="vertical-align:middle;">' + esc(p.slug) + '</span></h1>' +
      '<p class="page-subtitle">' + esc(p.description || 'No description yet.') + '</p></div>' +
      // The sub-page tab bar is rendered by templates/base.html, not here.
      //
      // It was here first, which made it wrong: each sub-page is a separate
      // template loading its own bundle, so a tab bar built in project.js appeared
      // on the overview and nowhere else, turning every other sub-page into a
      // dead end. The layout sees the request path and can put it on all of them.
      // See the comment on the tab bar in base.html.
      '<div class="detail-actions">' +
      // The join control, and until now there was NONE: POST
      // /projects/{id}/join has existed with a handler, an audit row and a 200
      // for any signed-in account, and nothing in the browser could reach it.
      // "Participate" was a route with no door.
      //
      // Hidden until this page knows there is a session, because a join is
      // attributed to an account: a signed-out visitor gets the same
      // "sign in" prompt every other action on this page uses, rather than a
      // button that can only fail.
      '<button type="button" class="btn" id="project-join" hidden>Join this project</button>' +
      '<a class="btn btn-primary" href="/projects/' + esc(p.slug) + '/board">Open board</a>' +
      '<a class="btn btn-secondary" href="/projects">All projects</a>' +
      '</div></div>' +
      '<div class="card mb-6">' + badges + healthBar(p.health_score) + meta + '</div>' +
      '<div class="section-head"><h2 class="section-title" style="font-size:1.25rem;">Complaints</h2>' +
      '<p class="section-sub">The problems being solved. A feature needs at least one ' +
      'validated complaint behind it before it is a candidate for anything.</p></div>' +
      (complaints && complaints.length
        ? '<div class="grid-responsive-2">' + complaintCards(complaints, p.slug) + '</div>'
        : '<div class="empty-state"><div class="empty-state-icon">\u{1F4CC}</div>' +
          '<p class="empty-state-title">No complaints filed</p>' +
          '<p class="empty-state-description">Nothing has been reported yet. ' +
          'A feature cannot be ranked until there is a problem for it to solve.</p></div>') +
      '<div class="section-head" style="margin-top:2rem;"><h2 class="section-title" style="font-size:1.25rem;">Features</h2>' +
      '<p class="section-sub">Proposed solutions ranked by pairwise comparison.</p>' +
      '<div class="pill-row">' +
        '<a class="btn btn-primary" href="/projects/' + esc(p.slug) + '/rank">Rank features</a>' +
        '<a class="btn" href="/projects/' + esc(p.slug) + '/ranking">See the ranking</a>' +
        // Settings is linked from the project page BODY, not only from the shared tab
        // bar. The tab bar lives in base.html, so a reachability gate that only checked
        // it would stay green even if every link to this page were deleted from here.
        '<a class="btn" href="/projects/' + esc(p.slug) + '/settings">Settings</a>' +
      '</div></div>' +
      (features && features.length
        ? '<div class="grid-responsive-2">' + featureCards + '</div>'
        : '<div class="empty-state"><div class="empty-state-icon">\u2728</div>' +
          '<p class="empty-state-title">No features yet</p>' +
          '<p class="empty-state-description">Features are proposed from validated complaints.</p></div>') +
      // §4.10's order: capabilities, then field reports, then the standing.
      // Between the header and Complaints, because the capability matrix is what
      // says what the project IS and complaints are what it is trying to fix.
      requestSection(requests, p) +
      listSection(lists, p) +
      capabilitiesPanel(panels.capabilities) +
      fieldReportsPanel(panels.fieldReports) +
      // §4.10's order puts standings in the roadmap, after field reports.
      solutionsPanel(panels.solutions) +
      alternativesPanel(panels.alternatives) +
      forms(p);
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-detail');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.innerHTML = notFound(''); return; }

    fetch('/api/v1/projects/' + encodeURIComponent(slug), { headers: apiAuth() })
      .then(function (r) {
        if (r.status === 404) throw { notFound: true };
        return r.ok ? r.json() : Promise.reject({ status: r.status });
      })
      .then(function (p) {
        box.innerHTML = (window.ConcordSkeleton
          ? window.ConcordSkeleton.title() + window.ConcordSkeleton.text(3)
          : '<div class="loading-spinner" role="status" aria-label="Loading project"></div>');
        // By slug, not by p.id. The project-scoped routes take a slug: the
        // sibling route is /api/v1/projects/{slug} and the server resolves it
        // with GetProject, which matches on p.slug. Passing p.id here made every
        // request 404, and because the handler below maps a non-OK response to
        // an empty array, the page rendered a confident "No features yet" for a
        // project that had three. A failure that looks like a valid empty
        // result is worse than an error, because nothing prompts anyone to look.
        return Promise.all([
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/features', { headers: apiAuth() })
            .then(function (r) {
              if (r.status === 401) throw { auth: true };
              if (!r.ok) throw { status: r.status };
              return r.json();
            }),
          // Complaints are read separately and a failure here is not fatal: the
          // feature list is the more important of the two, and losing it
          // because a second request failed would be a worse outcome than
          // showing the complaints as unavailable.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/complaints', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; }),
          // Documents are a count badge and a link, not content on this page,
          // so a failure here degrades to no badge rather than to an error. The
          // same reasoning as complaints: a third failing request must not cost
          // us the two that matter.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/documents', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; }),
          // The two §4.10 panels. Both swallow their own failures and resolve to
          // null, which panelError() renders differently from an empty result --
          // so an endpoint that 500s says "unavailable" instead of quietly
          // claiming the project has no capabilities and no field reports.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/capabilities', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; }),
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/field-reports', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; }),
          // The third §4.10 panel. Same rule as the other two: swallow its own
          // failure and resolve to null, which panelError renders differently
          // from an empty result.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/solutions', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; }),
          // The fourth §4.10 panel, and the first panel whose data is grouped by
          // something the project NAMED rather than by a fixed dimension. Same
          // failure rule: swallow its own failure, resolve to null.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/alternatives', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; }),
          // Requests (17), the same non-fatal shape as complaints: the board is one
          // of the sections, and a failure here must not cost the features.
          //
          // APPENDED, not inserted: this array is read positionally below, so a
          // fetch added in the middle silently renumbers every later index. The
          // first version went in after complaints and fed documents to
          // capabilities, which renders as a panel quietly missing rather than an
          // error. Anything added here goes LAST, and the mapping below is by index.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/requests', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; }),
          // Lists (16), appended last so no existing index shifts. Same non-fatal
          // shape as complaints and requests.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/lists', { headers: apiAuth() })
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; })
        ])
          .then(function (res) {
            // Positional, in the order of the Promise.all above. Requests is
            // last (res[7]) because it was appended last.
            var features = res[0], complaints = res[1], documents = res[2];
            var requests = res[7];
            // res[8] for the same reason: appended, so it is last.
            var lists = res[8];
            // The feature form's select needs the complaint list at render
            // time, so it is stashed rather than threaded through the markup.
            window.__complaints = complaints || [];
            box.innerHTML = render(p, features, complaints, documents, {
              capabilities: res[3],
              fieldReports: res[4],
              solutions: res[5],
              alternatives: res[6]
            }, requests, lists);
            if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
            wire(box, slug, p.id);
          });
      })
      .catch(function (err) {
        // Outside the branches on purpose. This catch has three outcomes to
        // render -- notFound, auth, and a plain failure -- and clearing inside
        // only the last one covers a third of them. Found by mutation M9, which
        // removed the clear and the test still passed, because the stubbed
        // failure happens to land in that last branch.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        if (err && err.notFound) {
          box.innerHTML = notFound(slug);
        } else if (err && err.auth) {
          // 401 on a project-scoped read is unexpected: reads are public. Say so
          // rather than rendering an empty page, because an empty page reads as
          // "this project has no features" and nobody investigates that.
          box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
            '<p class="empty-state-title">Sign in to see features</p>' +
            '<p class="empty-state-description">Reading features requires an account. ' +
            '<a class="btn btn-primary" href="/login">Sign in</a></p></div>';
        } else {
          var code = err && err.status ? ' (HTTP ' + err.status + ')' : '';
          box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
            '<p class="empty-state-title">Could not load project' + code + '</p>' +
            '<p class="empty-state-description">The API did not respond as expected. Try again.</p></div>';
        }
      });
  });
})();
