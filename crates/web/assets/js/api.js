// One way to call the API, and one way to fail.
//
// Before this, every file built its own headers (Content-Type, and the token read from localStorage under three different key literal sets),
// parsed its own body, and invented its own sentence for a 401 — "Sign in first — votes and filings need an account.", "sign in to see your
// notifications", "Sign in first — this needs an account.", and, in two files, a bare `throw { auth: true }` that the caller had to know how
// to read. Four sentences for one situation is four chances to tell a reader something untrue about what just happened.
//
// The model here is deliberately small:
//
//   * a request resolves with the parsed body when the server accepts it;
//   * it rejects with an `ApiError` otherwise, and the error carries `status`, the server's OWN sentence when there is one, and `auth` — the
//     one thing a caller must branch on, because "sign in" is the only refusal a page can fix by showing a form;
//   * a network failure is `status: 0`, never a 500: the server did not answer, and saying it failed would be inventing a response.
//
// The 401 sentence is fixed HERE rather than per call site. A page that needs a different wording can read `error.auth` and write its own;
// what it must not do is invent a different RULE, and it cannot, because the rule is the `auth` flag.
(function () {
  'use strict';

  function ApiError(status, message, payload) {
    this.name = 'ApiError';
    this.status = status;
    this.auth = status === 401;
    this.message = message;
    this.payload = payload || null;
  }
  ApiError.prototype = Object.create(Error.prototype);
  ApiError.prototype.constructor = ApiError;

  // The auth headers, from `session.js` — the one owner of the token. A caller with no session sends none, and the server decides.
  function headers(json) {
    var out = {};
    if (json) out['Content-Type'] = 'application/json';
    var session = window.ConcordSession && window.ConcordSession.instance();
    if (session && typeof session.authHeaders === 'function') {
      var auth = session.authHeaders();
      for (var key in auth) {
        if (Object.prototype.hasOwnProperty.call(auth, key)) out[key] = auth[key];
      }
    }
    return out;
  }

  // The server's sentence, when the server actually sent one.
  //
  // This is a fix, not a nicety: `payload.error` is not always a string — some routes answer `{"error": {"code": …}}` — and concatenating an
  // object into a message produced the literal text "[object Object]" in a reader's face. A string in `error`, a string in `error.error`, or a
  // string in `error.error`, in `error.message` (the shape `/api/v1` actually uses for an unmounted route: `{"error":{"code":"not_found",
  // "message":"No such API route."}}`), or a string in `message`; otherwise the status, which is unhelpful but true.
  function sentence(payload, status) {
    if (payload) {
      if (typeof payload.error === 'string' && payload.error) return payload.error;
      if (payload.error && typeof payload.error.error === 'string' && payload.error.error) return payload.error.error;
      if (payload.error && typeof payload.error.message === 'string' && payload.error.message) return payload.error.message;
      if (typeof payload.message === 'string' && payload.message) return payload.message;
    }
    return 'HTTP ' + status;
  }

  function request(method, path, body, options) {
    var opts = options || {};
    var init = { method: method, headers: headers(body !== undefined && body !== null) };
    if (body !== undefined && body !== null) init.body = JSON.stringify(body);
    if (opts.signal) init.signal = opts.signal;

    return fetch(path, init).then(function (response) {
      return response.text().then(function (text) {
        var payload = null;
        if (text) {
          try { payload = JSON.parse(text); } catch (e) { payload = null; }
        }
        if (response.status === 401) {
          throw new ApiError(401, 'Sign in first — this needs an account.', payload);
        }
        if (!response.ok) {
          throw new ApiError(response.status, sentence(payload, response.status), payload);
        }
        return payload;
      });
    }, function () {
      // No status, because there was no response. `status: 0` is the honest value: a caller that treats it as a server failure will tell the
      // reader the server is broken when in fact the network is.
      throw new ApiError(0, 'Could not reach the server.');
    });
  }

  window.ConcordApi = {
    ApiError: ApiError,
    headers: headers,
    request: request,
    get: function (path, options) { return request('GET', path, null, options); },
    post: function (path, body, options) { return request('POST', path, body === undefined ? null : body, options); },
    put: function (path, body, options) { return request('PUT', path, body === undefined ? null : body, options); },
    del: function (path, options) { return request('DELETE', path, null, options); }
  };
})();
