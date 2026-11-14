/* new-project.js — the intent check in front of the create form (docs/plans/post-port-roadmap.md, step 3b).
 *
 * The answer comes from `GET /api/v1/similar/project`, the route the port already ships: an embedder, a scored list, and a
 * `has_duplicate` flag. This file renders it. It deliberately does NOT re-rank, re-threshold or re-word the scores — a client that
 * invents its own bands would disagree with the duplicate gate the server applies when a project is actually filed.
 *
 * The one rule that matters here: an empty answer is said out loud. No "closest project" is manufactured out of nothing, because an
 * intent router that always has a suggestion teaches people to trust a wrong one.
 */
(function () {
  'use strict';

  var input = document.getElementById('intent');
  var results = document.getElementById('intent-results');
  var form = document.getElementById('create-form');
  var status = document.getElementById('create-status');
  var timer = null;
  var lastAsked = '';

  function esc(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // The row's own link, when the server sends one: `url` is the API's answer to "where does this live", and rebuilding it from the id was
  // how a project link can silently point at a numeric id the page does not route.
  function href(hit) {
    return hit && hit.url ? hit.url : '/projects/' + encodeURIComponent(hit ? hit.entity_id : '');
  }

  function token() {
    try {
      return localStorage.getItem('concord.token') || '';
    } catch (error) {
      return '';
    }
  }

  function say(text) {
    if (status) status.textContent = text;
  }

  // One rendering function for all three answers, so "nothing matched" cannot look like a failed request.
  function render(answer, intent) {
    if (!results) return;
    if (!answer || answer.available === false) {
      results.innerHTML =
        '<p class="muted">The similarity index is not available on this instance right now, so this page cannot tell you what already ' +
        'exists. That is a gap in the instance, not an answer about your idea.</p>';
      return;
    }
    var hits = answer.similar || [];
    if (!hits.length) {
      results.innerHTML =
        '<p class="muted">Nothing in the ' + esc(answer.kind ? 'catalogue' : 'catalogue') + ' is described the way you put it' +
        (intent ? ' (<code>' + esc(intent) + '</code>)' : '') + '. Starting fresh may well be right — but check the ' +
        '<a href="/search">search</a> once before you decide, because a project with a different name would not match this way.</p>';
      return;
    }
    var top = hits[0];
    var html = '<p><strong>' + hits.length + (hits.length === 1 ? ' project already matches' : ' projects already match') +
      '</strong> — the closest is <a href="' + esc(href(top)) + '">' + esc(top.title || top.entity_id) + '</a>';
    if (typeof top.score === 'number') html += ' <span class="muted">(' + top.score.toFixed(2) + ')</span>';
    html += '.</p>';
    if (hits.length > 1) {
      html += '<ul class="project-list">';
      for (var i = 1; i < hits.length; i++) {
        html += '<li><a href="' + esc(href(hits[i])) + '">' + esc(hits[i].title || hits[i].entity_id) + '</a>' +
          ' <span class="muted">' + (typeof hits[i].score === 'number' ? hits[i].score.toFixed(2) : '') + '</span></li>';
      }
      html += '</ul>';
    }
    if (answer.has_duplicate) {
      html += '<p class="muted">This looks like it may already exist. Contributing to it is usually worth more than a second copy.</p>';
    }
    results.innerHTML = html;
  }

  function check(intent) {
    if (!results) return;
    var trimmed = (intent || '').trim();
    if (trimmed.length < 3) {
      results.innerHTML = '';
      lastAsked = '';
      return;
    }
    if (trimmed === lastAsked) return;
    lastAsked = trimmed;
    var url = '/api/v1/similar/project?limit=5&text=' + encodeURIComponent(trimmed);
    fetch(url, { headers: { Accept: 'application/json' } })
      .then(function (reply) {
        if (!reply.ok) throw new Error('similar answered ' + reply.status);
        return reply.json();
      })
      .then(function (answer) {
        // A slow answer must not overwrite a newer one: the reader may have kept typing.
        if (trimmed !== lastAsked) return;
        render(answer, trimmed);
      })
      .catch(function () {
        if (!results) return;
        results.innerHTML = '<p class="muted">The intent check could not be reached. You can still create the project.</p>';
      });
  }

  if (input) {
    input.addEventListener('input', function () {
      clearTimeout(timer);
      var value = input.value;
      timer = setTimeout(function () {
        check(value);
      }, 350);
    });
    // Check the placeholder example on load? No: only what the reader typed. A page that answers a question nobody asked invites the
    // reader to believe the answer is about them.
  }

  if (form) {
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      var payload = {
        name: (document.getElementById('name') || {}).value || '',
        slug: (document.getElementById('slug') || {}).value || '',
        description: (document.getElementById('description') || {}).value || '',
        governance_model: 'collective',
        license: ''
      };
      var headers = { 'Content-Type': 'application/json' };
      if (token()) headers.Authorization = 'Bearer ' + token();
      say('Creating…');
      fetch('/api/v1/projects', { method: 'POST', headers: headers, body: JSON.stringify(payload) })
        .then(function (reply) {
          return reply.json().catch(function () { return {}; }).then(function (body) {
            return { status: reply.status, body: body };
          });
        })
        .then(function (answer) {
          if (answer.status === 201) {
            window.location.href = '/projects/' + encodeURIComponent(answer.body.slug || payload.slug);
            return;
          }
          if (answer.status === 401 || answer.status === 403) {
            // 403 is the guest case: `session.js` hands every visitor a guest identity, and a guest may read and propose but not hold a
            // namespace. Saying "sign in" for both is right — the fix is the same — and the server's own sentence is appended so the
            // reader sees which rule they hit.
            say('Sign in first — a project needs an account, not a guest session.' +
                (answer.body && answer.body.error ? ' (' + answer.body.error + ')' : ''));
            return;
          }
          if (answer.status === 409) {
            say('That slug is taken. Pick another, or open the project that has it.');
            return;
          }
          say(answer.body.error || answer.body.code || ('The project could not be created (' + answer.status + ').'));
        })
        .catch(function () {
          say('The request could not be sent.');
        });
    });
  }
})();
