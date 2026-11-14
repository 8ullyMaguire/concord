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
// Resolving id->slug per answer would be an N+1 (frontend-spec.md forbids it), so the
// answers are linked BY ID and the slug comes from the page, which already has it.

(function () {
  "use strict";

  var root = document.getElementById("request-root");
  if (!root) return;

  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";
  var requestID = parseInt((location.pathname.match(/requests\/(\d+)/) || [])[1], 10);

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

  // A Glicko rating is a POINT with a DEVIATION, and printing only the point is how
  // a page makes a reader think an untouched answer lost.
  //
  // elo_rd is the width of the confidence band: 350 is the seed (nobody has compared
  // it yet), and it narrows as comparisons land. A bare 1500 for an untouched answer
  // is indistinguishable from a settled 1500, so both the number and the band are
  // shown.
  // A Glicko rating is a POINT with a DEVIATION, and printing only the point is how a
  // page makes a reader think an untouched answer lost.
  //
  // Rendered the same way feature.js renders it: a proportional BAR plus the number,
  // with the bar as the honest signal and the text as the precise one. 350 is the
  // starting deviation, so it is the full-width case -- the same normalisation
  // feature.js uses, so the two pages cannot disagree about what "uncertain" means.
  function rating(a) {
    var r = typeof a.elo_r === "number" ? a.elo_r : 0;
    var rd = typeof a.elo_rd === "number" && isFinite(a.elo_rd) ? a.elo_rd : 0;
    var pct = Math.max(4, Math.min(100, Math.round((rd / 350) * 100)));
    return '<div class="rating">' +
      '<div class="rating-value">' + Math.round(r) + "</div>" +
      '<div class="rating-bars" role="img" aria-label="rating ' + Math.round(r) +
      " plus or minus " + Math.round(rd) + ", " + pct +
      ' percent of the maximum uncertainty">' +
      '<div class="rating-bar-track"><div class="rating-bar-fill" style="width:' +
      pct + '%"></div></div></div>' +
      '<p class="muted">&plusmn;' + Math.round(rd) +
      " &mdash; a wider bar means less certainty, not a worse rank.</p>" +
      "</div>";
  }

  function statusBadge(status) {
    return '<span class="badge badge-' + esc(status) + '">' + esc(status) + "</span>";
  }

  // An answer carries project_id and NOTHING ELSE -- no slug, no name. §17 says an
  // answer IS a project reference, so the page has to say WHICH project, and
  // "Project 3" is not a reference anybody can act on.
  //
  // Resolved with ONE fetch of the project list and an id->project map, NOT one fetch
  // per answer: frontend-spec.md forbids N+1 request patterns, and an answer list of
  // twenty would otherwise make twenty-one requests.
  //
  // A project the caller cannot see is absent from that list (the list is filtered by
  // visibility), so the card falls back to the bare id. That is the honest rendering:
  // the answer exists, the reader is not allowed to know whose it is, and pretending
  // otherwise would leak the existence of a private project.
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
    var label = p ? p.name || p.slug : "Project " + esc(a.project_id);
    if (!p) return esc(label);
    return '<a href="/projects/' + esc(p.slug) + '">' + esc(label) + "</a>";
  }

  // One answer card. `request` is passed IN rather than read from a global or a bare
  // identifier: the project page's `complaintCards(complaints, slug)` threw a
  // ReferenceError because the parameter was named `p`, and that one line blanked
  // the entire page. Every identifier used below is therefore a parameter or a local.
  function answerCard(a, request) {
    var accepted =
      request && request.accepted_answer_id && request.accepted_answer_id === a.id;

    // §17: "Answers about the proposer's own project carry an affiliation label.
    // Weight is unchanged, the affiliation is visible." So the label is rendered and
    // the rating is NOT touched -- there is deliberately no penalty term here.
    //
    // `affiliated` is computed by the caller, which knows who asked the question and
    // which project each answer is for.
    var affiliation = a.affiliated
      ? '<span class="badge badge-affiliated" ' +
        'title="This answer proposes the same project that asked the question. ' +
        "Its weight is unchanged (17) \u2014 the affiliation is shown, not penalised.\">" +
        "affiliated</span>"
      : "";

    var badge = accepted
      ? '<span class="badge badge-accepted" title="Marked accepted by the request ' +
        "author. Per 17 this is displayed but does not override the ranking.\">" +
        "accepted</span>"
      : "";

    return (
      '<article class="card answer' + (accepted ? " answer-accepted" : "") + '" ' +
      'data-answer-id="' + esc(a.id) + '">' +
      '<div class="card-head">' +
      "<h3>" + linkToProject(a) + "</h3>" +
      rating(a) +
      badge +
      affiliation +
      "</div>" +
      '<p class="answer-body">' + esc(a.body) + "</p>" +
      '<p class="card-meta">' +
      '<span>' + esc(a.status) + "</span> · " +
      "<span>proposed " + ago(a.created_at) + "</span>" +
      (a.status === "removed"
        ? ' · <span class="warn">removed by quorum</span>'
        : "") +
      "</p>" +
      "</article>"
    );
  }

  function renderError(message) {
    root.innerHTML =
      '<div class="empty-state"><p class="empty-state-title">' +
      esc(message) +
      "</p></div>";
  }

  // The pairwise comparison control, which is the whole point of the board: "which
  // fits this request better?". It needs TWO answers, so it appears only once there
  // are two, and it is absent for the asker on their own answers.
  function comparePanel(answers, meID, myProjectIDs) {
    var votable = answers.filter(function (a) {
      return a.status === "active" && myProjectIDs.indexOf(a.project_id) === -1;
    });
    if (votable.length < 2) {
      return (
        '<p class="note">Pairwise comparison needs two active answers from other ' +
        "projects. §17 ranks fits by asking which of two fits better \\u2014 there is " +
        "nothing to compare yet.</p>"
      );
    }

    var opts = votable
      .map(function (a) {
        return '<option value="' + esc(a.id) + '">Project ' + esc(a.project_id) + "</option>";
      })
      .join("");

    return (
      '<form class="compare" data-compare-form>' +
      '<h3>Which fits this request better?</h3>' +
      '<div class="compare-row">' +
      '<label class="visually-hidden" for="cmp-a">Answer A</label>' +
      '<select id="cmp-a" name="answer_a">' + opts + "</select>" +
      '<label class="visually-hidden" for="cmp-b">Answer B</label>' +
      '<select id="cmp-b" name="answer_b">' + opts + "</select>" +
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
      '<p class="note">A pairwise comparison, not a score: it moves both ratings by ' +
      "a Glicko-2 update weighted by each side's deviation (§17, §8.3).</p>" +
      "</form>"
    );
  }

  function answerForm(request, meID) {
    if (!meID) {
      return (
        '<p class="note">Sign in to answer this request with a project.</p>'
      );
    }
    if (request.project_id) {
      return (
        '<form class="answer-form" data-answer-form>' +
        '<h3>Answer with a project</h3>' +
        '<p class="note">An answer is a project reference and a fit rationale: why it ' +
        "fits, where it falls short. One answer per project per request.</p>" +
        '<label class="visually-hidden" for="ans-body">Fit rationale</label>' +
        '<textarea id="ans-body" name="body" rows="4" ' +
        'placeholder="Why does this project fit? Where does it fall short?"></textarea>' +
        '<button type="submit" class="btn">Post answer</button>' +
        "</form>"
      );
    }
    // An instance-wide request (project_id omitted) can be answered by ANY project,
    // so there is nothing to pick from this page -- it names no project.
    return (
      '<p class="note">This is an instance-wide request: any project may answer it. ' +
      "Open it from a project to post that project's answer.</p>"
    );
  }

  function render(request, answers, tags, me) {
    // §17: the badge is set by the AUTHOR and does not override the ranking, so the
    // accepted answer keeps its position in the sorted list rather than being moved to
    // the top. Sorting by elo_r DESC and marking the row is the honest rendering.
    var sorted = answers.slice().sort(function (a, b) {
      return (b.elo_r || 0) - (a.elo_r || 0);
    });

    var myProjectIDs = me && me.project_ids ? me.project_ids : [];
    // Affiliation = the answer names the SAME project that asked the question.
    var proposerProject = request.project_id || 0;
    sorted.forEach(function (a) {
      a.affiliated = proposerProject !== 0 && a.project_id === proposerProject;
    });

    var tagChips = tags.length
      ? tags
          .map(function (t) {
            return '<span class="chip">' + esc(t) + "</span>";
          })
          .join("")
      : '<span class="note">No constraints recorded.</span>';

    root.removeAttribute("aria-busy");
    root.innerHTML =
      '<section class="request-detail">' +
      '<div class="card request-body">' +
      '<div class="card-head"><h2>' + esc(request.title) + "</h2>" +
      statusBadge(request.status) +
      "</div>" +
      '<p class="body-text">' + esc(request.body) + "</p>" +
      '<p class="card-meta"><span>asked ' + ago(request.created_at) + "</span>" +
      (request.project_id
        ? ""
        : ' · <span class="badge badge-instance">instance-wide</span>') +
      "</p>" +
      "</div>" +
      '<section class="constraints"><h2>Constraints</h2>' +
      '<div class="chips">' + tagChips + "</div></section>" +
      "</section>" +
      '<section class="answers"><h2>Answers <span class="count">' +
      answers.length +
      "</span></h2>" +
      (sorted.length
        ? sorted.map(function (a) { return answerCard(a, request); }).join("")
        : '<div class="empty-state"><p class="empty-state-title">No project has ' +
          "answered this request yet.</p></div>") +
      "</section>" +
      comparePanel(sorted, me ? me.id : 0, myProjectIDs) +
      answerForm(request, me ? me.id : 0);

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
    var headers = { "Content-Type": "application/json" };
    var t = localStorage.getItem("concord.token");
    if (t) headers["Authorization"] = "Bearer " + t;
    fetch(url, {
      method: "POST",
      headers: headers,
      body: JSON.stringify(body)
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        return { ok: r.ok, body: j };
      });
    }).then(function (res) {
      if (res.ok) {
        done();
      } else {
        // A refusal is information, not a crash: §5.1 refuses a self-vote and the
        // weight cap can refuse a vote, and saying which is better than silence.
        renderError(res.body && res.body.error ? res.body.error : "That did not work.");
      }
    }).catch(function () {
      renderError("Could not reach the server.");
    });
  }

  function getJSON(url) {
    var headers = {};
    var t = localStorage.getItem("concord.token");
    if (t) headers["Authorization"] = "Bearer " + t;
    return fetch(url, { headers: headers })
      .then(function (r) {
        if (!r.ok) throw new Error(r.status);
        return r.json();
      });
  }

  function load() {
    // Three reads, all independent, so they run in PARALLEL rather than in the
    // chain: the request, its answers, its tags -- plus the project list that turns
    // each answer's project_id into a name and a link.
    //
    // frontend-spec.md forbids N+1 request patterns and caps the project page at
    // three; serialising these would be four sequential round trips on a page that
    // has everything it needs to paint after the first.
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
