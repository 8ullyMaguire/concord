
// Concord merge requests -- §5.7's confirmation gate, with a reader.
//
// Four endpoints have existed for this page's whole life and nothing called them:
// list, create, approve, execute, reject. Worse, the gate behind them was a pure
// function with no caller, so `execute` landed a change with zero confirmations in a
// charter that asked for two. This page is where the gate becomes visible.
//
// Two rules it does NOT follow:
//
// 1. It never computes the quorum. `needed`, `approvals`, `reviewer_ok` come from
//    the server's own verdict -- the same reason the criteria page renders the
//    forge's arithmetic instead of re-deriving it. A client that worked out
//    "two confirmations" for itself would be a second implementation of §5.7, and
//    the one that gets it wrong (the ratio is configurable per project).
// 2. It does not hide the buttons from somebody who may not act. Whether an account
//    holds the standing to confirm is the server's answer, in the server's words --
//    and a refusal that says "a reviewer approval is required" teaches the rule
//    better than a missing button does. Signed-out visitors get no controls at all,
//    because that is not a policy question.
//
// Rendering (step 2b): el() trees only; hostile titles and error sentences are
// text nodes or setAttribute values.
(function () {
  'use strict';
  var el = window.ConcordDom.el;

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function when(ts) {
    if (!ts) return '—';
    var d = new Date(Number(ts) * 1000);
    if (isNaN(d.getTime())) return '—';
    return d.toISOString().slice(0, 10);
  }

  var root = document.getElementById('merge-root');
  var notice = document.getElementById('merge-notice');
  if (!root) return;

  var slug = slugFromPath();
  var base = '/api/v1/projects/' + encodeURIComponent(slug);

  function say(msg, bad) {
    if (!notice) return;
    notice.textContent = msg || '';
    notice.className = bad ? 'form-note is-error' : 'form-note';
  }

  function signedIn() {
    return !!(window.ConcordSession && window.ConcordSession.instance().signedIn());
  }

  // The gate, as the server reported it. Nothing here recomputes it: the wording
  // describes the state, it does not decide it.
  function gateCell(g) {
    if (!g) return el('td', { class: 'merge-gate' }, '—');
    var parts = [];
    if (g.requires_quorum) {
      parts.push((g.quorum_ok ? 'met' : 'short') + ': ' + g.approvals + ' of ' + g.needed +
        ' confirmation' + (g.needed === 1 ? '' : 's'));
    } else {
      parts.push('the charter dropped the quorum' +
        (g.needed === 0 ? '' : ' (needed ' + g.needed + ')'));
    }
    parts.push(g.reviewer_ok ? 'a reviewer has signed off' : 'a reviewer has not signed off');
    var eligible = g.eligible == null ? '?' : g.eligible;
    return el('td', {
      class: 'merge-gate' + (g.passed ? ' is-met' : ''),
      title: 'measured against ' + eligible + ' eligible collaborator' + (g.eligible === 1 ? '' : 's')
    }, parts.join(' · '));
  }

  function row(mr) {
    var actions;
    if (signedIn() && mr.status === 'open') {
      actions = [
        el('button', { class: 'btn btn-sm', 'data-approve': mr.id }, 'Confirm'), ' ',
        el('button', { class: 'btn btn-sm btn-primary', 'data-execute': mr.id }, 'Merge'), ' ',
        el('button', { class: 'btn btn-sm', 'data-reject': mr.id }, 'Reject')
      ];
    } else if (!signedIn()) {
      actions = el('span', { class: 'muted' }, 'sign in to confirm or land this');
    } else {
      actions = el('span', { class: 'muted' }, mr.status);
    }
    return el('tr', { class: 'merge-row', 'data-mr': mr.id }, [
      el('td', { class: 'merge-title' }, mr.title),
      el('td', { class: 'merge-status', 'data-status': mr.status }, mr.status),
      gateCell(mr.gate),
      el('td', { class: 'merge-feature' },
        el('a', { href: '/projects/' + encodeURIComponent(slug) + '/features/' + mr.feature_id },
          '#' + mr.feature_id)),
      el('td', { class: 'merge-opened' }, when(mr.opened_at)),
      el('td', { class: 'merge-actions' }, actions)
    ]);
  }

  function createForm(features) {
    var select = el('select', { id: 'merge-feature', name: 'feature_id', required: true },
      features.map(function (f) { return el('option', { value: f.id }, f.title); }));
    var title = el('input', {
      id: 'merge-title', name: 'title', type: 'text', required: true, maxlength: 200,
      placeholder: 'what lands, in one line'
    });
    return el('section', { class: 'panel', id: 'merge-create' }, [
      el('h2', {}, 'Open a merge request'),
      el('p', { class: 'panel-note' },
        'A merge request says a change is ready to land; §5.7 then asks the project ' +
        'to confirm it. It must name the feature it lands.'),
      el('form', { id: 'merge-create-form', class: 'form-grid' }, [
        el('label', { class: 'form-label', for: 'merge-feature' }, 'Feature'), select,
        el('label', { class: 'form-label', for: 'merge-title' }, 'Title'), title,
        el('div', { class: 'detail-actions' },
          el('button', { type: 'submit', class: 'btn btn-primary' }, 'Open merge request'))
      ])
    ]);
  }

  function render(mrs, features) {
    var children = [el('section', { class: 'panel' }, [
      el('h2', {}, 'Merge requests'),
      mrs.length
        ? el('table', { class: 'data-table', id: 'merge-table' }, [
            el('thead', {}, el('tr', {}, [
              el('th', {}, 'Change'), el('th', {}, 'Status'),
              el('th', {}, 'Confirmations'), el('th', {}, 'Feature'),
              el('th', {}, 'Opened'), el('th', {})
            ])),
            el('tbody', {}, mrs.map(row))
          ])
        : el('p', { class: 'empty-state-title', id: 'merge-empty' },
            'No merge request has been opened here yet. A change has to pass ' +
            'consensus before this is the question — but this is the last step, ' +
            'and nothing lands without it.')
    ])];
    children.push(features.length ? createForm(features) : el('section', { class: 'panel' }, [
      el('h2', {}, 'Open a merge request'),
      el('p', { class: 'panel-note' }, 'This project has no features to land yet.')
    ]));
    root.replaceChildren.apply(root, children);
    root.setAttribute('aria-busy', 'false');
  }

  function get(path) {
    // One client (review item 9b): `body.error` was this file's own copy of the refusal branch.
    return window.ConcordApi.get(path);
  }

  function load() {
    return Promise.all([
      get(base + '/merge'),
      get(base + '/features').catch(function () { return []; })
    ]).then(function (out) {
      var mrs = out[0] || [];
      var feats = out[1] || [];
      render(Array.isArray(mrs) ? mrs : [], Array.isArray(feats) ? feats : []);
    }).catch(function (err) {
      root.replaceChildren(el('div', { class: 'empty-state' }, [
        el('p', { class: 'empty-state-title' }, 'Could not load the merge requests.'),
        el('p', {}, err.message)
      ]));
    });
  }

  // A write is a request to the server, and the server's answer is the only report
  // worth showing: a refusal carries the shortfall ("2 of 2 confirmations given; a
  // reviewer approval is required"), which is the gate explaining itself.
  function act(path, body) {
    say('Working…');
    return window.ConcordApi.request(body === undefined ? 'PUT' : 'POST', path, body === undefined ? null : body)
    .then(function (out) {
      say(out && out.status ? out.status : 'Done.');
      return load();
    }).catch(function (err) {
      say(err.message, true);
    });
  }

  // Attribute -> endpoint, in one place. The three writes are separate routes and
  // naming them explicitly is what keeps them greppable: a bare ternary of strings
  // is the same information with three places to get it wrong.
  var ACTIONS = { 'data-approve': 'approve', 'data-execute': 'execute', 'data-reject': 'reject' };

  root.addEventListener('click', function (e) {
    var el = e.target.closest ? e.target.closest('[data-approve],[data-execute],[data-reject]') : null;
    if (!el) return;
    var id = '', verb = '';
    for (var attr in ACTIONS) {
      if (Object.prototype.hasOwnProperty.call(ACTIONS, attr) && el.hasAttribute(attr)) {
        id = el.getAttribute(attr);
        verb = ACTIONS[attr];
        break;
      }
    }
    if (!verb) return;
    act(base + '/merge/' + encodeURIComponent(id) + '/' + verb);
  });

  root.addEventListener('submit', function (e) {
    if (!e.target || e.target.id !== 'merge-create-form') return;
    e.preventDefault();
    var title = document.getElementById('merge-title');
    var feature = document.getElementById('merge-feature');
    var body = {
      feature_id: Number(feature.value),
      title: title ? title.value.trim() : ''
    };
    if (!body.title) { say('a merge request needs a title', true); return; }
    act(base + '/merge', body).then(function () {
      if (title) title.value = '';
    });
  });

  load();
})();
