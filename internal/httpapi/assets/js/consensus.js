/*
 * The Consensus page (docs/specs/consensus-page-spec.md).
 *
 * The one rule that shapes this file: the TALLY IS NEVER COMPUTED HERE.
 *
 * There are two ratios and they have different denominators, because a stand-aside
 * is a reservation rather than opposition:
 *
 *   support  = consent / (consent + stand_aside + block)
 *   decisive = consent / (consent + block)
 *
 * 4 consent / 3 stand-aside / 0 block is support 0.57 and decisive 1.00, so a client
 * that shows one of them shows a bug, and a client that recomputes them can be
 * wrong about what consent means without anything looking broken. Both arrive in the
 * response's `tally` and are formatted here, never derived.
 *
 * The other rule: `position` is the only thing rendered from a position row.
 * `CastPosition` accepts no reason -- the column defaults to '' -- so any reason
 * shown would be empty by construction.
 */
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function token() {
    try { return window.localStorage.getItem('concord.token') || ''; }
    catch (e) { return ''; }
  }

  function headers() {
    var h = { 'Accept': 'application/json' };
    var t = token();
    if (t) h.Authorization = 'Bearer ' + t;
    return h;
  }

  function show(el, on) { if (el) el.hidden = !on; }
  function $(id) { return document.getElementById(id); }

  var root = $('consensus-root');
  if (!root) return;

  var callEl    = $('consensus-call');
  var errorEl   = $('consensus-error');
  var errTitle  = $('consensus-error-title');
  var errDetail = $('consensus-error-detail');
  var positionsEl = $('consensus-positions');
  var signedOutEl = $('consensus-signed-out');
  var objectionsEl = $('consensus-objections');

  var slug = root.getAttribute('data-project') || '';
  var callID = root.getAttribute('data-call') || '';

  /* Pending position, chosen by button or by key. Kept here so the two routes can
     never disagree about what is about to be submitted. */
  var pending = '';

  // --- rendering ------------------------------------------------------------

  // A ratio to a percentage, and only a percentage. No threshold verdict is drawn:
  // whether the call passes is the server's judgement (EvaluateConsensus), and a
  // client that re-derived it would disagree with the stored `result` the moment
  // the charter changed.
  function pct(v) {
    if (v == null) return '—';
    return Math.round(v * 100) + '%';
  }

  function renderMeta(call) {
    var bits = [];
    if (call.status) bits.push(esc(call.status));
    if (call.result) bits.push('result: ' + esc(call.result));
    if (call.outcome) bits.push('outcome: ' + esc(call.outcome));
    var opened = call.opens_at ? new Date(call.opens_at * 1000).toISOString().slice(0, 10) : '';
    if (opened) bits.push('opened ' + opened);
    $('consensus-meta').textContent = bits.join(' · ');
  }

  function renderTally(t, visible) {
    // Quorum first: it is the question "has this call enough people in it yet",
    // and it is answered before any ratio means anything. §6.6 keeps it visible even
    // while the tally is hidden, so it is rendered on both paths.
    var q = $('consensus-quorum');
    q.textContent = t.participants + ' of ' + t.quorum_required +
      ' needed to reach quorum (' + t.eligible + ' eligible)';

    var box = $('consensus-tally');
    if (!visible) {
      // §6.6: "Running counts are hidden until the call closes, to prevent
      // bandwagoning." The decision is the SERVER's -- `tally_visible` -- because a
      // rule enforced here would be bypassed with devtools. Say WHY, so the absence
      // reads as a decision rather than a missing feature.
      show(box, false);
      var note = $('consensus-tally-hidden');
      note.textContent = 'Running counts are hidden until this call closes, so nobody ' +
        'is influenced by how others have voted.';
      show(note, true);
      return;
    }

    show($('consensus-tally-hidden'), false);
    show(box, true);
    $('tally-support').textContent = pct(t.support_ratio);
    $('tally-support-detail').textContent =
      'consent ÷ (consent + reservations + blocks) · needs ' + pct(t.support_required);

    $('tally-decisive').textContent = pct(t.decisive_ratio);
    $('tally-decisive-detail').textContent =
      'consent ÷ (consent + blocks) · needs ' + pct(t.decisive_required);
  }

  function renderObjections(list, tallyVisible) {
    // Objections are stances on the public record while a call is open, and §6.6
    // hides the running picture. They appear when the call closes.
    if (!tallyVisible) { show(objectionsEl, false); return; }
    if (!list || !list.length) { show(objectionsEl, false); return; }
    var ul = $('objection-list');
    ul.textContent = '';
    list.forEach(function (o) {
      var li = document.createElement('li');
      li.className = 'objection' + (o.status && o.status !== 'open' ? ' objection-resolved' : '');
      // textContent throughout: these are user-authored strings from signed-in
      // accounts, which are more hostile than documents, not less.
      var head = document.createElement('p');
      head.className = 'objection-principle';
      head.textContent = o.principle || '';
      li.appendChild(head);
      if (o.violation) {
        var v = document.createElement('p');
        v.className = 'objection-violation';
        v.textContent = o.violation;
        li.appendChild(v);
      }
      if (o.remedy) {
        var r = document.createElement('p');
        r.className = 'objection-remedy';
        r.textContent = o.remedy;
        li.appendChild(r);
      }
      if (o.status) {
        var s = document.createElement('p');
        s.className = 'objection-status';
        s.textContent = o.status;
        li.appendChild(s);
      }
      ul.appendChild(li);
    });
    show(objectionsEl, true);
  }

  function render(data) {
    var call = data.call || {};
    $('consensus-question').textContent =
      call.question || call.summary || 'Untitled call';
    renderMeta(call);
    // `tally_visible` is the server's answer, not this page's.
    renderTally(data.tally || {}, data.tally_visible === true);
    renderObjections(data.objections, data.tally_visible === true);
    show(callEl, true);

    // Signed out: read-only, with a prompt where each write control would be.
    if (!token()) {
      show(positionsEl, false);
      show(signedOutEl, true);
    } else {
      show(positionsEl, true);
      show(signedOutEl, false);
    }
  }

  function fail(message, detail) {
    errTitle.textContent = message;
    errDetail.textContent = detail || '';
    show(errorEl, true);
    show(callEl, false);
  }

  // The skeleton is cleared on EVERY path, including the failure path. A spinner
  // left behind by a failed read looks like a page that is still loading.
  //
  // It removes only what the inline skeleton script injected. The first version
  // emptied the root's textContent, which took the article, the four position
  // buttons and the error region with it: the render then wrote into detached
  // nodes, so the page showed nothing at all and no console error said why. The
  // skeleton is `.skeleton-row` cards, and `aria-busy` goes through the library's
  // own `done` so the attribute is set the same way as on every other page.
  function done() {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(root);
    else root.setAttribute('aria-busy', 'false');
    var rows = root.querySelectorAll('.skeleton-row');
    for (var i = 0; i < rows.length; i++) rows[i].remove();
  }

  // --- reading --------------------------------------------------------------

  function load() {
    show(errorEl, false);
    root.setAttribute('aria-busy', 'true');

    var url = '/api/v1/projects/' + encodeURIComponent(slug) + '/consensus/' +
      encodeURIComponent(callID);
    fetch(url, { headers: headers() })
      .then(function (r) {
        return r.text().then(function (txt) {
          var data = {};
          try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = {}; }
          if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
          return data;
        });
      })
      .then(function (data) {
        done();
        render(data);
      })
      .catch(function (err) {
        done();
        fail('Could not load this call.', err.message);
      });
  }

  // --- writing --------------------------------------------------------------

  function choose(pos) {
    pending = pos;
    var btns = document.querySelectorAll('.position-btn');
    for (var i = 0; i < btns.length; i++) {
      // aria-pressed, not just a class: the button is the only route to this state
      // for anyone not using the keyboard.
      btns[i].setAttribute('aria-pressed', btns[i].getAttribute('data-position') === pos ? 'true' : 'false');
    }
  }

  function submit() {
    if (!pending) return;
    var url = '/api/v1/projects/' + encodeURIComponent(slug) + '/consensus/' +
      encodeURIComponent(callID) + '/position';
    fetch(url, {
      method: 'POST',
      headers: (function () {
        var h = { 'Content-Type': 'application/json' };
        var t = token();
        if (t) h.Authorization = 'Bearer ' + t;
        return h;
      })(),
      body: JSON.stringify({ position: pending })
    }).then(function (r) {
      return r.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = {}; }
        if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
        return data;
      });
    }).then(function () {
      // Re-read rather than patching the tally locally: the counts that decide
      // anything are the server's, and re-reading proves the position landed.
      var saved = $('consensus-saved');
      saved.textContent = 'Recorded: ' + pending.replace('_', ' ') + '.';
      show(saved, true);
      load();
    }).catch(function (err) {
      show($('consensus-saved'), false);
      fail('Could not record your position.', err.message);
    });
  }

  // --- wiring ---------------------------------------------------------------

  var btns = document.querySelectorAll('.position-btn');
  for (var i = 0; i < btns.length; i++) {
    btns[i].addEventListener('click', function (ev) {
      choose(ev.currentTarget.getAttribute('data-position'));
    });
    btns[i].addEventListener('keydown', function (ev) {
      if (ev.key !== 'ArrowRight' && ev.key !== 'ArrowLeft') return;
      ev.preventDefault();
      var idx = Array.prototype.indexOf.call(btns, ev.currentTarget);
      var next = (idx + (ev.key === 'ArrowRight' ? 1 : btns.length - 1)) % btns.length;
      btns[next].focus();
      choose(btns[next].getAttribute('data-position'));
    });
  }

  // Every shortcut is an accelerator; the buttons above are the route.
  var KEYS = { 'c': 'consent', 'a': 'abstain', 's': 'stand_aside', 'b': 'block' };
  document.addEventListener('keydown', function (ev) {
    if (token && positionsEl && positionsEl.hidden) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    var tag = (ev.target && ev.target.tagName) || '';
    if (tag === 'INPUT' || tag === 'TEXTAREA') return;
    if (ev.key === 'Enter' && pending) { ev.preventDefault(); submit(); return; }
    var pos = KEYS[ev.key.toLowerCase()];
    if (pos) { ev.preventDefault(); choose(pos); }
  });

  var retry = $('consensus-retry');
  if (retry) retry.addEventListener('click', function () { load(); });

  load();
})();