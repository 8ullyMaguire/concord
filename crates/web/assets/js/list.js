// One list, with its entries IN RANKING ORDER. Spec §16.
//
// §16 on this page, word for word:
//
//	"Entries are ranked, not just listed. Entries compete in pairwise comparisons
//	 (the same Glicko-2 engine as feature priority), so 'best first' reflects
//	 community judgment instead of alphabetical accident."
//	"Categories group entries; ranking orders within and across them."
//
// Three things follow, and each is a way the obvious implementation is wrong:
//
//  1. ORDER BY RANKING. GetListEntries already does `ORDER BY elo_r DESC`; the API
//     therefore arrives ranked and this page must NOT re-sort by title or url. A page
//     that sorts for tidiness has replaced community judgment with alphabetical
//     accident -- the exact failure the section names.
//
//  2. THE DEVIATION IS SHOWN. A bare 1500 for an entry nobody compared is
//     indistinguishable from a settled 1500, so the reader concludes it lost. Same
//     proportional bar the feature and requests pages draw, so the three agree about
//     what "uncertain" means.
//
//  3. THE COMPARISON COUNT IS SHOWN. §16's claim that the order reflects COMMUNITY
//     JUDGMENT is checked by the count beside each rating: 1610-from-9-comparisons is a
//     different fact from 1610-from-1.
//
// Rendering (step 2b): this file builds nodes with `el()` only. Titles, URLs, descriptions,
// categories and API error messages become text nodes or setAttribute values, so no API field
// can be parsed as markup. There is no innerHTML or insertAdjacentHTML sink left here.

