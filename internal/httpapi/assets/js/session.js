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

  // One shared instance.
  //
  // The constructor used to be exported on its own, so every caller built its
  // own copy: auth.js stored a token into one, session.js painted the header
  // from another, and a page that asked for the session got a fresh object
  // that had never seen the token. That is not visible as an error — it shows
  // up as a header that says "Sign in" to somebody who is signed in, and forms
  // that render as though nobody were present.
  var shared = null;
  ConcordSession.instance = function () {
    if (!shared) shared = new ConcordSession();
    return shared;
  };
  // The token is written by auth.js after a successful login, so an instance
  // created before that would keep reporting "signed out" for the life of the
  // page. Reloading from storage on demand makes the value current.
  ConcordSession.prototype.refresh = function () {
    try {
      this.token = localStorage.getItem(TOKEN_KEY);
      var raw = localStorage.getItem(USER_KEY);
      this.user = raw ? JSON.parse(raw) : null;
    } catch (e) {
      this.token = null;
      this.user = null;
    }
    return this;
  };

  window.ConcordSession = ConcordSession;

  // The account link used to point at '#account', a fragment that exists nowhere
  // in the document, so signing in led to a dead click. There is no account page
  // in this build, so it now goes to the projects list the account can act on and
  // stops pretending to be a link into a profile that is not implemented.
  var ACCOUNT_HREF = '/projects';

  function paint() {
    var link = document.querySelector('[data-auth-link]');
    var signout = document.querySelector('[data-signout]');
    var s = ConcordSession.instance().refresh();

    if (link) {
      if (!s.signedIn()) {
        link.textContent = 'Sign in';
        link.href = '/login';
        link.removeAttribute('title');
      } else {
        var name = s.name();
        link.textContent = name || 'Account';
        link.href = ACCOUNT_HREF;
        link.title = 'Signed in as ' + (name || 'this account');
      }
    }

    if (signout) {
      signout.hidden = !s.signedIn();
      signout.disabled = false;
      signout.textContent = 'Sign out';
    }
  }

  // signOut ends the session on the server and locally, in that order.
  //
  // The server call matters: RevokeToken invalidates the bearer token, so
  // clearing localStorage alone leaves a working credential in a database until
  // it expires. That is the difference between signing out and pretending to.
  //
  // The local clear runs in a finally, so a failed or slow request cannot leave
  // the person staring at a "Sign out" button that silently did nothing. If the
  // revoke fails the token is still gone from this browser; the failure is
  // reported rather than swallowed, because silently ignoring it would be a lie
  // about a security-relevant action.
  function signOut() {
    var s = ConcordSession.instance();
    var button = document.querySelector('[data-signout]');
    if (button) {
      button.disabled = true;
      button.textContent = 'Signing out\u2026';
    }

    var done = function (failed) {
      s.clear();
      if (failed) {
        if (button) {
          button.disabled = false;
          button.textContent = 'Sign out failed \u2014 try again';
        }
      } else {
        // Full reload rather than a soft swap: every page caches rendered data
        // keyed on the session, and none of them observe the change.
        window.location.href = '/';
      }
    };

    if (!s.token) { done(false); return; }

    try {
      fetch('/api/v1/auth/logout', {
        method: 'POST',
        headers: s.authHeaders()
      }).then(function (res) {
        // Only a 2xx counts as revoked. `res.status !== 204 && !res.ok` looked
        // equivalent and is not: it treats a 401 as success, which is the exact
        // case where the token is still live and the user believes otherwise.
        done(!(res.status >= 200 && res.status < 300));
      }).catch(function () {
        done(true);
      });
    } catch (e) {
      done(true);
    }
  }

  ConcordSession.signOut = signOut;

  document.addEventListener('click', function (e) {
    var t = e.target;
    if (t && t.closest && t.closest('[data-signout]')) {
      e.preventDefault();
      signOut();
    }
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', paint);
  } else {
    paint();
  }
})();
