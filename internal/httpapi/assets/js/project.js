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
          'here to weigh against the project\'s claims.') + '</div>';
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
        '<div class="grid-responsive-2" style="margin-top:1rem;">' + cards + '</div></div>';
    }

    function panelEmpty(title, description) {
      return '<div class="empty-state"><p class="empty-state-title">' + esc(title) + '</p>' +
        '<p class="empty-state-description">' + esc(description) + '</p></div>';
    }

    // A failed fetch renders differently from an empty one, on purpose. Both being
    // "nothing here" is how a broken endpoint passes for a project with no data.
    function panelError(what, message) {
      return '<div class="empty-state panel-error ' +
        (what === 'Capabilities' ? 'panel-capabilities' : 'panel-field-reports') +
        '"><p class="empty-state-title">' +
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

  function complaintCards(list) {
    return (list || []).map(function (c) {
      return '<div class="card">' +
        '<div class="card-title">' + esc(c.title) + '</div>' +
        (c.body ? '<p class="card-text">' + esc(c.body) + '</p>' : '') +
        '<div class="pill-row">' +
        '<span class="badge ' + (c.status === 'validated' ? 'badge-green' : 'badge-slate') + '">' +
          esc(c.status || 'open') + '</span>' +
        '<span class="badge badge-slate">severity ' + esc(c.severity) + '</span>' +
        (c.status === 'open'
          ? '<button class="btn btn-quiet" data-validate="' + esc(c.id) + '">Validate</button>'
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

    var complaintForm = document.getElementById('complaint-form');
    if (complaintForm) {
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

    Array.prototype.forEach.call(box.querySelectorAll('[data-validate]'), function (btn) {
      btn.addEventListener('click', function () {
        btn.disabled = true;
        postJSON(base + '/complaints/' + encodeURIComponent(btn.getAttribute('data-validate')) + '/validate', {})
          .then(function () { window.location.reload(); })
          .catch(function (err) {
            btn.disabled = false;
            setStatus(complaintForm || featureForm, err.message || String(err), true);
          });
      });
    });
  }

  function setStatus(form, message, isError) {
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

  function render(p, features, complaints, documents, panels) {
    panels = panels || {};
    var badges = '<div class="pill-row" style="margin-top:0.75rem;">' +
      '<span class="badge badge-indigo">' + esc(p.governance_model || 'governed') + '</span>' +
      (p.license ? '<span class="badge badge-slate">' + esc(p.license) + '</span>' : '') +
      '</div>';

    var meta = '<div class="meta-row">' +
      (p.created_at ? '<span class="meta-item">created ' + esc(fmtDate(p.created_at)) + '</span>' : '') +
      (p.updated_at ? '<span class="meta-item">updated ' + esc(fmtDate(p.updated_at)) + '</span>' : '') +
      '</div>';

    var featureCards = (features || []).map(function (f) {
      return '<div class="card">' +
        '<div class="card-title">' + esc(f.title) + '</div>' +
        (f.body ? '<p class="card-text">' + esc(f.body) + '</p>' : '') +
        '<div class="pill-row"><span class="badge badge-purple">rating ' + Math.round(f.elo_r || 0) +
          (f.elo_rd != null ? ' \u00b1' + Math.round(f.elo_rd) : '') + '</span>' +
        '<span class="badge badge-slate">' + esc(f.status || 'proposed') + '</span></div>' +
        '</div>';
    }).join('');

    return '<div class="detail-header">' +
      '<div><h1 class="page-title">' + esc(p.name) + ' <span class="badge badge-slate" style="vertical-align:middle;">' + esc(p.slug) + '</span></h1>' +
      '<p class="page-subtitle">' + esc(p.description || 'No description yet.') + '</p></div>' +
      '<div class="detail-actions">' +
      '<a class="btn btn-primary" href="/projects/' + esc(p.slug) + '/board">Open board</a>' +
      '<a class="btn" href="/projects/' + esc(p.slug) + '/documents">Documents' +
      (documents ? ' <span class="badge badge-slate">' + documents.length + '</span>' : '') +
      '</a>' +
      '<a class="btn btn-secondary" href="/projects">Back</a>' +
      '</div></div>' +
      '<div class="card mb-6">' + badges + healthBar(p.health_score) + meta + '</div>' +
      '<div class="section-head"><h2 class="section-title" style="font-size:1.25rem;">Complaints</h2>' +
      '<p class="section-sub">The problems being solved. A feature needs at least one ' +
      'validated complaint behind it before it is a candidate for anything.</p></div>' +
      (complaints && complaints.length
        ? '<div class="grid-responsive-2">' + complaintCards(complaints) + '</div>'
        : '<div class="empty-state"><div class="empty-state-icon">\u{1F4CC}</div>' +
          '<p class="empty-state-title">No complaints filed</p>' +
          '<p class="empty-state-description">Nothing has been reported yet. ' +
          'A feature cannot be ranked until there is a problem for it to solve.</p></div>') +
      '<div class="section-head" style="margin-top:2rem;"><h2 class="section-title" style="font-size:1.25rem;">Features</h2>' +
      '<p class="section-sub">Proposed solutions ranked by pairwise comparison.</p>' +
      '<div class="pill-row">' +
        '<a class="btn btn-primary" href="/projects/' + esc(p.slug) + '/rank">Rank features</a>' +
        '<a class="btn" href="/projects/' + esc(p.slug) + '/ranking">See the ranking</a>' +
      '</div></div>' +
      (features && features.length
        ? '<div class="grid-responsive-2">' + featureCards + '</div>'
        : '<div class="empty-state"><div class="empty-state-icon">\u2728</div>' +
          '<p class="empty-state-title">No features yet</p>' +
          '<p class="empty-state-description">Features are proposed from validated complaints.</p></div>') +
      // §4.10's order: capabilities, then field reports, then the standing.
      // Between the header and Complaints, because the capability matrix is what
      // says what the project IS and complaints are what it is trying to fix.
      capabilitiesPanel(panels.capabilities) +
      fieldReportsPanel(panels.fieldReports) +
      forms(p);
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-detail');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.innerHTML = notFound(''); return; }

    fetch('/api/v1/projects/' + encodeURIComponent(slug))
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
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/features')
            .then(function (r) {
              if (r.status === 401) throw { auth: true };
              if (!r.ok) throw { status: r.status };
              return r.json();
            }),
          // Complaints are read separately and a failure here is not fatal: the
          // feature list is the more important of the two, and losing it
          // because a second request failed would be a worse outcome than
          // showing the complaints as unavailable.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/complaints')
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; }),
          // Documents are a count badge and a link, not content on this page,
          // so a failure here degrades to no badge rather than to an error. The
          // same reasoning as complaints: a third failing request must not cost
          // us the two that matter.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/documents')
            .then(function (r) { return r.ok ? r.json() : []; })
            .catch(function () { return []; }),
          // The two §4.10 panels. Both swallow their own failures and resolve to
          // null, which panelError() renders differently from an empty result --
          // so an endpoint that 500s says "unavailable" instead of quietly
          // claiming the project has no capabilities and no field reports.
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/capabilities')
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; }),
          fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/field-reports')
            .then(function (r) { return r.ok ? r.json() : null; })
            .catch(function () { return null; })
        ])
          .then(function (res) {
            var features = res[0], complaints = res[1], documents = res[2];
            // The feature form's select needs the complaint list at render
            // time, so it is stashed rather than threaded through the markup.
            window.__complaints = complaints || [];
            box.innerHTML = render(p, features, complaints, documents, {
              capabilities: res[3],
              fieldReports: res[4]
            });
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
