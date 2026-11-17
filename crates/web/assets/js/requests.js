// The §17 request board detail page.
//
// §17 is specific and this page has to carry all of it, not a generic answer list:
//
//   - an answer is a PROJECT REFERENCE with a fit rationale, not an opinion
//   - one answer per project per request (UNIQUE (request_id, project_id))
//   - answers compete in pairwise comparisons on a Glicko rating
//   - the author may mark an accepted answer, DISPLAYED BUT NOT OVERRIDING THE RANK
//   - self-promotion is transparent: an answer by the proposer's own project carries
//     an affiliation label, and the weight is unchanged
//
// The affiliation label is the rule a naive page gets wrong: §17 says the answer
// still counts for exactly as much, so this renders a label and never a penalty.
//
// MEASURED SHAPES (tests/e2e/api_surface.py, not inferred from handler names):
//   GET /api/v1/requests/{id}          -> bare object, project_id OMITTED when the
//                                         request is instance-wide (omitempty)
//   GET /api/v1/requests/{id}/answers  -> bare ARRAY (not wrapped)
//   GET /api/v1/requests/{id}/tags     -> {tags: [...]}  (wrapped)
//   PUT /api/v1/requests/{id}/accept   -> the updated request object
//
// An answer carries project_id but NOT the project's slug, so the link needs a slug.
// Resolving id->slug per answer would be an N+1, so the answers are linked BY ID and
// the slug comes from the page, which already has it.
//
// Rendering (step 2b): `el()` nodes only. Titles, bodies, tags, project names and
// server refusal messages are text or setAttribute values; no HTML string sink.

