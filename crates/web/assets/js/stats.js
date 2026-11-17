/* Concord statistics page — renders the instance census from its own API.
 *
 * The page computes nothing. Every figure here is read out of
 * /api/v1/stats/summary, because spec §3.4 is explicit that a page which
 * recomputes an aggregate the API already returns is two answers waiting to
 * disagree, and the page is the one nobody notices is wrong.
 *
 * The provenance split is rendered as three numbers wherever the API carries
 * one, and a total is never shown without it: spec §2 withholds an aggregate
 * that cannot produce the split rather than publishing it with a footnote.
 *
 * Rendering (step 2b): el() nodes only. Facet values, language/tag/license
 * names, project names and slugs are text nodes or setAttribute values.
 */
(function () {
  "use strict";
  var el = window.ConcordDom.el;

  function emptyState(title, desc) {
    return el("div", { class: "empty-state" }, [
      el("div", { class: "empty-state-icon" }, "📊"),
      el("p", { class: "empty-state-title" }, title),
      el("p", { class: "empty-state-description" }, desc)
    ]);
  }

  // A facet row. `split` is a ProvenanceCount (three numbers) or a FacetCount
  // (one), and the branch is on the field's presence rather than on which
  // section we are in -- the API is the contract, not this file's layout.
  function facetRow(row) {
    if (row.organic !== undefined || row.seeded !== undefined || row.unknown !== undefined) {
      return el("tr", {}, [
        el("td", {}, row.value),
        el("td", {}, row.total),
        el("td", {}, row.organic),
        el("td", {}, row.seeded),
        el("td", {}, row.unknown)
      ]);
    }
    return el("tr", {}, [el("td", {}, row.value), el("td", {}, row.count)]);
  }

  function table(headCells, rows) {
    if (!rows || !rows.length) return null;
    return el("div", { class: "table-scroll", tabindex: "0", role: "region", "aria-label": "tabla" },
      el("table", { class: "ranking-table" }, [
        el("thead", {}, el("tr", {}, headCells.map(function (h) { return el("th", {}, h); }))),
        el("tbody", {}, rows.map(facetRow))
      ]));
  }

  function card(title, body) {
    return el("div", { class: "card" }, [
      el("div", { class: "card-title" }, title),
      el("div", { class: "card-text" }, body)
    ]);
  }

  // A latency card. The median is shown with its n and its unfinished count,
  // because spec §4 refuses a median without n: the same 216h means something
  // different for 3 episodes than for 300.
  function latencyCard(title, lat) {
    var value = lat.n === 0 ? "—" : lat.median_hours.toFixed(1) + " h";
    var counts = "n=" + lat.n + " · unfinished=" + lat.unfinished;
    if (lat.unmeasured) counts += " · unmeasured=" + lat.unmeasured;
    return el("div", { class: "card" }, [
      el("div", { class: "card-title" }, title),
      el("div", { class: "card-text" }, value),
      el("p", { class: "muted small" }, counts)
    ]);
  }

  function renderLatency(container, latency) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    container.replaceChildren(el("div", { class: "grid-responsive-3" }, [
      latencyCard("Complaint triage", latency.triage),
      latencyCard("Feature delivery", latency.delivery),
      latencyCard("Consensus decision", latency.decision)
    ]));
  }

  // The split columns are shown only when at least one row carries them, so a
  // plain facet is not padded with three empty columns.
  function hasSplit(rows) {
    return (rows || []).some(function (r) {
      return r.organic !== undefined || r.seeded !== undefined || r.unknown !== undefined;
    });
  }

  function rankedTable(headCells, rows) {
    return el("div", { class: "table-scroll", tabindex: "0", role: "region", "aria-label": "tabla" }, [
      el("table", { class: "ranking-table" }, [
        el("thead", {}, el("tr", {}, headCells.map(function (h) { return el("th", {}, h); }))),
        el("tbody", {}, rows)
      ])
    ]);
  }

  // The responsiveness leaderboard (STAT-3.5). Rows are rendered in the order the
  // API returns them and are NOT re-sorted here: the ranking rule lives in the
  // store's ORDER BY, and a page that sorted again would be a second answer.
  // A project under the sample floor is absent from the payload, so it is absent
  // from this table too -- the list is never padded with unmeasured rows.
  function renderResponsiveness(container, rows) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    if (!rows || !rows.length) {
      container.replaceChildren(emptyState("No responsiveness published yet",
        "A project appears here once at least five first-time pull requests were answered. " +
        "Below that it is absent rather than ranked last: a median nobody can act on is not a measurement."));
      return;
    }
    var body = rows.map(function (row, i) {
      return el("tr", {}, [
        el("td", {}, i + 1),
        el("td", {}, el("a", { href: "/projects/" + encodeURIComponent(row.slug) }, row.name)),
        el("td", {}, row.newcomer_response_hours.toFixed(1) + " h"),
        el("td", {}, row.n)
      ]);
    });
    container.replaceChildren(card("Newcomer responsiveness", [
      el("p", { class: "muted small" }, "Median hours from a first-time contributor's pull request " +
        "to the first answer by somebody else — fastest first."),
      rankedTable(["#", "project", "median", "n"], body)
    ]));
  }

  // Rising (STAT-3.4). Ranked by organic features shipped per week between the
  // project's own first and last snapshot. The SPAN is printed in every row: a rate over
  // 14 days and one over 90 are different claims, and a reader who is not told which is
  // reading a comparison between incomparable numbers.
  //
  // Health is shown BESIDE the slope, never instead of it -- "rising is a change, health
  // is a level" -- and an unmeasured health renders a dash, never a zero.
  function renderRising(container, rows) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    if (!rows || !rows.length) {
      container.replaceChildren(emptyState("No trend yet",
        "A project is ranked here once it has snapshots at least 14 days apart. " +
        "Below that there is no slope, and a rate over a day or two would be a number about the timer."));
      return;
    }
    var body = rows.map(function (row, i) {
      return el("tr", {}, [
        el("td", {}, i + 1),
        el("td", {}, el("a", { href: "/projects/" + encodeURIComponent(row.slug) }, row.name)),
        el("td", {}, row.features_per_week.toFixed(2)),
        el("td", {}, row.complaints_per_week.toFixed(2)),
        el("td", {}, row.health_score == null ? "\u2014" : Math.round(row.health_score * 100) + "%"),
        el("td", {}, row.days)
      ]);
    });
    container.replaceChildren(card("Rising", [
      el("p", { class: "muted small" }, "Organic features shipped, then organic complaints filed, " +
        "per week across each project's own recorded span — fastest first. " +
        "Health is the current level, beside the slope rather than instead of it."),
      rankedTable(["#", "project", "features/week", "complaints/week", "health", "days"], body)
    ]));
  }

  function addCard(parts, title, heads, rows) {
    var t = table(heads, rows);
    if (t) parts.push(card(title, t));
  }

  function render(container, data) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    var parts = [];
    if (hasSplit(data.features_by_status)) {
      var split = ["status", "total", "organic", "seeded", "unknown"];
      addCard(parts, "Features by status", split, data.features_by_status);
      addCard(parts, "Features by effort", ["effort"].concat(split.slice(1)), data.features_by_effort);
      addCard(parts, "Complaints by status", split, data.complaints_by_status);
    } else {
      addCard(parts, "Features by status", ["status", "total"], data.features_by_status);
      addCard(parts, "Complaints by status", ["status", "total"], data.complaints_by_status);
    }
    addCard(parts, "Projects by language", ["language", "projects"], data.projects_by_language);
    addCard(parts, "Projects by tag", ["tag", "projects"], data.projects_by_tag);
    addCard(parts, "Projects by license", ["license", "projects"], data.projects_by_license);
    addCard(parts, "Projects by governance", ["model", "projects"], data.projects_by_governance);
    addCard(parts, "Projects by visibility", ["visibility", "projects"], data.projects_by_visibility);
    addCard(parts, "Members by role", ["role", "members"], data.members_by_role);
    addCard(parts, "Documents by kind", ["kind", "documents"], data.documents_by_kind);
    addCard(parts, "Arenas by type", ["type", "arenas"], data.arenas_by_type);
    container.replaceChildren(el("div", { class: "grid-responsive-2" }, parts));
  }

  // One client (review item 9b). This file's own `fetch` + `authHeaders` pair was a copy of `api.js`, and its
  // failure said "HTTP 500" for a page the reader cannot act on either way -- the shared client turns the server's
  // own sentence into the message.
  function getJSON(path) {
    return window.ConcordApi.get(path);
  }

  function fail(container, title, desc) {
    if (window.ConcordSkeleton) window.ConcordSkeleton.done(container);
    container.replaceChildren(emptyState(title, desc));
  }

  document.addEventListener('DOMContentLoaded', function () {
    var summary = document.getElementById('stats-summary');
    var latency = document.getElementById('stats-latency');
    var responsiveness = document.getElementById('stats-responsiveness');
    var rising = document.getElementById('stats-rising');

    // Its own fetch and its own failure state: the leaderboard is the only
    // section that needs the forge webhook to have ever fired, so a blank one is
    // the normal state of a young instance and must not look like an outage.
    if (responsiveness) {
      responsiveness.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats/responsiveness')
        .then(function (rows) { renderResponsiveness(responsiveness, rows); })
        .catch(function () {
          fail(responsiveness, 'Responsiveness unavailable', 'The leaderboard did not load. Try again.');
        });
    }

    // The two fetches are independent: a failure in one must not blank the
    // other, and the latency cards are not a footnote to the census.
    if (latency) {
      latency.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats')
        .then(function (data) { renderLatency(latency, data.latency); })
        .catch(function () {
          // Clear aria-busy on the failure path too: aria-busy suppresses
          // live-region updates inside the region, so an error left busy is an
          // error nobody hears.
          fail(latency, 'Latency unavailable', 'The response times did not load. Try again.');
        });
    }

    // Rising needs the per-project series, so a fresh instance has none and says so.
    if (rising) {
      rising.setAttribute('aria-busy', 'true');
      getJSON('/api/v1/stats/rising')
        .then(function (rows) { renderRising(rising, rows); })
        .catch(function () {
          fail(rising, 'Trend unavailable', 'The rising ranking did not load. Try again.');
        });
    }

    if (!summary) return;
    summary.setAttribute('aria-busy', 'true');
    getJSON('/api/v1/stats/summary')
      .then(function (data) { render(summary, data); })
      .catch(function () {
        fail(summary, 'Statistics unavailable', 'The census did not load. Try again.');
      });
  });
})();
