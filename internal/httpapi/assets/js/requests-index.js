/* apiAuth: the session's Authorization header, for API reads.
 *
 * Every read on this page used to go out anonymous -- harmless while the project is
 * public, and fatal for the projects whose settings most need changing: a member
 * reaches a protected project (the navigation cookie opened the page) and then every
 * panel 404s, because the page's own reads carried no credential. session.js has
 * exported authHeaders() all along; this is the two lines that use it, and for a
 * signed-out visitor the request is identical to before. */
function apiAuth(extra) {
  var h = extra || {};
  var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
  for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
  return h;
}
// A project's open and settled requests (17).
//
// THE INDEX, and the thing the Requests tab needs. Until this file existed the tab
// linked to /projects/{slug}/requests, which had no route: the ONLY request page was
// the DETAIL one at /projects/{slug}/requests/{id}, so there was no way to discover a
// request id at all -- you could reach a request by knowing its number, and by no other
// means. The board showed tasks; the request board showed nothing.
//
// Instance-wide requests (17: a request with no project is open to the whole instance)
// are INCLUDED, for the same reason lists include instance-wide ones: a question
// everyone may answer belongs in everyone's view of it. Labelled, so the reader knows
// the comparison is not confined to this project.

(function () {
  "use strict";

  var root = document.getElementById("requests-root");
  if (!root) return;
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";

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

  // project_id is omitempty in Go, so an instance-wide request has NO KEY rather than
  // 0. Reading r.project_id === 0 would therefore never be true and every shared
  // request would be labelled as this project's own -- the same class of bug as
  // lists.title/name, one layer down: a Go sentinel that the JSON does not carry.
  function isInstanceWide(r) {
    return r.project_id === undefined || r.project_id === null || r.project_id === 0;
  }

  function card(r) {
    var href = "/projects/" + encodeURIComponent(slug) + "/requests/" +
      encodeURIComponent(r.id);
    var settled = r.status !== "open";
    return '<article class="card' + (settled ? " is-muted" : "") + '">' +
      '<h3><a href="' + href + '">' + esc(r.title) + "</a></h3>" +
      (isInstanceWide(r)
        ? '<p class="card-meta"><span class="badge badge-instance">instance-wide</span></p>'
        : "") +
      '<p class="card-meta">' +
      '<span class="badge ' +
      (r.status === "open" ? "badge-blue" : "badge-slate") + '">' +
      esc(r.status) + "</span>" +
      // 17: the accepted-fit badge is set only by the request's author, and its
      // presence is the only thing that distinguishes a settled request from a stale
      // one -- so a settled request with no badge reads as unresolved.
      (r.accepted_answer_id
        ? '<span class="badge badge-green">answer accepted</span>'
        : (settled ? '<span class="badge badge-slate">no accepted answer</span>' : "")) +
      "<span>asked " + ago(r.created_at) + "</span>" +
      "</p></article>";
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

  function sections(list) {
    var open = list.filter(function (r) { return r.status === "open"; });
    var rest = list.filter(function (r) { return r.status !== "open"; });
    var out = "";
    if (open.length) {
      out += '<section class="entry-group"><h3 class="entry-category">open ' +
        '<span class="count">' + open.length + "</span></h3>" +
        '<div class="grid-responsive-2">' + open.map(card).join("") + "</div></section>";
    }
    if (rest.length) {
      out += '<section class="entry-group"><h3 class="entry-category">settled ' +
        '<span class="count">' + rest.length + "</span></h3>" +
        '<div class="grid-responsive-2">' + rest.map(card).join("") + "</div></section>";
    }
    return out;
  }

  function load() {
    // BOTH fetches: a project request index that hid instance-wide requests would hide
    // exactly the questions that most need an answer. 404 is not an error here --
    // handleListRequests answers [] when the project has none.
    Promise.all([
      fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/requests", { headers: apiAuth() })
        .then(function (r) { return r.ok ? r.json() : []; })
        .catch(function () { return []; }),
      fetch("/api/v1/requests", { headers: apiAuth() })
        .then(function (r) { return r.ok ? r.json() : []; })
        .catch(function () { return []; })
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
          root.innerHTML = '<div class="empty-state">' +
            '<div class="empty-state-icon">\u{1F4CB}</div>' +
            '<p class="empty-state-title">No requests here</p>' +
            '<p class="empty-state-description">A request states a need with its ' +
            "constraints, so answers can be compared rather than argued about.</p></div>";
          return;
        }
        root.innerHTML = sections(order(all));
      })
      .catch(function () {
        root.removeAttribute("aria-busy");
        root.innerHTML = '<div class="empty-state">' +
          '<p class="empty-state-title">Requests could not be loaded.</p></div>';
      });
  }

  load();
})();
