// The adoption pool's one interaction: taking a dormant project on.
//
// Rendering is not this file's job — the list, the numbers and the reason all arrive in the document. What is left is the write, and the
// write needs a token, which is why it is here and not on the server: a claim that a crawler could make would be a claim nobody made.
(function () {
  'use strict';

  function status(el, message, isError) {
    el.hidden = false;
    el.textContent = message;
    el.className = 'form-status' + (isError ? ' form-status-error' : '');
  }

  document.addEventListener('DOMContentLoaded', function () {
    var list = document.querySelector('.adoption-list');
    if (!list) return;

    var note = document.createElement('p');
    note.hidden = true;
    note.setAttribute('role', 'status');
    list.parentNode.insertBefore(note, list.nextSibling);

    list.addEventListener('click', function (event) {
      var button = event.target.closest('button[data-claim]');
      if (!button) return;
      var slug = button.getAttribute('data-claim');

      button.disabled = true;
      // One request path: `api.js` owns the headers, the parsed body and the 401 sentence.
      window.ConcordApi.post('/api/v1/projects/' + encodeURIComponent(slug) + '/claim', {}).then(function (payload) {

        // The list is a measurement, so the honest thing after a successful claim is to say what happened and let the next visit show the
        // new state: the project only leaves this pool when its own commit history says so, which no claim can manufacture.
        status(note, 'You are a maintainer of ' + slug + ' now — ' + (payload.days_silent_display || '') +
          '. The project stays in this pool until its own history shows activity.', false);
        button.textContent = 'Adopted';
        button.disabled = true;
      }).catch(function (error) {
        button.disabled = false;
        status(note, error.message, true);
      });
    });
  });
})();
