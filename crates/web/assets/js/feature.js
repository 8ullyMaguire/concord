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
// 3. Every value from the server is a text node or a setAttribute value. Titles,
//    bodies, tag names, solution claims and refusal messages are authored free text;
//    numbers arrive as JSON and are not trusted to be finite.
//
// Rendering (step 2b): `el()` nodes only, no HTML string sink anywhere on this page.
(function () {
  'use strict';

  var el = window.ConcordDom.el;

  function where() {
    // /projects/{slug}/features/{id} splits into FOUR non-empty parts, so the slug
    // is index 1 and the id is index 3 -- NOT index 2. Reading parts[2] once made
    // the page request /features/features/solutions and 400 on all three fetches.
    var parts = window.location.pathname.split('/').filter(Boolean);
    var i = parts.indexOf('features');
    return {
      slug: parts.length >= 2 ? decodeURIComponent(parts[1]) : '',
      id: i > 0 && parts.length > i + 1 ? decodeURIComponent(parts[i + 1]) : ''
    };
  }

  var root = document.getElementById('feature-root');
  if (!root) return;

  function flatten(nodes) {
    return nodes.reduce(function (out, node) {
      return out.concat(Array.isArray(node) ? flatten(node) : [node]);
    }, []);
  }

  function json(path) {
    // One client (review item 9b): a 404 is `null` here — the page renders "no such feature" from it.
    return window.ConcordApi.get(path).catch(function (error) {
      if (error && error.status === 404) return null;
      throw error;
    });
  }

  // aria-busy is cleared through the house helper, not by setting the attribute.
  function done() {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    else root.setAttribute('aria-busy', 'false');
  }

  // wireContests attaches §6.4's challenge flow: open the reason field, send the
  // contest, and report the SERVER's refusal in the row it belongs to.
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
            if (err) { err.textContent = 'A challenge needs its reason: the claim’s author has to know what to answer.'; err.hidden = false; }
            return;
          }
          btn.disabled = true;
          var path = base + '/features/' + encodeURIComponent(featureID) + '/solutions/' +
            encodeURIComponent(btn.getAttribute('data-contest-send')) + '/coverage/' +
            encodeURIComponent(btn.getAttribute('data-contest-send-complaint')) + '/contest';
          window.ConcordApi.post(path, { reason: reason }).then(function () {
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
  function wireTags(base, featureID) {
    var writer = pageWriter(root.querySelector('[data-tag-error]'));
    function fail(msg) { writer.fail(msg); }
    function send(path, payload, btn) { writer.send(path, payload, btn); }
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
      return el('p', { class: 'muted' }, 'Not yet compared. A feature has no rating until it ' +
        'has been through at least one pairwise comparison.');
    }
    var rd = typeof f.elo_rd === 'number' && isFinite(f.elo_rd) ? f.elo_rd : 0;
    // 350 is the starting deviation, so it is the full-width case.
    var pct = Math.max(4, Math.min(100, Math.round((rd / 350) * 100)));
    var shown = Math.round(f.elo_r);
    var shownRd = Math.round(rd);
    return el('div', { class: 'rating' }, [
      el('div', { class: 'rating-value' }, shown),
      el('div', {
        class: 'rating-bars', role: 'img',
        'aria-label': 'rating ' + shown + ' plus or minus ' + shownRd +
          ', ' + pct + ' percent of the maximum uncertainty'
      }, el('div', { class: 'rating-bar-track' },
        el('div', { class: 'rating-bar-fill', style: 'width:' + pct + '%' }))),
      el('p', { class: 'muted' }, '±' + shownRd +
        ' — a wider bar means less certainty, not a worse rank.')
    ]);
  }

  // impact_ratio is the number the ranked list sorts on, so it is shown with its
  // inputs. A ratio with no visible numerator reads as a magic number.
  function impactEffort(f) {
    var imp = f.impact, eff = f.effort_score;
    if (imp == null || eff == null) {
      return el('p', { class: 'muted' },
        'No impact or effort recorded yet, so this feature is not in the ranked order.');
    }
    var ratio = f.impact_ratio;
    var shown = ratio == null ? (imp / eff).toFixed(2) : Number(ratio).toFixed(2);
    return el('div', { class: 'ie-score' }, [
      el('span', { class: 'ie-value' }, shown),
      el('span', { class: 'muted' }, 'impact ' + imp + ' ÷ effort ' + eff +
        (eff ? ' (' + (f.effort || 'unsized') + ')' : ''))
    ]);
  }

  function complaintRow(c) {
    return el('li', { class: 'linked-row' }, [
      el('span', { class: 'badge badge-slate' }, '#' + c.id),
      el('span', {}, c.title || 'complaint ' + c.id),
      c.severity != null ? el('span', { class: 'badge badge-slate' }, 'severity ' + c.severity) : null,
      el('span', { class: 'badge badge-slate' }, c.status || 'unknown'),
      // The inverse of the attach control. Without it the join could only grow.
      el('button', {
        type: 'button', class: 'btn btn-quiet', 'data-unlink-complaint': c.id,
        'aria-label': 'Detach complaint #' + c.id
      }, 'Detach')
    ]);
  }

  // §6.4's coverage claims, one line each, with the challenge beside them. The claims
  // arrive with the solution (the same endpoint), which is what makes the control possible.
  function claimRows(s) {
    var claims = Array.isArray(s.claims) ? s.claims : [];
    if (!claims.length) return null;
    return el('ul', { class: 'coverage-claims', 'data-coverage-claims': '' }, claims.map(function (c) {
      var reasonId = 'contest-reason-' + s.solution_id + '-' + c.complaint_id;
      return el('li', { class: 'coverage-claim', 'data-claim': c.complaint_id }, [
        el('span', { class: 'coverage-claim-text' }, [
          c.claim === 'leaves-unresolved' ? 'leaves complaint #' : 'resolves complaint #',
          c.complaint_id
        ]),
        c.contested
          ? el('span', { class: 'badge badge-amber', title: c.contest_reason || '' }, 'contested')
          : null,
        el('button', {
          type: 'button', class: 'btn btn-secondary btn-sm', 'data-contest': s.solution_id,
          'data-contest-complaint': c.complaint_id
        }, 'Contest'),
        el('span', { class: 'coverage-contest-form', hidden: true }, [
          el('label', { class: 'field-label', for: reasonId }, 'Why the claim does not stand'),
          el('input', {
            type: 'text', class: 'input', id: reasonId, 'data-contest-reason': '',
            placeholder: 'e.g. it only fixes the first page of results'
          }),
          el('button', {
            type: 'button', class: 'btn', 'data-contest-send': s.solution_id,
            'data-contest-send-complaint': c.complaint_id
          }, 'Record the challenge')
        ]),
        el('span', {
          class: 'coverage-contest-error', role: 'alert', 'data-contest-error': '', hidden: true
        })
      ]);
    }));
  }

  function solutionRow(s) {
    var rd = typeof s.elo_rd === 'number' && isFinite(s.elo_rd) ? s.elo_rd : null;
    return el('li', { class: 'linked-row' }, [
      el('span', { class: 'badge badge-purple' }, 'rating ' + Math.round(s.elo_r || 0) +
        (rd != null ? ' ±' + Math.round(rd) : '')),
      el('span', {}, s.title || 'solution'),
      el('span', { class: 'badge badge-slate' }, s.status || 'proposed'),
      claimRows(s)
    ]);
  }

  function section(title, body) {
    return el('section', { class: 'mb-6' }, [
      el('h2', { class: 'section-title' }, title),
      body
    ]);
  }

  // pageWriter is the page's write helper, shared by the two inline panels (tags, complaint
  // links). It re-reads the WHOLE page on success rather than patching the region it touched.
  function pageWriter(sink) {
    function fail(msg) {
      if (sink) { sink.textContent = msg; sink.hidden = false; }
    }
    function sendAs(method, path, payload, btn) {
      if (btn) btn.disabled = true;
      window.ConcordApi.request(method, path, payload === undefined ? null : payload)
        .then(function () {
          window.location.reload();
        })
        .catch(function (e) {
          if (btn) btn.disabled = false;
          fail(e.message || String(e));
        });
    }
    function send(path, payload, btn) { return sendAs('POST', path, payload, btn); }
    return { fail: fail, send: send, sendAs: sendAs };
  }

  // An empty select is NOT rendered. A control that cannot act reads as broken; the
  // section says why there is nothing to attach instead.
  function attachControl(linkedIds, candidates) {
    var eligible = (candidates || []).filter(function (c) {
      return linkedIds.indexOf(Number(c.id)) === -1;
    });
    if (!eligible.length) {
      return el('p', { class: 'muted' },
        'No other validated complaint in this project is unattached.');
    }
    return el('form', { id: 'attach-complaint-form', class: 'inline-form' }, [
      el('label', { class: 'form-label', for: 'attach-complaint' }, 'Attach a validated complaint'),
      el('select', { class: 'input', id: 'attach-complaint', name: 'complaint_id' },
        eligible.map(function (c) {
          return el('option', { value: c.id }, '#' + c.id + ' ' + (c.title || 'untitled'));
        })),
      el('button', { type: 'submit', class: 'btn', 'data-attach-complaint': '' }, 'Attach')
    ]);
  }

  function wireComplaintLinks(base, featureID) {
    var form = document.getElementById('attach-complaint-form');
    if (!form) return;
    var writer = pageWriter(root.querySelector('[data-complaint-error]'));
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var sel = form.querySelector('#attach-complaint');
      var id = sel ? Number(sel.value) : 0;
      if (!id) { writer.fail('Pick a complaint to attach.'); return; }
      writer.send(base + '/features/' + encodeURIComponent(featureID) + '/complaints',
        { complaint_id: id }, form.querySelector('button[type=submit]'));
    });
  }

  // wireUnlink detaches a complaint. The id is a PATH segment on the server (a DELETE
  // with a body is not portable), so the call is DELETE .../complaints/{id}.
  function wireUnlink(base, featureID) {
    var rows = root.querySelectorAll('[data-unlink-complaint]');
    if (!rows.length) return;
    var writer = pageWriter(root.querySelector('[data-complaint-error]'));
    Array.prototype.forEach.call(rows, function (btn) {
      btn.addEventListener('click', function () {
        var id = Number(btn.getAttribute('data-unlink-complaint'));
        if (!id) return;
        btn.disabled = true;
        writer.sendAs('DELETE', base + '/features/' + encodeURIComponent(featureID) +
          '/complaints/' + encodeURIComponent(id), undefined, btn);
      });
    });
  }

  // depLink names the blocker and its state: "blocked by #12 (ready)" is actionable,
  // "blocked" is not.
  function depLink(slug, b) {
    return el('span', {}, [
      el('a', { href: '/projects/' + slug + '/features/' + b.id }, b.title),
      ' ',
      el('span', { class: 'ms-badge' }, b.status)
    ]);
  }

  function send(method, path, body) {
    return window.ConcordApi.request(method, path, body === undefined ? null : body);
  }

  function status(node, msg, isError) {
    if (!node) return;
    node.hidden = !msg;
    node.textContent = msg || '';
    node.style.color = isError ? '#b91c1c' : '';
  }

  function wireMilestone(base, featureID) {
    var panel = document.querySelector('[data-milestone-select]');
    if (!panel) return;
    var sel = panel.querySelector('[data-ms-select]');
    var msg = panel.querySelector('[data-ms-status]');
    var btn = panel.querySelector('[data-ms-save]');
    btn.addEventListener('click', function () {
      var v = sel.value;
      send('PUT', base + '/features/' + featureID + '/milestone',
        { milestone_id: v ? Number(v) : null }).then(function () {
        status(msg, 'Saved. Reload to see the progress bar move.');
      }).catch(function (e) { status(msg, e.message, true); });
    });
  }

  function wireDeps(base, featureID) {
    var panel = document.querySelector('[data-deps-panel]');
    if (!panel) return;
    var msg = panel.querySelector('[data-dep-status]');
    var input = panel.querySelector('#dep-add');
    panel.querySelector('[data-dep-add]').addEventListener('click', function () {
      var id = Number(input.value);
      if (!id) return;
      send('POST', base + '/features/' + featureID + '/deps', { blocker_id: id })
        .then(function () { window.location.reload(); })
        .catch(function (e) { status(msg, e.message, true); });
    });
    panel.querySelectorAll('[data-dep-remove]').forEach(function (b) {
      b.addEventListener('click', function () {
        send('DELETE', base + '/features/' + featureID + '/deps?blocker_id=' + b.getAttribute('data-dep-remove'))
          .then(function () { window.location.reload(); })
          .catch(function (e) { status(msg, e.message, true); });
      });
    });
  }

  function wireEvidence(base, featureID) {
    var panel = document.querySelector('[data-evidence-panel]');
    if (!panel) return;
    var msg = document.querySelector('[data-evidence-status]');
    var kind = document.getElementById('ev-kind');
    var ref = document.getElementById('ev-ref');
    var add = document.querySelector('[data-evidence-add]');
    if (add) {
      add.addEventListener('click', function () {
        var id = Number(ref.value);
        if (!id) return;
        send('POST', base + '/features/' + featureID + '/evidence',
          { kind: kind.value, ref_id: id })
          .then(function () { window.location.reload(); })
          .catch(function (e) { status(msg, e.message, true); });
      });
    }
    document.querySelectorAll('[data-evidence-remove]').forEach(function (b) {
      b.addEventListener('click', function () {
        var parts = b.getAttribute('data-evidence-remove').split(':');
        send('DELETE', base + '/features/' + featureID + '/evidence?kind=' +
          encodeURIComponent(parts[0]) + '&ref_id=' + encodeURIComponent(parts[1]))
          .then(function () { window.location.reload(); })
          .catch(function (e) { status(msg, e.message, true); });
      });
    });
  }

  function notFound(slug, id) {
    return el('div', { class: 'empty-state' }, [
      el('p', { class: 'empty-state-title' }, 'No such feature'),
      el('p', { class: 'muted' }, 'Feature ' + id + ' does not exist in ' + slug +
        ', or it belongs to a project you cannot see.'),
      el('p', {}, el('a', { class: 'btn', href: '/projects/' + slug }, 'Back to the project'))
    ]);
  }

  function errorBox(msg) {
    return el('div', { class: 'empty-state' }, [
      el('p', { class: 'empty-state-title' }, 'Could not load this feature'),
      el('p', { class: 'muted' }, msg)
    ]);
  }

  var at = where();
  if (!at.slug || !at.id) {
    root.replaceChildren(notFound(at.slug, at.id));
    done();
    return;
  }

  var base = '/api/v1/projects/' + encodeURIComponent(at.slug);

  Promise.all([
    json(base + '/features/' + encodeURIComponent(at.id)),
    json(base + '/features/' + encodeURIComponent(at.id) + '/solutions'),
    json(base + '/features/' + encodeURIComponent(at.id) + '/consensus'),
    // Complaints come from their OWN endpoint. linked_complaints is write-only.
    json(base + '/features/' + encodeURIComponent(at.id) + '/complaints'),
    // §6.2's steering input, read on the page it steers.
    json(base + '/features/' + encodeURIComponent(at.id) + '/tags'),
    // §354's extra link, read TOLERANTLY: a page that cannot offer the control must
    // still render the feature.
    json(base + '/complaints?status=validated').then(window.ConcordApi.list).catch(function () { return []; }),
    // R17/R18's three reads, each TOLERANT for the same reason.
    json(base + '/milestones').then(window.ConcordApi.list).catch(function () { return []; }),
    json(base + '/features/' + encodeURIComponent(at.id) + '/deps').catch(function () { return {}; }),
    json(base + '/features/' + encodeURIComponent(at.id) + '/evidence').catch(function () { return []; })
  ]).then(function (res) {
    var f = res[0];
    if (!f) {
      root.replaceChildren(notFound(at.slug, at.id));
      done();
      return;
    }
    // BOTH sibling endpoints wrap their payload; getting this wrong is silent.
    var solutions = (res[1] && res[1].solutions) || [];
    if (!Array.isArray(solutions)) solutions = [];
    var call = res[2] || {};
    var complaints = Array.isArray(res[3]) ? res[3] : [];

    var parts = [
      el('div', { class: 'page-head' }, el('div', {}, [
        el('p', { class: 'breadcrumb' }, [
          el('a', { href: '/projects' }, 'Projects'), ' / ',
          el('a', { href: '/projects/' + at.slug }, at.slug), ' / ',
          el('span', {}, 'Feature')
        ]),
        el('h1', {}, [
          f.title || 'Untitled feature', ' ',
          el('span', { class: 'badge badge-slate' }, f.status || 'proposed'), ' ',
          // The evidence badge (spec §3.3), derived from what the page actually loaded.
          el('span', {
            class: 'ms-badge', 'data-evidence-badge': complaints.length ? 'complaint' : 'idea'
          }, complaints.length ? 'complaint-backed' : 'idea-first')
        ]),
        f.body ? el('p', { class: 'lede' }, f.body) : null
      ])),
      el('div', { class: 'card mb-6' }, [
        rating(f), impactEffort(f),
        el('p', { class: 'muted' }, 'Strategic weight ' +
          (f.strategic_weight == null ? 1 : f.strategic_weight) +
          ' — a multiplier on priority, proposed and ratified rather than set by hand.')
      ]),
      section('Solutions (' + solutions.length + ')',
        solutions.length
          ? el('ul', { class: 'linked-list' }, solutions.map(solutionRow))
          : el('p', { class: 'muted' }, 'No solutions proposed yet.')),
    ];

    // Consensus here is the SOLUTION ARENA's readiness for this feature, not a call
    // with an open/closed status: the endpoint returns leader_id, runner_up_id,
    // baseline_id and a `ready` flag.
    if (Object.keys(call).length) {
      parts.push(section('Consensus', [
        el('p', { class: 'muted' }, call.ready
          ? 'The solution arena for this feature has a baseline and enough comparisons to call a result.'
          : 'The solution arena for this feature has not called a result yet. A leader and a runner-up mean one comparison is not a verdict.'),
        el('p', { class: 'muted' }, [
          call.baseline_id ? 'baseline solution ' + call.baseline_id : 'no baseline yet',
          call.leader_id ? ' · leader ' + call.leader_id : '',
          call.runner_up_id ? ' · runner-up ' + call.runner_up_id : ''
        ])
      ]));
    }

    // The complaints section, always rendered: an empty list must SAY it has none.
    var candidates = Array.isArray(res[5]) ? res[5] : [];
    var linkedIds = complaints.map(function (c) { return Number(c.id); });
    parts.push(section('Complaints (' + complaints.length + ')', [
      complaints.length
        ? el('ul', { class: 'linked-list' }, complaints.map(complaintRow))
        : el('p', { class: 'muted' }, 'No validated complaint is linked to this feature, so ' +
          'its rank rests on comparison alone rather than on reported pain.'),
      attachControl(linkedIds, candidates),
      el('p', { class: 'form-status', 'data-complaint-error': '', hidden: true })
    ]));

    // The tags section, always rendered: a feature with no tags scores with no theme at all.
    var tagList = (res[4] && res[4].tags) || [];
    parts.push(section('Tags (' + tagList.length + ')', [
      tagList.length
        ? el('ul', { class: 'tag-list', 'data-feature-tags': '' }, tagList.map(function (tg) {
            return el('li', { class: 'tag-pill' }, [
              tg + ' ',
              el('button', { type: 'button', class: 'btn btn-quiet', 'data-tag-remove': tg }, 'Remove')
            ]);
          }))
        : el('p', { class: 'muted' }, 'No tags. A tag is what connects this feature to a ratified ' +
          'theme, so a theme only steers a feature that carries its tag.'),
      el('form', { id: 'add-tag-form', class: 'inline-form' }, [
        el('label', { class: 'form-label', for: 'add-tag' }, 'Add a tag'),
        el('input', { id: 'add-tag', name: 'tag', maxlength: '60', placeholder: 'an existing tag' }),
        el('button', { type: 'submit', class: 'btn' }, 'Add')
      ]),
      el('p', { class: 'form-status', 'data-tag-error': '', hidden: true })
    ]));

    // Planning (R17/R18): "no milestone" is an answer — those features ARE the backlog.
    var milestones = Array.isArray(res[6]) ? res[6] : [];
    var msOpts = milestones.map(function (m) {
      var chosen = f.milestone_id && Number(f.milestone_id) === Number(m.id);
      return el('option', { value: m.id, selected: chosen }, m.title);
    });
    var deps = res[7] || {};
    var blockedBy = Array.isArray(deps.blocked_by) ? deps.blocked_by : [];
    var blocks = Array.isArray(deps.blocks) ? deps.blocks : [];
    parts.push(section('Planning', [
      el('div', { class: 'ms-panel', 'data-milestone-select': at.id }, [
        el('h2', {}, 'Milestone'),
        el('div', { class: 'ms-row' }, [
          el('select', { 'data-ms-select': '', 'aria-label': 'Milestone' },
            [el('option', { value: '' }, '— none (backlog) —')].concat(msOpts)),
          el('button', { type: 'button', class: 'btn', 'data-ms-save': '' }, 'Save'),
          milestones.length ? null : el('a', { class: 'muted', href: '/projects/' + at.slug + '/roadmap' },
            'Create a milestone on the roadmap')
        ]),
        el('p', { class: 'form-status', 'data-ms-status': '', hidden: true })
      ]),
      el('div', { class: 'ms-panel', 'data-deps-panel': at.id }, [
        el('h2', {}, 'Dependencies'),
        el('p', { class: 'muted' }, 'Blocked by ' + (blockedBy.length ? '' : 'nothing')),
        blockedBy.length
          ? el('ul', { class: 'linked-list' }, blockedBy.map(function (b) {
              return el('li', {}, [
                depLink(at.slug, b), ' ',
                el('button', { type: 'button', class: 'btn btn-quiet', 'data-dep-remove': b.id }, 'Remove')
              ]);
            }))
          : null,
        el('p', { class: 'muted' }, 'Blocks ' + (blocks.length ? '' : 'nothing')),
        blocks.length
          ? el('ul', { class: 'linked-list' }, blocks.map(function (b) {
              return el('li', { 'data-blocked-by': b.id }, [
                el('a', { href: '/projects/' + at.slug + '/features/' + b.id }, b.title), ' ',
                el('span', { class: 'ms-badge' }, b.status)
              ]);
            }))
          : null,
        el('div', { class: 'ms-row' }, [
          el('label', { class: 'form-label', for: 'dep-add' }, 'Blocked by feature #'),
          el('input', { id: 'dep-add', type: 'number', min: '1', placeholder: 'feature id' }),
          el('button', { type: 'button', class: 'btn', 'data-dep-add': '' }, 'Add')
        ]),
        el('p', { class: 'form-status', 'data-dep-status': '', hidden: true })
      ])
    ]));

    // Evidence that is not a complaint (§3.4).
    var evidence = Array.isArray(res[8]) ? res[8] : [];
    parts.push(section('Other evidence (' + evidence.length + ')', [
      evidence.length
        ? el('ul', { class: 'linked-list', 'data-evidence-panel': at.id }, evidence.map(function (ev) {
            return el('li', {}, [
              ev.kind === 'document' ? 'Document' : 'Field report',
              ' #' + ev.ref_id + (ev.title ? ' · ' + ev.title : '') + ' ',
              el('button', {
                type: 'button', class: 'btn btn-quiet',
                'data-evidence-remove': ev.kind + ':' + ev.ref_id
              }, 'Remove')
            ]);
          }))
        : el('p', { class: 'muted', 'data-evidence-panel': at.id },
          'No document or field report is cited as evidence.'),
      el('div', { class: 'ms-row' }, [
        el('label', { class: 'form-label', for: 'ev-kind' }, 'Cite'),
        el('select', { id: 'ev-kind', 'aria-label': 'Evidence kind' }, [
          el('option', { value: 'document' }, 'Document'),
          el('option', { value: 'field_report' }, 'Field report')
        ]),
        el('input', { id: 'ev-ref', type: 'number', min: '1', 'aria-label': 'Reference id', placeholder: 'id' }),
        el('button', { type: 'button', class: 'btn', 'data-evidence-add': '' }, 'Add')
      ]),
      el('p', { class: 'form-status', 'data-evidence-status': '', hidden: true })
    ]));

    root.replaceChildren.apply(root, flatten(parts));
    done();
    wireMilestone(base, at.id);
    wireDeps(base, at.id);
    wireEvidence(base, at.id);
    wireContests(base, at.id);
    wireTags(base, at.id);
    wireComplaintLinks(base, at.id);
    wireUnlink(base, at.id);
  }).catch(function (err) {
    // aria-busy is cleared on the error path too: a skeleton that spins forever
    // over an error message reads as "still loading".
    root.replaceChildren(errorBox(err && err.message ? err.message : String(err)));
    done();
  });

  // A failed mount used to leave "Loading the feature..." as the page's final
  // sentence. Never let the placeholder outlive the request (D-210).
  setTimeout(function () {
    if (root.innerText.indexOf("Loading the feature") !== -1) {
      root.replaceChildren(errorBox("This feature could not be loaded. Try reloading the page."));
      done();
    }
  }, 8000);
})();
