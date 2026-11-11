/*
 * The curation queue (docs/plans/curation-queue.md).
 *
 * One list over taxonomy proposals, strategic-weight proposals and moderation acts, read from
 * GET /api/v1/curation, with the vote posted back to the kind's own route. Nothing here decides
 * anything: consent is counted by the proposal's decider, and a review of an act only moves a
 * count.
 *
 * The queue opens on work-to-do -- pending, and nothing this reader has already voted on. That
 * is the donor's default (stash-box's Edits list) and it is the difference between a work list
 * and a wall.
 *
 * Rationales are prose typed by a person and are written with textContent, never parsed as
 * markup.
 */
/*
 * apiAuth: the session's Authorization header, for API reads.
 *
 * This app authenticates with a bearer token in localStorage, not a cookie, so a fetch with
 * `credentials: 'same-origin'` and nothing else arrives anonymous -- and the queue's vote
 * routes then answer 401 while the page renders a list that came back fine, because the READ
 * path accepts an anonymous caller and the WRITE path does not.
 */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}

(function () {
  'use strict';

  var listEl = document.getElementById('curation-list');
  if (!listEl) return;

  var kindEl = document.getElementById('curation-kind');
  var stateEl = document.getElementById('curation-state');
  var unvotedEl = document.getElementById('curation-unvoted');
  var qEl = document.getElementById('curation-q');
  var moreEl = document.getElementById('curation-more');
  var statusEl = document.getElementById('curation-status');

  var cursor = '';
  var shown = 0;

  function stateLabel(state) {
    return {
      pending: 'waiting for review', ratified: 'ratified', rejected: 'rejected',
      withdrawn: 'withdrawn', applied: 'applied', undone: 'undone'
    }[state] || state;
  }

  function hrefFor(item) {
    if (!item.project_slug) return item.kind === 'taxonomy' ? '/taxonomy' : '';
    if (item.kind === 'act') {
      return '/projects/' + encodeURIComponent(item.project_slug) + '/audit?action=' +
        encodeURIComponent(item.action);
    }
    if (item.kind === 'taxonomy') return '/taxonomy';
    return '/projects/' + encodeURIComponent(item.project_slug) + '/ranking';
  }

  /*
   * An act's summary is audit_log.detail, which is JSON. A page that prints the JSON is
   * unreadable, and a page that assumes it is prose shows `{"reason":` to the reader. Read the
   * reason out when there is one, fall back to the raw text when there is not.
   */
  function summaryText(item) {
    var text = item.summary || '';
    if (item.kind !== 'act') return text;
    try {
      var parsed = JSON.parse(text);
      if (parsed && typeof parsed === 'object' && typeof parsed.reason === 'string') {
        return parsed.reason;
      }
    } catch (e) { /* not JSON: show what is there */ }
    return text;
  }

  function say(msg) {
    if (!statusEl) return;
    statusEl.textContent = msg || '';
  }

  function card(item) {
    var li = document.createElement('li');
    li.className = 'curation-card';
    li.setAttribute('data-kind', item.kind);
    li.setAttribute('data-id', String(item.id));

    var head = document.createElement('p');
    head.className = 'curation-card-head';
    head.textContent = item.kind + ' \u00b7 ' + item.action + ' \u00b7 ' + stateLabel(item.state);
    li.appendChild(head);

    var href = hrefFor(item);
    if (href) {
      var a = document.createElement('a');
      a.className = 'curation-target';
      a.href = href;
      a.textContent = item.target || '(no target)';
      li.appendChild(a);
    } else {
      var t = document.createElement('span');
      t.className = 'curation-target';
      t.textContent = item.target || '(no target)';
      li.appendChild(t);
    }

    var why = document.createElement('p');
    why.className = 'curation-summary';
    why.textContent = summaryText(item);
    li.appendChild(why);

    var meta = document.createElement('p');
    meta.className = 'curation-meta';
    meta.textContent = item.yes + ' for \u00b7 ' + item.no + ' against' +
      (item.contested ? ' \u00b7 contested' : '') +
      (item.my_vote ? ' \u00b7 you voted ' + (item.my_vote > 0 ? 'yes' : 'no') : '');
    li.appendChild(meta);

    // A decided proposal has nothing left to consent to, and a page that keeps the button
    // there is offering an action the server will refuse.
    if (item.state !== 'withdrawn' && item.state !== 'rejected') {
      if (item.can_vote) {
        li.appendChild(voteRow(item));
      } else {
        li.appendChild(noVoteNote());
      }
    }
    // Undo is the other half of the review: a contest records disagreement, and reversing is a
    // deliberate act with a reason attached, which is why it asks for one instead of firing.
    if (canUndo(item)) {
      li.appendChild(undoRow(item));
    }
    return li;
  }

  /*
   * Why there is no button. The server says who may vote on a row (can_vote); the page's job is
   * to say which of the two reasons it is, because "sign in" and "your account has no standing
   * here" are different sentences and a reader who is signed in should not be told to sign in.
   */
  function noVoteNote() {
    var signedIn = false;
    try {
      signedIn = !!(window.ConcordSession && window.ConcordSession.instance().user);
    } catch (e) { /* no session module: treat as anonymous */ }
    var p = document.createElement('p');
    p.className = 'curation-meta';
    p.textContent = signedIn
      ? 'Your account has no standing to vote on this one.'
      : 'Sign in to vote on this one.';
    return p;
  }

  function voteRow(item) {
    var wrap = document.createElement('p');
    wrap.className = 'curation-votes';

    var yes = document.createElement('button');
    yes.type = 'button';
    yes.className = 'btn btn-secondary';
    yes.textContent = item.kind === 'act' ? 'Endorse' : 'Consent';
    yes.addEventListener('click', function () { vote(item, 'yes', yes); });
    wrap.appendChild(yes);

    // Only acts take a no vote: a proposal ratifies on a count of consents, and offering an
    // "oppose" the server refuses is a button that teaches a lie.
    if (item.kind === 'act') {
      var no = document.createElement('button');
      no.type = 'button';
      no.className = 'btn btn-secondary';
      no.textContent = 'Contest';
      no.addEventListener('click', function () { vote(item, 'no', no); });
      wrap.appendChild(no);
    }
    return wrap;
  }

  /*
   * An undo exists only for an arena removal that is still applied and belongs to a project the
   * caller can name in a URL. A restore act has nothing to undo; an undone removal has already
   * been reversed; and an act with no project_slug is instance-scoped, where the restore route
   * has no path to live under.
   */
  function canUndo(item) {
    return item.kind === 'act' && item.action === 'arena.remove' &&
      item.state === 'applied' && !!item.project_slug;
  }

  function undoRow(item) {
    var wrap = document.createElement('div');
    wrap.className = 'curation-undo';

    var open = document.createElement('button');
    open.type = 'button';
    open.className = 'btn btn-secondary';
    open.textContent = 'Undo this removal';
    wrap.appendChild(open);

    var form = document.createElement('div');
    form.className = 'curation-undo-form';
    form.hidden = true;

    var why = document.createElement('input');
    why.type = 'text';
    why.placeholder = 'Why? The log records this.';
    why.setAttribute('aria-label', 'Reason for reversing this removal');
    form.appendChild(why);

    var confirmBtn = document.createElement('button');
    confirmBtn.type = 'button';
    confirmBtn.className = 'btn btn-secondary';
    confirmBtn.textContent = 'Reverse it';
    form.appendChild(confirmBtn);

    var cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn btn-secondary';
    cancel.textContent = 'Cancel';
    form.appendChild(cancel);

    open.addEventListener('click', function () {
      form.hidden = false;
      open.hidden = true;
      why.focus();
    });
    cancel.addEventListener('click', function () {
      form.hidden = true;
      open.hidden = false;
      why.value = '';
    });
    confirmBtn.addEventListener('click', function () {
      var reason = why.value.trim();
      // The route refuses an empty reason with a 400 and a sentence; asking here saves a
      // round trip and says the same thing where the reader is looking.
      if (!reason) {
        say('A reversal needs a reason: the log keeps it.');
        why.focus();
        return;
      }
      confirmBtn.disabled = true;
      say('Reversing\u2026');
      fetch('/api/v1/projects/' + encodeURIComponent(item.project_slug) + '/moderation/arena/restore', {
        method: 'POST',
        headers: apiAuth({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify({ undoes: item.id, reason: reason, confirm: true })
      }).then(function (res) {
        return res.json().catch(function () { return {}; }).then(function (data) {
          if (!res.ok) {
            say((data && data.error) || 'Could not reverse that.');
            confirmBtn.disabled = false;
            return;
          }
          // The message is passed INTO the reload: load() sets the status line itself, and a
          // "Reversed." written before it is replaced by "1 shown." a moment later -- which is
          // what the first version of this handler did, and what the browser test caught.
          load(false, 'Reversed. The restore act is in the log with that reason on it.');
        });
      }).catch(function () {
        say('Could not reach the server.');
        confirmBtn.disabled = false;
      });
    });

    wrap.appendChild(form);
    return wrap;
  }

  function vote(item, choice, btn) {
    if (btn) { btn.disabled = true; }
    say('Recording\u2026');
    fetch('/api/v1/curation/' + encodeURIComponent(item.kind) + '/' + item.id + '/vote', {
      method: 'POST',
      headers: apiAuth({ 'Content-Type': 'application/json' }),
      credentials: 'same-origin',
      body: JSON.stringify({ vote: choice, rationale: '' })
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (res.status !== 200 && res.status !== 202) {
          say((data && data.error) || 'Could not record that.');
          if (btn) { btn.disabled = false; }
          return;
        }
        item.my_vote = choice === 'yes' ? 1 : -1;
        item.yes = data.yes || 0;
        item.no = data.no || 0;
        var msg = 'Recorded.';
        if (res.status === 202) {
          msg = 'Recorded. Not decided yet \u2014 ' + item.yes + ' consent(s) so far.';
        } else if (item.kind !== 'act') {
          item.state = 'ratified';
        }
        // Re-read rather than patch the row in place: the default view is "only what I have
        // not voted on", so a row that stays after a successful vote makes the work list lie
        // about how much work is left. The message is carried into the reload for the same
        // reason as the Undo handler's -- load() owns the status line.
        load(false, msg);
      });
    }).catch(function () {
      say('Could not reach the server.');
      if (btn) { btn.disabled = false; }
    });
  }

  function load(append, note) {
    var params = new URLSearchParams();
    if (kindEl && kindEl.value) params.set('kind', kindEl.value);
    if (stateEl && stateEl.value) params.set('state', stateEl.value);
    if (unvotedEl && unvotedEl.checked) params.set('unvoted', '1');
    if (qEl && qEl.value.trim()) params.set('q', qEl.value.trim());
    if (append && cursor) params.set('cursor', cursor);
    params.set('limit', '20');

    say('Loading\u2026');
    fetch('/api/v1/curation?' + params.toString(), {
      headers: apiAuth({ Accept: 'application/json' }),
      credentials: 'same-origin'
    }).then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    }).then(function (page) {
      if (!append) { listEl.innerHTML = ''; shown = 0; }
      (page.items || []).forEach(function (item) {
        listEl.appendChild(card(item));
        shown++;
      });
      cursor = page.next_cursor || '';
      if (moreEl) moreEl.hidden = !cursor;
      if (note) {
        say(note);
      } else if (!shown) {
        say('Nothing is waiting. That is either good news or the filters are too narrow.');
      } else {
        say(shown + ' shown.');
      }
    }).catch(function (err) {
      say('Could not load the queue: ' + err.message);
    });
  }

  [kindEl, stateEl, unvotedEl].forEach(function (el) {
    if (el) el.addEventListener('change', function () { load(false); });
  });
  if (qEl) {
    var timer = null;
    qEl.addEventListener('input', function () {
      clearTimeout(timer);
      timer = setTimeout(function () { load(false); }, 300);
    });
  }
  if (moreEl) moreEl.addEventListener('click', function () { load(true); });

  // The first read waits for the document to finish: base.html loads session.js AFTER the
  // page's own script, so at parse time `window.ConcordSession` does not exist yet and a list
  // fetched now arrives anonymous -- which is how this page came to tell a signed-in reviewer
  // to sign in. A SYNCHRONOUS script at the end of <body> has run by DOMContentLoaded, so that
  // is the first moment the token is readable.
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { load(false); });
  } else {
    load(false);
  }
})();
