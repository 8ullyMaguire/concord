// Project settings: the charter, the members, and the criteria profiles.
//
// THREE ENDPOINTS THAT HAD NO READER AT ALL until this page existed:
// GET /charter, GET /members, GET /criteria-profiles. All three had handlers, auth
// guards and store coverage; nothing in assets/js referenced them. That is the §3.1
// shape -- a writer with no reader is not an unfinished feature, it is a silent hole --
// and it survived because the work was on the API side and every test was on the API
// side.
//
// MEASURED SHAPES. The charter is the odd one out and the whole page is written around
// it:
//
//   GET /charter            -> OBJECT with PASCAL-CASE keys: QuorumRatio, QuorumMin,
//                             SupportRatioMin, ConsentRatio, ... governance.Charter
//                             carries no json tags, so Go marshals the FIELD NAMES.
//   GET /members            -> bare ARRAY of {project_id, user_id, username, role,
//                             is_moderator, joined_at}
//   GET /criteria-profiles  -> bare ARRAY
//
// Every other endpoint in the API is snake_case. A page written from the house style
// would read `charter.quorum_ratio`, get undefined, and render every field as empty --
// which is why the keys below are transcribed from a live response rather than inferred
// from the struct. That is the same lesson as `lists.title` vs the JSON key `name`, and
// the same as the token key: read the value that exists, not the one you expect.
//
// THE WRITE IS ROLE-GATED SERVER-SIDE: handleUpdateCharter requires the "maintainer"
// role, so this page shows the form to everyone and lets the server refuse. Hiding the
// form would make the refusal invisible, and a reader who cannot see why their save did
// nothing concludes the page is broken.

