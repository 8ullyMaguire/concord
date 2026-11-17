// Every write the project page makes besides the claim and the sizing form: the status line a reader sees after one, the
// POST helper all of them share, §6.1's duplicate check (which reads, then warns before submit), and §14's field reports.
//
// Split out of project.js (review item 9's "split by capability"). Registers itself on window before the page script runs;
// `project.js` binds `setStatus`, `postJSON` and `showCardRefusal` from here, so each has ONE definition and the page's
// call sites did not change.
(function () {
  var esc = window.ConcordEscape.esc;
  var el = window.ConcordDom.el;

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
      panel.replaceChildren();
      return;
    }
    timer = setTimeout(function () { dupLookup(kind, projectID, text, panel); }, 350);
  });
}

function dupLookup(kind, projectID, text, panel) {
  var mine = ++dupSeq;
  panel.replaceChildren(el('p', { class: 'dup-note dup-checking' },
    'Checking for existing ' + kind + 's…'));
  // NO trailing slash after the kind: Axum matches `/api/v1/similar/{kind}` exactly, so `/similar/complaint/` is a 404
  // and the panel says "The duplicate check could not run" for every filer. The Go service answered both; the port
  // answers one. `duplicates_e2e` found this the moment it was pointed at PostgreSQL (2026-11-15).
  var url = '/api/v1/similar/' + encodeURIComponent(kind) + '?text=' +
    encodeURIComponent(text) + '&project_id=' + encodeURIComponent(projectID);
  // One client, including here: the duplicate check is a READ that a signed-out visitor can make, and `api.js`
  // sends no token when there is no session, so the request is the same one this line used to build by hand.
  window.ConcordApi.get(url)
    .then(function (data) {
      if (mine !== dupSeq) return;
      panel.replaceChildren.apply(panel, dupMarkup(kind, data));
    })
    .catch(function () {
      if (mine !== dupSeq) return;
      // Never an empty panel: "we could not ask" and "nothing similar exists"
      // are different statements, and the second one is a lie if the first is
      // what happened.
      panel.replaceChildren(el('p', { class: 'dup-note' },
        'The duplicate check could not run. Filing still works.'));
    });
}

function dupMarkup(kind, data) {
  if (!data || data.available === false) {
    return [el('p', { class: 'dup-note' },
      (data && data.note) || 'Duplicate detection is not configured on this instance.')];
  }
  var matches = data.similar || [];
  // The coverage line is not decoration. §6.1's promise is "nothing similar
  // exists on the parts of the instance we indexed", and a filer told "no
  // duplicates" by an instance that has indexed a twelfth of its corpus has
  // been told nothing. The endpoint returns the number; the panel says it.
  var cov = data.coverage || null;
  var covNote = '';
  if (cov && cov.coverage != null && cov.coverage < 1) {
    covNote = ' Only ' + Math.round(cov.coverage * 100) + '% of ' + kind +
      's are indexed, so this is not a complete answer.';
  }
  if (!matches.length) {
    return [el('p', { class: 'dup-note' }, 'No similar ' + kind + ' found.' + covNote)];
  }
  var nodes = [el('p', { class: data.has_duplicate === true ? 'dup-note dup-strong' : 'dup-note' },
    data.has_duplicate === true
      ? 'This looks like an existing ' + kind +
        '. Read it before filing — if it is the same problem, adding to it is worth ' +
        'more than a second report.'
      : 'Similar ' + kind + 's already exist:')];
  nodes.push(el('ul', { class: 'dup-list' }, matches.slice(0, 5).map(function (m) {
    var label = m.title || (kind + ' #' + m.entity_id);
    // The href is the API's own `url`, not a path reassembled here. The
    // endpoint used to hand out `/api/v1/features/222`, which is a 404 -- and
    // it did so for every match, because nothing ever followed one. Using the
    // server's value means a wrong URL fails a test instead of being written
    // correctly a second time in this file.
    return el('li', {}, [
      m.url ? el('a', { href: m.url }, label) : label,
      ' ', el('span', { class: 'dup-score' }, Math.round((m.score || 0) * 100) + '%')
    ]);
  })));
  if (covNote) nodes.push(covNote);
  return nodes;
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
  // One request path and one error model: `api.js` owns the headers (through `session.js`), the parsed body, the 401 sentence and the
  // distinction between "the server refused" and "the network did", so this file cannot disagree with another page about either.
  return window.ConcordApi.post(path, body);
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
  window.ConcordWrites = {
    setStatus: setStatus,
    postJSON: postJSON,
    showCardRefusal: showCardRefusal,
    panel: dupPanel,
    wire: wireDupPanel,
    wireFieldReports: wireFieldReports
  };
})();
