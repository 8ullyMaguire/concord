// Concord project detail — resolves the slug, then hydrates from the API.
(function () {
  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it. This file's copy replaced the same
  // five characters in its own way; a single pass over a character class cannot disagree with another page about which are dangerous.
  var esc = window.ConcordEscape.esc;
  var el = window.ConcordDom.el;
  // The panels live in their own file; one binding here keeps the composition below unchanged.
  var concordPanels = window.ConcordPanels;
  // The write helpers live in project-writes.js; binding them here keeps every call site below unchanged.
  var setStatus = window.ConcordWrites.setStatus;
  var postJSON = window.ConcordWrites.postJSON;
  var showCardRefusal = window.ConcordWrites.showCardRefusal;

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
// The node-only renderer lives in project-render.js (D-16); this file keeps loading, wire and failure states.

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function notFound(slug) {
    return el('div', { class: 'empty-state' }, [
      el('div', { class: 'empty-state-icon' }, '\u{1F50E}'),
      el('p', { class: 'empty-state-title' }, 'Project not found'),
      el('p', { class: 'empty-state-description' },
        'No project with slug \u201c' + slug + '\u201d exists on this forge.'),
      el('a', { class: 'btn btn-primary', href: '/projects' }, 'Browse projects')
    ]);
  }

  // The outer states are nodes too (D-16): a failure page is not allowed to
  // parse HTML just because the main render() still returns a string.
  function loading() {
    if (window.ConcordSkeleton && window.ConcordSkeleton.titleNode) {
      return [window.ConcordSkeleton.titleNode(), window.ConcordSkeleton.textNode(3)];
    }
    return el('div', { class: 'loading-spinner', role: 'status', 'aria-label': 'Loading project' });
  }

  function authFailure() {
    return el('div', { class: 'empty-state' }, [
      el('div', { class: 'empty-state-icon' }, '\u26A0\uFE0F'),
      el('p', { class: 'empty-state-title' }, 'Sign in to see features'),
      el('p', { class: 'empty-state-description' }, [
        'Reading features requires an account. ',
        el('a', { class: 'btn btn-primary', href: '/login' }, 'Sign in')
      ])
    ]);
  }

  function loadFailure(code) {
    return el('div', { class: 'empty-state' }, [
      el('div', { class: 'empty-state-icon' }, '\u26A0\uFE0F'),
      el('p', { class: 'empty-state-title' }, 'Could not load project' + code),
      el('p', { class: 'empty-state-description' },
        'The API did not respond as expected. Try again.')
    ]);
  }

  // forms renders the two ways to add to the project.
  //
  // Both are real <form> elements with ids, and both reload the page on
  // success rather than splicing a row into the DOM. The page is
  // server-rendered state: a complaint that appeared without a validation
  // round-trip would be shown as filed even when the server rejected it, and
  // the author would believe they had reported something.
// The node-only renderer lives in project-render.js (D-16); this file keeps loading, wire and failure states.

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
          // The refusal ("already a member", "a guest cannot join") arrives as the one error model's message.
          window.ConcordApi.post(base + '/join', {})
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
      window.ConcordWrites.wire(complaintForm, 'complaint', projectID);
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
      window.ConcordWrites.wire(featureForm, 'feature', projectID);
      featureForm.addEventListener('submit', function (ev) {
        ev.preventDefault();
        var fd = new FormData(featureForm);
        var complaintId = Number(fd.get('complaint_id') || 0);
        // Only complaint_first REQUIRES the link. In idea_first/both the server accepts
        // an unbacked idea, and a form that still demanded a complaint would be the
        // client refusing what the project just said was legitimate.
        // The mode is read off the FORM, not from a variable: wire() receives the
        // project's id, not the project, and an extra read would be a second source
        // for a value the render already had.
        if (!complaintId && featureForm.getAttribute('data-intake-mode') === 'complaint_first') {
          setStatus(featureForm, 'This project is complaint-first: pick the complaint this feature answers, or a maintainer can change the intake mode in Settings.', true);
          return;
        }
        setStatus(featureForm, 'Proposing…', false);
        postJSON(base + '/features', {
          project_id: projectID,
          title: String(fd.get('title') || '').trim(),
          body: String(fd.get('body') || '').trim(),
          linked_complaints: complaintId ? [complaintId] : []
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
    window.ConcordWrites.wireFieldReports(slug);
    // §5's arena loop, wired after every render like the rest.
    window.ConcordArena.wire(box, slug, { setStatus: setStatus, postJSON: postJSON });
    // §4.5.7's work-item panel: claim and size, both of which report their own refusal.
    concordPanels.wireWorkItems(box, slug);
  }

  // nextMilestone picks the nearest OPEN milestone: the dated ones first, in date
  // order, then the undated ones. It returns null rather than an empty object so the
  // caller can decide not to render a card at all -- a card saying "no milestone" on
  // every project without one is noise, not information.
// The node-only renderer lives in project-render.js (D-16); this file keeps loading, wire and failure states.

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-detail');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.replaceChildren(notFound('')); return; }

    // One client for the transport (review item 9b). The 404 is still told apart from the rest, because it is the one
    // refusal the page answers differently (it renders "no such project"); everything else is a status the caller
    // reports.
    window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug))
      .catch(function (error) {
        if (error && error.status === 404) throw { notFound: true };
        throw { status: error && error.status };
      })
      .then(function (p) {
        box.replaceChildren.apply(box, loading());
        // By slug, not by p.id. The project-scoped routes take a slug: the
        // sibling route is /api/v1/projects/{slug} and the server resolves it
        // with GetProject, which matches on p.slug. Passing p.id here made every
        // request 404, and because the handler below maps a non-OK response to
        // an empty array, the page rendered a confident "No features yet" for a
        // project that had three. A failure that looks like a valid empty
        // result is worse than an error, because nothing prompts anyone to look.
        return Promise.all([
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/features')
            .catch(function (error) {
              if (error && error.status === 401) throw { auth: true };
              throw { status: error && error.status };
            }),
          // Complaints are read separately and a failure here is not fatal: the
          // feature list is the more important of the two, and losing it
          // because a second request failed would be a worse outcome than
          // showing the complaints as unavailable.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/complaints')
            .catch(function () { return []; }),
          // Documents are a count badge and a link, not content on this page,
          // so a failure here degrades to no badge rather than to an error. The
          // same reasoning as complaints: a third failing request must not cost
          // us the two that matter.
          // `?fields=meta`: this panel shows a COUNT and a link, and the full list carries every document body — 11.7 MB for one
          // project on the live instance, measured, which held this page's whole `Promise.all` open past thirty seconds and left the
          // overview parked on its skeleton. The badge needs the length of an array, not the prose inside it.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/documents?fields=meta')
            .catch(function () { return []; }),
          // The two §4.10 panels. Both swallow their own failures and resolve to
          // null, which panelError() renders differently from an empty result --
          // so an endpoint that 500s says "unavailable" instead of quietly
          // claiming the project has no capabilities and no field reports.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/capabilities').catch(function () { return null; }),
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/field-reports').catch(function () { return null; }),
          // The third §4.10 panel. Same rule as the other two: swallow its own
          // failure and resolve to null, which panelError renders differently
          // from an empty result.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/solutions').catch(function () { return null; }),
          // The fourth §4.10 panel, and the first panel whose data is grouped by
          // something the project NAMED rather than by a fixed dimension. Same
          // failure rule: swallow its own failure, resolve to null.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/alternatives').catch(function () { return null; }),
          // Requests (17), the same non-fatal shape as complaints: the board is one
          // of the sections, and a failure here must not cost the features.
          //
          // APPENDED, not inserted: this array is read positionally below, so a
          // fetch added in the middle silently renumbers every later index. The
          // first version went in after complaints and fed documents to
          // capabilities, which renders as a panel quietly missing rather than an
          // error. Anything added here goes LAST, and the mapping below is by index.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/requests').catch(function () { return []; }),
          // Lists (16), appended last so no existing index shifts. Same non-fatal
          // shape as complaints and requests.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/lists').catch(function () { return []; }),
          // Work items (§4.5.7), appended last for the same reason as the two above:
          // this array is positional, so an insertion renumbers everything after it.
          // Non-fatal as well -- a failed work-item read costs the panel, not the page.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/work-items').catch(function () { return []; }),
          // The next milestone on the overview (R17: "the next milestone on the
          // project overview"). Appended LAST for the positional reason above, and
          // non-fatal: a project with no milestones is the ordinary case.
          window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/roadmap').catch(function () { return null; })
        ])
          .then(function (res) {
            // Positional, in the order of the Promise.all above. Requests is
            // last (res[7]) because it was appended last.
            var features = res[0], complaints = res[1], documents = res[2];
            var requests = res[7];
            // res[8] for the same reason: appended, so it is last.
            var lists = res[8];
            // res[9], appended after lists: the work items a reader can pick up.
            var workItems = res[9];
            // The feature form's select needs the complaint list at render
            // time, so it is stashed rather than threaded through the markup.
            window.__complaints = complaints || [];
            box.replaceChildren.apply(box, window.ConcordProjectRender.render(p, features, complaints, documents, {
              capabilities: res[3],
              fieldReports: res[4],
              solutions: res[5],
              alternatives: res[6]
            }, requests, lists, workItems, res[10]));
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
          box.replaceChildren(notFound(slug));
        } else if (err && err.auth) {
          // 401 on a project-scoped read is unexpected: reads are public. Say so
          // rather than rendering an empty page, because an empty page reads as
          // "this project has no features" and nobody investigates that.
          box.replaceChildren(authFailure());
        } else {
          var code = err && err.status ? ' (HTTP ' + err.status + ')' : '';
          box.replaceChildren(loadFailure(code));
        }
      });
  });
})();
