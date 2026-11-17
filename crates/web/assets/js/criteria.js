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
//
// Rendering (step 2b): `el()` builds nodes only. Criterion names, profile names,
// descriptions, feature titles and server refusals become text nodes or attribute
// values; this file has no innerHTML or insertAdjacentHTML sink.
(function () {
  'use strict';

  var el = window.ConcordDom.el;

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
    // Weights the user has typed, by criterion id. Empty until they press Rank.
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
    return window.ConcordApi.get(base + path).catch(toRefusal);
  }

  // The page's refusals keep the `{status, body}` shape seven call sites read.
  function toRefusal(error) {
    throw {
      status: error && error.status ? error.status : 0,
      body: { error: error && error.message ? error.message : String(error) }
    };
  }

  function send(method, path, payload) {
    return window.ConcordApi.request(method, base + path, payload).catch(toRefusal);
  }

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

    return window.ConcordApi.post(base + '/rank/composite', payload).catch(toRefusal)
      .then(function (body) {
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
        root.replaceChildren(el('div', { class: 'empty-state' },
          el('p', { class: 'empty-state-title' }, 'The criteria could not be loaded: ' + refusal(err))));
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

  // Columns come from the criteria the composite actually used (the union of every
  // result's contributions), in the criteria's own order.
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

  function cellNode(res, criterionID) {
    var found = null;
    (res.contributions || []).forEach(function (c) { if (c.criterion_id === criterionID) found = c; });
    if (!found) {
      // No contribution means no comparison ever involved this feature under this
      // criterion. That is a state, not the midpoint of an empty distribution.
      return el('td', {
        class: 'crit-cell crit-unrated',
        title: 'no comparison has involved this feature under this criterion yet, so there is nothing to measure'
      }, 'unrated');
    }
    if (!found.games) {
      return el('td', {
        class: 'crit-cell crit-unrated',
        title: 'no comparisons yet: rating ' + num(found.raw, 0) + ' ± ' + num(found.rd, 0) + ', the starting value'
      }, 'unrated');
    }
    var dir = criterionByID(criterionID);
    var flip = dir && dir.direction === 'lower_is_better';
    return el('td', {
      class: 'crit-cell',
      title: flip ? 'lower is better for this criterion: 1.00 is the best score here' : undefined
    }, [
      num(found.normalised, 2),
      el('span', { class: 'muted crit-games' }, ' · ' + found.games)
    ]);
  }

  function tableNodes() {
    var body = state.composite || {};
    var results = body.results || [];
    if (!state.criteria.length) {
      return el('div', { class: 'empty-state' }, [
        el('p', { class: 'empty-state-title' }, 'No criteria yet'),
        el('p', { class: 'muted' }, 'A project starts with no dimensions, and a criterion is what ' +
          'one dimension is called. Add one below — "simplicity and maintenance cost" and ' +
          '"risk" are the two the spec names — then vote on it, and the table here ' +
          'shows what the votes concluded and why the order came out that way.')
      ]);
    }
    if (!results.length) {
      return el('div', { class: 'empty-state' }, [
        el('p', { class: 'empty-state-title' }, 'Nothing to rank'),
        el('p', { class: 'muted' }, 'The composite came back with no features. A criterion ranks the ' +
          'features a project has, so with none proposed there is nothing to compare. ' +
          (state.requireMinRD ? 'The "ignore criteria with no votes" box is also on, ' +
            'which drops every criterion nobody has voted on.' : ''))
      ]);
    }
    var cols = columns(results);
    var head = el('tr', {}, [
      el('th', { class: 'crit-rank' }, '#'),
      el('th', {}, 'Feature'),
      el('th', {}, 'Composite')
    ]);
    cols.forEach(function (c) {
      var crit = criterionByID(c.id);
      head.appendChild(el('th', {}, [
        c.name,
        state.profile ? null : el('span', { class: 'muted' }, ' w ' + num((crit && state.weights[c.id] != null)
          ? state.weights[c.id] : (crit ? crit.default_weight : 0), 2)),
        c.direction === 'lower_is_better' ? el('span', { class: 'muted crit-dir' }, ' lower is better') : null,
        (!c.active || (crit && !crit.active)) ? el('span', { class: 'muted crit-dir' }, ' retired') : null
      ]));
    });
    var tbody = el('tbody');
    results.forEach(function (res) {
      var row = el('tr', {}, [
        el('td', { class: 'crit-rank' }, String(res.rank)),
        el('td', { class: 'crit-feature' }, titleOf(res.feature_id)),
        el('td', { class: 'crit-composite' }, [
          num(res.score, 2),
          el('span', { class: 'muted crit-conf' }, ' ± ' + num(res.confidence, 0))
        ])
      ]);
      cols.forEach(function (c) { row.appendChild(cellNode(res, c.id)); });
      tbody.appendChild(row);
    });
    var profileNote = state.profile
      ? [' , and the weights come from the saved profile ', el('strong', {}, state.profile)]
      : ['.'];
    return [
      el('table', { class: 'crit-table' }, [el('thead', {}, head), tbody]),
      el('p', { class: 'form-note' }, 'A feature nobody has compared under any criterion has no ' +
        'composite score and is not listed: it has no place in an order built from comparisons.'),
      el('p', { class: 'form-note' }, [
        'Composite is the weighted blend of the columns to its right; the number after ± is the ' +
        'mean rating deviation behind it, so a wide figure means the order is provisional. ' +
        'A cell reads ',
        el('em', {}, 'unrated'),
        ' when nobody has compared anything under that criterion yet'
      ].concat(profileNote))
    ];
  }

  function controlsNode() {
    var parts = [
      el('h2', {}, 'Ranking'),
      el('p', { class: 'form-note' }, 'A weight says how much this project cares about a criterion. ' +
        'The numbers are relative — 3:1 and 0.75:0.25 mean the same thing — and the default is ' +
        'the criterion\'s own default weight.'),
      el('div', { class: 'crit-weights' }, state.criteria.map(function (c) {
        var v = state.weights[c.id] != null ? state.weights[c.id] : c.default_weight;
        return el('label', { class: 'crit-weight' }, [
          c.name + ' ',
          el('input', {
            type: 'number', min: '0', step: '0.25',
            'data-weight': String(c.id), value: num(v, 2)
          })
        ]);
      })),
      el('label', { class: 'crit-check' }, [
        el('input', { type: 'checkbox', id: 'crit-include-inactive', checked: state.includeInactive }),
        ' Include retired criteria'
      ]),
      el('p', { class: 'form-note' }, 'Retiring a criterion takes it out of the blend. Ticking ' +
        'this ranks against it anyway, which is how you check whether retiring it changed anything.'),
      el('label', { class: 'crit-check' }, [
        el('input', { type: 'checkbox', id: 'crit-require-min-rd', checked: state.requireMinRD }),
        ' Ignore criteria with no votes'
      ]),
      el('p', { class: 'form-note' }, 'A criterion nobody has compared anything under still ' +
        'contributes its starting value to every feature equally. Ignoring it keeps an ' +
        'empty dimension from shaping a result nobody has an opinion about.'),
      el('div', { class: 'crit-actions' },
        el('button', { type: 'button', class: 'btn btn-primary', 'data-rank': '' }, 'Rank with these weights')),
      el('h3', {}, 'Saved weightings')
    ];
    if (!state.profiles.length) {
      parts.push(el('p', { class: 'form-note' }, 'None saved yet. A saved weighting is a nameable thing — ' +
        '"edge deployment" — rather than numbers retyped at every query.'));
    } else {
      parts.push(el('label', {}, [
        'Load ',
        el('select', { id: 'crit-profile' }, [el('option', { value: '' }, '— choose a saved weighting —')]
          .concat(state.profiles.map(function (p) {
            return el('option', { value: p.slug, selected: p.slug === state.profile }, p.name);
          })))
      ]));
      parts.push(el('button', { type: 'button', class: 'btn', 'data-load-profile': '' }, 'Load it'));
      if (state.profile) {
        parts.push(el('button', { type: 'button', class: 'btn', 'data-clear-profile': '' }, 'Back to my own weights'));
      }
    }
    parts.push(el('div', { class: 'crit-save' }, [
      el('label', {}, ['Name ', el('input', { id: 'crit-profile-name', type: 'text', placeholder: 'Edge deployment' })]),
      el('label', {}, ['Slug ', el('input', { id: 'crit-profile-slug', type: 'text', placeholder: 'edge-deployment' })]),
      el('button', { type: 'button', class: 'btn', 'data-save-profile': '' }, 'Save this weighting')
    ]));
    parts.push(el('p', { class: 'form-note' }, 'Saving stores the numbers currently in the boxes above. ' +
      'Loading one replaces them, and an explicit weight you type after that wins over ' +
      'the profile — so the selection clears when you edit a weight rather than sending ' +
      'both and letting the server pick.'));
    return el('div', { class: 'crit-controls' }, parts);
  }

  function optionsNodes() {
    return state.features.map(function (f) {
      return el('option', { value: String(f.id) }, f.title);
    });
  }

  function manageNode() {
    var parts = [el('h2', {}, 'Criteria')];
    if (!state.criteria.length) {
      parts.push(el('p', { class: 'form-note' }, 'No criteria yet — add the first one below.'));
    }
    state.criteria.forEach(function (c) {
      var mid = String(c.id);
      var row = el('div', { class: 'crit-row', 'data-criterion': mid }, [
        el('div', { class: 'crit-row-head' }, [
          el('h3', {}, [c.name, c.active ? null : el('span', { class: 'muted' }, ' (retired)')]),
          el('span', { class: 'muted crit-slug' }, c.slug + ' · ' +
            (c.direction === 'lower_is_better' ? 'lower is better' : 'higher is better') +
            ' · default weight ' + num(c.default_weight, 2))
        ]),
        el('p', { class: 'crit-desc' }, c.description || 'No description.'),
        el('button', { type: 'button', class: 'btn', 'data-toggle-active': mid },
          c.active ? 'Retire this criterion' : 'Bring it back'),
        el('p', { class: 'form-note' }, 'A retired criterion is left out of the blend; its ' +
          'votes stay on the record. Bringing it back needs no re-voting.'),
        el('div', { class: 'crit-vote' }, [el('h4', {}, 'Compare two features on this criterion')])
      ]);
      var vote = row.children[row.children.length - 1];
      if (state.features.length < 2) {
        vote.appendChild(el('p', { class: 'form-note' }, 'This project has fewer than two features, so there ' +
          'is nothing to compare on this criterion.'));
      } else {
        vote.appendChild(el('label', {}, ['Left ', el('select', { 'data-vote-a': mid }, optionsNodes())]));
        vote.appendChild(el('label', {}, ['Right ', el('select', { 'data-vote-b': mid }, optionsNodes())]));
        vote.appendChild(el('label', {}, [
          'Outcome ',
          el('select', { 'data-vote-outcome': mid }, OUTCOMES.map(function (o) {
            return el('option', { value: o.v }, o.v + ' — ' + o.label);
          }))
        ]));
        vote.appendChild(el('button', { type: 'button', class: 'btn', 'data-vote': mid }, 'Record it'));
        var notes = [];
        OUTCOMES.forEach(function (o, index) {
          notes.push(el('strong', {}, o.v), ' ' + o.note);
          if (index < OUTCOMES.length - 1) notes.push(' · ');
        });
        vote.appendChild(el('p', { class: 'form-note' }, notes));
      }
      parts.push(row);
    });

    parts.push(el('div', { class: 'crit-create' }, [
      el('h3', {}, 'Add a criterion'),
      el('label', {}, ['Slug ', el('input', { id: 'crit-slug', type: 'text', placeholder: 'risk' })]),
      el('label', {}, ['Name ', el('input', { id: 'crit-name', type: 'text', placeholder: 'Risk' })]),
      el('label', {}, ['Description ', el('input', { id: 'crit-desc', type: 'text', placeholder: 'How badly could this go wrong?' })]),
      el('label', {}, [
        'Direction ',
        el('select', { id: 'crit-direction' }, [
          el('option', { value: 'higher_is_better' }, 'higher is better'),
          el('option', { value: 'lower_is_better' }, 'lower is better')
        ])
      ]),
      el('label', {}, ['Default weight ', el('input', { id: 'crit-default-weight', type: 'number', min: '0', step: '0.25', value: '1.00' })]),
      el('button', { type: 'button', class: 'btn btn-primary', 'data-create': '' }, 'Add it'),
      el('p', { class: 'form-note' }, 'A criterion is a question to compare features against. ' +
        '"Lower is better" is for costs — maintenance effort, risk, migration pain — where ' +
        'the best feature is the one with the smallest number. Adding one needs contributor ' +
        'access to this project.')
    ]));
    return el('div', { class: 'crit-manage' }, parts);
  }

  function flatten(nodes) {
    return nodes.reduce(function (out, node) {
      return out.concat(Array.isArray(node) ? flatten(node) : [node]);
    }, []);
  }

  function render() {
    root.replaceChildren.apply(root, flatten([tableNodes(), controlsNode(), manageNode()]));
    root.setAttribute('aria-busy', 'false');
  }

  // ------------------------------------------------------------------ wiring

  function readWeights() {
    var out = {};
    Array.prototype.forEach.call(root.querySelectorAll('[data-weight]'), function (node) {
      var id = node.getAttribute('data-weight');
      var v = parseFloat(node.value);
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
          say('Not recorded: ' + refusal(err), true);
        })
        .then(function () { t.disabled = false; });
      return;
    }
  });

  load();
})();
