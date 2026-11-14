// §4.3's tag taxonomy, as a page.
//
// THE FIRST READER of three endpoints that had handlers, guards and store tests:
// GET /api/v1/taxonomy/tags, GET /api/v1/taxonomy/proposals, POST .../consent.
// `taxonomy/proposals` was named in docs/REMAINING.md §11.3 as an endpoint that
// answers `[]` and is read by nobody; the other two were not even counted, because a
// grep for endpoints-without-readers only finds the ones somebody thought to list.
//
// MEASURED SHAPES (from the handlers, not from the struct names):
//
//   GET /taxonomy/tags
//     -> bare ARRAY of {id, project_id, name, namespace, parent_id, relation,
//        suggested, aliases?}. `namespace` is NULL for an unnamespaced tag, and
//        `aliases` is OMITTED when empty (omitempty), not null. `project_id` is 0
//        for the global namespace.
//   GET /taxonomy/proposals
//     -> bare ARRAY of the proposal fields PLUS consent_count and eligible_count,
//        which were added because this page needed them: the count was previously
//        visible only in the 202 message after consenting, so "how many more
//        consents does this need" could be learned only by acting.
//   POST /taxonomy/proposals  -> 201 {proposal, message}
//   POST .../{id}/consent     -> 200 {proposal, status:"ratified"} when it applied,
//                                202 {proposal, message} when it did not -- and the
//                                202 is NOT an error. Below quorum and
//                                consent-to-your-own are ordinary states of a
//                                consensus process, and rendering them red teaches
//                                people the control is broken.
//
// The 401 from an anonymous write is rendered as itself rather than hidden: the page
// shows the control to signed-out visitors because the server's refusal is the only
// honest answer to "why did nothing happen".

