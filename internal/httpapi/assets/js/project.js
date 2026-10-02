// Concord project detail — resolves the slug, then hydrates from the API.
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
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

  function render(p, features, complaints, documents) {
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
            .catch(function () { return []; })
        ])
          .then(function (res) {
            var features = res[0], complaints = res[1], documents = res[2];
            // The feature form's select needs the complaint list at render
            // time, so it is stashed rather than threaded through the markup.
            window.__complaints = complaints || [];
            box.innerHTML = render(p, features, complaints, documents);
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
