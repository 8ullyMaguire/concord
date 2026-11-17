/* new-project.js — the intent check in front of the create form (docs/plans/post-port-roadmap.md, step 3b).
 *
 * The answer comes from `GET /api/v1/similar/project`, the route the port already ships: an embedder, a scored list, and a
 * `has_duplicate` flag. This file renders it. It deliberately does NOT re-rank, re-threshold or re-word the scores — a client that
 * invents its own bands would disagree with the duplicate gate the server applies when a project is actually filed.
 *
 * The one rule that matters here: an empty answer is said out loud. No "closest project" is manufactured out of nothing, because an
 * intent router that always has a suggestion teaches people to trust a wrong one.
 *
 * Rendering (step 2b): el() trees only; hostile hit titles and URLs are text
 * nodes or setAttribute values.
 */
(function () {
  'use strict';
  var el = window.ConcordDom.el;

  var input = document.getElementById('intent');
  var results = document.getElementById('intent-results');
  var form = document.getElementById('create-form');
  var status = document.getElementById('create-status');
  var timer = null;
  var lastAsked = '';

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
      results.replaceChildren(el('p', { class: 'muted' },
        'The similarity index is not available on this instance right now, so this page cannot tell you what already ' +
        'exists. That is a gap in the instance, not an answer about your idea.'));
      return;
    }
    var hits = answer.similar || [];
    if (!hits.length) {
      results.replaceChildren(el('p', { class: 'muted' }, [
        'Nothing in the catalogue is described the way you put it',
        intent ? [' (', el('code', {}, intent), ')'] : null,
        '. Starting fresh may well be right — but check the ',
        el('a', { href: '/search' }, 'search'),
        ' once before you decide, because a project with a different name would not match this way.'
      ]));
      return;
    }
    var top = hits[0];
    var children = [
      el('p', {}, [
        el('strong', {}, hits.length + (hits.length === 1 ? ' project already matches' : ' projects already match')),
        ' — the closest is ',
        el('a', { href: href(top) }, top.title || top.entity_id),
        typeof top.score === 'number' ? [' ', el('span', { class: 'muted' }, '(' + top.score.toFixed(2) + ')')] : null,
        '.'
      ])
    ];
    if (hits.length > 1) {
      children.push(el('ul', { class: 'project-list' }, hits.slice(1).map(function (hit) {
        return el('li', {}, [
          el('a', { href: href(hit) }, hit.title || hit.entity_id),
          ' ',
          el('span', { class: 'muted' }, typeof hit.score === 'number' ? hit.score.toFixed(2) : '')
        ]);
      })));
    }
    if (answer.has_duplicate) {
      children.push(el('p', { class: 'muted' },
        'This looks like it may already exist. Contributing to it is usually worth more than a second copy.'));
    }
    results.replaceChildren.apply(results, children);
  }

  function check(intent) {
    if (!results) return;
    var trimmed = (intent || '').trim();
    if (trimmed.length < 3) {
      results.replaceChildren();
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
        results.replaceChildren(el('p', { class: 'muted' },
          'The intent check could not be reached. You can still create the project.'));
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
          var code = err && err.status ? err.status : 0;
          if (code === 401 || code === 403) {
            say('Sign in first — a project needs an account, not a guest session.' +
                (err && err.message ? ' (' + err.message + ')' : ''));
            return;
          }
          if (code === 409) {
            say('That slug is taken. Pick another, or open the project that has it.');
            return;
          }
          if (code) {
            say((err && err.message) || ('The project could not be created (' + code + ').'));
            return;
          }
          say('The request could not be sent.');
        });
    });
  }
})();
