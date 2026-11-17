
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

  // One escaping rule for the whole frontend: `escape.js` (loaded before this file) owns it. This file's copy replaced the same
  // five characters in its own way; a single pass over a character class cannot disagree with another page about which are dangerous.
  var el = window.ConcordDom.el;

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
      return el('div', { class: 'empty-state' }, [
        el('div', { class: 'empty-state-icon' }, '\u{1F4CB}'),
        el('p', { class: 'empty-state-title' }, 'No lists yet'),
        el('p', { class: 'empty-state-description' },
          'A list is a curated collection of entries that the community ranks by ' +
          'comparing them. This project has not made one yet.')
      ]);
    }
    return el('div', { class: 'grid-responsive-2' }, lists.map(function (l) {
      // `p` is not in scope here; slug is, and it is the same value. Named explicitly
      // so the next reader does not reach for a `p` that is not here -- the
      // ReferenceError that blanked the project page once.
      var href = '/projects/' + encodeURIComponent(slug) + '/lists/' +
        encodeURIComponent(l.id);
      return el('article', { class: 'card' }, [
        el('h3', {}, el('a', { href: href }, l.name)),
        l.description ? el('p', { class: 'card-text' }, l.description) : null,
        el('p', { class: 'card-meta' }, [
          // project_id 0 = instance-wide. Labelled because this index is scoped to a
          // project slug, so an unlabelled shared list reads as the project's own.
          l.project_id ? null : el('span', { class: 'badge badge-instance' }, 'instance-wide'),
          ' ',
          el('span', { class: 'badge ' + (l.status === 'open' ? 'badge-blue' : 'badge-slate') }, l.status),
          ' ', el('span', {}, 'updated ' + ago(l.updated_at))
        ])
      ]);
    }));
  }

  function load() {
    window.ConcordApi.get("/api/v1/projects/" + encodeURIComponent(slug) + "/lists")
      .catch(function (error) {
        if (error && error.status === 404) throw { notFound: true };
        throw { status: error && error.status };
      })
      .then(function (list) {
        root.removeAttribute("aria-busy");
        root.replaceChildren(el('div', { class: 'list-cards' },
          cards(Array.isArray(list) ? list : [])));
      })
      .catch(function (err) {
        root.removeAttribute("aria-busy");
        if (err && err.notFound) {
          root.replaceChildren(el('div', { class: 'empty-state' },
            el('p', { class: 'empty-state-title' }, 'There are no lists at this address.')));
          return;
        }
        root.replaceChildren(el('div', { class: 'empty-state' },
          el('p', { class: 'empty-state-title' }, 'Lists could not be loaded.')));
      });
  }

  load();
})();
