// Concord sign-in and registration.
//
// The token is returned once by the API and only its SHA-256 is stored, so
// there is no endpoint that can give it back. It is kept in localStorage, and
// that is a deliberate trade: it means a signed-in reader stays signed in
// across visits without a cookie round trip, at the cost of the token being
// readable by any script on the origin. The API is same-origin and sets a
// restrictive CSP, so the exposure is limited to a compromise of this origin
// — which a cookie would not prevent either. A cookie with HttpOnly would be
// strictly better; it is noted in docs/specs/auth-and-portfolio-import.md as
// the next step rather than pretended to be done.
(function () {
  'use strict';

  function el(id) { return document.getElementById(id); }

  // session.js owns the storage keys; reading them from two places is how the
  // header and the form end up disagreeing about whether anyone is signed in.
  function session() {
    // The shared instance, not a fresh one. A private copy is a second source
    // of truth: it can disagree with the header the user is looking at, and
    // nothing reports the disagreement.
    return window.ConcordSession ? window.ConcordSession.instance() : null;
  }

  function showError(msg) {
    var box = el('auth-error');
    if (!box) return;
    if (!msg) { box.hidden = true; box.textContent = ''; return; }
    box.hidden = false;
    box.textContent = msg;
  }

  // Turn an API error body into something a person can act on. The server
  // returns {"error": "..."} with a 4xx; anything else is a network problem.
  function describe(status, body) {
    if (status === 0) return 'Could not reach the server. Check your connection and try again.';
    if (status === 409) return 'That username is already taken.';
    if (status === 401) return 'Wrong username or password.';
    if (status === 429) return 'Too many attempts. Wait a minute and try again.';
    if (body && body.error) return String(body.error);
    return 'Something went wrong (HTTP ' + status + ').';
  }

  function store(token, user) {
    try {
      // session.js owns the keys. Repeating the literals here is how the two
      // files drift apart and a user ends up "signed in" to a header that
      // cannot see the token that signed them in.
      localStorage.setItem('concord.token', token);
      localStorage.setItem('concord.user', JSON.stringify(user || {}));
      if (window.ConcordSession) window.ConcordSession.instance().refresh();
    } catch (e) {
      // Private mode or a storage policy that blocks writes. The session will
      // not persist, so say so rather than leaving a form that silently
      // forgets the token on the next click.
      showError('Signed in, but this browser will not store the session. It will end when you close the tab.');
    }
  }

  // Where to go after signing in: the page the visitor was trying to reach.
  function nextUrl(fallback) {
    try {
      var p = new URLSearchParams(window.location.search).get('next');
      // Only same-site paths. Accepting an absolute URL here would turn the
      // login page into an open redirect.
      if (p && p.charAt(0) === '/' && p.charAt(1) !== '/') return p;
    } catch (e) { /* no URLSearchParams; fall through */ }
    return fallback || '/projects';
  }

  function submit(form, path, extra, fallback) {
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      showError('');
      var btn = form.querySelector('button[type=submit]');
      if (btn) btn.disabled = true;

      var payload = {
        username: el(form.id === 'login-form' ? 'login-username' : 'reg-username').value.trim(),
        password: el(form.id === 'login-form' ? 'login-password' : 'reg-password').value
      };
      if (extra) {
        var dn = el('reg-display');
        payload.display_name = dn ? dn.value.trim() : '';
      }

      fetch('/api/v1' + path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      }).then(function (res) {
        return res.json().catch(function () { return null; }).then(function (body) {
          if (!res.ok) throw { status: res.status, body: body };
          return body;
        });
      }).then(function (data) {
        store(data.token, data.user);
        window.location.assign(nextUrl(fallback));
      }).catch(function (err) {
        if (btn) btn.disabled = false;
        showError(describe(err.status || 0, err.body));
      });
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    // Already signed in? Go straight through rather than showing a form whose
    // submission would replace a working token.
    var s = session();
    var existing = s ? s.token : null;
    if (existing) {
      fetch('/api/v1/auth/me', { headers: { Authorization: 'Bearer ' + existing } })
        .then(function (r) {
          if (r.ok) { window.location.assign(nextUrl()); return; }
          if (s) s.clear();
        })
        .catch(function () { /* network: leave the form up */ });
    }

    var login = el('login-form');
    if (login) submit(login, '/auth/login', false, '/projects');
    var reg = el('register-form');
    if (reg) submit(reg, '/auth/register', true, '/projects');
  });
})();
