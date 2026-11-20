#!/usr/bin/env node
// The cookie's page half, tested as behaviour rather than as a grep. Loading the SHIPPED session.js in a sandbox with a fake
// `localStorage` and document answers the only questions that matter: does a reload still see a signed-in browser, does it send
// no Authorization header, and does any code path put the token back into storage where a script can read it?
//
// A grep would catch today's literal and miss a renamed key. This executes the file the browser executes, so a regression in
// `store`, `refresh`, `signedIn` or the constructor reddens the gate even if the spelling changes.
'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const path = process.argv[2] || 'crates/web/assets/js/session.js';
const source = fs.readFileSync(path, 'utf8');
const stored = Object.create(null);
const localStorage = {
  getItem(k) { return Object.prototype.hasOwnProperty.call(stored, k) ? stored[k] : null; },
  setItem(k, v) { stored[k] = String(v); },
  removeItem(k) { delete stored[k]; },
};
const document = {
  readyState: 'complete',
  addEventListener() {},
  querySelector() { return null; },
};
// A guest has no HttpOnly cookie, so its bearer lives in sessionStorage for the life of the tab.
// The fake browser must provide that storage or the constructor's catch turns every reload into a guest.
const ephemeral = Object.create(null);
const sessionStorage = {
  getItem(k) { return Object.prototype.hasOwnProperty.call(ephemeral, k) ? ephemeral[k] : null; },
  setItem(k, v) { ephemeral[k] = String(v); },
  removeItem(k) { delete ephemeral[k]; },
};
const sandbox = { console, document, localStorage, sessionStorage };
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(source, sandbox, { filename: path });

const Session = sandbox.ConcordSession;
assert.strictEqual(typeof Session, 'function', 'session.js did not export ConcordSession');

// A browser reloading after a login has a user record and an HttpOnly cookie, but no readable token. The page must still say
// "signed in" and must not manufacture a bearer header from nothing: the browser attaches the cookie itself.
stored['concord.token'] = 'token-left-by-an-older-build';
stored['concord.user'] = JSON.stringify({ username: 'alice', display_name: 'Alice' });
const reloaded = new Session();
assert.ok(!('concord.token' in stored), 'a stale localStorage token was not removed on load');
assert.strictEqual(reloaded.signedIn(), true, 'a reloaded page with a user record was called signed out');
assert.strictEqual(Object.keys(reloaded.authHeaders()).length, 0, 'a reloaded page sent an Authorization header without a token');

// Logging in keeps the token for THIS page's requests, but never persists it. The user record is the only durable key.
reloaded.store('fresh-token', { username: 'bob' });
assert.ok(!('concord.token' in stored), 'store() persisted the token to localStorage');
assert.strictEqual(reloaded.authHeaders().Authorization, 'Bearer fresh-token', 'the in-memory token was lost');
assert.strictEqual(JSON.parse(stored['concord.user']).username, 'bob', 'the user record was not stored');

// Signing out clears what the page owns; the server revoke is a separate request and is not faked here.
reloaded.clear();
assert.ok(!('concord.user' in stored), 'clear() left the user record behind');
assert.strictEqual(reloaded.signedIn(), false, 'clear() still reports signed in');

console.log('session storage: PASS — no token in localStorage, reload stays signed in, no bearer header without a token');
