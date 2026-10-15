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

  // EVERY READ CARRIES THE TOKEN, and it did not until the visibility panel made the
  // consequence visible. `get` sent only `Accept`, so the page was an ANONYMOUS reader
  // for every panel on it. That is invisible while a project is public -- the reads
  // succeed -- and it breaks the page for exactly the projects that need a settings page
  // most: as soon as §? makes a project protected or private, GET /charter, /members,
  // /criteria-profiles, /tags, /completeness, /invites and /languages all answer 404 and
  // the page renders seven "could not load" panels for a member who is allowed to see
  // all of it. Measured, not reasoned: PUT /visibility answered 200, SQLite said
  // `protected`, and every member-only panel on the page went blank.
  //
  // cache: "no-store" is HYGIENE, and it is labelled as such rather than as a fix,
  // because the fix was measured and the cache was not. The symptom that started this:
  // PUT /visibility answered 200, SQLite held `protected`, and the panel still rendered
  // `public`. The cause was the DOCUMENT reload that followed the write (window.location
  // .reload() on a page whose project had just become protected, so the reload raced the
  // visibility change) -- and what fixed it was re-reading the panels instead, which is
  // the `load()` below. Removing `cache: "no-store"` from this line and from the server
  // does NOT reproduce the stale read: verified by mutation, both sides, and the panel
  // still re-read correctly. So "the browser cached the GET" is a plausible story that
  // the experiment did not support, and it is not written down as though it had.
  function authHeaders(extra) {
    var h = extra || {};
    var tok = localStorage.getItem("concord.token");
    if (tok) h["Authorization"] = "Bearer " + tok;
    return h;
  }

  function get(path) {
    return fetch(path, { headers: authHeaders({ Accept: "application/json" }), cache: "no-store" })
      .then(function (r) {
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
    // §6.2's propose form. The route has existed since the strategy panel was added
    // and the panel could only CONSENT to proposals other people had made: there was
    // no way to open one. Same class as the rest of this pass -- a write with a
    // handler and no reader.
    var features = (s.features && s.features.features) || s.features || [];
    if (!Array.isArray(features)) features = [];
    var featureOptions = features.map(function (f) {
      return '<option value="' + esc(f.id) + '">#' + esc(f.id) + " " + esc(f.title || "untitled") +
        " (weight " + esc(f.strategic_weight == null ? 1 : f.strategic_weight) + ")</option>";
    }).join("");
    var propose = '<form id="propose-weight-form" class="card">' +
      '<h3 class="form-title">Propose a weight change</h3>' +
      '<p class="form-note">The proposal opens a decision other contributors consent to; ' +
      "the feature's weight does not move until that quorum is reached. The proposer's own " +
      "consent does not count toward it.</p>" +
      (features.length
        ? '<div class="field"><label class="form-label" for="weight-feature">Feature</label>' +
          '<select id="weight-feature" name="feature_id" required>' + featureOptions + "</select></div>"
        : '<p class="form-note">This project has no features yet, so there is nothing to weigh.</p>') +
      '<div class="field-row">' +
      '<div class="field"><label class="form-label" for="weight-value">Proposed weight</label>' +
      '<input id="weight-value" name="weight" type="number" step="0.1" min="0" value="1.5" required></div>' +
      "</div>" +
      '<div class="field"><label class="form-label" for="weight-rationale">Why</label>' +
      '<input id="weight-rationale" name="rationale" required maxlength="300" ' +
      'placeholder="what makes this one more important than it scores"></div>' +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary"' +
      (features.length ? "" : " disabled") + ">Open the proposal</button></div>" +
      '<p class="form-status" data-status hidden></p>' +
      "</form>";

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
      propose +
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

  // ---------------------------------------------------------------------------
  // The project's administration surface: visibility, tags, languages, invites.
  //
  // SIX ENDPOINTS THAT HAD NO UI AT ALL. PUT /visibility, GET+PUT /tags,
  // PUT /languages, POST+GET /invites and POST /invites/{id}/revoke all had handlers,
  // guards and store tests; the frontend inventory in docs/specs/frontend-spec.md
  // listed them as "none", and they stayed that way. They are also the only way to
  // change what a project looks like to the rest of the instance, so the gap was not
  // cosmetic: a project could not be made private, tagged or invited to from the site.
  //
  // The completeness meter above makes the tags half of this concrete: it printed
  // "Add 2 more tags and 1 more namespaced tag" with no control to add one anywhere.
  // A meter that instructs and cannot be obeyed is worse than no meter.
  // ---------------------------------------------------------------------------

  // Only the four levels the store accepts (store.ValidVisibility). Rendered from a
  // list so the labels and the reasons sit together -- a bare <select> of four opaque
  // words is the kind of control this project's specs call out.
  var VISIBILITIES = [
    { value: "public", label: "Public",
      note: "Listed in search and the project index. Anyone can read it." },
    { value: "unlisted", label: "Unlisted",
      note: "Not listed anywhere; the URL is the capability. Anyone with the link can read it." },
    { value: "protected", label: "Protected",
      note: "Members only, and an invite is how somebody becomes one." },
    { value: "private", label: "Private",
      note: "Members only, and no invite is handed out. Reachable only by people already in it." }
  ];

  function visibilityPanel(p) {
    if (failed(p)) {
      return '<section class="panel"><h2>Visibility</h2>' + failedNote(p, "visibility") + "</section>";
    }
    var current = String(p.visibility || "");
    var known = VISIBILITIES.some(function (v) { return v.value === current; });
    var opts = VISIBILITIES.map(function (v) {
      return '<option value="' + esc(v.value) + '"' + (v.value === current ? " selected" : "") + ">" +
        esc(v.label) + "</option>";
    }).join("");
    var note = (VISIBILITIES.filter(function (v) { return v.value === current; })[0] || {}).note || "";
    return '<section class="panel"><h2>Visibility</h2>' +
      '<p class="form-note">Who can see this project. A member of it is the only one who can change this: ' +
      "anyone able to reach this control could hide a project they do not own.</p>" +
      (known ? "" : '<p class="form-note error-state">The server reports a level this page does not know ' +
        "(" + esc(current) + "), so the select below shows no current value rather than a wrong one.</p>") +
      '<form id="visibility-form">' +
      '<p>Currently <strong data-visibility-current>' + esc(current) + "</strong>" +
      (note ? ' &mdash; <span data-visibility-note>' + esc(note) + "</span>" : "") + "</p>" +
      '<div class="field"><label class="form-label" for="visibility-select">Set visibility</label>' +
      '<select id="visibility-select" name="visibility">' + opts + "</select></div>" +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">Save visibility</button></div>' +
      "</form></section>";
  }

  // GET /tags is the read half that PUT /tags needed to be verifiable; these are the
  // names, and the form applies existing tags.
  function tagsPanel(t) {
    if (failed(t)) {
      return '<section class="panel"><h2>Tags</h2>' + failedNote(t, "tags") + "</section>";
    }
    var tags = (t && t.tags) || [];
    var list = tags.length
      ? '<ul class="tag-list" data-project-tags>' + tags.map(function (name) {
          return '<li class="tag">' + esc(name) + "</li>";
        }).join("") + "</ul>"
      : '<div class="empty-state" data-project-tags><p class="empty-state-title">No tags yet</p>' +
        "<p>A project with no tags is invisible to every taxonomy-filtered search, so " +
        "tagging it is the difference between being findable and not.</p></div>";
    return '<section class="panel"><h2>Tags</h2>' +
      '<p class="form-note">Tags are the shared vocabulary the taxonomy is built on. Applying one ' +
      "requires contributor standing and an existing tag: this route attaches tags that exist " +
      "rather than minting new ones, because a new tag is a proposal with a quorum (§4.3).</p>" +
      list +
      '<form id="tags-form">' +
      '<div class="field"><label class="form-label" for="tags-input">Apply tags</label>' +
      '<input id="tags-input" name="tags" placeholder="self-hosted, kanban, rust">' +
      '<p class="form-note">Comma-separated. A tag this instance does not have yet is refused ' +
      "with the server's reason, and the ones that applied before it stay applied.</p></div>" +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">Apply tags</button></div>' +
      "</form></section>";
  }

  // §15: "collaboratively correctable percentages". The page shows every row so a
  // correction is a correction of a number somebody can see, and the sum rule (100) is
  // enforced by the store rather than restated here.
  function languagesPanel(l) {
    if (failed(l)) {
      return '<section class="panel"><h2>Languages</h2>' + failedNote(l, "languages") + "</section>";
    }
    var langs = (l && l.languages) || [];
    var rows = langs.map(function (x, i) {
      return '<div class="field lang-row">' +
        '<label class="form-label" for="lang-' + i + '">' + esc(x.language) + "</label>" +
        '<input id="lang-' + i + '" name="language" data-language="' + esc(x.language) + '" ' +
        'data-kind="number" value="' + esc(x.pct) + '">' +
        "</div>";
    }).join("");
    var body = langs.length
      ? '<div data-project-languages>' + rows + "</div>"
      : '<div class="empty-state" data-project-languages><p class="empty-state-title">' +
        "No language breakdown yet</p><p>Discovery filters on language, so an unset project " +
        "is missing from every language-filtered search.</p></div>";
    return '<section class="panel"><h2>Languages</h2>' +
      '<p class="form-note">The share of each language, as percentages. §15 makes these ' +
      "collaboratively correctable: the forge's own count is a starting point, not a verdict.</p>" +
      body +
      '<div class="detail-actions"><button type="button" class="btn" id="languages-edit">' +
      (langs.length ? "Edit percentages" : "Add languages") + "</button></div>" +
      '<form id="languages-form" hidden>' +
      '<div class="field"><label class="form-label" for="languages-input">Languages</label>' +
      '<input id="languages-input" name="languages" placeholder="Rust 70, Go 30">' +
      '<p class="form-note">Comma-separated <code>name pct</code> pairs. The server refuses a ' +
      "total above 100, naming the total it computed.</p></div>" +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">Save languages</button></div>' +
      "</form></section>";
  }

  // Invites: the grant a protected project hands out. Member-only, so the panel says so
  // rather than rendering an empty table for a visitor who cannot read it.
  function invitesPanel(inv) {
    if (failed(inv)) {
      return '<section class="panel"><h2>Invites</h2>' +
        '<div class="empty-state"><p class="empty-state-title">Not available</p>' +
        '<p>Invites are visible to members of this project. ' +
        esc(inv.__failed) + "</p></div></section>";
    }
    var invites = inv || [];
    var rows = invites.map(function (v) {
      var state = v.revoked_at ? "revoked" : (v.expires_at && v.expires_at * 1000 < Date.now() ? "expired" : "active");
      return '<tr data-invite="' + esc(v.id) + '">' +
        '<td><code data-invite-token>' + esc(v.token) + "</code></td>" +
        "<td>" + esc(v.uses) + (v.max_uses ? " / " + esc(v.max_uses) : " / unlimited") + "</td>" +
        "<td>" + esc(state) + "</td>" +
        "<td>" + (state === "active"
          ? '<button type="button" class="btn btn-danger" data-revoke="' + esc(v.id) + '">Revoke</button>'
          : "") + "</td></tr>";
    }).join("");
    var table = invites.length
      ? '<table class="table" data-invites><thead><tr><th>Token</th><th>Uses</th><th>State</th>' +
        "<th></th></tr></thead><tbody>" + rows + "</tbody></table>"
      : '<div class="empty-state" data-invites><p class="empty-state-title">No invites</p>' +
        "<p>A protected project is joined by redeeming an invite; without one, nobody new can get in.</p></div>";
    return '<section class="panel"><h2>Invites</h2>' +
      '<p class="form-note">An invite is the credential a protected project hands out. ' +
      "Revoking keeps the record of it rather than deleting it, so the history of who was " +
      "invited survives the revocation.</p>" + table +
      '<form id="invite-form">' +
      '<div class="field"><label class="form-label" for="invite-days">Expires in (days)</label>' +
      '<input id="invite-days" name="days" value="7"><p class="form-note">0 means no expiry.</p></div>' +
      '<div class="field"><label class="form-label" for="invite-uses">Maximum uses</label>' +
      '<input id="invite-uses" name="uses" value="1"><p class="form-note">0 means unlimited.</p></div>' +
      '<div class="detail-actions"><button type="submit" class="btn btn-primary">Mint an invite</button></div>' +
      "</form>" +
      '<p class="form-note">Redeeming needs a signed-in account: it is ' +
      "<code>POST /api/v1/auth/redeem-invite</code> with the token. There is no redeem page " +
      "yet, which is why the token is shown here rather than as a clickable link.</p>" +
      "</section>";
  }

  function render(charter, members, profiles, completeness, strategy, project, tags, languages, invites) {
    root.innerHTML =
      (failed(charter) ? '<section class="panel"><h2>Charter</h2>' + failedNote(charter, "charter") + "</section>"
                       : '<section class="panel"><h2>Charter</h2>' + charterForm(charter || {}) + "</section>") +
      '<section class="panel"><h2>Members</h2>' + memberTable(members) + "</section>" +
      '<section class="panel"><h2>Criteria profiles</h2>' + profileList(profiles) + "</section>" +
      completenessPanel(completeness) +
      strategyPanel(strategy) +
      visibilityPanel(project) +
      tagsPanel(tags) +
      languagesPanel(languages) +
      invitesPanel(invites);
    root.removeAttribute("aria-busy");
  }

  // The strategy reads are keyed by slug, like every other project-scoped route --
  // `{project_id}` in the route pattern carries a SLUG (requireProjectID resolves it
  // with GetProject), which is worth knowing before reaching for a numeric id.
  //
  // load() rather than window.location.reload() after a write, and that is not a style
  // choice: #settings-live is OUTSIDE #settings-root, so re-rendering the panels keeps the
  // message while a document reload destroys it -- and the invite panel's whole point is
  // showing a token once ("Invite minted: <43 chars>"). A reload reported that message for
  // zero frames. Every wiring function re-attaches to the freshly rendered DOM on each
  // load, so there is no double-binding either.
  // Each load is counted on the element, and that is a testing affordance with a
  // measured reason behind it: a browser test that clicks Save and then reads the panel
  // has no way to tell "the panels re-read their data" from "the panels are still the
  // previous render" -- the pre-write DOM already contains every panel, so any wait for
  // panel COUNT is satisfied before the write lands. The first version of the visibility
  // test did exactly that and read `public` back, which looks like a server that did not
  // persist and was a test that did not wait. `data-loads` makes the second read
  // observable, so the test can name what it is waiting for.
  function load() {
    root.setAttribute("data-loading", "true");
    return Promise.all([
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/charter"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/members"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/criteria-profiles"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/tags/completeness"),
      Promise.all([
        getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals"),
        getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/themes"),
        // The propose form needs the features to choose from. A number input asking
        // for a feature id would be a control that requires the reader to know
        // something the page is holding.
        getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/features/")
      ]).then(function (s) { return { proposals: s[0], themes: s[1], features: s[2] }; }),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug)),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/tags"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/languages"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/invites")
    ]).then(function (res) {
      render(res[0], res[1], res[2], res[3], res[4], res[5], res[6], res[7], res[8]);
      wireSave();
      wireConsent();
      wirePropose();
      wireVisibility();
      wireTags();
      wireLanguages();
      wireInvites();
      root.setAttribute("data-loads", String(Number(root.getAttribute("data-loads") || 0) + 1));
      root.removeAttribute("data-loading");
    });
  }

  load();

  // Consent is a write, and the page reports which write happened: a 202 means the
  // consent was recorded and the quorum is not reached yet, which is not a failure and
  // must not read like one.

  // The four writers. Each sends the token the server reads and reports the SERVER's
  // refusal rather than a generic failure, because "nothing happened" is the least
  // diagnosable symptom a page can produce (the lesson the charter save already encodes).
  function write(path, method, payload, onOk, btn) {
    if (btn) btn.disabled = true;
    return fetch(path, {
      method: method,
      headers: authHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify(payload)
    })
      .then(function (r) {
        return r.text().then(function (t) {
          var body = {};
          try { body = JSON.parse(t); } catch (e) { /* a non-JSON body is reported raw */ }
          if (r.status === 401 || r.status === 403) {
            say(body.error || ("Not allowed (" + r.status + ")."), true);
            if (btn) btn.disabled = false;
            return;
          }
          if (!r.ok) {
            say("The server refused it: " + (body.error || t || r.status), true);
            if (btn) btn.disabled = false;
            return;
          }
          onOk(body);
        });
      })
      .catch(function (err) {
        if (btn) btn.disabled = false;
        say("Could not reach the server: " + (err.message || String(err)), true);
      });
  }

  function wireVisibility() {
    var form = document.getElementById("visibility-form");
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var sel = document.getElementById("visibility-select");
      // The rendering is not patched in place: a save that reports success and leaves the
      // old level on screen is a page that contradicts the server. Reload instead.
      write("/api/v1/projects/" + encodeURIComponent(slug) + "/visibility", "PUT",
        { visibility: sel.value },
        function () { say("Visibility set to " + sel.value + "."); reload(); },
        form.querySelector("button"));
    });
  }

  function wireTags() {
    var form = document.getElementById("tags-form");
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var input = document.getElementById("tags-input");
      var names = String(input.value || "").split(",")
        .map(function (s) { return s.trim(); })
        .filter(function (s) { return s.length > 0; });
      if (!names.length) {
        // An empty submit is not a no-op the server can describe: PUT /tags with an
        // empty list succeeds and changes nothing, which looks like a broken button.
        say("Name at least one tag to apply.", true);
        return;
      }
      write("/api/v1/projects/" + encodeURIComponent(slug) + "/tags", "PUT", { tags: names },
        function (body) {
          // The response carries the applied tags: report what STUCK rather than what
          // was asked for, which differ when a tag is already on the project.
          var applied = (body && body.tags) || [];
          say("Applied. This project now has " + applied.length + " tag" +
            (applied.length === 1 ? "" : "s") + ": " + applied.join(", ") + ".");
          reload();
        },
        form.querySelector("button"));
    });
  }

  function wireLanguages() {
    var edit = document.getElementById("languages-edit");
    var form = document.getElementById("languages-form");
    if (edit && form) {
      edit.addEventListener("click", function () {
        var rows = root.querySelectorAll("[data-language]");
        // The form is seeded from what is on screen, so an edit starts from the current
        // numbers instead of an empty box -- the value of a "correctable" field is that
        // the correction is visible.
        var seed = [];
        rows.forEach(function (input) {
          seed.push(input.getAttribute("data-language") + " " + input.value);
        });
        var box = document.getElementById("languages-input");
        if (box) box.value = seed.join(", ");
        form.hidden = false;
        edit.hidden = true;
      });
    }
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var raw = String((document.getElementById("languages-input") || {}).value || "");
      var langs = [], bad = [];
      raw.split(",").forEach(function (chunk) {
        var s = chunk.trim();
        if (!s) return;
        var m = s.match(/^(.*?)[\s=:]+(\d+(?:\.\d+)?)$/);
        if (!m) { bad.push(s); return; }
        langs.push({ language: m[1].trim(), pct: Number(m[2]) });
      });
      if (bad.length) {
        // Refused here, with the offending text, rather than by a server that can only
        // say "n languages expected".
        say("Could not read a percentage from: " + bad.join(", ") +
          ". Use name pct, e.g. \"Rust 70, Go 30\".", true);
        return;
      }
      write("/api/v1/projects/" + encodeURIComponent(slug) + "/languages", "PUT",
        { languages: langs },
        function () {
          say("Languages saved (" + langs.length + ").");
          reload();
        },
        form.querySelector("button"));
    });
  }

  function wireInvites() {
    var form = document.getElementById("invite-form");
    if (form) {
      form.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var days = Number((document.getElementById("invite-days") || {}).value || 0);
        var uses = Number((document.getElementById("invite-uses") || {}).value || 0);
        write("/api/v1/projects/" + encodeURIComponent(slug) + "/invites", "POST", {
          expires_in_seconds: days > 0 ? Math.round(days * 86400) : 0,
          max_uses: uses > 0 ? uses : 0
        }, function (inv) {
          // The token is reported once, in the live region, because the table below is
          // reloaded: a mint that only re-rendered would look identical to one that
          // silently failed.
          say("Invite minted: " + (inv.token || "(no token in the response)"));
          reload();
        }, form.querySelector("button"));
      });
    }
    root.querySelectorAll("[data-revoke]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        write("/api/v1/projects/" + encodeURIComponent(slug) + "/invites/" +
          encodeURIComponent(btn.getAttribute("data-revoke")) + "/revoke", "POST", {},
          function () { say("Invite revoked. The record of it stays."); reload(); }, btn);
      });
    });
  }

  // Re-read the panels after a write, without a document reload (see load()).
  function reload() {
    load();
  }

  function wireConsent() {
    root.querySelectorAll("[data-consent]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-consent");
        btn.disabled = true;
        fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals/" +
              encodeURIComponent(id) + "/consent", { method: "POST", headers: authHeaders({ "Content-Type": "application/json" }) })
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

  // The propose form. `write` already reports the server's refusal in the server's
  // words, and a 201 here means the proposal was OPENED, not that the weight moved --
  // so the message says which of the two happened.
  function wirePropose() {
    var form = document.getElementById("propose-weight-form");
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var fd = new FormData(form);
      var btn = form.querySelector("button[type=submit]");
      write("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals",
        "POST", {
          feature_id: Number(fd.get("feature_id") || 0),
          weight: Number(fd.get("weight") || 0),
          rationale: String(fd.get("rationale") || "").trim()
        },
        function (body) {
          say("Proposal opened. " + (body.message || "It needs other contributors' consent " +
            "before the weight moves."));
          load();
        }, btn);
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

      fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/charter", {
        method: "PUT",
        headers: authHeaders({ "Content-Type": "application/json" }),
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