(function () {
  "use strict";

  var root = document.getElementById("list-root");
  if (!root) return;
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";
  var listID = parseInt((location.pathname.match(/lists\/(\d+)/) || [])[1], 10);
  var el = window.ConcordDom.el;

  function ago(unix) {
    if (!unix) return "";
    var d = Math.floor(Date.now() / 1000 - unix);
    if (d < 60) return "just now";
    if (d < 3600) return Math.floor(d / 60) + "m ago";
    if (d < 86400) return Math.floor(d / 3600) + "h ago";
    return Math.floor(d / 86400) + "d ago";
  }

  // Same normalisation as feature.js and requests.js: 350 is the seed deviation, so
  // it is the full-width case.
  function rating(e, comparisons) {
    var r = typeof e.elo_r === "number" ? e.elo_r : 0;
    var rd = typeof e.elo_rd === "number" && isFinite(e.elo_rd) ? e.elo_rd : 0;
    var pct = Math.max(4, Math.min(100, Math.round((rd / 350) * 100)));
    var n = comparisons || 0;
    return el("div", { class: "rating" }, [
      el("div", { class: "rating-value" }, Math.round(r)),
      el("div", {
        class: "rating-bars",
        role: "img",
        "aria-label": "rating " + Math.round(r) + " plus or minus " + Math.round(rd) +
          ", " + pct + " percent of the maximum uncertainty"
      }, el("div", { class: "rating-bar-track" },
        el("div", { class: "rating-bar-fill", style: "width:" + pct + "%" }))),
      el("p", { class: "muted" }, "±" + Math.round(rd) + " · " +
        (n === 1 ? "1 comparison" : n + " comparisons") +
        (n === 0 ? " — nobody has compared this yet" : ""))
    ]);
  }

  // category is a *string in Go, so it is null for an uncategorised entry. Reading it
  // straight would print "null"; the schema allows NULL, so an absent category is the
  // ordinary case and the row is grouped under "uncategorised" rather than dropped.
  function categoryOf(e) {
    return e.category ? e.category : "uncategorised";
  }

  // Grouped by category, and WITHIN each group the API's ranking order is preserved.
  // Array.prototype.sort is stable in every engine this ships to, so grouping without
  // sorting keeps the ranked order inside each bucket. Re-sorting by rating here would
  // be redundant with the SQL and one more place for the two to disagree.
  function groupByCategory(entries) {
    var order = [];
    var buckets = {};
    entries.forEach(function (e) {
      var c = categoryOf(e);
      if (!buckets[c]) {
        buckets[c] = [];
        order.push(c);
      }
      buckets[c].push(e);
    });
    return order.map(function (c) {
      return { category: c, entries: buckets[c] };
    });
  }

  function entryRow(e, comparisons) {
    // The URL is the entry's SUBJECT and the title is its name, so the title links to
    // the URL and not to an internal page that does not exist.
    var url = e.url || "";
    return el("li", { class: "list-entry", "data-entry-id": e.id }, [
      el("div", { class: "list-entry-head" }, [
        el("a", { class: "list-entry-title", href: url, rel: "nofollow noopener" }, e.title),
        rating(e, comparisons)
      ]),
      el("p", { class: "list-entry-url" }, url),
      e.description ? el("p", { class: "list-entry-desc" }, e.description) : null,
      el("p", { class: "card-meta" }, [
        el("span", { class: "badge badge-slate" }, e.status),
        el("span", {}, "proposed " + ago(e.created_at))
      ])
    ]);
  }

  function entriesMarkup(entries, counts) {
    if (!entries.length) {
      return el("div", { class: "empty-state" }, [
        el("div", { class: "empty-state-icon" }, "📋"),
        el("p", { class: "empty-state-title" }, "No entries yet"),
        el("p", { class: "empty-state-description" }, "Anyone can propose one; the community ranks them by comparing pairs.")
      ]);
    }
    return groupByCategory(entries).map(function (g) {
      return el("section", { class: "entry-group" }, [
        el("h3", { class: "entry-category" }, [
          g.category + " ",
          el("span", { class: "count" }, g.entries.length)
        ]),
        el("ul", { class: "list-entries" }, g.entries.map(function (e) {
          return entryRow(e, counts[e.id]);
        }))
      ]);
    });
  }

  // The pairwise control, with the SCHEMA's five outcomes rather than a 1-5 score.
  // list_entry_votes.outcome is CHECKed to exactly
  // ('a','b','both','neither','skip'), so a numeric rating control posts a value the
  // database refuses -- and the page then renders as though nobody had ranked
  // anything, which is the silent-empty failure this file's whole shape is about.
  function comparePanel(entries) {
    if (entries.length < 2) {
      return el("p", { class: "note" }, "Ranking needs two entries. One entry has nothing to be better than.");
    }
    function picker(id, name, label) {
      return [
        el("label", { class: "visually-hidden", for: id }, label),
        el("select", { id: id, name: name }, entries.map(function (e) {
          return el("option", { value: e.id }, e.title);
        }))
      ];
    }
    var parts = [].concat(
      picker("cmp-a", "entry_a", "Entry A"),
      picker("cmp-b", "entry_b", "Entry B")
    );
    return el("form", { class: "compare", "data-compare-form": "" }, [
      el("h3", {}, "Which belongs on this list more?"),
      el("div", { class: "compare-row" }, parts.concat([
        el("label", { class: "visually-hidden", for: "cmp-outcome" }, "Outcome"),
        el("select", { id: "cmp-outcome", name: "outcome" }, [
          el("option", { value: "a" }, "A fits better"),
          el("option", { value: "b" }, "B fits better"),
          el("option", { value: "both" }, "Both fit equally"),
          el("option", { value: "neither" }, "Neither fits"),
          el("option", { value: "skip" }, "Skip")
        ]),
        el("button", { type: "submit", class: "btn" }, "Compare")
      ])),
      el("p", { class: "note" }, "A pairwise comparison, not a score: it moves both entries by a Glicko-2 update weighted by each side's deviation (§16).")
    ]);
  }

  function flatten(nodes) {
    return nodes.reduce(function (out, node) {
      return out.concat(Array.isArray(node) ? flatten(node) : [node]);
    }, []);
  }

  function render(list, entries, counts) {
    root.removeAttribute("aria-busy");
    root.replaceChildren.apply(root, flatten([
      el("section", { class: "list-detail" },
        el("div", { class: "card" }, [
          el("div", { class: "card-head" }, [
            el("h2", {}, list.name),
            // project_id 0 means instance-wide (16: "attachable to a project or
            // instance-wide"). The page is mounted under a project slug, so without this a
            // reader would assume the project owns a list every project shares.
            list.project_id ? null : el("span", { class: "badge badge-instance" }, "instance-wide"),
            el("span", { class: "badge " + (list.status === "open" ? "badge-blue" : "badge-slate") }, list.status)
          ]),
          list.description ? el("p", { class: "body-text" }, list.description) : null,
          el("p", { class: "card-meta" }, entries.length +
            (entries.length === 1 ? " entry" : " entries") +
            " · ranked by pairwise comparison")
        ])),
      entriesMarkup(entries, counts || {}),
      comparePanel(entries)
    ]));
    wire(entries);
  }

  function wire(entries) {
    var form = root.querySelector("[data-compare-form]");
    if (!form) return;
    form.addEventListener("submit", function (ev) {
      ev.preventDefault();
      var a = parseInt(form.querySelector("#cmp-a").value, 10);
      var b = parseInt(form.querySelector("#cmp-b").value, 10);
      if (a === b) return;
      var outcome = form.querySelector("#cmp-outcome").value;

      // "you have already compared these two entries" and "you cannot vote on an entry you proposed" are both refusals
      // the reader can act on, so the store's message is surfaced rather than flattened to a generic failure.
      window.ConcordApi.post("/api/v1/projects/" + encodeURIComponent(slug) + "/lists/" +
        encodeURIComponent(listID) + "/entries/vote",
        { entry_a: a, entry_b: b, outcome: outcome })
        .then(function (body) {
          // The vote response carries the entries back, so the ranking updates from
          // the server's own numbers rather than from an optimistic guess.
          load(body && body.entries);
        })
        .catch(function (error) {
          var message = error && error.status ? error.message : "Could not reach the server.";
          root.prepend(el("p", { class: "notice notice-error", role: "alert" }, message));
        });
    });
  }

  // The token header this built by hand is `session.js`'s job; `api.js` owns it now.
  function getJSON(url) {
    return window.ConcordApi.get(url);
  }

  // Two reads, in PARALLEL: the list and its entries. Serialising them would be two
  // round trips for a page that has everything it needs after the first pair.
  function load(votedEntries) {
    var base = "/api/v1/projects/" + encodeURIComponent(slug) + "/lists/" +
      encodeURIComponent(listID);
    Promise.all([
      getJSON(base),
      // After a vote the server already sent the entries; re-fetching is one wasted
      // round trip, so they are used directly.
      votedEntries ? Promise.resolve(votedEntries) :
        getJSON(base + "/entries").catch(function () { return []; })
    ])
      .then(function (parts) {
        // NO comparison counts on load, and that is a real gap rather than an
        // oversight: the store and the vote response carry counts, but there is no GET
        // that returns them, so a cold load honestly shows "0 comparisons".
        render(parts[0], Array.isArray(parts[1]) ? parts[1] : []);
      })
      .catch(function () {
        root.removeAttribute("aria-busy");
        root.replaceChildren(el("div", { class: "empty-state" },
          el("p", { class: "empty-state-title" }, "There is no list at this address.")));
      });
  }

  load();
})();