(function () {
  "use strict";

  var root = document.getElementById("settings-root");
  if (!root) return;
  var live = document.getElementById("settings-live");
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";

  function esc(s) {
    if (s === null || s === undefined) return "";
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function say(msg, bad) {
    if (!live) return;
    // The live region, not an inline banner: a save that is refused by the server must
    // be announced, and it happens without a navigation.
    live.innerHTML = '<p class="' + (bad ? "error-state" : "form-note") + '">' + esc(msg) + "</p>";
  }

  function get(path) {
    return fetch(path, { headers: { Accept: "application/json" } }).then(function (r) {
      if (!r.ok) throw new Error(path + " -> " + r.status);
      return r.json();
    });
  }

  // The charter is 22 scalars. Rendered as a labelled grid rather than 22 hand-written
  // inputs, because the FIELD LIST comes from the response: a field added to
  // governance.Charter shows up here without a line of JS being touched, and one removed
  // disappears instead of rendering an input for a field the server no longer accepts.
  //
  // The grouping is the one judgement in this file, and it is the page's reason to exist:
  // a bare list of 22 numbers tells a reader nothing about which ones decide an outcome.
  var GROUPS = [
    { title: "Quorum", note: "How much of the room has to agree before anything counts.",
      keys: ["QuorumRatio", "QuorumMin", "VoteWindowDays"] },
    { title: "Consent", note: "Two different bars. Support is consent over everyone; consent is over those who took a side.",
      keys: ["SupportRatioMin", "ConsentRatio", "OverrideRatio"] },
    { title: "Merges", note: "What a merge must clear.",
      keys: ["MergeRequiresQuorum", "MergeQuorumMin", "MergeQuorumRatio", "RequireReviewerApproval"] },
    { title: "Board limits", note: "Enforced on every card move, not just displayed.",
      keys: ["WIPInProgress", "WIPReview"] },
    { title: "Ranking", note: "How priority and Glicko-2 ratings are computed.",
      keys: ["Lam", "Mu", "PainHalflifeDays", "RepHalflifeDays", "GlickoTau", "VoteWeightCap"] },
    { title: "Solution calls", note: "§6.5's three conditions for opening a call on the leading solution.",
      keys: ["SolutionCallMinVoters", "SolutionCallConfidence", "SolutionStableHours"] }
  ];

  function field(name, value) {
    var isBool = typeof value === "boolean";
    var shown = isBool ? (value ? "yes" : "no") : value;
    return '<div class="field">' +
      '<label class="form-label" for="charter-' + esc(name) + '">' + esc(name) + "</label>" +
      '<input id="charter-' + esc(name) + '" name="' + esc(name) + '" ' +
      'data-kind="' + (isBool ? "bool" : typeof value) + '" ' +
      'value="' + esc(shown) + '">' +
      "</div>";
  }

  function charterForm(c) {
    var groups = GROUPS.map(function (g) {
      // Only render a group whose fields the response actually has, so a field removed
      // from governance.Charter leaves no orphan input behind.
      var present = g.keys.filter(function (k) { return k in c; });
      if (!present.length) return "";
      return '<fieldset><legend>' + esc(g.title) + "</legend>" +
        '<p class="form-note">' + esc(g.note) + "</p>" +
        '<div class="form-grid">' +
        present.map(function (k) { return field(k, c[k]); }).join("") +
        "</div></fieldset>";
    }).join("");

    // Anything the response has that the grouping above does not mention is still shown,
    // in an "other" group. A charter field that is invisible on the settings page is the
    // §3.1 defect again, one level up.
    var grouped = {};
    GROUPS.forEach(function (g) { g.keys.forEach(function (k) { grouped[k] = true; }); });
    var rest = Object.keys(c).filter(function (k) { return !grouped[k]; });
    if (rest.length) {
      groups += '<fieldset><legend>Other</legend><div class="form-grid">' +
        rest.map(function (k) { return field(k, c[k]); }).join("") + "</div></fieldset>";
    }

    return '<form id="charter-form">' + groups +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">Save charter</button></div>' +
      "</form>";
  }

  function memberTable(members) {
    if (!members || !members.length) {
      return '<div class="empty-state"><p class="empty-state-title">No members yet</p></div>';
    }
    var rank = { owner: 5, maintainer: 4, reviewer: 3, contributor: 2, user: 1, guest: 0 };
    var rows = members.slice().sort(function (a, b) {
      var d = (rank[b.role] || 0) - (rank[a.role] || 0);
      return d !== 0 ? d : String(a.username).localeCompare(String(b.username));
    }).map(function (m) {
      return "<tr><td>" + esc(m.username) + "</td><td>" + esc(m.role) + "</td>" +
        '<td>' + (m.is_moderator ? "yes" : "no") + "</td></tr>";
    }).join("");
    return '<table class="table"><thead><tr><th>Member</th><th>Role</th>' +
      "<th>Moderator</th></tr></thead><tbody>" + rows + "</tbody></table>";
  }

  function profileList(profiles) {
    if (!profiles || !profiles.length) {
      return '<div class="empty-state"><p class="empty-state-title">' +
        "No criteria profiles yet</p></div>";
    }
    return '<div class="grid-responsive-2">' + profiles.map(function (p) {
      var name = p.name || p.label || p.slug || "(unnamed)";
      return '<article class="card"><h3 class="card-title">' + esc(name) + "</h3></article>";
    }).join("") + "</div>";
  }

  // §4.3: "A completeness meter shows how well-tagged each project is."
  //
  // THE ENDPOINT HAD NO READER. GET /tags/completeness has answered this exact
  // question since the taxonomy work, and nothing in assets/js referenced it, so the
  // spec's meter did not exist. The handler's own comment names it as the §4.3 meter,
  // which is the trap: a route comment describing the feature it serves reads like the
  // feature being present.
  //
  // The score is six tenths "enough tags" and four tenths "enough namespaced tags",
  // and the numbers behind it are on screen because a meter that shows 60% with no
  // components is a number a reader cannot act on. The threshold is a judgement in the
  // store (wantTags, wantNamespaced), so the page reports it rather than restating it.
  function completenessPanel(c) {
    if (failed(c)) {
      return '<section class="panel"><h2>Tag completeness</h2>' + failedNote(c, "completeness") + "</section>";
    }
    var score = Number(c.score || 0);
    var pct = Math.round(score * 100);
    var tags = Number(c.tags || 0), want = Number(c.want_tags || 0);
    var ns = Number(c.namespaced || 0), wantNs = Number(c.want_namespaced || 0);
    var pending = Number(c.suggested_pending || 0);

    var move = [];
    if (tags < want) move.push((want - tags) + " more tag" + (want - tags === 1 ? "" : "s"));
    if (ns < wantNs) move.push((wantNs - ns) + " more namespaced tag" + (wantNs - ns === 1 ? "" : "s"));
    var advice = c.complete
      ? "This project is findable through both the category filters and the hierarchy queries."
      : "Add " + move.join(" and ") + " to reach the threshold.";

    return '<section class="panel"><h2>Tag completeness</h2>' +
      '<p class="form-note">A project with no tags is not merely untidy: it is invisible to ' +
      "every taxonomy-filtered search, so tagging is a contribution rather than housekeeping.</p>" +
      '<div class="meter" data-completeness-score="' + esc(score) + '" ' +
        'data-completeness-complete="' + (c.complete ? "true" : "false") + '" ' +
        'role="img" aria-label="tag completeness ' + pct + ' percent">' +
        '<div class="meter-fill" style="width:' + pct + '%"></div></div>' +
      "<p><strong data-completeness-pct>" + pct + "%</strong> complete &mdash; " +
        esc(tags) + " of " + esc(want) + " tags, " + esc(ns) + " of " + esc(wantNs) +
        " namespaced.</p>" +
      '<p class="form-note">' + esc(advice) +
        (pending > 0 ? " " + esc(pending) + " suggested tag" + (pending === 1 ? " is" : "s are") +
          " still waiting to be applied." : "") +
      "</p></section>";
  }

  // §6.2: strategic weights move only by consensus, and the page has to say so,
  // because a proposal that looked applied would be the hand-steering the mechanism
  // exists to prevent.
  function strategyPanel(s) {
    if (failed(s)) {
      return '<section class="panel"><h2>Strategic weights</h2>' + failedNote(s, "strategic weights") + "</section>";
    }
    var proposals = s.proposals || [];
    var themes = s.themes || [];

    var pending = proposals.length
      ? '<table class="table" data-strategy-proposals><thead><tr><th>Feature</th>' +
        "<th>Weight</th><th>Rationale</th><th></th></tr></thead><tbody>" +
        proposals.map(function (p) {
          return '<tr data-proposal="' + esc(p.id) + '">' +
            "<td>#" + esc(p.feature_id) + "</td>" +
            "<td>" + esc(p.weight_before) + " &rarr; " + esc(p.weight_after) + "</td>" +
            "<td>" + esc(p.rationale) + "</td>" +
            '<td><button type="button" class="btn" data-consent="' + esc(p.id) + '">Consent</button></td>' +
            "</tr>";
        }).join("") + "</tbody></table>"
      : '<div class="empty-state"><p class="empty-state-title">No weight proposals open</p>' +
        "<p>Nobody can pin an item to the top by hand. Steering happens through pain, " +
        "pairwise votes, and ratified themes.</p></div>";

    var liveThemes = themes.length
      ? '<table class="table" data-strategy-themes><thead><tr><th>Theme</th><th>Weight</th>' +
        "<th>Rationale</th></tr></thead><tbody>" +
        themes.map(function (t) {
          return "<tr><td>" + esc(t.namespace ? t.namespace + "/" + t.tag : t.tag) + "</td>" +
            "<td>" + esc(t.weight) + "</td><td>" + esc(t.rationale) + "</td></tr>";
        }).join("") + "</tbody></table>"
      : '<div class="empty-state"><p class="empty-state-title">No ratified themes</p>' +
        "<p>A theme is a multiplier on every feature carrying its tag, and it expires at " +
        "the next release cycle rather than being switched off by hand.</p></div>";

    return '<section class="panel"><h2>Strategic weights</h2>' +
      '<p class="form-note">A weight proposal is ratified by consensus: contributors ' +
      "consent to it, the proposer cannot consent to their own, and the feature's weight " +
      "does not move until the quorum is reached.</p>" +
      "<h3>Open proposals</h3>" + pending +
      "<h3>Ratified themes</h3>" + liveThemes + "</section>";
  }

  // A failed fetch must cost its own panel and nothing else. The page previously used
  // Promise.all with one catch, which blanked all three panels when one fetch failed --
  // the opposite of what its own comment claimed. Two more fetches made that a real
  // risk rather than a hypothetical one.
  function failed(v) {
    return v && typeof v === "object" && v.__failed;
  }
  function failedNote(v, what) {
    return '<div class="empty-state"><p class="empty-state-title">Could not load ' + esc(what) +
      '</p><p>' + esc(v.__failed) + "</p></div>";
  }
  function getOrNull(path) {
    return get(path).catch(function (e) { return { __failed: e.message || String(e) }; });
  }

  function render(charter, members, profiles, completeness, strategy) {
    root.innerHTML =
      (failed(charter) ? '<section class="panel"><h2>Charter</h2>' + failedNote(charter, "charter") + "</section>"
                       : '<section class="panel"><h2>Charter</h2>' + charterForm(charter || {}) + "</section>") +
      '<section class="panel"><h2>Members</h2>' + memberTable(members) + "</section>" +
      '<section class="panel"><h2>Criteria profiles</h2>' + profileList(profiles) + "</section>" +
      completenessPanel(completeness) +
      strategyPanel(strategy);
    root.removeAttribute("aria-busy");
  }

  // The strategy reads are keyed by slug, like every other project-scoped route --
  // `{project_id}` in the route pattern carries a SLUG (requireProjectID resolves it
  // with GetProject), which is worth knowing before reaching for a numeric id.
  Promise.all([
    getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/charter"),
    getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/members"),
    getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/criteria-profiles"),
    getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/tags/completeness"),
    Promise.all([
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/themes")
    ]).then(function (s) { return { proposals: s[0], themes: s[1] }; })
  ]).then(function (res) {
    render(res[0], res[1], res[2], res[3], res[4]);
    wireSave();
    wireConsent();
  });

  // Consent is a write, and the page reports which write happened: a 202 means the
  // consent was recorded and the quorum is not reached yet, which is not a failure and
  // must not read like one.
  function wireConsent() {
    root.querySelectorAll("[data-consent]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-consent");
        var headers = { "Content-Type": "application/json" };
        var tok = localStorage.getItem("concord.token");
        if (tok) headers["Authorization"] = "Bearer " + tok;
        btn.disabled = true;
        fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals/" +
              encodeURIComponent(id) + "/consent", { method: "POST", headers: headers })
          .then(function (r) {
            return r.text().then(function (t) {
              var body = {};
              try { body = JSON.parse(t); } catch (e) { /* a non-JSON body is reported raw */ }
              if (r.status === 401 || r.status === 403) {
                say(body.error || ("Not allowed to consent (" + r.status + ")."), true);
                btn.disabled = false;
                return;
              }
              if (r.status === 202) {
                say("Consent recorded. " + (body.message || "The quorum is not reached yet."));
                return;
              }
              if (!r.ok) {
                say("The server refused the consent: " + (body.error || t || r.status), true);
                btn.disabled = false;
                return;
              }
              say("Consent recorded and the quorum is reached: " + (body.message || "ratified."));
            });
          })
          .catch(function (err) {
            btn.disabled = false;
            say("Could not reach the server: " + (err.message || String(err)), true);
          });
      });
    });
  }

  function wireSave() {
    var form = document.getElementById("charter-form");
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();

      // Read the VALUES BACK OUT OF THE FORM into the shape the server expects, which is
      // the PascalCase object it just sent us. Sending a hand-written object would mean a
      // field added to the charter is silently dropped on save -- and the page would show
      // the new value until the next reload, then revert it.
      var payload = {};
      Array.prototype.forEach.call(form.querySelectorAll("input[name]"), function (input) {
        var kind = input.getAttribute("data-kind");
        var raw = input.value;
        if (kind === "bool") {
          payload[input.name] = /^(yes|true|1)$/i.test(String(raw).trim());
        } else if (kind === "number") {
          var n = Number(raw);
          // NaN serialises to null, and a null where the server wants a number is a 400
          // with a message that names the field but not the cause.
          payload[input.name] = isNaN(n) ? 0 : n;
        } else {
          payload[input.name] = raw;
        }
      });

      // The same shape as requests.js: Content-Type always, Authorization only when a
      // token exists, so an anonymous save is a plain request that gets a plain 401
      // rather than a header of "Bearer null".
      var headers = { "Content-Type": "application/json" };
      var tok = localStorage.getItem("concord.token");
      if (tok) headers["Authorization"] = "Bearer " + tok;

      fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/charter", {
        method: "PUT",
        headers: headers,
        body: JSON.stringify(payload)
      }).then(function (r) {
        if (r.status === 401 || r.status === 403) {
          // The two the token typo produced. Named explicitly because "save did nothing"
          // is the least diagnosable symptom there is.
          say("Not allowed to change the charter. Sign in as a maintainer.", true);
          return;
        }
        if (!r.ok) {
          return r.text().then(function (t) {
            say("The server refused the charter: " + (t || r.status), true);
          });
        }
        say("Charter saved.");
      }).catch(function (err) {
        say("Could not reach the server: " + (err.message || String(err)), true);
      });
    });
  }
})();