// The comment thread, shared by the feature and complaint detail pages.
//
// One script, two pages. The mount point in the template carries the kind and the id
// (data-thread-kind / data-thread-id), so this file does not know or care which page it
// is on -- two copies of the same renderer drift, and then one page keeps a bug the
// other has fixed.
//
// MEASURED SHAPE (tests/e2e/api_surface.py, not inferred):
//   GET  /api/v1/projects/{project_id}/threads/{thread_kind}/{thread_id} -> bare ARRAY
//   POST the same URL -> the created Comment
//   POST .../threads/{kind}/{id}/{comment_id}/vote
//
// A bare ARRAY, not a wrapper. Array.isArray on a wrapped object is false, the code
// falls back to [], and the page states the truth about data it failed to load: "no
// comments" on a feature that has some.
//
// THE THREE THINGS THAT ARE EASY TO GET WRONG, all of which this file is shaped by:
//
//  1. Label is `json:"label,omitempty"`, so the COMMON case has no label key at all.
//     Calling a method straight on it throws and takes the whole thread with it.
//
//  2. ParentID is `json:"parent_id,omitempty"`, so a top-level comment has no
//     parent_id -- for the same reason. A reply is a child of a comment, and rendering
//     the thread flat loses the structure the column records.
//
//  3. The comments are PROJECT-scoped, and thread_id is a bare integer with no foreign
//     key (fixed in the store). The server refuses a mismatch now, and this renders
//     whatever it is actually given.
//
// Rendering (step 2b): `el()` nodes only. Bodies, labels, author ids, comment ids and
// server refusal messages are text or setAttribute values; no HTML string sink.

