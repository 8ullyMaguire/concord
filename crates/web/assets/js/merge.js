/* apiAuth: the session's Authorization header, for API calls.
 *
 * The page's own reads need the token even when the navigation cookie opened the
 * HTML: the cookie is deliberately not an API credential (it cannot authorize a
 * write), so a member whose page loaded would otherwise see every panel 404.
 * Auth is the one thing this file does not decide for itself. */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}

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
    if (!g) return '<td class="merge-gate">—</td>';
    var parts = [];
    if (g.requires_quorum) {
      parts.push((g.quorum_ok ? 'met' : 'short') + ': ' + g.approvals + ' of ' + g.needed +
        ' confirmation' + (g.needed === 1 ? '' : 's'));
    } else {
      parts.push('the charter dropped the quorum' +
        (g.needed === 0 ? '' : ' (needed ' + g.needed + ')'));
    }
    parts.push(g.reviewer_ok ? 'a reviewer has signed off' : 'a reviewer has not signed off');
    return '<td class="merge-gate' + (g.passed ? ' is-met' : '') + '" title="measured against ' +
      (g.eligible == null ? '?' : g.eligible) + ' eligible collaborator' +
      (g.eligible === 1 ? '' : 's') + '">' + esc(parts.join(' · ')) + '</td>';
  }

  function row(mr) {
    var actions = '';
    if (signedIn() && mr.status === 'open') {
      actions =
        '<button class="btn btn-sm" data-approve="' + esc(mr.id) + '">Confirm</button> ' +
        '<button class="btn btn-sm btn-primary" data-execute="' + esc(mr.id) + '">Merge</button> ' +
        '<button class="btn btn-sm" data-reject="' + esc(mr.id) + '">Reject</button>';
    } else if (!signedIn()) {
      actions = '<span class="muted">sign in to confirm or land this</span>';
    } else {
      actions = '<span class="muted">' + esc(mr.status) + '</span>';
    }
    return '<tr class="merge-row" data-mr="' + esc(mr.id) + '">' +
      '<td class="merge-title">' + esc(mr.title) + '</td>' +
      '<td class="merge-status" data-status="' + esc(mr.status) + '">' + esc(mr.status) + '</td>' +
      gateCell(mr.gate) +
      '<td class="merge-feature"><a href="/projects/' + encodeURIComponent(slug) +
        '/features/' + esc(mr.feature_id) + '">#' + esc(mr.feature_id) + '</a></td>' +
      '<td class="merge-opened">' + esc(when(mr.opened_at)) + '</td>' +
      '<td class="merge-actions">' + actions + '</td>' +
      '</tr>';
  }

  function createForm(features) {
    var opts = features.map(function (f) {
      return '<option value="' + esc(f.id) + '">' + esc(f.title) + '</option>';
    }).join('');
    return '<section class="panel" id="merge-create">' +
      '<h2>Open a merge request</h2>' +
      '<p class="panel-note">A merge request says a change is ready to land; §5.7 then ' +
      'asks the project to confirm it. It must name the feature it lands.</p>' +
      '<form id="merge-create-form" class="form-grid">' +
      '<label class="form-label" for="merge-feature">Feature</label>' +
      '<select id="merge-feature" name="feature_id" required>' + opts + '</select>' +
      '<label class="form-label" for="merge-title">Title</label>' +
      '<input id="merge-title" name="title" type="text" required maxlength="200" ' +
      'placeholder="what lands, in one line">' +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">' +
      'Open merge request</button></div>' +
      '</form></section>';
  }

  function render(mrs, features) {
    var rows = mrs.map(row).join('');
    root.innerHTML =
      '<section class="panel"><h2>Merge requests</h2>' +
      (mrs.length
        ? '<table class="data-table" id="merge-table"><thead><tr><th>Change</th>' +
          '<th>Status</th><th>Confirmations</th><th>Feature</th><th>Opened</th>' +
          '<th></th></tr></thead><tbody>' + rows + '</tbody></table>'
        : '<p class="empty-state-title" id="merge-empty">No merge request has been ' +
          'opened here yet. A change has to pass consensus before this is the ' +
          'question — but this is the last step, and nothing lands without it.</p>') +
      '</section>' +
      (features.length ? createForm(features) :
        '<section class="panel"><h2>Open a merge request</h2>' +
        '<p class="panel-note">This project has no features to land yet.</p></section>');
    root.setAttribute('aria-busy', 'false');
  }

  function get(path) {
    return fetch(path, { headers: apiAuth() }).then(function (res) {
      return res.json().then(function (body) {
        if (!res.ok) throw new Error(body && body.error ? body.error : ('HTTP ' + res.status));
        return body;
      });
    });
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
      root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
        'Could not load the merge requests.</p><p>' + esc(err.message) + '</p></div>';
    });
  }

  // A write is a request to the server, and the server's answer is the only report
  // worth showing: a refusal carries the shortfall ("2 of 2 confirmations given; a
  // reviewer approval is required"), which is the gate explaining itself.
  function act(path, body) {
    say('Working…');
    return fetch(path, {
      method: body === undefined ? 'PUT' : 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' }),
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (out) {
        if (!res.ok) throw new Error(out && out.error ? out.error : ('HTTP ' + res.status));
        return out;
      });
    }).then(function (out) {
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
