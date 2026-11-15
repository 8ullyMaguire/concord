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

  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it. This file's copy replaced the same
  // five characters in its own way; a single pass over a character class cannot disagree with another page about which are dangerous.
  var esc = window.ConcordEscape.esc;

  // The row's own link, when the server sends one: `url` is the API's answer to "where does this live", and rebuilding it from the id was
  // how a project link can silently point at a numeric id the page does not route.
  function href(hit) {
    return hit && hit.url ? hit.url : '/projects/' + encodeURIComponent(hit ? hit.entity_id : '');
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
    window.ConcordApi.get(url)
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
      say('Creating…');
      // One client (review item 9b). The three refusals this form explains differently are still explained differently —
      // a 401/403 is "sign in" (403 is the guest case: `session.js` gives every visitor a guest identity, and a guest may
      // read and propose but not hold a namespace), a 409 is "that slug is taken" — and the server's own sentence is
      // appended so the reader sees which rule they hit.
      window.ConcordApi.post('/api/v1/projects', payload)
        .then(function (body) {
          window.location.href = '/projects/' + encodeURIComponent((body && body.slug) || payload.slug);
        })
        .catch(function (err) {
          var status = err && err.status ? err.status : 0;
          if (status === 401 || status === 403) {
            say('Sign in first — a project needs an account, not a guest session.' +
                (err && err.message ? ' (' + err.message + ')' : ''));
            return;
          }
          if (status === 409) {
            say('That slug is taken. Pick another, or open the project that has it.');
            return;
          }
          if (status) {
            say((err && err.message) || ('The project could not be created (' + status + ').'));
            return;
          }
          say('The request could not be sent.');
        });
    });
  }
})();
