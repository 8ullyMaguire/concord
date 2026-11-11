/*
 * The feature page's moderation panel: take an idea out of the arena, or put it back.
 *
 * The panel is only rendered for readers who may use it. There is no capability endpoint
 * that names the viewer's role, so this probes with the act itself and reads the status:
 * a permitted caller gets 400 ("this act needs confirmation") from the handler's confirm
 * check, and a refused one gets 401/403/404 before the body is even read. Nothing is
 * mutated by the probe -- confirm is false -- and the two outcomes are the two answers the
 * panel needs.
 *
 * Every value from the API is written with textContent. A reason is prose typed by a person
 * and comes back through the log; it must never be parsed as markup.
 */
(function () {
  'use strict';

  var root = document.getElementById('moderation-panel');
  if (!root) return;

  var slug = root.getAttribute('data-project') || '';
  var featureID = root.getAttribute('data-feature') || '';
  var form = document.getElementById('moderation-form');
  var reason = document.getElementById('moderation-reason');
  var removeBtn = document.getElementById('moderation-remove');
  var cancelBtn = document.getElementById('moderation-cancel');
  var confirmBox = document.getElementById('moderation-confirm');
  var status = document.getElementById('moderation-status');
  var undoBox = document.getElementById('moderation-undo');
  var undoBtn = document.getElementById('moderation-undo-btn');

  if (!slug || !featureID || !form) return;

  var base = '/api/v1/projects/' + encodeURIComponent(slug) + '/moderation/arena';
  var lastAct = null;

  function say(text) {
    if (!status) return;
    status.textContent = text || '';
    status.hidden = !text;
  }

  function post(url, body) {
    return fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(body)
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        return { status: res.status, data: data };
      });
    });
  }

  // The probe: permitted -> 400 with "confirmation", refused -> 401/403/404.
  post(base + '/remove', { feature_id: Number(featureID), confirm: false }).then(function (r) {
    if (r.status !== 400 && r.status !== 201) {
      root.hidden = true;
      return;
    }
    root.hidden = false;
  }).catch(function () {
    root.hidden = true;
  });

  var armed = false;

  form.addEventListener('submit', function (ev) {
    ev.preventDefault();
    var text = (reason && reason.value || '').trim();
    if (!text) {
      say('A reason is required: the log publishes it beside the act.');
      if (reason) reason.focus();
      return;
    }
    if (!armed) {
      // Two steps, in place: a button that removes an idea on the first press is the
      // mis-click this panel exists to prevent.
      armed = true;
      if (confirmBox) confirmBox.hidden = false;
      if (removeBtn) removeBtn.hidden = true;
      say('');
      return;
    }
    removeBtn.disabled = true;
    say('Removing\u2026');
    post(base + '/remove', {
      feature_id: Number(featureID),
      reason: text,
      confirm: true
    }).then(function (r) {
      removeBtn.disabled = false;
      armed = false;
      if (confirmBox) confirmBox.hidden = true;
      if (removeBtn) removeBtn.hidden = false;
      if (r.status !== 201) {
        say((r.data && r.data.error) || 'Could not remove this idea.');
        return;
      }
      say('Removed from the arena. The act is in the modlog.');
      lastAct = r.data && r.data.act ? r.data.act.id : null;
      if (undoBox && lastAct) undoBox.hidden = false;
    }).catch(function () {
      removeBtn.disabled = false;
      say('Could not reach the server.');
    });
  });

  if (cancelBtn) {
    cancelBtn.addEventListener('click', function () {
      armed = false;
      if (confirmBox) confirmBox.hidden = true;
      if (removeBtn) removeBtn.hidden = false;
      say('');
    });
  }

  if (undoBtn) {
    undoBtn.addEventListener('click', function () {
      if (!lastAct) return;
      undoBtn.disabled = true;
      post(base + '/restore', {
        undoes: lastAct,
        reason: 'undone from the feature page',
        confirm: true
      }).then(function (r) {
        undoBtn.disabled = false;
        if (r.status !== 200) {
          say((r.data && r.data.error) || 'Could not undo that.');
          return;
        }
        if (undoBox) undoBox.hidden = true;
        say('Put back in the arena.');
        lastAct = null;
      }).catch(function () {
        undoBtn.disabled = false;
        say('Could not reach the server.');
      });
    });
  }
})();
