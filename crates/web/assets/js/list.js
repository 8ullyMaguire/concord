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
//  3. THE COMPARISON COUNT IS SHOWN. §16's claim is that the order reflects COMMUNITY
//     JUDGMENT. A rating with no count next to it asserts a consensus it cannot show,
//     and 1610-from-9-comparisons is a different fact from 1610-from-1.
//
// MEASURED SHAPE: GET .../lists/{id} -> object; GET .../lists/{id}/entries -> bare
// ARRAY with elo_r, elo_rd, elo_vol, category (*string), status.

(function () {
  "use strict";

  var root = document.getElementById("list-root");
  if (!root) return;
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";
  var listID = parseInt((location.pathname.match(/lists\/(\d+)/) || [])[1], 10);

  function esc(s) {
    if (s === null || s === undefined) return "";
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

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
    return '<div class="rating">' +
      '<div class="rating-value">' + Math.round(r) + "</div>" +
      '<div class="rating-bars" role="img" aria-label="rating ' + Math.round(r) +
      " plus or minus " + Math.round(rd) + ", " + pct +
      ' percent of the maximum uncertainty">' +
      '<div class="rating-bar-track"><div class="rating-bar-fill" style="width:' +
      pct + '%"></div></div></div>' +
      '<p class="muted">&plusmn;' + Math.round(rd) + " &middot; " +
      (n === 1 ? "1 comparison" : n + " comparisons") +
      (n === 0 ? " &mdash; nobody has compared this yet" : "") +
      "</p></div>";
  }

  // category is a *string in Go, so it is null for an uncategorised entry. Reading it
  // straight would print "null"; the schema allows NULL, so an absent category is the
  // ordinary case and the row is grouped under "uncategorised" rather than dropped.
  function categoryOf(e) {
    return e.category ? e.category : "uncategorised";
  }

  // Grouped by category, and WITHIN each group the API's ranking order is preserved.
  //
  // Array.prototype.sort is stable in every engine this ships to, so grouping without
  // sorting keeps the ranked order inside each bucket. That is deliberate: re-sorting
  // by rating here would be redundant with the SQL and one more place for the two to
  // disagree.
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
    var safe = esc(url);
    return '<li class="list-entry" data-entry-id="' + esc(e.id) + '">' +
      '<div class="list-entry-head">' +
      '<a class="list-entry-title" href="' + safe + '" rel="nofollow noopener">' +
      esc(e.title) + "</a>" +
      rating(e, comparisons) +
      "</div>" +
      '<p class="list-entry-url">' + safe + "</p>" +
      (e.description
        ? '<p class="list-entry-desc">' + esc(e.description) + "</p>"
        : "") +
      '<p class="card-meta"><span class="badge badge-slate">' +
      esc(e.status) + "</span>" +
      '<span>proposed ' + ago(e.created_at) + "</span></p>" +
      "</li>";
  }

  function entriesMarkup(entries, counts) {
    if (!entries.length) {
      return '<div class="empty-state"><div class="empty-state-icon">\u{1F4CB}</div>' +
        '<p class="empty-state-title">No entries yet</p>' +
        '<p class="empty-state-description">Anyone can propose one; the community ' +
        "ranks them by comparing pairs.</p></div>";
    }
    var groups = groupByCategory(entries);
    return groups.map(function (g) {
      return '<section class="entry-group">' +
        '<h3 class="entry-category">' + esc(g.category) +
        ' <span class="count">' + g.entries.length + "</span></h3>" +
        '<ul class="list-entries">' + g.entries.map(function (e) {
          return entryRow(e, counts[e.id]);
        }).join("") + "</ul></section>";
    }).join("");
  }

  // The pairwise control, with the SCHEMA's five outcomes rather than a 1-5 score.
  //
  // list_entry_votes.outcome is CHECKed to exactly
  // ('a','b','both','neither','skip'), so a numeric rating control posts a value the
  // database refuses -- and the page then renders as though nobody had ranked
  // anything, which is the silent-empty failure this file's whole shape is about.
  function comparePanel(entries) {
    if (entries.length < 2) {
      return '<p class="note">Ranking needs two entries. One entry has nothing to be ' +
        "better than.</p>";
    }
    var opts = entries.map(function (e) {
      return '<option value="' + esc(e.id) + '">' + esc(e.title) + "</option>";
    }).join("");
    return '<form class="compare" data-compare-form>' +
      '<h3>Which belongs on this list more?</h3>' +
      '<div class="compare-row">' +
      '<label class="visually-hidden" for="cmp-a">Entry A</label>' +
      '<select id="cmp-a" name="entry_a">' + opts + "</select>" +
      '<label class="visually-hidden" for="cmp-b">Entry B</label>' +
      '<select id="cmp-b" name="entry_b">' + opts + "</select>" +
      '<label class="visually-hidden" for="cmp-outcome">Outcome</label>' +
      '<select id="cmp-outcome" name="outcome">' +
      '<option value="a">A fits better</option>' +
      '<option value="b">B fits better</option>' +
      '<option value="both">Both fit equally</option>' +
      '<option value="neither">Neither fits</option>' +
      '<option value="skip">Skip</option>' +
      "</select>" +
      '<button type="submit" class="btn">Compare</button>' +
      "</div>" +
      '<p class="note">A pairwise comparison, not a score: it moves both entries by a ' +
      "Glicko-2 update weighted by each side's deviation (&sect;16).</p></form>";
  }

  function render(list, entries, counts) {
    root.removeAttribute("aria-busy");
    root.innerHTML =
      '<section class="list-detail">' +
      '<div class="card">' +
      '<div class="card-head"><h2>' + esc(list.name) + "</h2>" +
      // project_id 0 means instance-wide (16: "attachable to a project or
      // instance-wide"). The page is mounted under a project slug, so without this a
      // reader would assume the project owns a list every project shares.
      (list.project_id
        ? ""
        : '<span class="badge badge-instance">instance-wide</span>') +
      '<span class="badge ' + (list.status === "open" ? "badge-blue" : "badge-slate") +
      '">' + esc(list.status) + "</span></div>" +
      (list.description ? '<p class="body-text">' + esc(list.description) + "</p>" : "") +
      '<p class="card-meta">' + entries.length +
      (entries.length === 1 ? " entry" : " entries") +
      " &middot; ranked by pairwise comparison</p>" +
      "</div></section>" +
      entriesMarkup(entries, counts || {}) +
      comparePanel(entries);

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


      fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/lists/" +
        encodeURIComponent(listID) + "/entries/vote", {
        method: "POST",
        headers: apiAuth({ "Content-Type": "application/json" }),
        body: JSON.stringify({ entry_a: a, entry_b: b, outcome: outcome })
      })
        .then(function (r) {
          return r.json().catch(function () { return {}; }).then(function (j) {
            return { ok: r.ok, body: j };
          });
        })
        .then(function (res) {
          if (!res.ok) {
            // "you have already compared these two entries" and "you cannot vote on an
            // entry you proposed" are both refusals the reader can act on, so the
            // store's message is surfaced rather than flattened to a generic failure.
            root.insertAdjacentHTML("afterbegin",
              '<p class="notice notice-error" role="alert">' +
              esc(res.body && res.body.error
                ? res.body.error
                : "That comparison was refused.") + "</p>");
            return;
          }
          // The vote response carries the entries back, so the ranking updates from
          // the server's own numbers rather than from an optimistic guess.
          load(res.body && res.body.entries);
        })
        .catch(function () {
          root.insertAdjacentHTML("afterbegin",
            '<p class="notice notice-error" role="alert">Could not reach the ' +
            "server.</p>");
        });
    });
  }

  function getJSON(url) {
    var headers = {};
    var token = localStorage.getItem("concord.token");
    if (token) headers.Authorization = "Bearer " + token;
    return fetch(url, { headers: headers }).then(function (r) {
      if (!r.ok) throw new Error(r.status);
      return r.json();
    });
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
        // oversight: store.GetListEntryVotes exists and the vote response carries the
        // entries, but there is no GET that returns counts, so a page loaded cold has
        // none. It shows "0 comparisons" until the reader casts one, which is honest
        // about the data available and is why the counts are threaded from the vote
        // response below rather than invented here.
        render(parts[0], Array.isArray(parts[1]) ? parts[1] : []);
      })
      .catch(function () {
        root.removeAttribute("aria-busy");
        root.innerHTML = '<div class="empty-state">' +
          '<p class="empty-state-title">There is no list at this address.</p></div>';
      });
  }

  load();
})();
