// Concord's cookie notice, its consequences, and guest identities.
//
// Loaded from base.html on every page, BEFORE the page's own script, and it is the
// only client-side code that knows about either of the two rules:
//
//   1. Interactions are refused until the notice is accepted.
//   2. A visitor who is not signed in can still act, as a guest, until the instance
//      grows past its limit -- at which point the server says so and this file says
//      the same thing in the same words.
//
// Both are enforced by the SERVER (internal/httpapi/guest_handlers.go). What
// happens here is a courtesy, and the ordering matters: this file must never be the
// thing that keeps a rule true. It exists so a visitor is told why a button did
// nothing, instead of watching a request come back refused.
//
// Written as one wrapper around window.fetch rather than a helper each page must
// remember to call: there are ~25 page scripts and roughly 60 mutating endpoints,
// and a rule that has to be applied 60 times is a rule that is wrong 60 times a
// week. The wrapper sees every request the product makes by construction.
(function () {
  'use strict';

  var originalFetch = window.fetch.bind(window);
  var guestClaim = null; // in-flight POST /api/v1/guest, so two actions mint one identity
  var pendingAccept = null; // in-flight POST /api/v1/consent, so a second click does not race it

  var CONSENT_REQUIRED = 'cookies must be accepted before interacting with this site';
  var GUEST_REFUSED = 'this instance has grown past its guest limit; create an account to take part';

  function bodyConsent() {
    return document.body ? document.body.getAttribute('data-consent') : null;
  }

  function consented() {
    return bodyConsent() === 'accepted';
  }

  function isMutating(method) {
    var m = (method || 'GET').toUpperCase();
    return m !== 'GET' && m !== 'HEAD' && m !== 'OPTIONS';
  }

  // tell shows a sentence the visitor can act on, in a bar that does not depend on
  // the page having an error slot of its own.
  function tell(message, options) {
    var el = document.getElementById('concord-message');
    if (!el) {
      el = document.createElement('div');
      el.id = 'concord-message';
      el.className = 'concord-message';
      el.setAttribute('role', 'status');
      document.body.insertBefore(el, document.body.firstChild);
    }
    el.textContent = message;
    el.hidden = false;
    if (options && options.action) {
      var link = document.createElement('a');
      link.href = options.action.href;
      link.textContent = options.action.text;
      link.className = 'concord-message-action';
      el.appendChild(link);
    }
  }

  // refused builds the response the server would have sent, so a caller that reads
  // .status and .json() sees the same shape it sees from the network and does not
  // need a second error path.
  function refused(what, action) {
    tell(what, action ? { action: action } : null);
    return Promise.resolve(new Response(
      JSON.stringify({ error: what, consent: 'required', code: 'consent_required' }),
      { status: 403, headers: { 'Content-Type': 'application/json' } }
    ));
  }

  // claimGuest mints a guest identity once, stores it exactly where an account's
  // token goes, and returns it.
  //
  // Stored like a token because it IS one: a bearer credential obtained without a
  // password. Keeping it anywhere else (a cookie the server trusts) would make a
  // cookie authorize writes, which is the property this codebase refuses to give up
  // -- it is what stops any other site from making a visitor cast a stance.
  function claimGuest() {
    if (guestClaim) return guestClaim;
    guestClaim = originalFetch('/api/v1/guest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin'
    }).then(function (res) {
      if (!res.ok) {
        return res.json().catch(function () { return {}; }).then(function (body) {
          if (body && body.code === 'guest_limit_reached') {
            tell(GUEST_REFUSED, { href: '/login', text: 'Create an account' });
          } else {
            tell((body && body.error) || 'Could not start a guest session.');
          }
          return null;
        });
      }
      return res.json().then(function (body) {
        var session = window.ConcordSession ? window.ConcordSession.instance() : null;
        if (session) {
          // The session's own write path, so the key names live in one file. It
          // reports whether the browser KEPT it; either way the identity is this
          // page's, and a visitor whose storage is blocked can still act until
          // the tab closes -- which is what they are told if it matters.
          if (!session.store(body.token, body.user)) {
            tell('Your browser is not storing this session, so what you do now is ' +
                 'kept until you close the tab. Creating an account keeps it.');
          }
        }
        document.dispatchEvent(new CustomEvent('concord:guest', { detail: body.user }));
        return body.token;
      });
    }).finally(function () { guestClaim = null; });
    return guestClaim;
  }

  window.ConcordConsent = {
    accepted: consented,
    claimGuest: claimGuest,
    tell: tell
  };

  // The notice's Accept button.
  function wireNotice() {
    var button = document.getElementById('cookie-accept');
    if (!button) return;
    button.addEventListener('click', function () {
      button.disabled = true;
      pendingAccept = originalFetch('/api/v1/consent', {
        method: 'POST',
        headers: window.ConcordSession
          ? Object.assign({ 'Content-Type': 'application/json' },
              window.ConcordSession.instance().authHeaders())
          : { 'Content-Type': 'application/json' },
        credentials: 'same-origin'
      }).then(function (res) {
        if (!res.ok) throw new Error('the server refused the acceptance');
        document.body.setAttribute('data-consent', 'accepted');
        var notice = document.getElementById('cookie-notice');
        if (notice) notice.remove();
        document.dispatchEvent(new CustomEvent('concord:consent'));
      }).catch(function () {
        button.disabled = false;
        tell('Could not record your choice. Nothing was sent, and interactions stay off.');
      }).finally(function () {
        pendingAccept = null;
      });
    });
  }

  // The wrapper: refuse locally what the server would refuse, and mint a guest
  // identity for a visitor who has none.
  window.fetch = function (input, init) {
    var method = (init && init.method) || (input && input.method) || 'GET';
    var url = typeof input === 'string' ? input : (input && input.url) || '';
    // Routes that must not go through the guest path, for three different reasons:
    //
    //   /api/v1/consent  accepting is how the visitor gets out of the notice, so
    //                    gating it would deadlock the wrapper;
    //   /api/v1/guest    minting is how a visitor gets an identity;
    //   /api/v1/auth/    registering or logging in is how a visitor STOPS being a
    //                    guest. Treating it as an ordinary interaction minted a
    //                    fresh guest identity and then registered INTO it, so a
    //                    visitor's earlier work stayed on the identity they had been
    //                    using while the account was created somewhere else. The
    //                    server exempts these routes from consent for the same
    //                    reason, and this list has to match it.
    var isGateRoute = url.indexOf('/api/v1/consent') >= 0 ||
                      url.indexOf('/api/v1/guest') >= 0 ||
                      url.indexOf('/api/v1/auth/') >= 0;

    if (!isMutating(method) || isGateRoute) {
      return originalFetch(input, init);
    }
    if (!consented()) {
      // A visitor who has just clicked Accept and immediately acted would otherwise
      // be refused by this file for a request the server would have allowed: the
      // acceptance is in flight, not absent. Waiting for it is the difference
      // between a courtesy and a lie about the visitor's own state.
      if (pendingAccept) {
        return pendingAccept.then(function () {
          return afterConsent(input, init);
        });
      }
      return refused(CONSENT_REQUIRED);
    }
    return afterConsent(input, init);
  };

  // afterConsent is the rest of the routing, reachable both directly (the notice was
  // already accepted) and after an in-flight acceptance settles.
  function afterConsent(input, init) {
    if (!consented()) {
      // The acceptance failed after all: say so rather than sending a request that
      // the server will refuse with a message the page cannot explain.
      return refused(CONSENT_REQUIRED);
    }
    var session = window.ConcordSession ? window.ConcordSession.instance() : null;
    if (session && !session.signedIn()) {
      // A visitor with no identity who has accepted the notice acts as a guest.
      return claimGuest().then(function (token) {
        if (!token) return refused(GUEST_REFUSED, { href: '/login', text: 'Create an account' });
        var headers = Object.assign({}, (init && init.headers) || {}, { Authorization: 'Bearer ' + token });
        return originalFetch(input, Object.assign({}, init, { headers: headers }));
      });
    }
    return originalFetch(input, init);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireNotice);
  } else {
    wireNotice();
  }
})();
