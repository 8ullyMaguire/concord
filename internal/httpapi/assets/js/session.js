// Concord session state, shared by every page.
//
// Loaded from base.html rather than from the auth pages, because the header
// has to reflect the session everywhere: a "Sign in" link shown to a signed-in
// visitor is the kind of small untruth that makes people register twice.
//
// Deliberately tiny and dependency-free. It reads localStorage and nothing
// else; it never sends a token anywhere, and it does not verify the token
// because doing so on every page load would put an authenticated request on
// every navigation to keep a label correct.
(function () {
  'use strict';

  var TOKEN_KEY = 'concord.token';
  var USER_KEY = 'concord.user';

  function ConcordSession() {
    this.token = null;
    this.user = null;
    try {
      this.token = localStorage.getItem(TOKEN_KEY);
      var raw = localStorage.getItem(USER_KEY);
      this.user = raw ? JSON.parse(raw) : null;
    } catch (e) {
      // Storage can be unavailable (private mode, or a policy that blocks it).
      // That means "not signed in" for our purposes, not an error worth
      // interrupting the page for.
      this.token = null;
      this.user = null;
    }
  }

  ConcordSession.prototype.signedIn = function () {
    return !!this.token;
  };

  ConcordSession.prototype.name = function () {
    if (!this.user) return '';
    return this.user.display_name || this.user.username || '';
  };

  ConcordSession.prototype.clear = function () {
    try {
      localStorage.removeItem(TOKEN_KEY);
      localStorage.removeItem(USER_KEY);
    } catch (e) { /* nothing to do */ }
    this.token = null;
    this.user = null;
  };

  // authHeaders returns the Authorization header for an authenticated request,
  // or an empty object. Callers spread this so an anonymous call stays a plain
  // fetch.
  ConcordSession.prototype.authHeaders = function () {
    return this.token ? { Authorization: 'Bearer ' + this.token } : {};
  };

  window.ConcordSession = ConcordSession;

  function paint() {
    var link = document.querySelector('[data-auth-link]');
    if (!link) return;
    var s = new ConcordSession();
    if (!s.signedIn()) {
      link.textContent = 'Sign in';
      link.href = '/login';
      link.removeAttribute('title');
      return;
    }
    var name = s.name();
    link.textContent = name || 'Account';
    link.href = '#account';
    link.title = 'Signed in as ' + (name || 'this account');
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', paint);
  } else {
    paint();
  }
})();
