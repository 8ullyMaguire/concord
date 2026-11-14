/* apiAuth: the session's Authorization header, for API calls.
 *
 * Every read on this page used to go out anonymous -- harmless while the project is
 * public, and fatal for the projects whose settings most need changing: a member
 * reaches a protected project (the navigation cookie opened the page) and then every
 * panel 404s, because the page's own reads carried no credential. session.js has
 * exported authHeaders() all along; this is the two lines that use it, and for a
 * signed-out visitor the request is identical to before. The hand-built copies
 * ("var headers = {Content-Type}; if (token) headers.Authorization = ...") are gone
 * for the same reason there is one of these: five copies of a rule is five chances
 * to leave the token out, and two of them had. */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}

// Concord criteria page -- spec §6.4's comparison table.
//
// The whole table comes from ONE request, POST /rank/composite, whose results carry
// the per-criterion contributions that produced each order. Three things this page
// deliberately does NOT do:
//
// 1. Rank anything client-side. The API returns "the order the forge concluded";
//    re-sorting by score or by a bare Glicko rating would discard the normalisation
//    and the weights, and produce an order nobody agreed to.
// 2. Print a normalised value for a criterion with no votes. A factor with
//    games == 0 sits at the Glicko starting rating, and its normalised midpoint is
//    an artifact of an empty distribution, not a measurement. It reads "unrated".
// 3. Fold `both`/`neither`/`skip` into the score. They are recorded (the audit
//    trail keeps them) and move no rating, which is the difference between a vote
//    that did nothing and a vote that was lost.
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function num(n, dp) {
    if (n == null || isNaN(n)) return '—';
    return Number(n).toFixed(dp == null ? 2 : dp);
  }

  // The five outcomes the schema's CHECK allows, with what each one DOES. They are
  // not interchangeable and the labels say so rather than leaving a voter to guess.
  var OUTCOMES = [
    { v: 'a', label: 'the left one', note: 'moves both ratings: the left one gains, the right one loses' },
    { v: 'b', label: 'the right one', note: 'moves both ratings: the right one gains, the left one loses' },
    { v: 'both', label: 'both hold', note: 'recorded, and moves no rating' },
    { v: 'neither', label: 'neither holds', note: 'recorded, and moves no rating' },
    { v: 'skip', label: 'skip', note: 'recorded, changes nothing — abstention is data' }
  ];

  var root = document.getElementById('criteria-root');
  var notice = document.getElementById('criteria-notice');
  if (!root) return;

  var slug = slugFromPath();
  var base = '/api/v1/projects/' + encodeURIComponent(slug);

  var state = {
    criteria: [],
    profiles: [],
    features: [],
    // Weights the user has typed, by criterion id. Empty until they press Rank:
    // the server treats an empty body as "every criterion's default weight", and
    // that is the honest initial state rather than freezing defaults into a vector.
    weights: {},
    profile: '',
    includeInactive: false,
    requireMinRD: false,
    composite: null
  };

  function say(msg, bad) {
    if (!notice) return;
    notice.textContent = msg || '';
    notice.className = 'form-note' + (bad ? ' form-note-bad' : '');
  }

  function get(path) {
    return fetch(base + path, { headers: apiAuth() }).then(function (r) {
      return r.text().then(function (t) {
        var body = {};
        try { body = t ? JSON.parse(t) : {}; } catch (e) { body = { error: t }; }
        if (!r.ok) throw { status: r.status, body: body };
        return body;
      });
    });
  }

  function send(method, path, payload) {
    return fetch(base + path, {
      method: method,
      headers: apiAuth({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(payload)
    }).then(function (r) {
      return r.text().then(function (t) {
        var body = {};
        try { body = t ? JSON.parse(t) : {}; } catch (e) { body = { error: t }; }
        if (!r.ok) throw { status: r.status, body: body };
        return body;
      });
    });
  }

  // refusal(): the server's own words, and for the two statuses whose meaning is
  // not obvious a sentence saying what to do about them.
  function refusal(err) {
    var msg = (err && err.body && (err.body.error || err.body.message)) || 'the server refused it';
    if (err && err.status === 401) {
      return msg + ' — this is a signed-in action; sign in and try again.';
    }
    if (err && err.status === 403) {
      return msg + ' — your role in this project is not enough for this change.';
    }
    return msg;
  }

  function titleOf(featureID) {
    for (var i = 0; i < state.features.length; i++) {
      if (state.features[i].id === featureID) return state.features[i].title;
    }
    return 'feature ' + featureID;
  }

  // rank(msg): `msg` is what to say once the new order is on screen. The message
  // belongs to this function rather than to the caller because the rank always
  // re-renders, and the first version's caller-set message was wiped by the
  // `say('')` on the success path -- "Added UX" appeared and disappeared inside one
  // tick, which is a write that reports nothing.
  function rank(msg) {
    say('Ranking…');
    var payload = {};
    // Explicit weights beat a saved profile on the server, so sending both would
    // mean the profile is decorative. Editing a weight clears the profile instead.
    if (state.profile) {
      payload.profile = state.profile;
    } else if (Object.keys(state.weights).length) {
      payload.weights = state.weights;
    }
    if (state.includeInactive) payload.include_inactive = true;
    if (state.requireMinRD) payload.require_min_rd = true;

    return fetch(base + '/rank/composite', {
      method: 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(payload)
    }).then(function (r) {
      return r.text().then(function (t) {
        var body = {};
        try { body = t ? JSON.parse(t) : {}; } catch (e) { body = {}; }
        if (!r.ok) throw { status: r.status, body: body };
        return body;
      });
    }).then(function (body) {
      state.composite = body;
      say(msg || '');
      render();
    }).catch(function (err) {
      say('The ranking could not be built: ' + refusal(err), true);
    });
  }

  function loadCriteria() {
    return get('/criteria').then(function (list) { state.criteria = list || []; });
  }

  function loadProfiles() {
    return get('/criteria-profiles').then(function (list) { state.profiles = list || []; });
  }

  function loadFeatures() {
    return get('/priorities').then(function (list) { state.features = list || []; });
  }

  function load() {
    root.setAttribute('aria-busy', 'true');
    return Promise.all([loadCriteria(), loadProfiles(), loadFeatures()])
      .then(function () { return rank(); })
      .then(function () {
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
      })
      .catch(function (err) {
        root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
          esc('The criteria could not be loaded: ' + refusal(err)) + '</p></div>';
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
      });
  }

  // ------------------------------------------------------------------ rendering

  function criterionByID(id) {
    for (var i = 0; i < state.criteria.length; i++) {
      if (state.criteria[i].id === id) return state.criteria[i];
    }
    return null;
  }

  // The column set is taken from the criteria the composite actually used (the
  // union of every result's contributions), in the criteria's own order, so a
  // retired criterion is absent from the table for the same reason it is absent
  // from the score -- rather than present and mysteriously empty.
  function columns(results) {
    var seen = {};
    var order = [];
    results.forEach(function (res) {
      (res.contributions || []).forEach(function (c) {
        if (!seen[c.criterion_id]) { seen[c.criterion_id] = true; order.push(c.criterion_id); }
      });
    });
    return order.map(function (id) {
      var c = criterionByID(id);
      return { id: id, slug: c ? c.slug : String(id), name: c ? c.name : ('criterion ' + id),
               direction: c ? c.direction : '', active: c ? c.active : true };
    });
  }

  function cellHTML(res, criterionID) {
    var found = null;
    (res.contributions || []).forEach(function (c) { if (c.criterion_id === criterionID) found = c; });
    if (!found) {
      // The composite builds a contribution only from a criterion_ratings ROW, so
      // an absent cell means no comparison has ever involved this feature under
      // this criterion. That is the same fact as games == 0, and both are a state
      // rather than a number: the midpoint of an empty distribution is not a
      // measurement.
      return '<td class="crit-cell crit-unrated" title="no comparison has involved ' +
        'this feature under this criterion yet, so there is nothing to measure">unrated</td>';
    }
    if (!found.games) {
      return '<td class="crit-cell crit-unrated" title="no comparisons yet: rating ' +
        esc(num(found.raw, 0)) + ' ± ' + esc(num(found.rd, 0)) + ', the starting value">unrated</td>';
    }
    var dir = criterionByID(criterionID);
    var flip = dir && dir.direction === 'lower_is_better';
    return '<td class="crit-cell"' +
      (flip ? ' title="lower is better for this criterion: 1.00 is the best score here"' : '') +
      '>' + esc(num(found.normalised, 2)) +
      '<span class="muted crit-games"> · ' + found.games + '</span></td>';
  }

  function tableHTML() {
    var body = state.composite || {};
    var results = body.results || [];
    if (!state.criteria.length) {
      return '<div class="empty-state"><p class="empty-state-title">No criteria yet</p>' +
        '<p class="muted">A project starts with no dimensions, and a criterion is what ' +
        'one dimension is called. Add one below — "simplicity and maintenance cost" and ' +
        '"risk" are the two the spec names — then vote on it, and the table here ' +
        'shows what the votes concluded and why the order came out that way.</p></div>';
    }
    if (!results.length) {
      return '<div class="empty-state"><p class="empty-state-title">Nothing to rank</p>' +
        '<p class="muted">The composite came back with no features. A criterion ranks the ' +
        'features a project has, so with none proposed there is nothing to compare. ' +
        (state.requireMinRD ? 'The "ignore criteria with no votes" box is also on, ' +
          'which drops every criterion nobody has voted on.' : '') + '</p></div>';
    }
    var cols = columns(results);
    var html = '<table class="crit-table"><thead><tr>' +
      '<th class="crit-rank">#</th><th>Feature</th>' +
      '<th>Composite</th>';
    cols.forEach(function (c) {
      var crit = criterionByID(c.id);
      var w = state.profile ? '' :
        ' <span class="muted">w ' + esc(num((crit && state.weights[c.id] != null)
          ? state.weights[c.id] : (crit ? crit.default_weight : 0), 2)) + '</span>';
      html += '<th>' + esc(c.name) + w +
        (c.direction === 'lower_is_better'
          ? ' <span class="muted crit-dir">lower is better</span>' : '') +
        (!c.active || (crit && !crit.active)
          ? ' <span class="muted crit-dir">retired</span>' : '') + '</th>';
    });
    html += '</tr></thead><tbody>';
    results.forEach(function (res) {
      html += '<tr><td class="crit-rank">' + esc(String(res.rank)) + '</td>' +
        '<td class="crit-feature">' + esc(titleOf(res.feature_id)) + '</td>' +
        '<td class="crit-composite">' + esc(num(res.score, 2)) +
        '<span class="muted crit-conf"> ± ' + esc(num(res.confidence, 0)) + '</span></td>';
      cols.forEach(function (c) { html += cellHTML(res, c.id); });
      html += '</tr>';
    });
    html += '</tbody></table>';
    html += '<p class="form-note">A feature nobody has compared under any criterion has no ' +
      'composite score and is not listed: it has no place in an order built from ' +
      'comparisons.</p>';
    html += '<p class="form-note">Composite is the weighted blend of the columns to its right; ' +
      'the number after ± is the mean rating deviation behind it, so a wide figure means ' +
      'the order is provisional. A cell reads <em>unrated</em> when nobody has compared ' +
      'anything under that criterion yet' +
      (state.profile ? ', and the weights come from the saved profile <strong>' +
        esc(state.profile) + '</strong>' : '') + '.</p>';
    return html;
  }

  function controlsHTML() {
    var html = '<div class="crit-controls">';
    html += '<h2>Ranking</h2>';
    html += '<p class="form-note">A weight says how much this project cares about a ' +
      'criterion. The numbers are relative — 3:1 and 0.75:0.25 mean the same thing — ' +
      'and the default is the criterion\'s own default weight.</p>';
    html += '<div class="crit-weights">';
    state.criteria.forEach(function (c) {
      var v = state.weights[c.id] != null ? state.weights[c.id] : c.default_weight;
      html += '<label class="crit-weight">' + esc(c.name) +
        ' <input type="number" min="0" step="0.25" data-weight="' + esc(String(c.id)) +
        '" value="' + esc(num(v, 2)) + '"></label>';
    });
    html += '</div>';
    html += '<label class="crit-check"><input type="checkbox" id="crit-include-inactive"' +
      (state.includeInactive ? ' checked' : '') + '> Include retired criteria</label>';
    html += '<p class="form-note">Retiring a criterion takes it out of the blend. Ticking ' +
      'this ranks against it anyway, which is how you check whether retiring it changed ' +
      'anything.</p>';
    html += '<label class="crit-check"><input type="checkbox" id="crit-require-min-rd"' +
      (state.requireMinRD ? ' checked' : '') + '> Ignore criteria with no votes</label>';
    html += '<p class="form-note">A criterion nobody has compared anything under still ' +
      'contributes its starting value to every feature equally. Ignoring it keeps an ' +
      'empty dimension from shaping a result nobody has an opinion about.</p>';
    html += '<div class="crit-actions">' +
      '<button type="button" class="btn btn-primary" data-rank="1">Rank with these weights</button>' +
      '</div>';

    html += '<h3>Saved weightings</h3>';
    if (!state.profiles.length) {
      html += '<p class="form-note">None saved yet. A saved weighting is a nameable thing — ' +
        '"edge deployment" — rather than numbers retyped at every query.</p>';
    } else {
      html += '<label>Load <select id="crit-profile">' +
        '<option value="">— choose a saved weighting —</option>';
      state.profiles.forEach(function (p) {
        html += '<option value="' + esc(p.slug) + '"' +
          (p.slug === state.profile ? ' selected' : '') + '>' + esc(p.name) + '</option>';
      });
      html += '</select></label>';
      html += '<button type="button" class="btn" data-load-profile="1">Load it</button>';
      if (state.profile) {
        html += '<button type="button" class="btn" data-clear-profile="1">Back to my own weights</button>';
      }
    }
    html += '<div class="crit-save">' +
      '<label>Name <input id="crit-profile-name" type="text" placeholder="Edge deployment"></label>' +
      '<label>Slug <input id="crit-profile-slug" type="text" placeholder="edge-deployment"></label>' +
      '<button type="button" class="btn" data-save-profile="1">Save this weighting</button></div>';
    html += '<p class="form-note">Saving stores the numbers currently in the boxes above. ' +
      'Loading one replaces them, and an explicit weight you type after that wins over ' +
      'the profile — so the selection clears when you edit a weight rather than sending ' +
      'both and letting the server pick.</p>';
    html += '</div>';
    return html;
  }

  function manageHTML() {
    var html = '<div class="crit-manage"><h2>Criteria</h2>';
    if (!state.criteria.length) {
      html += '<p class="form-note">No criteria yet — add the first one below.</p>';
    }
    state.criteria.forEach(function (c) {
      var mid = esc(String(c.id));
      html += '<div class="crit-row" data-criterion="' + mid + '">';
      html += '<div class="crit-row-head"><h3>' + esc(c.name) +
        (c.active ? '' : ' <span class="muted">(retired)</span>') + '</h3>' +
        '<span class="muted crit-slug">' + esc(c.slug) + ' · ' +
        esc(c.direction === 'lower_is_better' ? 'lower is better' : 'higher is better') +
        ' · default weight ' + esc(num(c.default_weight, 2)) + '</span></div>';
      html += '<p class="crit-desc">' + esc(c.description || 'No description.') + '</p>';
      html += '<button type="button" class="btn" data-toggle-active="' + mid + '">' +
        (c.active ? 'Retire this criterion' : 'Bring it back') + '</button>';
      html += '<p class="form-note">A retired criterion is left out of the blend; its ' +
        'votes stay on the record. Bringing it back needs no re-voting.</p>';

      html += '<div class="crit-vote"><h4>Compare two features on this criterion</h4>';
      if (state.features.length < 2) {
        html += '<p class="form-note">This project has fewer than two features, so there ' +
          'is nothing to compare on this criterion.</p>';
      } else {
        html += '<label>Left <select data-vote-a="' + mid + '">' + optionsHTML() + '</select></label>';
        html += '<label>Right <select data-vote-b="' + mid + '">' + optionsHTML() + '</select></label>';
        html += '<label>Outcome <select data-vote-outcome="' + mid + '">' +
          OUTCOMES.map(function (o) {
            return '<option value="' + esc(o.v) + '">' + esc(o.v + ' — ' + o.label) + '</option>';
          }).join('') + '</select></label>';
        html += '<button type="button" class="btn" data-vote="' + mid + '">Record it</button>';
        html += '<p class="form-note">' + OUTCOMES.map(function (o) {
          return '<strong>' + esc(o.v) + '</strong> ' + esc(o.note);
        }).join(' · ') + '</p>';
      }
      html += '</div></div>';
    });

    html += '<div class="crit-create"><h3>Add a criterion</h3>' +
      '<label>Slug <input id="crit-slug" type="text" placeholder="risk"></label>' +
      '<label>Name <input id="crit-name" type="text" placeholder="Risk"></label>' +
      '<label>Description <input id="crit-desc" type="text" placeholder="How badly could this go wrong?"></label>' +
      '<label>Direction <select id="crit-direction">' +
      '<option value="higher_is_better">higher is better</option>' +
      '<option value="lower_is_better">lower is better</option></select></label>' +
      '<label>Default weight <input id="crit-default-weight" type="number" min="0" step="0.25" value="1.00"></label>' +
      '<button type="button" class="btn btn-primary" data-create="1">Add it</button>' +
      '<p class="form-note">A criterion is a question to compare features against. ' +
      '"Lower is better" is for costs — maintenance effort, risk, migration pain — where ' +
      'the best feature is the one with the smallest number. Adding one needs contributor ' +
      'access to this project.</p></div>';
    html += '</div>';
    return html;
  }

  function optionsHTML() {
    return state.features.map(function (f) {
      return '<option value="' + esc(String(f.id)) + '">' + esc(f.title) + '</option>';
    }).join('');
  }

  function render() {
    root.innerHTML = tableHTML() + controlsHTML() + manageHTML();
    root.setAttribute('aria-busy', 'false');
  }

  // ------------------------------------------------------------------ wiring

  function readWeights() {
    var out = {};
    Array.prototype.forEach.call(root.querySelectorAll('[data-weight]'), function (el) {
      var id = el.getAttribute('data-weight');
      var v = parseFloat(el.value);
      if (!isNaN(v) && v >= 0) out[id] = v;
    });
    return out;
  }

  root.addEventListener('change', function (e) {
    var t = e.target;
    if (!t) return;
    if (t.hasAttribute && t.hasAttribute('data-weight')) {
      // The typed number is now the intent, so the profile stops being the source.
      state.weights = readWeights();
      if (state.profile) {
        state.profile = '';
        say('Using your own weights now; the saved weighting is no longer applied.');
      }
    }
    if (t.id === 'crit-include-inactive') state.includeInactive = t.checked;
    if (t.id === 'crit-require-min-rd') state.requireMinRD = t.checked;
  });

  root.addEventListener('click', function (e) {
    var t = e.target;
    if (!t || !t.getAttribute) return;

    if (t.hasAttribute('data-rank')) {
      state.weights = readWeights();
      state.profile = '';
      rank();
      return;
    }

    if (t.hasAttribute('data-load-profile')) {
      var sel = root.querySelector('#crit-profile');
      var val = sel ? sel.value : '';
      if (!val) { say('Choose a saved weighting first.', true); return; }
      state.profile = val;
      state.weights = {};
      rank();
      return;
    }

    if (t.hasAttribute('data-clear-profile')) {
      state.profile = '';
      state.weights = {};
      rank();
      return;
    }

    if (t.hasAttribute('data-save-profile')) {
      var nameEl = root.querySelector('#crit-profile-name');
      var slugEl = root.querySelector('#crit-profile-slug');
      var name = nameEl ? nameEl.value.trim() : '';
      var pslug = slugEl ? slugEl.value.trim() : '';
      if (!name) { say('A saved weighting needs a name.', true); return; }
      if (!pslug) {
        pslug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
      }
      send('POST', '/criteria-profiles', { slug: pslug, name: name, weights: readWeights() })
        .then(function () {
          return loadProfiles().then(function () {
            render();
            say('Saved "' + name + '" with the weights in the boxes above.');
          });
        })
        .catch(function (err) { say('Not saved: ' + refusal(err), true); });
      return;
    }

    if (t.hasAttribute('data-create')) {
      var cs = root.querySelector('#crit-slug');
      var cn = root.querySelector('#crit-name');
      var cd = root.querySelector('#crit-desc');
      var cdir = root.querySelector('#crit-direction');
      var cw = root.querySelector('#crit-default-weight');
      var payload = {
        slug: cs ? cs.value.trim() : '',
        name: cn ? cn.value.trim() : '',
        description: cd ? cd.value.trim() : '',
        direction: cdir ? cdir.value : 'higher_is_better'
      };
      var w = cw ? parseFloat(cw.value) : NaN;
      if (!isNaN(w)) payload.default_weight = w;
      if (!payload.slug || !payload.name) {
        say('A criterion needs a slug and a name.', true);
        return;
      }
      send('POST', '/criteria', payload)
        .then(function () {
          return loadCriteria().then(function () {
            return rank('Added "' + payload.name + '". Vote on it below to start ' +
              'filling its pool.');
          });
        })
        .catch(function (err) { say('Not added: ' + refusal(err), true); });
      return;
    }

    if (t.hasAttribute('data-toggle-active')) {
      var id = t.getAttribute('data-toggle-active');
      var c = criterionByID(Number(id));
      if (!c) return;
      send('PUT', '/criteria/' + encodeURIComponent(id) + '/active', { active: !c.active })
        .then(function () {
          var msg = c.active
            ? 'Retired "' + c.name + '". Retired criteria are out of the blend until ' +
              'you tick "include retired criteria".'
            : 'Brought "' + c.name + '" back; its votes are as they were.';
          return loadCriteria().then(function () { return rank(msg); });
        })
        .catch(function (err) { say('Not changed: ' + refusal(err), true); });
      return;
    }

    if (t.hasAttribute('data-vote')) {
      var cid = t.getAttribute('data-vote');
      var aEl = root.querySelector('[data-vote-a="' + cid + '"]');
      var bEl = root.querySelector('[data-vote-b="' + cid + '"]');
      var oEl = root.querySelector('[data-vote-outcome="' + cid + '"]');
      var a = aEl ? Number(aEl.value) : 0;
      var b = bEl ? Number(bEl.value) : 0;
      if (a === b) {
        say('Pick two different features — a feature cannot be compared with itself.', true);
        return;
      }
      t.disabled = true;
      var outcome = oEl ? oEl.value : '';
      send('POST', '/criteria/' + encodeURIComponent(cid) + '/vote',
        { feature_a: a, feature_b: b, outcome: outcome })
        .then(function (body) {
          // The two updated ratings go in the NOTICE, not in the row: the rank that
          // follows re-renders the whole page, so anything written into the row is
          // gone before it can be read.
          var bits = ['Recorded.'];
          if (outcome === 'skip' || outcome === 'both' || outcome === 'neither') {
            bits.push('"' + outcome + '" moves no rating — it is on the record and ' +
              'changes nothing.');
          }
          ['feature_a', 'feature_b'].forEach(function (k) {
            var r = body[k];
            if (!r) return;
            bits.push(titleOf(r.feature_id) + ': ' + num(r.r, 0) + ' ± ' +
              num(r.rd, 0) + ' over ' + r.games + ' comparison(s).');
          });
          return loadCriteria().then(function () { return rank(bits.join(' ')); });
        })
        .catch(function (err) {
          // A re-vote on the same pair is refused as a double count, which is worth
          // saying in those words rather than as a bare 400.
          say('Not recorded: ' + refusal(err), true);
        })
        .then(function () { t.disabled = false; });
      return;
    }
  });

  load();
})();
