// The archive control: a maintainer opens it, states a reason, and the project is archived or restored.
//
// Why this is a file and not an inline <script>: the response now carries `Content-Security-Policy: script-src 'self'`,
// which refuses inline scripts outright, and a policy that needs a nonce threaded through 39 script tags is a policy
// that fails silently the first time somebody adds a fortieth. The tag is still emitted only for a maintainer — a page
// should not ship the JavaScript for a control it does not render.
(function () {
  var form = document.getElementById('project-archive');
  if (!form || !window.ConcordApi) return;
  var toggle = document.getElementById('project-archive-toggle');
  var status = form.querySelector('[data-archive-status]');
  if (!toggle) return;
  var slug = toggle.getAttribute('data-archive-slug');
  var archived = toggle.getAttribute('data-archived') === '1';
  toggle.addEventListener('click', function () {
    form.hidden = !form.hidden;
    if (!form.hidden) {
      var reason = form.querySelector('input[name=reason]');
      if (reason) reason.focus();
    }
  });
  form.addEventListener('submit', function (event) {
    event.preventDefault();
    var input = form.querySelector('input[name=reason]');
    var reason = input ? input.value.trim() : '';
    var path = '/api/v1/projects/' + encodeURIComponent(slug) + '/archive';
    var call = archived ? window.ConcordApi.del(path) : window.ConcordApi.post(path, { reason: reason });
    call
      .then(function () { location.reload(); })
      .catch(function (error) {
        if (status) status.textContent = (error && error.message) || 'the request failed';
      });
  });
})();
