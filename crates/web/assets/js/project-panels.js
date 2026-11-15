// The panels the project overview draws from data it has already fetched: complaint cards, the request board, §16's lists,
// the work items a reader can claim, and the two sentences that describe a project's intake mode.
//
// Split out of project.js (review item 9's "split by capability"). Nothing here fetches on its own except the claim and
// sizing writes, which go through `api.js` like everything else, and the only thing it takes from the page is `esc` — which
// is itself a shared asset. Registers itself on window; loaded before project.js.
(function () {
  var esc = window.ConcordEscape.esc;

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

// §4.5.7's work items: the fifteen-minute complaint queue and the ready features nobody has
// claimed.
//
// This panel is the difference between "contribute" as a sentence and as an action: before it,
// a reader could see a project's features and had no way to say "I will take that one", and the
// API could hold a claim no page offered.
function workItemsSection(items, p) {
  var list = items || [];
  // The label form is offered only to a signed-in reader. It is still refused by the
  // SERVER for anyone who is not a maintainer (a client-side role check is a claim the
  // client cannot verify), but a signed-OUT visitor would get a control that can only
  // ever answer 401 -- and a control that can only fail is one the page should not draw.
  var session = window.ConcordSession && window.ConcordSession.instance();
  var signedIn = !!(session && session.signedIn());
  var rows = list.map(function (w) {
    var badges = '';
    if (w.kind === 'complaint') {
      badges += '<span class="badge badge-amber">needs validating</span>';
    } else {
      badges += '<span class="badge badge-slate">' + esc(w.status) + '</span>';
      if (w.time_budget_minutes) {
        badges += '<span class="badge badge-indigo">' + esc(String(w.time_budget_minutes)) + ' min</span>';
      }
      if (w.difficulty) {
        badges += '<span class="badge badge-purple">difficulty ' + esc(w.difficulty) + '</span>';
      }
      if (w.needs_mentor) {
        badges += '<span class="badge badge-emerald">mentor offered</span>';
      }
    }
    var action = '';
    if (w.kind === 'feature') {
      if (w.claimed_by) {
        action = '<span class="meta-item" data-claimed="' + esc(w.id) + '">claimed by another contributor</span>';
      } else if (!signedIn) {
        // The claim needs an account and a membership. Offering the button to a signed-out
        // visitor is offering a control whose only possible answer is 401.
        action = '<a class="btn" href="/login">Sign in to claim</a>';
      } else {
        action = '<button type="button" class="btn" data-claim="' + esc(w.id) + '">Claim this</button>';
      }
    } else {
      action = '<a class="btn" href="/projects/' + esc(p.slug) + '/complaints">Read it</a>';
    }
    // Only a maintainer may size a task: the form is rendered for every signed-in reader
    // and REFUSED by the server, which is the house rule -- a client-side role check is a
    // claim it cannot verify.
    var label = (w.kind === 'feature' && signedIn)
      ? '<form class="inline-form" data-work-item-form="' + esc(w.id) + '">' +
          '<select name="difficulty" aria-label="Difficulty">' +
            '<option value="">difficulty?</option>' +
            '<option value="S"' + (w.difficulty === 'S' ? ' selected' : '') + '>S</option>' +
            '<option value="M"' + (w.difficulty === 'M' ? ' selected' : '') + '>M</option>' +
            '<option value="L"' + (w.difficulty === 'L' ? ' selected' : '') + '>L</option>' +
          '</select>' +
          // step="1", not the 15-minute granularity this first shipped with: `min="1"`
          // with `step="15"` makes valid values 1, 16, 31, … so a typed 90 (and the
          // fixture's own 60) is a STEP MISMATCH, and the browser refuses to submit the
          // form at all -- no request, no error, no navigation, and a Save button that
          // does nothing. Found by the browser test, invisible to every Go test.
          '<input name="minutes" type="number" min="1" step="1" aria-label="Time budget in minutes" placeholder="minutes" value="' +
            (w.time_budget_minutes ? esc(String(w.time_budget_minutes)) : '') + '">' +
          '<label class="meta-item"><input type="checkbox" name="mentor"' +
            (w.needs_mentor ? ' checked' : '') + '> mentor</label>' +
          '<button type="submit" class="btn">Save</button>' +
          '<span class="meta-item" data-work-item-status="' + esc(w.id) + '"></span>' +
        '</form>'
      : '';
    return '<div class="card" data-work-item="' + esc(w.kind) + '-' + esc(w.id) + '">' +
      '<div class="card-title">' + esc(w.title) + '</div>' +
      '<div class="pill-row">' + badges + '</div>' + action + label + '</div>';
  }).join('');

  return '<div class="section-head" style="margin-top:2rem;">' +
    '<h2 class="section-title" style="font-size:1.25rem;">Work items</h2>' +
    '<p class="section-sub">What you can pick up, cheapest first. One live claim per item, and a ' +
    'claim does not move the feature \u2014 the board does that.</p></div>' +
    (list.length
      ? '<div class="grid-responsive-2">' + rows + '</div>'
      : '<div class="empty-state"><div class="empty-state-icon">\u{1F9F0}</div>' +
        '<p class="empty-state-title">Nothing to pick up yet</p>' +
        '<p class="empty-state-description">Work items appear when a feature is sized and moved to ' +
        'ready, or when a complaint is filed that still needs validating.</p></div>');
}

// wireWorkItems gives the panel its two actions. Both report their own refusal rather than
// swallowing it: a 409 is "somebody got there first", which is information the reader needs.
//
// sendJSON is local rather than postJSON because postJSON THROWS on any non-2xx (it is written for
// the forms that reload on success), and these two controls exist to READ the status: 409 and 403
// are the two outcomes a claim actually produces, and a helper that turns them into a thrown
// string loses the number the panel has to branch on.
function wireWorkItems(box, slug) {
  function sendJSON(method, path, body) {
    var session = window.ConcordSession && window.ConcordSession.instance();
    var headers = { 'Content-Type': 'application/json' };
    if (session) {
      var h = session.authHeaders();
      for (var k in h) { if (Object.prototype.hasOwnProperty.call(h, k)) headers[k] = h[k]; }
    }
    // `request(method, …)` — NOT `get(path, { method: … })`. `api.js`'s `get` hardcodes GET and drops the body, so a
    // `get` here sent a GET for a POST and a GET for a PUT: claiming work and saving a work item both went out as reads.
    // This shape is kept because the callers below read `{ok, status}`, and a refusal is `ApiError` now.
    return window.ConcordApi.request(method, path, body || {})
      .then(function (payload) { return { ok: true, status: 200, payload: payload || {} }; })
      .catch(function (error) {
        return { ok: false, status: error && error.status ? error.status : 0, payload: {} };
      });
  }

  var claims = box.querySelectorAll('[data-claim]');
  Array.prototype.forEach.call(claims, function (btn) {
    btn.addEventListener('click', function () {
      var id = btn.getAttribute('data-claim');
      btn.disabled = true;
      btn.textContent = 'Claiming\u2026';
      sendJSON('POST', '/api/v1/projects/' + encodeURIComponent(slug) + '/features/' + id + '/claim', {})
        .then(function (res) {
          if (res.ok) { btn.textContent = 'Claimed'; return; }
          if (res.status === 409) { btn.textContent = 'Somebody got there first'; return; }
          if (res.status === 401 || res.status === 403) {
            btn.textContent = 'Join the project to claim work';
            return;
          }
          btn.disabled = false;
          btn.textContent = 'Claim this';
        })
        .catch(function () {
          btn.disabled = false;
          btn.textContent = 'Claim this';
        });
    });
  });

  var forms = box.querySelectorAll('[data-work-item-form]');
  Array.prototype.forEach.call(forms, function (form) {
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var id = form.getAttribute('data-work-item-form');
      var note = form.querySelector('[data-work-item-status]');
      var minutes = form.querySelector('[name="minutes"]').value;
      var body = {
        difficulty: form.querySelector('[name="difficulty"]').value || null,
        needs_mentor: form.querySelector('[name="mentor"]').checked
      };
      // An empty box means "nobody has sized this", which is different from zero minutes: sending
      // 0 would be refused by the schema, and sending nothing keeps the honest null.
      if (minutes !== '') body.time_budget_minutes = parseInt(minutes, 10);
      if (note) note.textContent = 'Saving\u2026';
      sendJSON('PUT', '/api/v1/projects/' + encodeURIComponent(slug) + '/features/' + id + '/work-item', body)
        .then(function (res) {
          if (!note) return;
          note.textContent = res.ok ? 'Saved'
            : (res.status === 403 ? 'Refused: maintainers only' : 'Refused: HTTP ' + res.status);
        })
        .catch(function () { if (note) note.textContent = 'Refused: network error'; });
    });
  });
}
// intakeMode and intakeNote read the project's mode once, in the two places the
// form needs it: what it will accept, and what it asks for.
//
// The default is complaint_first -- the same default the migration carries -- so a
// project whose payload predates the column (or a cached page) still behaves the
// way it always did rather than silently accepting unbacked ideas.
function intakeMode(p) {
  return (p && p.intake_mode) || 'complaint_first';
}

function intakeNote(p) {
  var mode = intakeMode(p);
  if (mode === 'idea_first') {
    return 'Idea-first: file an idea before anybody is hurt. It may be filed without a ' +
      'complaint, but it still needs evidence (a complaint, a document or a field report) ' +
      'before it can reach ready.';
  }
  if (mode === 'both') {
    return 'Both are accepted here: answer one of the complaints above, or file an idea if ' +
      'nothing is broken yet. Evidence is still required before ready.';
  }
  return 'A solution to one of the complaints above. Pick which it answers — this project ' +
    'is complaint-first, so a feature traces to reported pain.';
}
  window.ConcordPanels = {
    listSection: listSection,
    requestSection: requestSection,
    complaintCards: complaintCards,
    workItemsSection: workItemsSection,
    wireWorkItems: wireWorkItems,
    intakeMode: intakeMode,
    intakeNote: intakeNote
  };
})();