(function () {
  "use strict";

  var el = window.ConcordDom.el;

  var root = document.getElementById("request-root");
  if (!root) return;

  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";
  var requestID = parseInt((location.pathname.match(/requests\/(\d+)/) || [])[1], 10);

  function ago(unix) {
    if (!unix) return "";
    var d = Math.floor(Date.now() / 1000 - unix);
    if (d < 60) return "just now";
    if (d < 3600) return Math.floor(d / 60) + "m ago";
    if (d < 86400) return Math.floor(d / 3600) + "h ago";
    return Math.floor(d / 86400) + "d ago";
  }

  // A Glicko rating is a POINT with a DEVIATION, and printing only the point is how
  // a page makes a reader think an untouched answer lost. The bar is the honest
  // signal; 350 is the seed and therefore the full-width case, matching feature.js.
  function rating(a) {
    var r = typeof a.elo_r === "number" ? a.elo_r : 0;
    var rd = typeof a.elo_rd === "number" && isFinite(a.elo_rd) ? a.elo_rd : 0;
    var pct = Math.max(4, Math.min(100, Math.round((rd / 350) * 100)));
    var shown = Math.round(r);
    var shownRd = Math.round(rd);
    return el("div", { class: "rating" }, [
      el("div", { class: "rating-value" }, shown),
      el("div", {
        class: "rating-bars", role: "img",
        "aria-label": "rating " + shown + " plus or minus " + shownRd +
          ", " + pct + " percent of the maximum uncertainty"
      }, el("div", { class: "rating-bar-track" },
        el("div", { class: "rating-bar-fill", style: "width:" + pct + "%" }))),
      el("p", { class: "muted" }, "±" + shownRd +
        " — a wider bar means less certainty, not a worse rank.")
    ]);
  }

  function statusBadge(status) {
    return el("span", { class: "badge badge-" + status }, status);
  }

  // Resolved with ONE fetch of the project list and an id->project map, NOT one fetch
  // per answer. A project the caller cannot see is absent from that list, so the
  // card falls back to the bare id rather than leaking its existence.
  var projectsById = {};

  function loadProjects() {
    return getJSON("/api/v1/projects")
      .then(function (list) {
        (list || []).forEach(function (p) {
          projectsById[p.id] = p;
        });
      })
      .catch(function () {
        // A failed project list is not a failed page: the answers still render, by id.
      });
  }

  function linkToProject(a) {
    var p = projectsById[a.project_id];
    if (!p) return el("span", {}, "Project " + a.project_id);
    return el("a", { href: "/projects/" + p.slug }, p.name || p.slug);
  }

  // One answer card. Every identifier used below is a parameter or a local, never a
  // global: a misnamed parameter once blanked the whole project page.
  function answerCard(a, request) {
    var accepted =
      request && request.accepted_answer_id && request.accepted_answer_id === a.id;

    // §17: affiliation is shown and the rating is NOT touched -- no penalty term.
    var affiliation = a.affiliated
      ? el("span", {
          class: "badge badge-affiliated",
          title: "This answer proposes the same project that asked the question. " +
            "Its weight is unchanged (§17) — the affiliation is shown, not penalised."
        }, "affiliated")
      : null;

    var badge = accepted
      ? el("span", {
          class: "badge badge-accepted",
          title: "Marked accepted by the request author. Per §17 this is displayed but does not override the ranking."
        }, "accepted")
      : null;

    var meta = el("p", { class: "card-meta" }, [
      el("span", {}, a.status), " · ",
      el("span", {}, "proposed " + ago(a.created_at)),
      a.status === "removed" ? " · " : null,
      a.status === "removed" ? el("span", { class: "warn" }, "removed by quorum") : null
    ]);

    return el("article", {
      class: "card answer" + (accepted ? " answer-accepted" : ""),
      "data-answer-id": a.id
    }, [
      el("div", { class: "card-head" }, [
        el("h3", {}, linkToProject(a)),
        rating(a), badge, affiliation
      ]),
      el("p", { class: "answer-body" }, a.body),
      meta
    ]);
  }

  function renderError(message) {
    root.replaceChildren(el("div", { class: "empty-state" },
      el("p", { class: "empty-state-title" }, message)));
  }

  // The pairwise comparison control appears only once there are TWO active answers
  // from projects other than the reader's.
  function comparePanel(answers, meID, myProjectIDs) {
    var votable = answers.filter(function (a) {
      return a.status === "active" && myProjectIDs.indexOf(a.project_id) === -1;
    });
    if (votable.length < 2) {
      return el("p", { class: "note" }, "Pairwise comparison needs two active answers from other " +
        "projects. §17 ranks fits by asking which of two fits better — there is " +
        "nothing to compare yet.");
    }

    var opts = votable.map(function (a) {
      return el("option", { value: a.id }, "Project " + a.project_id);
    });

    return el("form", { class: "compare", "data-compare-form": "" }, [
      el("h3", {}, "Which fits this request better?"),
      el("div", { class: "compare-row" }, [
        el("label", { class: "visually-hidden", for: "cmp-a" }, "Answer A"),
        el("select", { id: "cmp-a", name: "answer_a" }, opts),
        el("label", { class: "visually-hidden", for: "cmp-b" }, "Answer B"),
        el("select", { id: "cmp-b", name: "answer_b" }, opts),
        el("label", { class: "visually-hidden", for: "cmp-outcome" }, "Outcome"),
        el("select", { id: "cmp-outcome", name: "outcome" }, [
          el("option", { value: "a" }, "A fits better"),
          el("option", { value: "b" }, "B fits better"),
          el("option", { value: "both" }, "Both fit equally"),
          el("option", { value: "neither" }, "Neither fits"),
          el("option", { value: "skip" }, "Skip")
        ]),
        el("button", { type: "submit", class: "btn" }, "Compare")
      ]),
      el("p", { class: "note" }, "A pairwise comparison, not a score: it moves both ratings by " +
        "a Glicko-2 update weighted by each side's deviation (§17, §8.3).")
    ]);
  }

  function answerForm(request, meID) {
    if (!meID) {
      return el("p", { class: "note" }, "Sign in to answer this request with a project.");
    }
    if (request.project_id) {
      return el("form", { class: "answer-form", "data-answer-form": "" }, [
        el("h3", {}, "Answer with a project"),
        el("p", { class: "note" }, "An answer is a project reference and a fit rationale: why it " +
          "fits, where it falls short. One answer per project per request."),
        el("label", { class: "visually-hidden", for: "ans-body" }, "Fit rationale"),
        el("textarea", {
          id: "ans-body", name: "body", rows: "4",
          placeholder: "Why does this project fit? Where does it fall short?"
        }),
        el("button", { type: "submit", class: "btn" }, "Post answer")
      ]);
    }
    // An instance-wide request (project_id omitted) can be answered by ANY project.
    return el("p", { class: "note" }, "This is an instance-wide request: any project may answer it. " +
      "Open it from a project to post that project's answer.");
  }

  function render(request, answers, tags, me) {
    // §17: the badge is set by the AUTHOR and does not override the ranking, so the
    // accepted answer keeps its position in the sorted list rather than moving to top.
    var sorted = answers.slice().sort(function (a, b) {
      return (b.elo_r || 0) - (a.elo_r || 0);
    });

    var myProjectIDs = me && me.project_ids ? me.project_ids : [];
    var proposerProject = request.project_id || 0;
    sorted.forEach(function (a) {
      a.affiliated = proposerProject !== 0 && a.project_id === proposerProject;
    });

    root.removeAttribute("aria-busy");
    root.replaceChildren(
      el("section", { class: "request-detail" }, [
        el("div", { class: "card request-body" }, [
          el("div", { class: "card-head" }, [
            el("h2", {}, request.title), statusBadge(request.status)
          ]),
          el("p", { class: "body-text" }, request.body),
          el("p", { class: "card-meta" }, [
            el("span", {}, "asked " + ago(request.created_at)),
            request.project_id ? null : " · ",
            request.project_id ? null : el("span", { class: "badge badge-instance" }, "instance-wide")
          ])
        ]),
        el("section", { class: "constraints" }, [
          el("h2", {}, "Constraints"),
          el("div", { class: "chips" }, tags.length
            ? tags.map(function (t) { return el("span", { class: "chip" }, t); })
            : el("span", { class: "note" }, "No constraints recorded."))
        ])
      ]),
      el("section", { class: "answers" }, [
        el("h2", {}, ["Answers ", el("span", { class: "count" }, answers.length)]),
        sorted.length
          ? sorted.map(function (a) { return answerCard(a, request); })
          : el("div", { class: "empty-state" },
            el("p", { class: "empty-state-title" }, "No project has answered this request yet."))
      ]),
      comparePanel(sorted, me ? me.id : 0, myProjectIDs),
      answerForm(request, me ? me.id : 0)
    );

    wire(request, me);
  }

  function wire(request, me) {
    var answerEl = root.querySelector("[data-answer-form]");
    if (answerEl) {
      answerEl.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var body = answerEl.querySelector("textarea").value.trim();
        if (!body) return;
        postJSON("/api/v1/requests/" + requestID + "/answers", { body: body }, function () {
          load();
        });
      });
    }

    var cmp = root.querySelector("[data-compare-form]");
    if (cmp) {
      cmp.addEventListener("submit", function (ev) {
        ev.preventDefault();
        var a = parseInt(cmp.querySelector("#cmp-a").value, 10);
        var b = parseInt(cmp.querySelector("#cmp-b").value, 10);
        if (a === b) return;
        var outcome = cmp.querySelector("#cmp-outcome").value;
        postJSON("/api/v1/answers/" + a + "/vote", {
          answer_a: a,
          answer_b: b,
          outcome: outcome
        }, function () {
          load();
        });
      });
    }
  }

  function postJSON(url, body, done) {
    window.ConcordApi.post(url, body).then(function () {
      done();
    }).catch(function (error) {
      renderError(error && error.message ? error.message : "That did not work.");
    });
  }

  function getJSON(url) {
    return window.ConcordApi.get(url);
  }

  function load() {
    // Three reads, all independent, so they run in PARALLEL rather than in a chain;
    // the project list turns each answer's project_id into a name and a link.
    Promise.all([
      loadProjects(),
      getJSON("/api/v1/requests/" + requestID),
      getJSON("/api/v1/requests/" + requestID + "/answers").catch(function () { return []; }),
      getJSON("/api/v1/requests/" + requestID + "/tags")
        .then(function (j) { return j.tags || []; })
        .catch(function () { return []; })
    ])
      .then(function (parts) {
        render(parts[1], parts[2] || [], parts[3] || [], window.ConcordMe || null);
      })
      .catch(function () {
        // A 404 here is the private-project guard firing. Say there is no such
        // request rather than leaking that the project exists.
        renderError("There is no request at this address.");
      });
  }

  load();

})();
