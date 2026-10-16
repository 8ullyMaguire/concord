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
  var closeEl = $('consensus-close');
  var closeSubmit = $('consensus-close-submit');
  var closeErr = $('consensus-close-error');
  var closeDone = $('consensus-close-done');
  var overrideEl = $('consensus-override');
  var overrideNoneEl = $('consensus-override-none');
  var overrideOpenBtn = $('consensus-override-open');
  var overrideLiveEl = $('consensus-override-live');
  var overrideProgress = $('consensus-override-progress');
  var overridePositionsEl = $('consensus-override-positions');
  var overrideNote = $('consensus-override-note');
  var overrideCloseBtn = $('consensus-override-close-submit');
  var overrideResult = $('consensus-override-result');
  var overrideErr = $('consensus-override-error');
  var overrideDone = $('consensus-override-done');

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

  // §6.6's objections can be resolved, and until now nothing in the browser could:
  // the route existed with a handler, a role check and a store call, and no reader.
  //
  // Three things this control deliberately does NOT do.
  //
  //   - It does not decide who may resolve one. The role is the server's fact; the
  //     control is offered to any signed-in reader and the server's refusal is what
  //     the page shows. A client-side role check is a claim the page cannot check.
  //   - It does not offer "open". Open is the column's DEFAULT, not an outcome --
  //     the store refuses it, so offering it would be a button that always fails.
  //   - It does not resolve anything locally. It re-reads, so the status shown is
  //     the status stored.
  var RESOLUTIONS = [
    ['resolved', 'resolved \u2014 the concern was addressed'],
    ['overridden', 'overridden \u2014 considered, and set aside'],
    ['vetoed', 'vetoed \u2014 a governance veto'],
    ['withdrawn', 'withdrawn \u2014 the objector dropped it']
  ];

  // Whether the signed-in reader is the account that raised this objection.
  //
  // This is the ONE thing this page checks for itself, and it can: it is a fact about
  // the reader's own session, not a claim about standing. The server enforces the
  // rule either way; what the check buys is that the objector is shown the act that
  // is theirs (withdrawing) instead of a control the server would refuse them.
  //
  // An unreadable session is "not mine", so the default is the control the server
  // decides on.
  function mine(o) {
    try {
      var raw = window.localStorage.getItem('concord.user');
      var me = raw ? JSON.parse(raw) : null;
      return !!me && !!me.id && Number(me.id) === Number(o.user_id);
    } catch (e) {
      return false;
    }
  }

  // The objector's own act: withdraw the objection. One status, and it is not a
  // choice -- a withdrawal is the only outcome the person who raised a block may
  // record, and the reason is required because the store records a resolution, not a
  // flag.
  //
  // It replaces the four-status control rather than joining it: an objector cannot
  // resolve their own objection (the server refuses that), so offering both would be
  // offering a button that always fails beside one that works.
  function withdrawControls(o) {
    var wrap = document.createElement('div');
    wrap.className = 'objection-resolve objection-withdraw';

    var reasonId = 'objection-withdraw-reason-' + o.id;
    var label = document.createElement('label');
    label.className = 'field-label';
    label.setAttribute('for', reasonId);
    label.textContent = 'Withdraw this objection';

    var reason = document.createElement('input');
    reason.type = 'text';
    reason.id = reasonId;
    reason.className = 'input';
    reason.setAttribute('data-objection-withdraw-reason', '');
    reason.placeholder = 'why you are dropping it (recorded with the decision)';

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn';
    btn.textContent = 'Withdraw my objection';
    btn.setAttribute('data-objection-withdraw', String(o.id));

    var err = document.createElement('p');
    err.className = 'objection-resolve-error';
    err.setAttribute('role', 'alert');
    err.setAttribute('data-objection-withdraw-error', String(o.id));
    err.hidden = true;

    btn.addEventListener('click', function () {
      sendOutcome(o, 'withdrawn', reason.value, btn, err);
    });

    wrap.appendChild(label);
    wrap.appendChild(reason);
    wrap.appendChild(btn);
    wrap.appendChild(err);
    return wrap;
  }

  // sendOutcome records one outcome. Both controls go through here, so the request
  // shape (and the server's refusal, reported verbatim) cannot differ between them.
  function sendOutcome(o, status, why, btn, err) {
    btn.disabled = true;
    err.hidden = true;
    var url = '/api/v1/projects/' + encodeURIComponent(slug) + '/consensus/objections/' +
      encodeURIComponent(String(o.id)) + '/resolve';
    fetch(url, {
      method: 'PUT',
      headers: (function () {
        var h = { 'Content-Type': 'application/json' };
        var t = token();
        if (t) h.Authorization = 'Bearer ' + t;
        return h;
      })(),
      body: JSON.stringify({ status: status, resolution: why })
    }).then(function (r) {
      return r.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = {}; }
        // The server's own sentence, including the store's list of the statuses it
        // accepts and -- since the standing refusals stopped being bare -- the role
        // this action needed. Rewriting it here would tell the reader what this page
        // thinks the rule is.
        if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
        return data;
      });
    }).then(function () {
      load();
    }).catch(function (e) {
      btn.disabled = false;
      err.textContent = e.message;
      err.hidden = false;
    });
  }

  function resolveControls(o) {
    var wrap = document.createElement('div');
    wrap.className = 'objection-resolve';

    var selId = 'objection-status-' + o.id;
    var label = document.createElement('label');
    label.className = 'field-label';
    label.setAttribute('for', selId);
    label.textContent = 'Resolve this objection';

    var sel = document.createElement('select');
    sel.id = selId;
    sel.setAttribute('data-objection-status', '');
    RESOLUTIONS.forEach(function (pair) {
      var opt = document.createElement('option');
      opt.value = pair[0];
      opt.textContent = pair[1];
      sel.appendChild(opt);
    });

    var reasonId = 'objection-reason-' + o.id;
    var reasonLabel = document.createElement('label');
    reasonLabel.className = 'field-label';
    reasonLabel.setAttribute('for', reasonId);
    reasonLabel.textContent = 'Why (recorded with the decision)';
    var reason = document.createElement('input');
    reason.type = 'text';
    reason.id = reasonId;
    reason.className = 'input';
    reason.setAttribute('data-objection-reason', '');
    reason.placeholder = 'e.g. the export now resumes from the last checkpoint';

    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn';
    btn.textContent = 'Record the resolution';
    btn.setAttribute('data-objection-resolve', String(o.id));

    var err = document.createElement('p');
    err.className = 'objection-resolve-error';
    err.setAttribute('role', 'alert');
    err.setAttribute('data-objection-error', String(o.id));
    err.hidden = true;

    btn.addEventListener('click', function () {
      sendOutcome(o, sel.value, reason.value, btn, err);
    });

    wrap.appendChild(label);
    wrap.appendChild(sel);
    wrap.appendChild(reasonLabel);
    wrap.appendChild(reason);
    wrap.appendChild(btn);
    wrap.appendChild(err);
    return wrap;
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
      // Why it is no longer open: a maintainer's reason, or -- when the call was
      // accepted over the block -- §6.6's answer to the remedy. Labelled, because an
      // unexplained paragraph under a resolution would read as another objection.
      if (o.resolution) {
        var res = document.createElement('p');
        res.className = 'objection-resolution';
        res.textContent = 'resolution: ' + o.resolution;
        li.appendChild(res);
      }
      // Signed out: the objection is readable and the control is not offered --
      // there is nobody to attribute a resolution to.
      if (token() && (!o.status || o.status === 'open')) {
        // The objector gets the one act that is theirs; everyone else gets the
        // control the server decides on. See `mine` above for why this page may
        // answer that question about itself and not about anybody else.
        li.appendChild(mine(o) ? withdrawControls(o) : resolveControls(o));
      }
      ul.appendChild(li);
    });
    show(objectionsEl, true);
  }

  // §6.8's close, and the only judgement this page makes about it: is the call open,
  // and is there somebody signed in to attribute the act to. Who is ALLOWED to close is
  // the server's answer -- a client-side role check would be a claim the page cannot
  // check, exactly as with the objection controls above.
  function closeControls(call) {
    if (!token() || !callEl) { show(closeEl, false); return; }
    if ((call.status || '') !== 'open') {
      // A closed call shows its result in the meta line instead; offering a second
      // close would be offering a second verdict on a decision that has one.
      show(closeEl, false);
      return;
    }
    show(closeEl, true);
  }

  // overrideControls shows §6.6's window, and only where it can mean something: a call
  // that closed BLOCKED. An override answers a standing block, so a call accepted or
  // rejected has nothing for it to decide, and an open call has no verdict for it to act
  // on. Which HALF of the section appears is the window's own state, which is the
  // server's answer (see renderOverride).
  function overrideControls(call) {
    var blocked = (call.status || '') === 'closed' && call.result === 'blocked';
    if (!blocked) { show(overrideEl, false); return; }
    show(overrideEl, true);
    loadOverride();
  }

  function closeCall() {
    closeSubmit.disabled = true;
    show(closeErr, false);
    show(closeDone, false);
    var url = '/api/v1/projects/' + encodeURIComponent(slug) +
      '/consensus/' + encodeURIComponent(callID) + '/close';
    fetch(url, {
      method: 'POST',
      headers: headers()
    }).then(function (r) {
      return r.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = {}; }
        if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
        return data;
      });
    }).then(function (data) {
      // The server's result, quoted rather than derived: whether the call passed is
      // EvaluateConsensus's judgement and the stored `result` is the record of it.
      closeDone.textContent = 'Recorded: ' + (data.result || 'closed') + '.';
      show(closeDone, true);
      closeSubmit.disabled = false;
      // Re-read rather than patch the page: closing changes the tally's visibility,
      // the objections' statuses and the call's own record, and the page should show
      // what the store now holds. It is also what reveals §6.6's override window, which
      // only exists for a call that closed blocked.
      load();
    }).catch(function (e) {
      closeSubmit.disabled = false;
      closeErr.textContent = e.message;
      show(closeErr, true);
    });
  }

  // --- §6.6's second window --------------------------------------------------

  // The window's controls, driven entirely by the SERVER's answer about the window.
  //
  // Nothing here decides standing or the ratio. The page cannot know who is a maintainer
  // (the role is per project and not published to the reader) and cannot know the
  // charter's override ratio, so a page that hid a button would be making a claim it
  // cannot check -- the same discipline as the call's own controls.
  function overrideHeaders() {
    var h = headers();
    h['Content-Type'] = 'application/json';
    return h;
  }

  function overrideURL(suffix) {
    return '/api/v1/projects/' + encodeURIComponent(slug) +
      '/consensus/' + encodeURIComponent(callID) + '/override' + (suffix || '');
  }

  function overrideSend(method, suffix, body) {
    var opts = { method: method, headers: overrideHeaders() };
    if (body !== undefined) opts.body = JSON.stringify(body);
    return fetch(overrideURL(suffix), opts).then(function (r) {
      return r.text().then(function (txt) {
        var data = {};
        try { data = txt ? JSON.parse(txt) : {}; } catch (e) { data = {}; }
        if (!r.ok) {
          var err = new Error(data.error || ('HTTP ' + r.status));
          err.status = r.status;
          throw err;
        }
        return data;
      });
    });
  }

  function clearOverrideMessages() {
    show(overrideErr, false);
    show(overrideDone, false);
  }

  function overrideFailure(e) {
    overrideErr.textContent = e.message;
    show(overrideErr, true);
    if (overrideCloseBtn) overrideCloseBtn.disabled = false;
    if (overrideOpenBtn) overrideOpenBtn.disabled = false;
  }

  // renderOverride draws the window from the server's own numbers. `data` is the
  // window as the read returned it, or null when the call has none (a 404, which is the
  // difference between "no window" and "a window with no voters").
  function renderOverride(data) {
    show(overrideNoneEl, false);
    show(overrideLiveEl, false);
    show(overrideResult, false);

    // Signed out: the window is readable like the call, and no write control is offered.
    // The prompt above the page covers what to do about it.
    var signedIn = !!token();

    if (!data) {
      if (signedIn) show(overrideNoneEl, true);
      return;
    }
    if ((data.status || '') === 'closed') {
      // The record of the decision, in the server's words: whether the block stood.
      var line = data.result === 'overridden'
        ? 'Overridden: the call is accepted over the block.'
        : 'Upheld: the block stood.';
      line += ' ' + data.participants + ' participant' + (data.participants === 1 ? '' : 's') +
        ', ' + data.consent + ' consenting (' + pct(data.consent / (data.consent + data.stand_aside + data.block || 1)) + ' of non-abstain).';
      if (data.note) line += ' Answer recorded: “' + data.note + '”';
      overrideResult.textContent = line;
      show(overrideResult, true);
      return;
    }

    // Open: participation and the quorum bar are the server's numbers, and the counts
    // are deliberately absent -- the page is not told a running tally, so it cannot
    // leak one.
    overrideProgress.textContent = data.participants + ' of ' + data.quorum_needed +
      ' needed to reach quorum (' + data.eligible + ' eligible).';
    var buttons = overridePositionsEl.querySelectorAll('[data-override-position]');
    for (var i = 0; i < buttons.length; i++) {
      var chosen = buttons[i].getAttribute('data-override-position') === (data.my_position || '');
      buttons[i].className = chosen ? 'btn btn-primary' : 'btn';
      buttons[i].disabled = !signedIn;
    }
    show(overrideProgress, true);
    show(overridePositionsEl, signedIn);
    show(overrideNote, signedIn);
    show(overrideCloseBtn, signedIn);
    show(overrideLiveEl, true);
  }

  function loadOverride() {
    // No token: read-only. The window is public like the call, but the page has no
    // controls to offer and the sign-in prompt covers it.
    return overrideSend('GET', '', undefined).then(function (data) {
      renderOverride(data);
    }).catch(function (e) {
      // 404 is the answer "this call has no window", not a failure.
      if (e.status === 404) { renderOverride(null); return; }
      overrideFailure(e);
    });
  }

  function openOverride() {
    clearOverrideMessages();
    overrideOpenBtn.disabled = true;
    overrideSend('POST', '', undefined).then(function () {
      // Re-read: the window's own state is what decides which controls appear.
      return loadOverride();
    }).then(function () {
      overrideDone.textContent = 'Window opened.';
      show(overrideDone, true);
    }).catch(overrideFailure);
  }

  function castOverride(stance) {
    clearOverrideMessages();
    overrideSend('POST', '/position', { position: stance, reason: '' }).then(function () {
      return loadOverride();
    }).then(function () {
      var label = stance === 'consent' ? 'Override' : (stance === 'block' ? 'Uphold' : 'Recorded');
      overrideDone.textContent = label + ' recorded.';
      show(overrideDone, true);
    }).catch(overrideFailure);
  }

  function closeOverride() {
    clearOverrideMessages();
    overrideCloseBtn.disabled = true;
    // The note is sent even when empty, and the SERVER decides whether this window
    // needed it: the verdict is what makes the answer required, and the page cannot
    // compute the verdict without the charter.
    overrideSend('POST', '/close', { note: (overrideNote.value || '').trim() }).then(function (data) {
      overrideDone.textContent = data.result === 'overridden'
        ? 'Recorded: overridden.'
        : 'Recorded: upheld.';
      show(overrideDone, true);
      overrideCloseBtn.disabled = false;
      // The call's own record changed (its result, and the objection's status), so the
      // page re-reads the call rather than patching one line of it.
      load();
    }).catch(overrideFailure);
  }

  function render(data) {
    var call = data.call || {};
    $('consensus-question').textContent =
      call.question || call.summary || 'Untitled call';
    renderMeta(call);
    // `tally_visible` is the server's answer, not this page's.
    renderTally(data.tally || {}, data.tally_visible === true);
    renderObjections(data.objections, data.tally_visible === true);
    closeControls(call);
    overrideControls(call);
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
  if (closeSubmit) closeSubmit.addEventListener('click', closeCall);

  // §6.6's second window. The four stance buttons are wired by attribute, and the
  // keyboard accelerators above deliberately do NOT cover them: those shortcuts submit
  // the call's FIRST-window position, and a keystroke that silently meant "override the
  // block" on a page that is also showing the call's own ballot is a way to vote twice
  // by accident.
  if (overrideOpenBtn) overrideOpenBtn.addEventListener('click', openOverride);
  if (overrideCloseBtn) overrideCloseBtn.addEventListener('click', closeOverride);
  if (overridePositionsEl) {
    var oBtns = overridePositionsEl.querySelectorAll('[data-override-position]');
    for (var j = 0; j < oBtns.length; j++) {
      oBtns[j].addEventListener('click', function (ev) {
        castOverride(ev.currentTarget.getAttribute('data-override-position'));
      });
    }
  }

  load();
})();