(function () {
  "use strict";

  var root = document.getElementById("taxonomy-root");
  if (!root) return;
  var live = document.getElementById("taxonomy-live");

  // Every action the store accepts, with the operand each one takes. Sourced from
  // store.ProposeTaxonomyChange's switch, not invented: an action the server rejects
  // as "unknown taxonomy action" is a 400 whose message names the action, which
  // reads like a client bug from here.
  var ACTIONS = [
    { id: "create", label: "Create a tag", needsValue: false,
      note: "Adds a tag that nothing has used yet." },
    { id: "rename", label: "Rename a tag", needsValue: true,
      note: "Replaces the tag's own name. Aliases and references follow it." },
    { id: "alias", label: "Add an alias", needsValue: true,
      note: "Another spelling that resolves to the same tag." },
    { id: "merge", label: "Merge into another tag", needsValue: true,
      note: "Calls marked with the target end up marked with the value instead." },
    { id: "reparent", label: "Move under another tag", needsValue: true,
      note: "Changes the tag's parent in its namespace." },
    { id: "delete", label: "Delete a tag", needsValue: false,
      note: "Removes the tag. Uses of it are not removed with it." }
  ];

  function esc(s) {
    if (s === null || s === undefined) return "";
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function say(msg, bad) {
    if (!live) return;
    // The live region, not a banner inside the list: a consent re-reads both lists,
    // so a message rendered into a proposal row would be replaced by the re-read.
    live.innerHTML = '<p class="' + (bad ? "error-state" : "form-note") + '">' +
      esc(msg) + "</p>";
  }

  function get(path) {
    return fetch(path, { headers: { Accept: "application/json" } }).then(function (r) {
      if (!r.ok) throw new Error(path + " -> " + r.status);
      return r.json();
    });
  }

  function token() {
    return localStorage.getItem("concord.token");
  }

  // The server's own message, when it sent one. Every refusal this page can provoke
  // arrives as {"error": "..."} (mapError), and showing it beats any sentence written
  // here: a 403 from the self-consent rule says why in words the page would have to
  // duplicate, and duplicating it is how the two drift apart.
  function serverError(res) {
    if (res.body && res.body.error) return String(res.body.error);
    if (res.body && res.body.message) return String(res.body.message);
    return String(res.text || res.status);
  }

  function post(path, body) {
    var headers = { "Content-Type": "application/json" };
    var tok = token();
    // Authorization only when a token exists, so an anonymous write is a plain
    // request that gets a plain 401 rather than a header of "Bearer null".
    if (tok) headers["Authorization"] = "Bearer " + tok;
    return fetch(path, { method: "POST", headers: headers, body: JSON.stringify(body) })
      .then(function (r) {
        return r.text().then(function (t) {
          var parsed = null;
          try { parsed = t ? JSON.parse(t) : null; } catch (e) { parsed = null; }
          return { status: r.status, ok: r.ok, body: parsed, text: t };
        });
      });
  }

  // ---------------------------------------------------------------- tags

  // §4.3 requires machine-inferred tags to be LABELLED, and the store returns the
  // flag rather than filtering, so this page offers them and marks them. Dropping
  // them would make the list disagree with what a tag search finds.
  function tagChip(tag) {
    var label = esc(tag.name);
    var title = "tag:" + (tag.namespace ? esc(tag.namespace) + ":" : "") + label;
    // A div of spans rather than a <ul>: .chip-row is a flex row of pills and the
    // rest of the site builds it that way, so a list would carry bullets the
    // stylesheet does not remove.
    return '<span class="chip" data-tag="' + label + '" title="' + title + '">' + label +
      (tag.suggested ? ' <span class="badge badge-amber">suggested</span>' : "") +
      (tag.relation ? ' <span class="muted">' + esc(tag.relation) + "</span>" : "") +
      "</span>";
  }

  function tagGroups(tags) {
    if (!tags || !tags.length) {
      return '<div class="empty-state"><p class="empty-state-title">No tags yet</p>' +
        '<p class="empty-state-description">Tags are created by proposing one. The ' +
        "first create proposal needs consent from another eligible tagger, so a " +
        "fresh instance ratifies its first tags once two people are active.</p></div>";
    }
    var byNS = {};
    tags.forEach(function (t) {
      var ns = t.namespace || "(no namespace)";
      (byNS[ns] = byNS[ns] || []).push(t);
    });
    // The namespaces in the server's own order of significance, then anything else
    // alphabetically: a fixed list would silently drop a namespace added later, and
    // a tag that is invisible here is the §3.1 defect on a taxonomy page.
    var known = ["topic", "domain", "platform", "lang", "role", "deploy", "audience"];
    var names = Object.keys(byNS).sort(function (a, b) {
      var ia = known.indexOf(a), ib = known.indexOf(b);
      if (ia === -1 && ib === -1) return a.localeCompare(b);
      if (ia === -1) return 1;
      if (ib === -1) return -1;
      return ia - ib;
    });
    return names.map(function (ns) {
      var list = byNS[ns].slice().sort(function (a, b) {
        return String(a.name).localeCompare(String(b.name));
      });
      return '<fieldset><legend>' + esc(ns) + ' <span class="muted">(' +
        list.length + ")</span></legend>" +
        '<div class="chip-row">' + list.map(tagChip).join("") + "</div></fieldset>";
    }).join("");
  }

  // ----------------------------------------------------------- proposals

  // The rule, stated where the control is. Two halves of it are the reason a first
  // consent does nothing, and neither is guessable:
  //
  //   * a proposal is ratified by QORUM, not by a single consent -- one consent on a
  //     proposal that needs three leaves it pending, and that is the ordinary case;
  //   * the proposer cannot consent to their own proposal, so a lone proposer cannot
  //     ratify anything alone.
  //
  // The counts beside the button are per-proposal measurements from the server. The
  // exact ratio comes from a charter this page does not read -- an instance-wide
  // change has no project to read one from -- so the rule is stated and the numbers
  // are shown, rather than a threshold being invented here and displayed as fact.
  var QUORUM_NOTE = "Consent is counted against the eligible taggers below, and the " +
    "proposal applies only once the charter's quorum is met \u2014 one consent does " +
    "not ratify anything by itself. The proposer cannot consent to their own " +
    "proposal (\u00a75.1), which is why the button is absent on yours.";

  function proposalCard(p, tagNames) {
    var mine = token() && p.proposed_by === currentUserID();
    var pending = p.status === "pending";
    var target = esc(p.target_tag);
    var value = p.value ? " &rarr; <code>" + esc(p.value) + "</code>" : "";
    var unknownTarget = tagNames[String(p.target_tag).toLowerCase()] ? "" :
      ' <span class="badge badge-slate" title="no tag with this name exists">target not ' +
      "an existing tag</span>";

    var counts = pending
      ? '<p class="form-note">' + p.consent_count + " of " + p.eligible_count +
        " eligible taggers have consented.</p>"
      : "";

    var control = "";
    if (pending) {
      control = mine
        ? '<p class="form-note">You proposed this, so you cannot consent to it.</p>'
        : '<button type="button" class="btn btn-primary" data-consent="' + esc(p.id) + '">' +
          "Consent</button>";
    }

    return '<article class="card" data-proposal="' + esc(p.id) + '">' +
      '<div class="pill-row">' +
        '<span class="badge ' + (pending ? "badge-amber" : "badge-green") + '">' +
          esc(p.status) + "</span>" +
        '<span class="badge badge-slate">' + esc(p.action) + "</span>" +
        '<span class="badge badge-slate">' +
          (p.project_id ? "project " + esc(p.project_id) : "instance-wide") + "</span>" +
      "</div>" +
      "<h3 class=\"card-title\"><code>" + target + "</code>" + value + unknownTarget + "</h3>" +
      '<p class="card-body">' + esc(p.rationale) + "</p>" +
      counts +
      '<div class="detail-actions">' + control + "</div>" +
      "</article>";
  }

  function proposalList(title, emptyTitle, note, proposals, tagNames) {
    var body = proposals && proposals.length
      ? '<div class="grid-responsive-2">' + proposals.map(function (p) {
          return proposalCard(p, tagNames);
        }).join("") + "</div>"
      : '<div class="empty-state"><p class="empty-state-title">' + esc(emptyTitle) +
        "</p></div>";
    return "<section class=\"panel\"><h2>" + esc(title) + "</h2>" +
      (note ? '<p class="form-note">' + esc(note) + "</p>" : "") + body + "</section>";
  }

  function proposeForm() {
    var options = ACTIONS.map(function (a) {
      return '<option value="' + esc(a.id) + '" data-note="' + esc(a.note) + '">' +
        esc(a.label) + "</option>";
    }).join("");
    // A <select> whose value is never sent as a whole: action is one field and the
    // operand is another, which is the shape taxonomyRequest takes.
    return '<section class="panel"><h2>Propose a change</h2>' +
      '<p class="form-note">' + esc(QUORUM_NOTE) + "</p>" +
      '<form id="taxonomy-propose">' +
        '<div class="form-grid">' +
          '<div class="field"><label class="form-label" for="tax-action">Action</label>' +
            '<select id="tax-action" name="action">' + options + "</select></div>" +
          '<div class="field"><label class="form-label" for="tax-target">Target tag</label>' +
            '<input id="tax-target" name="target_tag" required ' +
              'placeholder="topic:self-hosted"></div>' +
          '<div class="field"><label class="form-label" for="tax-value">Value</label>' +
            '<input id="tax-value" name="value" placeholder="only for rename, alias, ' +
              'merge and reparent"></div>' +
        "</div>" +
        '<p class="form-note" id="tax-action-note"></p>' +
        '<div class="field"><label class="form-label" for="tax-rationale">Rationale</label>' +
          '<textarea id="tax-rationale" name="rationale" rows="2" required ' +
            'placeholder="Why this change is worth making"></textarea></div>' +
        '<div class="detail-actions">' +
          '<button type="submit" class="btn btn-primary">Propose it</button>' +
        "</div>" +
      "</form></section>";
  }

  function render(tags, proposals) {
    var tagNames = {};
    (tags || []).forEach(function (t) { tagNames[String(t.name).toLowerCase()] = true; });

    var pending = [], decided = [];
    (proposals || []).forEach(function (p) {
      (p.status === "pending" ? pending : decided).push(p);
    });
    // Decided ones newest first by decision time; pending ones keep the server's
    // order, which is already oldest-first within pending.
    decided.sort(function (a, b) { return (b.decided_at || 0) - (a.decided_at || 0); });

    root.innerHTML =
      '<section class="panel"><h2>Tags</h2>' +
        '<p class="form-note">Every tag on the instance, grouped by namespace. ' +
        "Suggested tags are machine-inferred and marked as such.</p>" +
        tagGroups(tags) + "</section>" +
      proposeForm() +
      proposalList("Open proposals", "Nothing is proposed right now",
        QUORUM_NOTE, pending, tagNames) +
      proposalList("Decided", "Nothing has been decided yet", "", decided, tagNames);
    root.removeAttribute("aria-busy");
    wirePropose();
    wireConsent();
  }

  function wirePropose() {
    var form = document.getElementById("taxonomy-propose");
    if (!form) return;
    var action = document.getElementById("tax-action");
    var note = document.getElementById("tax-action-note");

    function syncNote() {
      var chosen = ACTIONS.filter(function (a) { return a.id === action.value; })[0];
      if (!note || !chosen) return;
      note.textContent = chosen.note;
      // The value field is only meaningful for four of the six actions. Leaving it
      // enabled for create/delete invites a 400 from a field the server ignores.
      var value = document.getElementById("tax-value");
      if (value) value.disabled = !chosen.needsValue;
    }
    action.addEventListener("change", syncNote);
    syncNote();

    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var payload = {
        action: action.value,
        target_tag: form.elements.target_tag.value.trim(),
        value: form.elements.value.value.trim(),
        rationale: form.elements.rationale.value.trim(),
        project_id: 0
      };
      // 0 is instance-wide, and the page says so: this page is not project-scoped,
      // and sending no project_id would leave the field at Go's zero value anyway.
      post("/api/v1/taxonomy/proposals", payload).then(function (res) {
        if (res.status === 401) {
          say("Sign in first \u2014 a taxonomy change is proposed by an account, " +
            "not by an anonymous visitor. The server said: " + serverError(res), true);
          return;
        }
        if (!res.ok) {
          say("The server refused the proposal: " + serverError(res), true);
          return;
        }
        say((res.body && res.body.message) ||
          "Proposed. It applies once it has consent.");
        form.reset();
        syncNote();
        reload();
      });
    });
  }

  function wireConsent() {
    Array.prototype.forEach.call(root.querySelectorAll("[data-consent]"), function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-consent");
        btn.disabled = true;
        post("/api/v1/taxonomy/proposals/" + encodeURIComponent(id) + "/consent", {}).then(function (res) {
          btn.disabled = false;
          if (res.status === 401) {
            say("Sign in first \u2014 consent is recorded against an account. " +
              "The server said: " + serverError(res), true);
            return;
          }
          // No 403 branch, deliberately: consenting needs an ACCOUNT, not a role
          // (handleConsentTaxonomyProposal checks requireWriteActor and nothing
          // else), so the only permission failure this route can produce is the
          // self-consent one -- and that arrives as 202 with a message, because it is
          // an ordinary state of a consensus process. A 403 branch here would be a
          // branch that cannot fire, written as though it could.
          // 202 is the ordinary answer, not a failure. The server's own message
          // carries the count it reached ("... (2/5 consents)"), which is the only
          // place the exhaustion of the quorum is explained.
          if (res.status === 202) {
            say((res.body && res.body.message) ||
              "Recorded. The quorum is not met yet, so nothing has changed.",
              false);
            reload();
            return;
          }
          if (res.status === 200) {
            say("Ratified \u2014 the change has been applied.");
            reload();
            return;
          }
          say("The server refused the consent: " + serverError(res), true);
        });
      });
    });
  }

  // currentUserID answers "am I the proposer", and it is used for ONE thing: to say
  // why the consent button is absent on your own proposal. It is not a guard -- the
  // server refuses the write regardless, and the store's check is the one that
  // matters.
  //
  // RESOLVED AT RENDER TIME, NOT AT SCRIPT LOAD, and that is the whole point of it
  // being a function. base.html loads session.js AFTER the page's own script, so at
  // load time `window.ConcordSession` does not exist yet. The first version of this
  // file snapshotted it into a variable at the top level, silently got null, and
  // offered the proposer a Consent button on their own proposal -- nothing threw,
  // because the falsy branch is "show the button" and a page that shows a control
  // looks working.
  //
  // `user` is a PROPERTY on the session object, not a method -- session.js's prototype
  // has user/signedIn/name/clear/authHeaders, so `user()` would throw "is not a
  // function" and take the whole page's script down with it. Two ways to read the
  // wrong thing from the same object, both caught by the browser suite and by neither
  // Go test.
  function currentUserID() {
    if (window.ConcordSession && window.ConcordSession.instance) {
      var sess = window.ConcordSession.instance();
      if (sess.user && sess.user.id) return sess.user.id;
    }
    return null;
  }

  function reload() {
    // include_suggested=true, because §4.3 requires suggested tags to be LABELLED
    // and the default read drops them: a page that used the default would show a
    // taxonomy that disagrees with what tag search finds.
    return Promise.all([
      get("/api/v1/taxonomy/tags?include_suggested=true"),
      get("/api/v1/taxonomy/proposals")
    ]).then(function (res) {
      render(res[0], res[1]);
    }).catch(function (err) {
      root.removeAttribute("aria-busy");
      root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
        "Could not load the taxonomy</p><p>" + esc(err.message || String(err)) +
        "</p></div>";
    });
  }

  reload();
})();
