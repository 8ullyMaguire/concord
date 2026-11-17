// A project's open and settled requests (§17).
//
// THE INDEX, and the thing the Requests tab needs. Until this file existed the tab
// linked to /projects/{slug}/requests, which had no route: the ONLY request page was
// the DETAIL one at /projects/{slug}/requests/{id}, so there was no way to discover a
// request id at all -- you could reach a request by knowing its number, and by no other
// means.
//
// Instance-wide requests (§17: a request with no project is open to the whole instance)
// are INCLUDED and labelled, so the reader knows the comparison is not confined to
// this project.
//
// Rendering (step 2b): `el()` nodes only. Titles, statuses and hrefs are text or
// setAttribute values; no HTML string sink.

(function () {
  "use strict";

  var el = window.ConcordDom.el;

  var root = document.getElementById("requests-root");
  if (!root) return;
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";

  function ago(unix) {
    if (!unix) return "";
    var d = Math.floor(Date.now() / 1000 - unix);
    if (d < 60) return "just now";
    if (d < 3600) return Math.floor(d / 60) + "m ago";
    if (d < 86400) return Math.floor(d / 3600) + "h ago";
    return Math.floor(d / 86400) + "d ago";
  }

  // project_id is omitempty in Go, so an instance-wide request has NO KEY rather than
  // 0. Reading r.project_id === 0 would never be true and every shared request would
  // be labelled as this project's own.
  function isInstanceWide(r) {
    return r.project_id === undefined || r.project_id === null || r.project_id === 0;
  }

  function card(r) {
    var href = "/projects/" + encodeURIComponent(slug) + "/requests/" +
      encodeURIComponent(r.id);
    var settled = r.status !== "open";
    return el("article", { class: "card" + (settled ? " is-muted" : "") }, [
      el("h3", {}, el("a", { href: href }, r.title)),
      isInstanceWide(r)
        ? el("p", { class: "card-meta" },
          el("span", { class: "badge badge-instance" }, "instance-wide"))
        : null,
      el("p", { class: "card-meta" }, [
        el("span", { class: "badge " + (r.status === "open" ? "badge-blue" : "badge-slate") }, r.status),
        // §17: the accepted-fit badge is set only by the request's author, and its
        // presence is the only thing that distinguishes a settled request from a stale one.
        r.accepted_answer_id
          ? el("span", { class: "badge badge-green" }, "answer accepted")
          : (settled ? el("span", { class: "badge badge-slate" }, "no accepted answer") : null),
        el("span", {}, "asked " + ago(r.created_at))
      ])
    ]);
  }

  // Open first, then newest. A request board whose settled entries sit above its open
  // ones is a backlog nobody works.
  function order(list) {
    return list.slice().sort(function (a, b) {
      var ao = a.status === "open" ? 0 : 1;
      var bo = b.status === "open" ? 0 : 1;
      if (ao !== bo) return ao - bo;
      return (b.created_at || 0) - (a.created_at || 0);
    });
  }

  function group(label, list) {
    return el("section", { class: "entry-group" }, [
      el("h3", { class: "entry-category" }, [label + " ", el("span", { class: "count" }, list.length)]),
      el("div", { class: "grid-responsive-2" }, list.map(card))
    ]);
  }

  function sections(list) {
    var open = list.filter(function (r) { return r.status === "open"; });
    var rest = list.filter(function (r) { return r.status !== "open"; });
    return [
      open.length ? group("open", open) : null,
      rest.length ? group("settled", rest) : null
    ];
  }

  function load() {
    // BOTH fetches: a project request index that hid instance-wide requests would hide
    // exactly the questions that most need an answer.
    Promise.all([
      window.ConcordApi.get("/api/v1/projects/" + encodeURIComponent(slug) + "/requests").catch(function () { return []; }),
      window.ConcordApi.get("/api/v1/requests").catch(function () { return []; })
    ])
      .then(function (parts) {
        var seen = {};
        var all = [];
        parts.forEach(function (list) {
          (Array.isArray(list) ? list : []).forEach(function (r) {
            if (seen[r.id]) return;
            seen[r.id] = true;
            all.push(r);
          });
        });
        root.removeAttribute("aria-busy");
        if (!all.length) {
          root.replaceChildren(el("div", { class: "empty-state" }, [
            el("div", { class: "empty-state-icon" }, "📋"),
            el("p", { class: "empty-state-title" }, "No requests here"),
            el("p", { class: "empty-state-description" }, "A request states a need with its " +
              "constraints, so answers can be compared rather than argued about.")
          ]));
          return;
        }
        root.replaceChildren.apply(root, sections(order(all)));
      })
      .catch(function () {
        root.removeAttribute("aria-busy");
        root.replaceChildren(el("div", { class: "empty-state" },
          el("p", { class: "empty-state-title" }, "Requests could not be loaded.")));
      });
  }

  load();
})();
