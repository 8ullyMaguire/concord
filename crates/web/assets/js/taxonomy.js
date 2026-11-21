// §4.3's tag taxonomy, as a page.
//
// THE FIRST READER of three endpoints that had handlers, guards and store tests:
// GET /api/v1/taxonomy/tags, GET /api/v1/taxonomy/proposals, POST .../consent.
//
// MEASURED SHAPES (from the handlers, not from the struct names):
//   GET /taxonomy/tags       -> bare ARRAY of {id, project_id, name, namespace,
//                              parent_id, relation, suggested, aliases?}
//   GET /taxonomy/proposals  -> bare ARRAY plus consent_count and eligible_count
//   POST .../consent         -> 200 {proposal, status:"ratified"} when it applied,
//                                200 with ratified:false when the quorum was not met
//
// The 401 from an anonymous write is rendered as itself rather than hidden: the page
// shows the control to signed-out visitors because the server's refusal is the only
// honest answer to "why did nothing happen".
//
// Rendering (step 2b): `el()` nodes only. Tag names, namespaces, relations, proposal
// targets/values/rationales and error messages are text or setAttribute values.

(function () {
  "use strict";

  var el = window.ConcordDom.el;

  var root = document.getElementById("taxonomy-root");
  if (!root) return;
  var live = document.getElementById("taxonomy-live");

  // Every action the store accepts, with the operand each one takes. Sourced from
  // store.ProposeTaxonomyChange's switch, not invented.
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

  function say(msg, bad) {
    if (!live) return;
    // The live region, not a banner inside the list: a consent re-reads both lists.
    live.replaceChildren(el("p", { class: bad ? "error-state" : "form-note" }, msg));
  }

  function get(path) {
    return window.ConcordApi.get(path);
  }

  function post(path, body) {
    return window.ConcordApi.post(path, body);
  }

  // §4.3 requires machine-inferred tags to be LABELLED, and the store returns the
  // flag rather than filtering, so this page offers them and marks them.
  function tagChip(tag) {
    // D-221: a trailing numeric suffix is a database id, not part of the vocabulary.
    // data-tag keeps the exact server key for proposals and consent.
    var stripped = String(tag.name).replace(/[\d#]+$/, "");
    var label = stripped || String(tag.name);
    var title = (tag.namespace ? tag.namespace + ": " : "") + label;
    // A div of spans rather than a <ul>: .chip-row is a flex row of pills.
    return el("span", { class: "chip", "data-tag": label, title: title }, [
      label,
      tag.suggested ? " " : null,
      tag.suggested ? el("span", { class: "badge badge-amber" }, "suggested") : null,
      tag.relation ? " " : null,
      tag.relation ? el("span", { class: "muted" }, tag.relation) : null
    ]);
  }

  function tagGroups(tags) {
    if (!tags || !tags.length) {
      return el("div", { class: "empty-state" }, [
        el("p", { class: "empty-state-title" }, "No tags yet"),
        el("p", { class: "empty-state-description" }, "Tags are created by proposing one. The " +
          "first create proposal needs consent from another eligible tagger, so a " +
          "fresh instance ratifies its first tags once two people are active.")
      ]);
    }
    var byNS = {};
    tags.forEach(function (t) {
      var ns = t.namespace || "(no namespace)";
      (byNS[ns] = byNS[ns] || []).push(t);
    });
    // The namespaces in the server's own order of significance, then anything else
    // alphabetically; a fixed list would silently drop a namespace added later.
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
      return el("fieldset", {}, [
        el("legend", {}, [ns + " ", el("span", { class: "muted" }, "(" + list.length + ")")]),
        el("div", { class: "chip-row" }, list.map(tagChip))
      ]);
    });
  }

  // The rule, stated where the control is: ratification takes a quorum, and the
  // proposer cannot consent to their own proposal. The counts are per-proposal
  // measurements from the server, not a threshold invented on this page.
  var QUORUM_NOTE = "Consent is counted against the eligible taggers below, and the " +
    "proposal applies only once the charter's quorum is met — one consent does " +
    "not ratify anything by itself. The proposer cannot consent to their own " +
    "proposal (§5.1), which is why the button is absent on yours.";

  function proposalCard(p, tagNames) {
    var uid = currentUserID();
    var mine = uid !== null && p.proposed_by === uid;
    var pending = p.status === "pending";
    var unknownTarget = tagNames[String(p.target_tag).toLowerCase()] ? null :
      el("span", {
        class: "badge badge-slate", title: "no tag with this name exists"
      }, "target not an existing tag");

    var control = pending
      ? (mine
          ? el("p", { class: "form-note" }, "You proposed this, so you cannot consent to it.")
          : el("button", { type: "button", class: "btn btn-primary", "data-consent": p.id }, "Consent"))
      : null;

    return el("article", { class: "card", "data-proposal": p.id }, [
      el("div", { class: "pill-row" }, [
        el("span", { class: "badge " + (pending ? "badge-amber" : "badge-green") }, p.status),
        el("span", { class: "badge badge-slate" }, p.action),
        el("span", { class: "badge badge-slate" }, p.project_id ? "project " + p.project_id : "instance-wide")
      ]),
      el("h3", { class: "card-title" }, [
        el("code", {}, p.target_tag),
        p.value ? " → " : null,
        p.value ? el("code", {}, p.value) : null,
        unknownTarget ? " " : null,
        unknownTarget
      ]),
      el("p", { class: "card-body" }, p.rationale),
      pending
        ? el("p", { class: "form-note" }, p.consent_count + " of " + p.eligible_count +
          " eligible taggers have consented.")
        : null,
      el("div", { class: "detail-actions" }, control)
    ]);
  }

  function proposalList(title, emptyTitle, note, proposals, tagNames) {
    return el("section", { class: "panel" }, [
      el("h2", {}, title),
      note ? el("p", { class: "form-note" }, note) : null,
      proposals && proposals.length
        ? el("div", { class: "grid-responsive-2" }, proposals.map(function (p) {
            return proposalCard(p, tagNames);
          }))
        : el("div", { class: "empty-state" }, el("p", { class: "empty-state-title" }, emptyTitle))
    ]);
  }

  function proposeForm() {
    return el("section", { class: "panel" }, [
      el("h2", {}, "Propose a change"),
      el("p", { class: "form-note" }, QUORUM_NOTE),
      el("form", { id: "taxonomy-propose" }, [
        el("div", { class: "form-grid" }, [
          el("div", { class: "field" }, [
            el("label", { class: "form-label", for: "tax-action" }, "Action"),
            el("select", { id: "tax-action", name: "action" }, ACTIONS.map(function (a) {
              return el("option", { value: a.id, "data-note": a.note }, a.label);
            }))
          ]),
          el("div", { class: "field" }, [
            el("label", { class: "form-label", for: "tax-target" }, "Target tag"),
            el("input", { id: "tax-target", name: "target_tag", required: true,
              placeholder: "topic:self-hosted" })
          ]),
          el("div", { class: "field" }, [
            el("label", { class: "form-label", for: "tax-value" }, "Value"),
            el("input", { id: "tax-value", name: "value",
              placeholder: "only for rename, alias, merge and reparent" })
          ])
        ]),
        el("p", { class: "form-note", id: "tax-action-note" }),
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "tax-rationale" }, "Rationale"),
          el("textarea", { id: "tax-rationale", name: "rationale", rows: "2", required: true,
            placeholder: "Why this change is worth making" })
        ]),
        el("div", { class: "detail-actions" },
          el("button", { type: "submit", class: "btn btn-primary" }, "Propose it"))
      ])
    ]);
  }

  function render(tags, proposals) {
    var tagNames = {};
    (tags || []).forEach(function (t) { tagNames[String(t.name).toLowerCase()] = true; });

    var pending = [], decided = [];
    (proposals || []).forEach(function (p) {
      (p.status === "pending" ? pending : decided).push(p);
    });
    // Decided ones newest first by decision time; pending ones keep the server's order.
    decided.sort(function (a, b) { return (b.decided_at || 0) - (a.decided_at || 0); });

    root.replaceChildren(
      el("section", { class: "panel" }, [
        el("h2", {}, "Tags"),
        el("p", { class: "form-note" }, "Every tag on the instance, grouped by namespace. " +
          "Suggested tags are machine-inferred and marked as such."),
        tagGroups(tags)
      ]),
      proposeForm(),
      proposalList("Open proposals", "Nothing is proposed right now",
        QUORUM_NOTE, pending, tagNames),
      proposalList("Decided", "Nothing has been decided yet", "", decided, tagNames)
    );
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
      // The value field is only meaningful for four of the six actions.
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
      post("/api/v1/taxonomy/proposals", payload).then(function (body) {
        say((body && body.message) || "Proposed. It applies once it has consent.");
        form.reset();
        syncNote();
        reload();
      }).catch(function (err) {
        var status = err && err.status ? err.status : 0;
        var why = err && err.message ? err.message : String(err);
        if (status === 401) {
          say("Sign in first — a taxonomy change is proposed by an account, " +
            "not by an anonymous visitor. The server said: " + why, true);
          return;
        }
        say("The server refused the proposal: " + why, true);
      });
    });
  }

  function wireConsent() {
    Array.prototype.forEach.call(root.querySelectorAll("[data-consent]"), function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-consent");
        btn.disabled = true;
        post("/api/v1/taxonomy/proposals/" + encodeURIComponent(id) + "/consent", {}).then(function (body) {
          btn.disabled = false;
          // A below-quorum consent answers 202 with the proposal still pending and the honest
          // count in `message`. There is no `ratified` flag; reading one made every 202 say "Ratified".
          var stillPending = body && body.proposal && body.proposal.status === "pending";
          if (stillPending) {
            say((body && body.message) ||
              "Recorded. The quorum is not met yet, so nothing has changed.", false);
            reload();
            return;
          }
          say("Ratified — the change has been applied.");
          reload();
        }).catch(function (err) {
          btn.disabled = false;
          var status = err && err.status ? err.status : 0;
          var why = err && err.message ? err.message : String(err);
          if (status === 401) {
            say("Sign in first — consent is recorded against an account. " +
              "The server said: " + why, true);
            return;
          }
          say("The server refused the consent: " + why, true);
        });
      });
    });
  }

  // currentUserID answers "am I the proposer", and it is used for ONE thing: to say
  // why the consent button is absent on your own proposal. RESOLVED AT RENDER TIME:
  // base.html loads session.js AFTER this script, so a load-time snapshot was null.
  function currentUserID() {
    if (window.ConcordSession && window.ConcordSession.instance) {
      var sess = window.ConcordSession.instance();
      if (sess.user && sess.user.id) return sess.user.id;
    }
    return null;
  }

  function reload() {
    // include_suggested=true, because §4.3 requires suggested tags to be LABELLED.
    return Promise.all([
      get("/api/v1/taxonomy/tags?include_suggested=true"),
      get("/api/v1/taxonomy/proposals")
    ]).then(function (res) {
      render(res[0], res[1]);
    }).catch(function (err) {
      root.removeAttribute("aria-busy");
      root.replaceChildren(el("div", { class: "empty-state" }, [
        el("p", { class: "empty-state-title" }, "Could not load the taxonomy"),
        el("p", {}, err.message || String(err))
      ]));
    });
  }

  reload();
})();
