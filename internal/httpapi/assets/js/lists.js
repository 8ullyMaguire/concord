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
// A project's lists (spec §16).
//
// §16's line that matters: "Awesome lists are a structural weakness of the git-forge
// model: they live as markdown files, merge wars decide what gets in, nothing ranks or
// classifies entries, and one list owner gatekeeps."
//
// So a list here is an OBJECT with entries that carry a category and a rating -- not a
// rendered markdown file. The page exists to make that difference visible: it links to
// each list's detail page, which is where the ranking is.
//
// MEASURED SHAPE (tests/e2e/api_surface.py):
//   GET /api/v1/projects/{slug}/lists -> bare ARRAY of
//        {id, project_id, slug, name, description, status, created_at, updated_at}
//   GET /api/v1/projects/{slug}/lists/{id} -> the same object for one list
//   GET .../lists/{id}/entries -> bare ARRAY of
//        {id, list_id, url, title, description, category, status,
//         elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at}
//
// Note `name`, not `title`, on a LIST -- while a list ENTRY has `title`. The two shapes
// are one letter apart and a page that reads the wrong one renders a list with no
// heading, silently.

(function () {
  "use strict";

  var root = document.getElementById("lists-root");
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

  function cards(lists) {
    if (!lists.length) {
      return '<div class="empty-state"><div class="empty-state-icon">\u{1F4CB}</div>' +
        '<p class="empty-state-title">No lists yet</p>' +
        '<p class="empty-state-description">A list is a curated collection of entries ' +
        "that the community ranks by comparing them. This project has not made one " +
        "yet.</p></div>";
    }
    return '<div class="grid-responsive-2">' + lists.map(function (l) {
      // `p` is not in scope here; slug is, and it is the same value. Named explicitly
      // so the next reader does not reach for a `p` that is not here -- the
      // ReferenceError that blanked the project page once.
      var href = "/projects/" + encodeURIComponent(slug) + "/lists/" +
        encodeURIComponent(l.id);
      return '<article class="card">' +
        '<h3><a href="' + href + '">' + esc(l.name) + "</a></h3>" +
        (l.description ? '<p class="card-text">' + esc(l.description) + "</p>" : "") +
        '<p class="card-meta">' +
        // project_id 0 = instance-wide. Labelled because this index is scoped to a
        // project slug, so an unlabelled shared list reads as the project's own.
        (l.project_id
          ? ""
          : '<span class="badge badge-instance">instance-wide</span>') +
        '<span class="badge ' +
        (l.status === "open" ? "badge-blue" : "badge-slate") + '">' +
        esc(l.status) + "</span>" +
        '<span>updated ' + ago(l.updated_at) + "</span>" +
        "</p></article>";
    }).join("") + "</div>";
  }

  function load() {
    fetch("/api/v1/projects/" + encodeURIComponent(slug) + "/lists", { headers: apiAuth() })
      .then(function (r) {
        if (r.status === 404) throw { notFound: true };
        if (!r.ok) throw { status: r.status };
        return r.json();
      })
      .then(function (list) {
        root.removeAttribute("aria-busy");
        root.innerHTML = '<div class="list-cards">' +
          cards(Array.isArray(list) ? list : []) + "</div>";
      })
      .catch(function (err) {
        root.removeAttribute("aria-busy");
        if (err && err.notFound) {
          root.innerHTML = '<div class="empty-state">' +
            '<p class="empty-state-title">There are no lists at this address.</p></div>';
          return;
        }
        root.innerHTML = '<div class="empty-state">' +
          '<p class="empty-state-title">Lists could not be loaded.</p></div>';
      });
  }

  load();
})();