(function () {
  "use strict";

  var el = window.ConcordDom.el;

  var mount = document.getElementById("comment-thread");
  if (!mount) return;

  var kind = mount.getAttribute("data-thread-kind");
  var threadID = mount.getAttribute("data-thread-id");
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";

  if (!kind || !threadID) {
    // Better a visible gap than a script that throws on every page it is loaded into.
    mount.replaceChildren(el("p", { class: "note" },
      "Discussion unavailable: this page did not say which thread to load."));
    return;
  }

  // The {project_id} segment is a SLUG, not a numeric id: requireProjectID resolves
  // it with GetProject(slug), and that is why passing p.id made every project-scoped
  // request 404 in project.js once already.
  var projectSlug = slug;

  function ago(unix) {
    if (!unix) return "";
    var d = Math.floor(Date.now() / 1000 - unix);
    if (d < 60) return "just now";
    if (d < 3600) return Math.floor(d / 60) + "m ago";
    if (d < 86400) return Math.floor(d / 3600) + "h ago";
    return Math.floor(d / 86400) + "d ago";
  }

  // The schema's own vocabulary. comments.label is CHECKed to exactly these five, so
  // offering a free-text label would post a value the database refuses.
  var LABELS = ["question", "objection", "support", "evidence", "offtopic"];

  function labelChip(label) {
    // Optional: label is omitempty, so the ordinary case is undefined here.
    if (!label) return null;
    return el("span", { class: "badge badge-slate" }, label);
  }

  function commentNode(c, replies) {
    var kids = replies[c.id] || [];
    var head = el("div", { class: "comment-head" }, [
      el("span", { class: "comment-author" }, "user " + c.author_id),
      labelChip(c.label),
      el("span", { class: "comment-time" }, ago(c.created_at))
    ]);
    var actions = el("div", { class: "comment-actions" }, [
      el("span", { class: "comment-score", title: "community score" }, c.score),
      el("button", {
        type: "button", class: "btn btn-link js-reply", "data-reply-to": c.id
      }, "Reply")
    ]);
    var attrs = { class: "comment", "data-comment-id": c.id };
    if (c.parent_id) attrs["data-parent-id"] = c.parent_id;
    return el("li", attrs, [
      head,
      el("div", { class: "comment-body" }, c.body),
      actions,
      kids.length
        ? el("ul", { class: "comment-replies" }, kids.map(function (r) {
            return commentNode(r, replies);
          }))
        : null
    ]);
  }

  // Top-level comments, in order. Replies are nested under their parent rather than
  // listed flat, because parent_id is the structure and a flat list loses it.
  function threadNode(comments) {
    var replies = {};
    var tops = [];
    var byId = {};
    comments.forEach(function (c) {
      byId[c.id] = c;
      if (c.parent_id) {
        if (!replies[c.parent_id]) replies[c.parent_id] = [];
        replies[c.parent_id].push(c);
      } else {
        tops.push(c);
      }
    });
    // A reply whose parent is not in this thread (deleted, or on another thread) is
    // promoted to top level rather than DROPPED. Losing it silently would be the
    // failure this file exists to avoid; showing it as an orphan says what happened.
    comments.forEach(function (c) {
      if (c.parent_id && !byId[c.parent_id]) tops.push(c);
    });

    if (!tops.length) {
      return el("div", { class: "empty-state" }, [
        el("p", { class: "empty-state-title" }, "No comments yet"),
        el("p", { class: "empty-state-description" }, "Discussion is open on this thread.")
      ]);
    }
    return el("ul", { class: "comment-list" }, tops.map(function (c) {
      return commentNode(c, replies);
    }));
  }

  function formNode(replyTo) {
    var children = [];
    if (replyTo) {
      children.push(el("p", { class: "note" }, [
        "Replying to comment ", replyTo, " ",
        el("button", { type: "button", class: "btn btn-link js-cancel-reply" }, "cancel")
      ]));
    }
    children.push(el("label", { class: "visually-hidden", for: "cmt-body" }, "Comment"));
    children.push(el("textarea", {
      id: "cmt-body", name: "body", rows: "3", placeholder: "Add to the discussion"
    }));
    // "no label" is the first option and the default: comments.label is nullable and
    // most comments carry none, so making it required would invent a constraint the
    // schema does not have.
    children.push(el("div", { class: "comment-form-row" }, [
      el("label", { class: "visually-hidden", for: "cmt-label" }, "Label"),
      el("select", { id: "cmt-label", name: "label" },
        [el("option", { value: "" }, "no label")].concat(LABELS.map(function (l) {
          return el("option", { value: l }, l);
        }))),
      el("button", { type: "submit", class: "btn" }, "Post comment")
    ]));
    return el("form", { class: "comment-form", "data-comment-form": "" }, children);
  }

  var replyingTo = null;
  var comments = [];

  function render() {
    mount.replaceChildren(
      el("h2", {}, ["Discussion ", el("span", { class: "count" }, comments.length)]),
      formNode(replyingTo),
      el("div", { class: "comment-thread-body" }, threadNode(comments))
    );
    wire();
  }

  function fail(message) {
    var notice = el("p", { class: "notice notice-error", role: "alert" }, message);
    if (mount.querySelector(".comment-form")) {
      // Prepend without an HTML string: the notice sits above the form it belongs to.
      mount.replaceChildren.apply(mount,
        [notice].concat(Array.prototype.slice.call(mount.children)));
    } else {
      mount.replaceChildren(notice);
    }
  }

  function wire() {
    Array.prototype.forEach.call(mount.querySelectorAll(".js-reply"), function (b) {
      b.addEventListener("click", function () {
        replyingTo = b.getAttribute("data-reply-to");
        render();
        var ta = mount.querySelector("#cmt-body");
        if (ta) ta.focus();
      });
    });
    var cancel = mount.querySelector(".js-cancel-reply");
    if (cancel) {
      cancel.addEventListener("click", function () {
        replyingTo = null;
        render();
      });
    }
    var form = mount.querySelector("[data-comment-form]");
    if (form) form.addEventListener("submit", onSubmit);
  }

  function onSubmit(ev) {
    ev.preventDefault();
    var body = ev.target.querySelector("#cmt-body").value.trim();
    if (!body) {
      // The store refuses a blank body too; refusing here saves the round trip and
      // keeps the textarea's contents, which a server round trip would not.
      fail("A comment needs a body.");
      return;
    }
    var payload = { body: body };
    if (replyingTo) payload.parent_id = parseInt(replyingTo, 10);
    var label = ev.target.querySelector("#cmt-label").value;
    if (label) payload.label = label;

    // One client (review item 9b). A refusal is information: the store's messages are
    // specific, and swallowing them into a generic failure is how a cross-project post
    // looks like a permissions problem.
    window.ConcordApi.post(baseURL(), payload)
      .then(function () {
        // Only re-read on success. On failure nothing is prepended, so the comment
        // that was never saved never appears to be there.
        replyingTo = null;
        load();
      })
      .catch(function (error) {
        fail(error && error.status
          ? error.message
          : "Could not reach the server. Your comment was not posted.");
      });
  }

  function baseURL() {
    return "/api/v1/projects/" + encodeURIComponent(projectSlug) + "/threads/" +
      encodeURIComponent(kind) + "/" + encodeURIComponent(threadID);
  }

  function load() {
    window.ConcordApi.get(baseURL())
      .then(function (list) {
        // Guard the shape rather than trusting it: a wrapped response here would make
        // Array.isArray false and the thread render empty with no error.
        comments = Array.isArray(list) ? list : (list && list.comments) || [];
        render();
      })
      .catch(function () {
        mount.replaceChildren(
          el("h2", {}, "Discussion"),
          el("p", { class: "note" }, "Comments could not be loaded.")
        );
      });
  }

  load();
})();
