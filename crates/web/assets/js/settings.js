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

  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it. This file's copy replaced the same
  // five characters in its own way; a single pass over a character class cannot disagree with another page about which are dangerous.
  var el = window.ConcordDom.el;

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
  // (The function that used to sit here, `authHeaders`, read `concord.token` out of localStorage itself. It is gone:
  // `api.js` asks `session.js` for the header, and there is one answer to "which token does this request carry".)

  // One client (review item 9b). The old body threw `path -> 500`, which told a reader nothing the server had not
  // already said better; `ApiError.message` is the server's own sentence. `no-store` still applies — a settings page
  // reading a cached charter is a page showing a number somebody changed.
  function get(path) {
    return window.ConcordApi.get(path, { cache: "no-store" });
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
      keys: ["SolutionCallMinVoters", "SolutionCallConfidence", "SolutionStableHours"]},
    { title: "Themes", note: "§6.2: a ratified theme expires after this many days unless it is renewed.",
      keys: ["ThemeLifetimeDays"] }
  ];


  function say(msg, bad) {
    if (!live) return;
    // The live region, not an inline banner: a save that is refused by the server must
    // be announced, and it happens without a navigation.
    live.replaceChildren(el("p", { class: bad ? "error-state" : "form-note" }, msg));
  }

  // EVERY READ CARRIES THE TOKEN via api.js; the old authHeaders read localStorage itself.
  // cache: "no-store" is hygiene, not the fix -- the fix was re-reading the panels after
  // a write rather than reloading the document, which was measured (mutation-tested).
  function get(path) {
    return window.ConcordApi.get(path, { cache: "no-store" });
  }

  // The charter is 22 scalars rendered from the FIELD LIST in the response: a field
  // added to governance.Charter shows up without a line of JS being touched.
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
      keys: ["SolutionCallMinVoters", "SolutionCallConfidence", "SolutionStableHours"]},
    { title: "Themes", note: "§6.2: a ratified theme expires after this many days unless it is renewed.",
      keys: ["ThemeLifetimeDays"] }
  ];

  function field(name, value) {
    var isBool = typeof value === "boolean";
    var shown = isBool ? (value ? "yes" : "no") : value;
    var id = "charter-" + name;
    return el("div", { class: "field" }, [
      el("label", { class: "form-label", for: id }, name),
      el("input", {
        id: id, name: name, "data-kind": isBool ? "bool" : typeof value, value: shown
      })
    ]);
  }

  function charterForm(c) {
    var groups = [];
    GROUPS.forEach(function (g) {
      // Only render a group whose fields the response actually has, so a field removed
      // from governance.Charter leaves no orphan input behind.
      var present = g.keys.filter(function (k) { return k in c; });
      if (!present.length) return;
      groups.push(el("fieldset", {}, [
        el("legend", {}, g.title),
        el("p", { class: "form-note" }, g.note),
        el("div", { class: "form-grid" }, present.map(function (k) { return field(k, c[k]); }))
      ]));
    });
    // An invisible charter field is the §3.1 defect again, one level up.
    var grouped = {};
    GROUPS.forEach(function (g) { g.keys.forEach(function (k) { grouped[k] = true; }); });
    var rest = Object.keys(c).filter(function (k) { return !grouped[k]; });
    if (rest.length) {
      groups.push(el("fieldset", {}, [
        el("legend", {}, "Other"),
        el("div", { class: "form-grid" }, rest.map(function (k) { return field(k, c[k]); }))
      ]));
    }
    return el("form", { id: "charter-form" }, [
      groups,
      el("div", { class: "detail-actions" },
        el("button", { type: "submit", class: "btn btn-primary" }, "Save charter"))
    ]);
  }

  function memberTable(members) {
    if (!members || !members.length) {
      return el("div", { class: "empty-state" },
        el("p", { class: "empty-state-title" }, "No members yet"));
    }
    var rank = { owner: 5, maintainer: 4, reviewer: 3, contributor: 2, user: 1, guest: 0 };
    var rows = members.slice().sort(function (a, b) {
      var d = (rank[b.role] || 0) - (rank[a.role] || 0);
      return d !== 0 ? d : String(a.username).localeCompare(String(b.username));
    }).map(function (m) {
      return el("tr", {}, [
        el("td", {}, m.username), el("td", {}, m.role),
        el("td", {}, m.is_moderator ? "yes" : "no")
      ]);
    });
    return el("table", { class: "table" }, [
      el("thead", {}, el("tr", {}, [
        el("th", {}, "Member"), el("th", {}, "Role"), el("th", {}, "Moderator")
      ])),
      el("tbody", {}, rows)
    ]);
  }

  function profileList(profiles) {
    if (!profiles || !profiles.length) {
      return el("div", { class: "empty-state" },
        el("p", { class: "empty-state-title" }, "No criteria profiles yet"));
    }
    return el("div", { class: "grid-responsive-2" }, profiles.map(function (p) {
      var name = p.name || p.label || p.slug || "(unnamed)";
      return el("article", { class: "card" }, el("h3", { class: "card-title" }, name));
    }));
  }

  function failed(v) {
    return v && typeof v === "object" && v.__failed;
  }
  function failedNote(v, what) {
    return el("div", { class: "empty-state" }, [
      el("p", { class: "empty-state-title" }, "Could not load " + what),
      el("p", {}, v.__failed)
    ]);
  }
  function whenUnix(seconds) {
    return new Date(seconds * 1000).toISOString().slice(0, 10);
  }

  function panel(title, body) {
    return el("section", { class: "panel" }, [el("h2", {}, title), body]);
  }

  // §4.3: a completeness meter with its components on screen, because a number
  // without its inputs is a number a reader cannot act on.
  function completenessPanel(c) {
    if (failed(c)) return panel("Tag completeness", failedNote(c, "completeness"));
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
    return panel("Tag completeness", [
      el("p", { class: "form-note" }, "A project with no tags is not merely untidy: it is invisible to " +
        "every taxonomy-filtered search, so tagging is a contribution rather than housekeeping."),
      el("div", {
        class: "meter", "data-completeness-score": score,
        "data-completeness-complete": c.complete ? "true" : "false",
        role: "img", "aria-label": "tag completeness " + pct + " percent"
      }, el("div", { class: "meter-fill", style: "width:" + pct + "%" })),
      el("p", {}, [
        el("strong", { "data-completeness-pct": "" }, pct + "%"),
        " complete — " + tags + " of " + want + " tags, " + ns + " of " + wantNs + " namespaced."
      ]),
      el("p", { class: "form-note" }, advice +
        (pending > 0 ? " " + pending + " suggested tag" + (pending === 1 ? " is" : "s are") +
          " still waiting to be applied." : ""))
    ]);
  }

  // §6.2: strategic weights move only by consensus. Every control below opens a
  // proposal; none of them applies a value directly.
  function strategyPanel(s) {
    if (failed(s)) return panel("Strategic weights", failedNote(s, "strategic weights"));
    var proposals = s.proposals || [];
    var themes = s.themes || [];
    var themeProposals = s.themeProposals || [];
    var features = (s.features && s.features.features) || s.features || [];
    if (!Array.isArray(features)) features = [];

    var propose = el("form", { id: "propose-weight-form", class: "card" }, [
      el("h3", { class: "form-title" }, "Propose a weight change"),
      el("p", { class: "form-note" }, "The proposal opens a decision other contributors consent to; " +
        "the feature's weight does not move until that quorum is reached. The proposer's own " +
        "consent does not count toward it."),
      features.length
        ? el("div", { class: "field" }, [
            el("label", { class: "form-label", for: "weight-feature" }, "Feature"),
            el("select", { id: "weight-feature", name: "feature_id", required: true },
              features.map(function (f) {
                return el("option", { value: f.id }, "#" + f.id + " " + (f.title || "untitled") +
                  " (weight " + (f.strategic_weight == null ? 1 : f.strategic_weight) + ")");
              }))
          ])
        : el("p", { class: "form-note" }, "This project has no features yet, so there is nothing to weigh."),
      el("div", { class: "field-row" },
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "weight-value" }, "Proposed weight"),
          el("input", { id: "weight-value", name: "weight", type: "number", step: "0.1", min: "0", value: "1.5", required: true })
        ])),
      el("div", { class: "field" }, [
        el("label", { class: "form-label", for: "weight-rationale" }, "Why"),
        el("input", { id: "weight-rationale", name: "rationale", required: true, maxlength: "300",
          placeholder: "what makes this one more important than it scores" })
      ]),
      el("div", { class: "detail-actions" },
        el("button", { type: "submit", class: "btn btn-primary", disabled: features.length ? null : true },
          "Open the proposal")),
      el("p", { class: "form-status", "data-status": "", hidden: true })
    ]);

    var pending = proposals.length
      ? el("table", { class: "table", "data-strategy-proposals": "" }, [
          el("thead", {}, el("tr", {}, [
            el("th", {}, "Feature"), el("th", {}, "Weight"), el("th", {}, "Rationale"), el("th", {})
          ])),
          el("tbody", {}, proposals.map(function (p) {
            return el("tr", { "data-proposal": p.id }, [
              el("td", {}, "#" + p.feature_id),
              el("td", {}, p.weight_before + " → " + p.weight_after),
              el("td", {}, p.rationale),
              el("td", {}, [
                el("button", { type: "button", class: "btn", "data-consent": p.id }, "Consent"),
                el("button", { type: "button", class: "btn btn-quiet", "data-withdraw-weight": p.id }, "Withdraw")
              ])
            ]);
          }))
        ])
      : el("div", { class: "empty-state" }, [
          el("p", { class: "empty-state-title" }, "No weight proposals open"),
          el("p", {}, "Nobody can pin an item to the top by hand. Steering happens through pain, " +
            "pairwise votes, and ratified themes.")
        ]);

    var liveThemes = themes.length
      ? el("table", { class: "table", "data-strategy-themes": "" }, [
          el("thead", {}, el("tr", {}, [
            el("th", {}, "Theme"), el("th", {}, "Weight"), el("th", {}, "Rationale"),
            el("th", {}, "Expires"), el("th", {})
          ])),
          el("tbody", {}, themes.map(function (t) {
            // §366: a theme that shows its weight but not its expiry looks permanent.
            return el("tr", { "data-theme": t.tag }, [
              el("td", {}, t.namespace ? t.namespace + "/" + t.tag : t.tag),
              el("td", {}, t.weight),
              el("td", {}, t.rationale),
              el("td", {}, t.expires_at ? whenUnix(t.expires_at) : ""),
              el("td", {},
                el("button", { type: "button", class: "btn btn-quiet", "data-renew": t.tag }, "Renew"))
            ]);
          }))
        ])
      : el("div", { class: "empty-state" }, [
          el("p", { class: "empty-state-title" }, "No ratified themes"),
          el("p", {}, "A theme is a weight on every feature carrying its tag, and it expires rather " +
            "than being switched off by hand.")
        ]);

    var lifetime = (s.charter && s.charter.ThemeLifetimeDays) || 90;
    var themePropose = el("form", { id: "propose-theme-form", class: "card" }, [
      el("h3", { class: "form-title" }, "Propose a theme"),
      el("p", { class: "form-note" }, "A theme is a weight on a TAG, so \"security-hardening: +2\" moves " +
        "every feature carrying that tag. It is consensus-ratified like a weight, and it " +
        "expires: a theme nobody renews stops counting " + lifetime + " days after it " +
        "is ratified (the charter's theme lifetime). Nothing changes until the quorum " +
        "consents."),
      el("div", { class: "field-row" }, [
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "theme-tag" }, "Tag"),
          el("input", { id: "theme-tag", name: "theme_tag", required: true, maxlength: "60",
            placeholder: "an existing tag, e.g. security-hardening" })
        ]),
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "theme-weight" }, "Weight"),
          el("input", { id: "theme-weight", name: "weight", type: "number", step: "0.1", min: "-2", max: "3",
            value: "1", required: true })
        ])
      ]),
      el("div", { class: "field" }, [
        el("label", { class: "form-label", for: "theme-rationale" }, "Why"),
        el("input", { id: "theme-rationale", name: "rationale", required: true, maxlength: "300",
          placeholder: "why this tag should steer the roadmap" })
      ]),
      el("div", { class: "detail-actions" },
        el("button", { type: "submit", class: "btn btn-primary" }, "Open the proposal")),
      el("p", { class: "form-status", "data-status": "", hidden: true })
    ]);

    var themePending = themeProposals.length
      ? el("table", { class: "table", "data-theme-proposals": "" }, [
          el("thead", {}, el("tr", {}, [
            el("th", {}, "Tag"), el("th", {}, "Weight"), el("th", {}, "Rationale"),
            el("th", {}, "Status"), el("th", {})
          ])),
          el("tbody", {}, themeProposals.map(function (p) {
            var open = p.status === "pending";
            return el("tr", { "data-theme-proposal": p.id }, [
              el("td", {}, p.theme_namespace ? p.theme_namespace + "/" + p.theme_tag : p.theme_tag),
              el("td", {}, (p.weight_before == null ? "none" : p.weight_before) + " → " + p.weight_after),
              el("td", {}, p.rationale),
              el("td", {}, p.status),
              el("td", {}, open ? [
                el("button", { type: "button", class: "btn", "data-consent-theme": p.id }, "Consent"),
                el("button", { type: "button", class: "btn btn-quiet", "data-withdraw-theme": p.id }, "Withdraw")
              ] : null)
            ]);
          }))
        ])
      : el("div", { class: "empty-state" }, [
          el("p", { class: "empty-state-title" }, "No theme proposals"),
          el("p", {}, "A proposal opens a decision: a tag's weight moves only once the project " +
            "consents, and the proposer's own consent does not count.")
        ]);

    return panel("Strategic weights", [
      el("p", { class: "form-note" }, "A weight proposal is ratified by consensus: contributors " +
        "consent to it, the proposer cannot consent to their own, and the feature's weight " +
        "does not move until the quorum is reached."),
      propose,
      el("h3", {}, "Open proposals"), pending,
      el("h3", {}, "Themes"),
      el("p", { class: "form-note" }, "A theme applies to every feature carrying its tag and counts " +
        "toward that feature's priority beside its own weight. It lapses on the date shown " +
        "unless it is renewed."),
      themePropose, themePending, liveThemes
    ]);
  }

  function getOrNull(path) {
    return get(path).catch(function (e) { return { __failed: e.message || String(e) }; });
  }

  // Only the four levels the store accepts (store.ValidVisibility), each with its own reason.
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
    if (failed(p)) return panel("Visibility", failedNote(p, "visibility"));
    var current = String(p.visibility || "");
    var known = VISIBILITIES.some(function (v) { return v.value === current; });
    var note = (VISIBILITIES.filter(function (v) { return v.value === current; })[0] || {}).note || "";
    return panel("Visibility", [
      el("p", { class: "form-note" }, "Who can see this project. A member of it is the only one who can change this: " +
        "anyone able to reach this control could hide a project they do not own."),
      known ? null : el("p", { class: "form-note error-state" },
        "The server reports a level this page does not know (" + current + "), so the select below " +
        "shows no current value rather than a wrong one."),
      el("form", { id: "visibility-form" }, [
        el("p", {}, [
          "Currently ", el("strong", { "data-visibility-current": "" }, current),
          note ? " — " : null,
          note ? el("span", { "data-visibility-note": "" }, note) : null
        ]),
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "visibility-select" }, "Set visibility"),
          el("select", { id: "visibility-select", name: "visibility" },
            VISIBILITIES.map(function (v) {
              return el("option", { value: v.value, selected: v.value === current ? true : null }, v.label);
            }))
        ]),
        el("div", { class: "detail-actions" },
          el("button", { type: "submit", class: "btn btn-primary" }, "Save visibility"))
      ])
    ]);
  }

  function tagsPanel(t) {
    if (failed(t)) return panel("Tags", failedNote(t, "tags"));
    var tags = (t && t.tags) || [];
    var list = tags.length
      ? el("ul", { class: "tag-list", "data-project-tags": "" }, tags.map(function (name) {
          return el("li", { class: "tag" }, name);
        }))
      : el("div", { class: "empty-state", "data-project-tags": "" }, [
          el("p", { class: "empty-state-title" }, "No tags yet"),
          el("p", {}, "A project with no tags is invisible to every taxonomy-filtered search, so " +
            "tagging it is the difference between being findable and not.")
        ]);
    return panel("Tags", [
      el("p", { class: "form-note" }, "Tags are the shared vocabulary the taxonomy is built on. Applying one " +
        "requires contributor standing and an existing tag: this route attaches tags that exist " +
        "rather than minting new ones, because a new tag is a proposal with a quorum (§4.3)."),
      list,
      el("form", { id: "tags-form" }, [
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "tags-input" }, "Apply tags"),
          el("input", { id: "tags-input", name: "tags", placeholder: "self-hosted, kanban, rust" }),
          el("p", { class: "form-note" }, "Comma-separated. A tag this instance does not have yet is refused " +
            "with the server's reason, and the ones that applied before it stay applied.")
        ]),
        el("div", { class: "detail-actions" },
          el("button", { type: "submit", class: "btn btn-primary" }, "Apply tags"))
      ])
    ]);
  }

  // §15: collaboratively correctable percentages, every row visible so a correction
  // is a correction of a number somebody can see.
  function languagesPanel(l) {
    if (failed(l)) return panel("Languages", failedNote(l, "languages"));
    var langs = (l && l.languages) || [];
    var body = langs.length
      ? el("div", { "data-project-languages": "" }, langs.map(function (x, i) {
          return el("div", { class: "field lang-row" }, [
            el("label", { class: "form-label", for: "lang-" + i }, x.language),
            el("input", { id: "lang-" + i, name: "language", "data-language": x.language,
              "data-kind": "number", value: x.pct })
          ]);
        }))
      : el("div", { class: "empty-state", "data-project-languages": "" }, [
          el("p", { class: "empty-state-title" }, "No language breakdown yet"),
          el("p", {}, "Discovery filters on language, so an unset project " +
            "is missing from every language-filtered search.")
        ]);
    return panel("Languages", [
      el("p", { class: "form-note" }, "The share of each language, as percentages. §15 makes these " +
        "collaboratively correctable: the forge's own count is a starting point, not a verdict."),
      body,
      el("div", { class: "detail-actions" },
        el("button", { type: "button", class: "btn", id: "languages-edit" },
          langs.length ? "Edit percentages" : "Add languages")),
      el("form", { id: "languages-form", hidden: true }, [
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "languages-input" }, "Languages"),
          el("input", { id: "languages-input", name: "languages", placeholder: "Rust 70, Go 30" }),
          el("p", { class: "form-note" }, "Comma-separated name pct pairs. The server refuses a " +
            "total above 100, naming the total it computed.")
        ]),
        el("div", { class: "detail-actions" },
          el("button", { type: "submit", class: "btn btn-primary" }, "Save languages"))
      ])
    ]);
  }

  // R16 §3.1: the labels spell out the governance choice, not just the column value.
  function intakePanel(project) {
    var mode = (project && project.intake_mode) || "complaint_first";
    var labels = {
      complaint_first: "Complaint-first — every feature traces to reported pain (the default)",
      idea_first: "Idea-first — ideas may be filed before anybody is hurt",
      both: "Both — either is accepted"
    };
    return panel("Intake", [
      el("p", { class: "form-note" }, "The intake mode decides what counts as legitimate input on the " +
        "filing form. Whichever it is, a feature still needs evidence before it can reach " +
        "ready — the difference is whether an idea may be FILED without a complaint."),
      el("div", { class: "field" }, [
        el("label", { class: "form-label", for: "intake-mode" }, "Intake mode"),
        el("select", { id: "intake-mode", "data-intake-mode": mode },
          Object.keys(labels).map(function (k) {
            return el("option", { value: k, selected: k === mode ? true : null }, labels[k]);
          })),
        el("p", { class: "form-note" }, ["Currently ",
          el("code", { "data-intake-current": "" }, mode), "."])
      ]),
      el("div", { class: "detail-actions" },
        el("button", { type: "button", class: "btn btn-primary", "data-intake-save": "" }, "Save intake mode")),
      el("p", { class: "form-status", "data-intake-status": "", hidden: true })
    ]);
  }

  function invitesPanel(inv) {
    if (failed(inv)) {
      return panel("Invites", [
        el("div", { class: "empty-state" }, [
          el("p", { class: "empty-state-title" }, "Not available"),
          el("p", {}, "Invites are visible to members of this project. " + inv.__failed)
        ])
      ]);
    }
    var invites = inv || [];
    var table = invites.length
      ? el("table", { class: "table", "data-invites": "" }, [
          el("thead", {}, el("tr", {}, [
            el("th", {}, "Token"), el("th", {}, "Uses"), el("th", {}, "State"), el("th", {})
          ])),
          el("tbody", {}, invites.map(function (v) {
            var state = v.revoked_at ? "revoked"
              : (v.expires_at && v.expires_at * 1000 < Date.now() ? "expired" : "active");
            return el("tr", { "data-invite": v.id }, [
              el("td", {}, el("code", { "data-invite-token": "" }, v.token)),
              el("td", {}, v.uses + (v.max_uses ? " / " + v.max_uses : " / unlimited")),
              el("td", {}, state),
              el("td", {}, state === "active"
                ? el("button", { type: "button", class: "btn btn-danger", "data-revoke": v.id }, "Revoke")
                : null)
            ]);
          }))
        ])
      : el("div", { class: "empty-state", "data-invites": "" }, [
          el("p", { class: "empty-state-title" }, "No invites"),
          el("p", {}, "A protected project is joined by redeeming an invite; without one, nobody new can get in.")
        ]);
    return panel("Invites", [
      el("p", { class: "form-note" }, "An invite is the credential a protected project hands out. " +
        "Revoking keeps the record of it rather than deleting it, so the history of who was " +
        "invited survives the revocation."),
      table,
      el("form", { id: "invite-form" }, [
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "invite-days" }, "Expires in (days)"),
          el("input", { id: "invite-days", name: "days", value: "7" }),
          el("p", { class: "form-note" }, "0 means no expiry.")
        ]),
        el("div", { class: "field" }, [
          el("label", { class: "form-label", for: "invite-uses" }, "Maximum uses"),
          el("input", { id: "invite-uses", name: "uses", value: "1" }),
          el("p", { class: "form-note" }, "0 means unlimited.")
        ]),
        el("div", { class: "detail-actions" },
          el("button", { type: "submit", class: "btn btn-primary" }, "Mint an invite"))
      ]),
      el("p", { class: "form-note" }, "Redeeming needs a signed-in account: it is " +
        "POST /api/v1/auth/redeem-invite with the token. There is no redeem page " +
        "yet, which is why the token is shown here rather than as a clickable link.")
    ]);
  }

  function render(charter, members, profiles, completeness, strategy, project, tags, languages, invites) {
    // The charter reaches the strategy panel because §6.2's themes expire on the charter's
    // release cycle; a user deciding whether to renew needs the number in front of them.
    if (strategy && !strategy.__failed) strategy.charter = failed(charter) ? null : charter;
    root.replaceChildren(
      panel("Charter", failed(charter) ? failedNote(charter, "charter") : charterForm(charter || {})),
      panel("Members", memberTable(members)),
      panel("Criteria profiles", profileList(profiles)),
      completenessPanel(completeness),
      strategyPanel(strategy),
      intakePanel(project),
      visibilityPanel(project),
      tagsPanel(tags),
      languagesPanel(languages),
      invitesPanel(invites)
    );
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
    // Four of these routes are owner-only, and a signed-out reader asking them gets four 401s for panels that cannot
    // render anything but empty anyway: `probe_requests.py` saw exactly four 401s on an anonymous crawl. Asking only when
    // there is a session keeps the crawl honest — a 401 there would mean a route that broke, not one that was never ours
    // to read. `members` stays unconditional: a member list is public on this product.
    var signedIn = !!(window.ConcordSession && window.ConcordSession.instance().signedIn());
    var mine = function (path) { return signedIn ? getOrNull(path) : Promise.resolve(null); };
    return Promise.all([
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/charter"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/members"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/criteria-profiles"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/tags/completeness"),
      Promise.all([
        mine("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals"),
        mine("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/themes"),
        // The propose form needs the features to choose from. A number input asking
        // for a feature id would be a control that requires the reader to know
        // something the page is holding.
        getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/features"),
        // §6.2's themes: the proposals beside the live weights, so a reader can see a
        // pending change to a tag and consent to it. The live list alone shows only what
        // already won.
        mine("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/theme-proposals")
      ]).then(function (s) {
        return { proposals: s[0], themes: s[1], features: s[2], themeProposals: s[3] };
      }),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug)),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/tags"),
      getOrNull("/api/v1/projects/" + encodeURIComponent(slug) + "/languages"),
      mine("/api/v1/projects/" + encodeURIComponent(slug) + "/invites")
    ]).then(function (res) {
      render(res[0], res[1], res[2], res[3], res[4], res[5], res[6], res[7], res[8]);
      wireSave();
      wireConsent();
      wirePropose();
      wireThemes();
      wireIntake();
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
  // Both consent controls answer with the same shape — `recorded`, `ratified`, and the tally the decision turns on — so
  // they get one sentence rather than two that can disagree about what "consent recorded" means.
  function consentSentence(body) {
    if (!body) return "Consent recorded.";
    var tally = (body.consents !== undefined && body.eligible !== undefined)
      ? " " + body.consents + " of " + body.eligible + " eligible consents so far."
      : "";
    return body.ratified
      ? "Consent recorded and the quorum is reached: ratified." + tally
      : "Consent recorded. The quorum is not reached yet." + tally;
  }

  function write(path, method, payload, onOk, btn) {
    if (btn) btn.disabled = true;
    // One client (review item 9b). The three branches below are the three answers a write can get, and they are now read
    // off the error model instead of a status and a hand-parsed body: `auth` is 401, a role refusal is a 403 status, and
    // anything else that reached here never left the browser.
    return window.ConcordApi.request(method, path, payload)
      .then(function (body) { onOk(body); })
      .catch(function (err) {
        if (btn) btn.disabled = false;
        var status = err && err.status ? err.status : 0;
        if (status === 401 || status === 403) {
          say((err && err.message) || ("Not allowed (" + status + ")."), true);
        } else if (status) {
          say("The server refused it: " + ((err && err.message) || status), true);
        } else {
          say("Could not reach the server: " + ((err && err.message) || String(err)), true);
        }
      });
  }

  // The intake mode is a governance column, so this is a WRITE with the server's
  // refusal reported verbatim: a maintainer who is not a maintainer should learn
  // that from the response, not from a control that silently does nothing.
  function wireIntake() {
    var btn = document.querySelector("[data-intake-save]");
    if (!btn) return;
    btn.addEventListener("click", function () {
      var sel = document.getElementById("intake-mode");
      var note = document.querySelector("[data-intake-status]");
      write("/api/v1/projects/" + encodeURIComponent(slug) + "/intake-mode", "PUT",
        { mode: sel.value },
        function () {
          if (note) {
            note.hidden = false;
            note.textContent = "Intake mode set to " + sel.value + ".";
          }
          reload();
        },
        btn);
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
        // One client (review item 9b). `202` was a branch because the server answers it when the quorum is not yet reached;
        // it is still a success and still carries the message that says which of the two happened.
        window.ConcordApi.post("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/weight-proposals/" +
              encodeURIComponent(id) + "/consent", {})
          .then(function (body) {
            // The server reports the tally, so the page quotes it. `ratified` is the route's own answer; a page that
            // inferred it from the status code would be deciding the rule itself.
            say(consentSentence(body));
          })
          .catch(function (err) {
            btn.disabled = false;
            say("Could not reach the server: " + (err.message || String(err)), true);
          });
      });
    });
  }

  // The theme half of the strategy panel: propose, consent, withdraw, renew.
  //
  // 202 and 200 must not be collapsed. A 202 means the consent was RECORDED and the quorum
  // is not reached; reporting it as ratified tells a user the roadmap moved when nothing
  // did. The e2e mutant that treats the two alike is why this branch is spelled out.
  function wireThemes() {
    var form = document.getElementById("propose-theme-form");
    if (form) {
      form.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var fd = new FormData(form);
        var btn = form.querySelector("button[type=submit]");
        write("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/themes",
          "POST", {
            theme_tag: String(fd.get("theme_tag") || "").trim(),
            theme_namespace: "topic",
            weight: Number(fd.get("weight") || 0),
            rationale: String(fd.get("rationale") || "").trim()
          },
          function (body) {
            say("Theme proposed. " + (body.message ||
              "It takes effect when the project consents."));
            load();
          }, btn);
      });
    }
    root.querySelectorAll("[data-consent-theme]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var id = btn.getAttribute("data-consent-theme");
        btn.disabled = true;
        window.ConcordApi.post("/api/v1/projects/" + encodeURIComponent(slug) + "/strategy/themes/" +
              encodeURIComponent(id) + "/consent", {})
          .then(function (body) {
            // The server answers 202 while the quorum is unmet and 200 once it holds; both are "recorded", and the
            // tally it returns says which.
            say(body && body.ratified
              ? "Ratified. The theme is now in force."
              : consentSentence(body));
            if (body && body.ratified) load();
          })
          .catch(function (err) {
            var status = err && err.status ? err.status : 0;
            if (status === 401 || status === 403) {
              say((err && err.message) || ("Not allowed to consent (" + status + ")."), true);
            } else if (status) {
              say("The server refused the consent: " + ((err && err.message) || status), true);
            } else {
              say("Could not reach the server: " + ((err && err.message) || String(err)), true);
            }
            btn.disabled = false;
          });
      });
    });
    // One withdrawal wiring for both halves: the theme routes and the weight routes differ
    // only in the path.
    function withdrawAll(attr, path, note) {
      root.querySelectorAll("[" + attr + "]").forEach(function (btn) {
        btn.addEventListener("click", function () {
          var id = btn.getAttribute(attr);
          btn.disabled = true;
          window.ConcordApi.post("/api/v1/projects/" + encodeURIComponent(slug) + path +
                encodeURIComponent(id) + "/withdraw", {})
            .then(function () {
              say(note);
              load();
            })
            .catch(function (err) {
              var status = err && err.status ? err.status : 0;
              if (status) {
                say("The server refused the withdrawal: " + ((err && err.message) || status), true);
              } else {
                say("Could not reach the server: " + ((err && err.message) || String(err)), true);
              }
              btn.disabled = false;
            });
        });
      });
    }
    withdrawAll("data-withdraw-theme", "/strategy/themes/",
      "Proposal withdrawn. No weight changed.");
    // The weight proposals' withdrawal: a store function that existed with no caller, so a
    // proposer who changed their mind had no way to cancel one.
    withdrawAll("data-withdraw-weight", "/strategy/weight-proposals/",
      "Proposal withdrawn. The weight did not move.");
    // Renew fills the propose form for a tag already in force. The weight field is left
    // alone on purpose: pre-filling it invites renewing at the current value without anyone
    // deciding what it should be now.
    root.querySelectorAll("[data-renew]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var tag = btn.getAttribute("data-renew");
        var form = document.getElementById("propose-theme-form");
        if (!form) return;
        form.querySelector("#theme-tag").value = tag;
        form.querySelector("#theme-rationale").focus();
        say("Renewing " + tag + ": say why it should stay in force, then open the proposal.");
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

      // Read the VALUES BACK OUT OF THE FORM into the shape the server expects — the same object it just sent us,
      // `snake_case` and all (`governance::Charter` has no `rename_all`, so the rendered `name` IS the wire key; this
      // comment said "PascalCase" for as long as it was copied from the Go page). Sending a hand-written object would
      // mean a field added to the charter is silently dropped on save -- and the page would show the new value until
      // the next reload, then revert it.
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

      // One client (review item 9b): the token is `session.js`'s, the parse is `api.js`'s, and an anonymous save is a
      // plain request that gets a plain 401 rather than a header of "Bearer null".
      window.ConcordApi.put("/api/v1/projects/" + encodeURIComponent(slug) + "/charter", payload)
        .then(function () {
          say("Charter saved.");
        })
        .catch(function (err) {
          var status = err && err.status ? err.status : 0;
          if (status === 401 || status === 403) {
            // Named explicitly because "save did nothing" is the least diagnosable symptom there is.
            say("Not allowed to change the charter. Sign in as a maintainer.", true);
          } else if (status) {
            say("The server refused the charter: " + ((err && err.message) || status), true);
          } else {
            say("Could not reach the server: " + ((err && err.message) || String(err)), true);
          }
        });
    });
  }
})();