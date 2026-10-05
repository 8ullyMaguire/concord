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

  function render(charter, members, profiles) {
    root.innerHTML =
      '<section class="panel"><h2>Charter</h2>' + charterForm(charter || {}) + "</section>" +
      '<section class="panel"><h2>Members</h2>' + memberTable(members) + "</section>" +
      '<section class="panel"><h2>Criteria profiles</h2>' + profileList(profiles) + "</section>";
    root.removeAttribute("aria-busy");
  }

  // Each panel degrades on its own. A project with no charter profiles is a normal state,
  // and one failing fetch must not cost the two panels that worked -- the rule
  // project.js already follows.
  Promise.all([
    get("/api/v1/projects/" + encodeURIComponent(slug) + "/charter"),
    get("/api/v1/projects/" + encodeURIComponent(slug) + "/members"),
    get("/api/v1/projects/" + encodeURIComponent(slug) + "/criteria-profiles")
  ]).then(function (res) {
    render(res[0], res[1], res[2]);
    wireSave();
  }).catch(function (err) {
    root.removeAttribute("aria-busy");
    root.innerHTML = '<div class="empty-state"><p class="empty-state-title">' +
      "Could not load settings</p><p>" + esc(err.message || String(err)) + "</p></div>";
  });

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