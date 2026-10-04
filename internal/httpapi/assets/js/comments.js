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
//     Calling a method straight on it throws and takes the whole thread with it. Same
//     ReferenceError class that blanked the project page once.
//
//  2. ParentID is `json:"parent_id,omitempty"`, so a top-level comment has no
//     parent_id -- for the same reason. A reply is a child of a comment, and rendering
//     the thread flat loses the structure the column records.
//
//  3. The comments are PROJECT-scoped, and thread_id is a bare integer with no foreign
//     key (fixed in the store). So the project in the URL and the project the thread
//     belongs to are not guaranteed to be the same thing from the client's side -- the
//     server refuses the mismatch now, and this renders whatever it is actually given.

(function () {
  "use strict";

  var mount = document.getElementById("comment-thread");
  if (!mount) return;

  var kind = mount.getAttribute("data-thread-kind");
  var threadID = mount.getAttribute("data-thread-id");
  var slugEl = document.querySelector("[data-slug]");
  var slug = slugEl ? slugEl.textContent.trim() : "";

  if (!kind || !threadID) {
    // Better a visible gap than a script that throws on every page it is loaded into.
    mount.innerHTML =
      '<p class="note">Discussion unavailable: this page did not say which ' +
      "thread to load.</p>";
    return;
  }

  // The {project_id} segment is a SLUG, not a numeric id: requireProjectID resolves
  // it with GetProject(slug), and that is why passing p.id made every project-scoped
  // request 404 in project.js once already. So the path segment here is the slug the
  // page already carries in data-slug, and data-project-id is only a fallback.
  var projectSlug = slug;

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

  // The schema's own vocabulary. comments.label is CHECKed to exactly these five, so
  // offering a free-text label would post a value the database refuses.
  var LABELS = ["question", "objection", "support", "evidence", "offtopic"];

  function labelChip(label) {
    // Optional: label is omitempty, so the ordinary case is undefined here.
    if (!label) return "";
    return ' <span class="badge badge-slate">' + esc(label) + "</span>";
  }

  function commentNode(c, replies) {
    var kids = replies[c.id] || [];
    return (
      '<li class="comment" data-comment-id="' + esc(c.id) + '"' +
      (c.parent_id ? ' data-parent-id="' + esc(c.parent_id) + '"' : "") +
      ">" +
      '<div class="comment-head">' +
      '<span class="comment-author">user ' + esc(c.author_id) + "</span>" +
      labelChip(c.label) +
      '<span class="comment-time">' + ago(c.created_at) + "</span>" +
      "</div>" +
      '<div class="comment-body">' + esc(c.body) + "</div>" +
      '<div class="comment-actions">' +
      '<span class="comment-score" title="community score">' + esc(c.score) + "</span>" +
      '<button type="button" class="btn btn-link js-reply" data-reply-to="' +
      esc(c.id) + '">Reply</button>' +
      "</div>" +
      (kids.length
        ? '<ul class="comment-replies">' +
          kids.map(function (r) { return commentNode(r, replies); }).join("") +
          "</ul>"
        : "") +
      "</li>"
    );
  }

  // Top-level comments, in order. Replies are nested under their parent rather than
  // listed flat, because parent_id is the structure and a flat list loses it.
  function threadMarkup(comments) {
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
      return '<div class="empty-state"><p class="empty-state-title">No comments ' +
        "yet</p><p class=\"empty-state-description\">Discussion is open on this " +
        "thread.</p></div>";
    }
    return "<ul class=\"comment-list\">" +
      tops.map(function (c) { return commentNode(c, replies); }).join("") +
      "</ul>";
  }

  function formMarkup(replyTo) {
    var opts = LABELS.map(function (l) {
      return '<option value="' + esc(l) + '">' + esc(l) + "</option>";
    }).join("");
    return (
      '<form class="comment-form" data-comment-form>' +
      (replyTo
        ? '<p class="note">Replying to comment ' + esc(replyTo) +
          ' <button type="button" class="btn btn-link js-cancel-reply">cancel</button></p>'
        : "") +
      '<label class="visually-hidden" for="cmt-body">Comment</label>' +
      '<textarea id="cmt-body" name="body" rows="3" ' +
      'placeholder="Add to the discussion"></textarea>' +
      '<div class="comment-form-row">' +
      '<label class="visually-hidden" for="cmt-label">Label</label>' +
      // "no label" is the first option and the default: comments.label is nullable and
      // most comments carry none, so making it required would invent a constraint the
      // schema does not have.
      '<select id="cmt-label" name="label"><option value="">no label</option>' +
      opts +
      "</select>" +
      '<button type="submit" class="btn">Post comment</button>' +
      "</div></form>"
    );
  }

  var replyingTo = null;
  var comments = [];

  function render() {
    mount.innerHTML =
      "<h2>Discussion <span class=\"count\">" + comments.length + "</span></h2>" +
      formMarkup(replyingTo) +
      '<div class="comment-thread-body">' + threadMarkup(comments) + "</div>";
    wire();
  }

  function fail(message) {
    var el = mount.querySelector(".comment-form");
    if (el) {
      mount.insertAdjacentHTML(
        "afterbegin",
        '<p class="notice notice-error" role="alert">' + esc(message) + "</p>"
      );
    } else {
      mount.innerHTML = '<p class="notice notice-error" role="alert">' +
        esc(message) + "</p>";
    }
  }

  function wire() {
    Array.prototype.forEach.call(
      mount.querySelectorAll(".js-reply"),
      function (b) {
        b.addEventListener("click", function () {
          replyingTo = b.getAttribute("data-reply-to");
          render();
          var ta = mount.querySelector("#cmt-body");
          if (ta) ta.focus();
        });
      }
    );
    var cancel = mount.querySelector(".js-cancel-reply");
    if (cancel) {
      cancel.addEventListener("click", function () {
        replyingTo = null;
        render();
      });
    }
    var form = mount.querySelector("[data-comment-form]");
    if (form) {
      form.addEventListener("submit", onSubmit);
    }
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

    var headers = { "Content-Type": "application/json" };
    var token = localStorage.getItem("concord_token");
    if (token) headers.Authorization = "Bearer " + token;

    fetch(baseURL(), {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload)
    })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (j) {
          return { ok: r.ok, body: j };
        });
      })
      .then(function (res) {
        if (!res.ok) {
          // A refusal is information. The store's messages are specific -- "a comment
          // needs a body", "belongs to project 2, not 1" -- and swallowing them into a
          // generic failure is how a cross-project post looks like a permissions
          // problem.
          fail(res.body && res.body.error ? res.body.error : "That comment was refused.");
          return;
        }
        // Only re-read on success. On failure nothing is prepended, so the comment
        // that was never saved never appears to be there.
        replyingTo = null;
        load();
      })
      .catch(function () {
        fail("Could not reach the server. Your comment was not posted.");
      });
  }

  function baseURL() {
    return "/api/v1/projects/" + encodeURIComponent(projectSlug) + "/threads/" +
      encodeURIComponent(kind) + "/" + encodeURIComponent(threadID);
  }

  function load() {
    var headers = {};
    var token = localStorage.getItem("concord_token");
    if (token) headers.Authorization = "Bearer " + token;
    fetch(baseURL(), { headers: headers })
      .then(function (r) {
        if (!r.ok) throw new Error(r.status);
        return r.json();
      })
      .then(function (list) {
        // Guard the shape rather than trusting it: a wrapped response here would make
        // Array.isArray false and the thread render empty with no error, which is the
        // exact failure this whole exercise keeps finding.
        comments = Array.isArray(list) ? list : (list && list.comments) || [];
        render();
      })
      .catch(function () {
        mount.innerHTML =
          '<h2>Discussion</h2><p class="note">Comments could not be loaded.</p>';
      });
  }

  load();
})();