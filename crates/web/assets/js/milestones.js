// Concord roadmap and one milestone (docs/specs/idea-intake-and-milestones-spec.md
// §4/§5).
//
// Two pages, one reader, because they read the same shape: the roadmap is
// GET /roadmap and a milestone is GET /milestones/{id}, and the roadmap's numbers
// are the milestone rows summed by the server -- a page that recomputed the totals
// would be a page that can disagree with the route it printed.
//
// Three decisions worth stating:
//
// 1. A missing target date renders as "no date", never as today or as a dash.
//    A milestone without a date has made no promise, and a placeholder date is a
//    promise the project never made (spec §4.2).
// 2. Progress is printed as shipped/total with its provenance beside it
//    ("N seeded, M unshipped"), because a bar computed from `status` measures
//    whoever ran the import -- 925 of 1,874 features on this instance have no
//    creation event at all.
// 3. Slip is printed only when it was measured. A zero-day slip is the best
//    possible answer and the easiest thing to fake, so null renders as "not
//    measured" (the same absence rule as UnknownReviewHours).
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  // apiAuth is the shared name for "this request carries the session", and it is the
  // string the asset audit looks for (internal/httpapi/assets_auth_reads_test.go):
  // a fetch that assembles its headers some other way is a read that works for a
  // signed-out visitor and 404s for the member whose project it is.
  function apiAuth(extra) {
    var h = extra || {};
    var tok = window.ConcordSession ? window.ConcordSession.instance().authHeaders() : {};
    for (var k in tok) { if (Object.prototype.hasOwnProperty.call(tok, k)) h[k] = tok[k]; }
    return h;
  }

  function api(path, opts) {
    var o = opts || {};
    // The token carrier is INLINE in the fetch call on purpose: the asset audit
    // reads call sites, and a helper that adds the header one level away is exactly
    // the shape that shipped a member-only read as anonymous.
    // Only the SLUG is encoded. Encoding the whole path turned "ms-demo/roadmap"
    // into "ms-demo%2Froadmap" and the server answered `not found: project
    // "ms-demo/roadmap"` -- which the browser suite caught on its first run and no
    // Go test could have: the route is correct, the URL the page built was not.
    var cut = path.indexOf('/');
    var url = cut < 0
      ? '/api/v1/projects/' + encodeURIComponent(path)
      : '/api/v1/projects/' + encodeURIComponent(path.slice(0, cut)) + path.slice(cut);
    return fetch(url, {
      method: o.method || 'GET',
      body: o.body,
      headers: apiAuth(o.headers || {})
    }).then(function (r) {
      if (!r.ok) {
        return r.text().then(function (t) {
          var msg = t;
          try { msg = JSON.parse(t).error || t; } catch (e) { /* plain text */ }
          throw new Error(msg || ('HTTP ' + r.status));
        });
      }
      return r.status === 204 ? null : r.json();
    });
  }

  function dateText(ts) {
    if (!ts) return '<span class="muted">no date</span>';
    var d = new Date(ts * 1000);
    return d.toISOString().slice(0, 10);
  }

  function statusTag(m) {
    var t = '<span class="ms-status ms-' + esc(m.status) + '">' + esc(m.status) + '</span>';
    if (m.at_risk) {
      // A target date in the past with open features. The badge names the reason
      // rather than just colouring the row: "at risk" alone is an adjective.
      t += ' <span class="ms-risk" title="target date passed with features still open">at risk</span>';
    }
    return t;
  }

  function progress(m) {
    var total = m.total || 0, shipped = m.shipped || 0;
    var pct = total ? Math.round((shipped / total) * 100) : 0;
    var prov = '';
    if (m.seeded) prov += ' · ' + m.seeded + ' seeded';
    if (m.unshipped) prov += ' · ' + m.unshipped + ' unshipped';
    return '<div class="ms-progress" title="' + pct + '% shipped"><span style="width:' + pct + '%"></span></div>' +
      '<div class="ms-counts">' + shipped + '/' + total + ' shipped · ' + (m.open || 0) + ' open' + prov + '</div>';
  }

  function slipText(row) {
    if (row.slip_days != null) {
      var d = Math.round(row.slip_days);
      return d > 0 ? ('slipped ' + d + 'd') : (d < 0 ? ('early by ' + (-d) + 'd') : 'on time (0d)');
    }
    if (row.oldest_open_days != null) return 'oldest open ' + Math.round(row.oldest_open_days) + 'd';
    return 'not measured';
  }

  function renderRoadmap(box, slug) {
    api(slug + '/roadmap').then(function (data) {
      var ms = data.milestones || [];
      var slip = {};
      (data.slippage || []).forEach(function (s) { slip[s.milestone_id] = s; });
      var html = '<div class="roadmap-totals">' +
        '<span>' + (data.totals.total || 0) + ' features in milestones</span>' +
        '<span>' + (data.totals.open || 0) + ' open</span>' +
        '<span>' + (data.totals.shipped || 0) + ' shipped</span></div>';

      html += '<form class="ms-create" data-ms-create="" onsubmit="return false;">' +
        '<input name="title" placeholder="Milestone title" aria-label="Milestone title" required>' +
        '<input name="target_date" type="date" aria-label="Target date">' +
        '<button type="submit" class="btn">Create milestone</button>' +
        '<span class="ms-error" role="alert"></span></form>';

      if (!ms.length) {
        html += '<div class="empty-state"><p class="empty-state-title">No milestones yet</p>' +
          '<p>A milestone groups features towards a date. The features no milestone claims are the backlog.</p></div>';
      } else {
        html += '<div class="roadmap-timeline" data-roadmap-column="">';
        ms.forEach(function (m) {
          var row = slip[m.id] || {};
          html += '<div class="roadmap-column">' +
            '<div class="roadmap-head">' +
            '<a class="roadmap-title" href="/projects/' + encodeURIComponent(slug) + '/milestones/' + m.id + '">' + esc(m.title) + '</a>' +
            statusTag(m) + '</div>' +
            '<div class="roadmap-date">' + dateText(m.target_date) + ' · ' + esc(slipText(row)) + '</div>' +
            progress(m) +
            '<button class="btn btn-quiet" data-ms-close="' + m.id + '"' + (m.status === 'done' || m.status === 'cancelled' ? ' disabled' : '') + '>Close</button>' +
            '</div>';
        });
        html += '</div>';
      }
      box.innerHTML = html;
      box.setAttribute('aria-busy', 'false');

      var form = box.querySelector('[data-ms-create]');
      form.addEventListener('submit', function () {
        var title = form.querySelector('[name=title]').value.trim();
        var date = form.querySelector('[name=target_date]').value;
        if (!title) return;
        var body = { title: title };
        if (date) body.target_date = Date.parse(date + 'T00:00:00Z') / 1000;
        api(slug + '/milestones', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
        }).then(function () { renderRoadmap(box, slug); }).catch(function (e) {
          form.querySelector('.ms-error').textContent = e.message;
        });
      });

      box.querySelectorAll('[data-ms-close]').forEach(function (btn) {
        btn.addEventListener('click', function () {
          var id = btn.getAttribute('data-ms-close');
          // Cancel rather than "done": the server refuses to call a milestone done
          // while features are open, and this button has no successor to offer.
          api(slug + '/milestones/' + id + '/close', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cancel: true })
          }).then(function () { renderRoadmap(box, slug); }).catch(function (e) { alert(e.message); });
        });
      });
    }).catch(function (e) {
      box.innerHTML = '<div class="empty-state"><p class="empty-state-title">Could not load the roadmap</p><p>' + esc(e.message) + '</p></div>';
      box.setAttribute('aria-busy', 'false');
    });
  }

  function renderMilestone(box, slug, id) {
    // The candidates for bulk assign come from the project's feature list, read
    // tolerantly: a reader who cannot list features still sees this milestone.
    Promise.all([
      api(slug + '/milestones/' + id),
      api(slug + '/features/').catch(function () { return null; })
    ]).then(function (res) {
      var data = res[0];
      var allFeatures = (res[1] && (res[1].features || res[1])) || [];
      if (!Array.isArray(allFeatures)) allFeatures = [];
      var m = data.milestone, features = data.features || [];
      document.title = m.title + ' · Concord';
      var titleEl = document.getElementById('ms-title');
      if (titleEl) titleEl.textContent = m.title;
      var sub = document.getElementById('ms-subtitle');
      if (sub) {
        sub.innerHTML = statusTag(m) + ' · ' + dateText(m.target_date) +
          (m.body ? ' · ' + esc(m.body) : '');
      }
      var groups = [
        ['open', features.filter(function (f) { return f.status !== 'shipped' && f.status !== 'rejected'; })],
        ['shipped', features.filter(function (f) { return f.status === 'shipped'; })],
        ['rejected', features.filter(function (f) { return f.status === 'rejected'; })]
      ];
      // Eligible candidates: anything in the project that is not already here.
      var here = {};
      features.forEach(function (f) { here[f.id] = true; });
      var candidates = allFeatures.filter(function (f) {
        return !here[f.id] && f.status !== 'shipped' && f.status !== 'rejected';
      });

      var html = progress(m);
      groups.forEach(function (g) {
        if (!g[1].length) return;
        html += '<h2 class="ms-group">' + esc(g[0]) + ' <span class="muted">(' + g[1].length + ')</span></h2><ul class="ms-features">';
        g[1].forEach(function (f, i) {
          // Order inside the milestone (§6): up/down rather than drag, because a drag
          // has no keyboard path and the order is a list, not a canvas.
          var pos = (g[0] === 'open')
            ? '<button class="btn btn-quiet" data-ms-up="' + esc(f.id) + '"' + (i === 0 ? ' disabled' : '') + '>&uarr;</button>' +
              '<button class="btn btn-quiet" data-ms-down="' + esc(f.id) + '"' +
              (i === g[1].length - 1 ? ' disabled' : '') + '>&darr;</button>'
            : '';
          html += '<li data-ms-feature="' + esc(f.id) + '"><a href="/projects/' + encodeURIComponent(slug) + '/features/' + f.id + '">' + esc(f.title) + '</a>' +
            ' <span class="muted">#' + f.id + ' · ' + esc(f.status) + '</span> ' + pos + '</li>';
        });
        html += '</ul>';
      });
      // §6: a selection into the milestone. The candidate list is every feature the
      // project has that is not already in it -- a number input would require the
      // reader to know an id the page is already holding.
      html += '<div class="ms-panel"><h2>Add features</h2>' +
        '<p class="muted">Open features not in this milestone. Selecting several adds them in one act.</p>' +
        '<div class="ms-row"><select data-ms-add multiple size="6" aria-label="Features to add">' +
        (candidates.length ? candidates.map(function (f) {
          return '<option value="' + esc(f.id) + '">#' + esc(f.id) + ' ' + esc(f.title) + ' (' + esc(f.status) + ')</option>';
        }).join('') : '<option disabled>none available</option>') + '</select>' +
        '<button class="btn" data-ms-assign' + (candidates.length ? '' : ' disabled') + '>Add selected</button></div>' +
        '<p class="form-status" data-ms-assign-status hidden></p></div>';

      // The close control names BOTH paths: a successor moves the unfinished work,
      // and cancel closes without moving it. One button that quietly did the wrong
      // one of those is how a milestone loses its features.
      html += '<div class="ms-actions">' +
        '<button class="btn" data-ms-close="' + m.id + '"' + (m.status === 'done' || m.status === 'cancelled' ? ' disabled' : '') + '>Close as cancelled</button>' +
        '<span class="muted">Closing with a successor moves the open features; cancelling leaves them where they are.</span></div>';
      box.innerHTML = html;
      box.setAttribute('aria-busy', 'false');
      var assignBtn = box.querySelector('[data-ms-assign]');
      if (assignBtn) {
        assignBtn.addEventListener('click', function () {
          var sel = box.querySelector('[data-ms-add]');
          var ids = Array.prototype.map.call(sel.selectedOptions || sel.querySelectorAll('option:checked'),
            function (o) { return Number(o.value); });
          if (!ids.length) return;
          api(slug + '/milestones/' + id + '/features', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ feature_ids: ids })
          }).then(function () { renderMilestone(box, slug, id); })
            .catch(function (e) { assignBtn.parentNode.querySelector('.form-status').textContent = e.message; });
        });
      }

      // The order is the DOM's order: up/down swap two entries and PUT the whole list,
      // so the server never has to infer what a "move" meant.
      function wireMove(dir) {
        box.querySelectorAll('[data-ms-' + dir + ']').forEach(function (btn) {
          btn.addEventListener('click', function () {
            var li = btn.closest('li');
            var sibling = dir === 'up' ? li.previousElementSibling : li.nextElementSibling;
            if (!sibling) return;
            if (dir === 'up') li.parentNode.insertBefore(li, sibling);
            else li.parentNode.insertBefore(sibling, li);
            var order = Array.prototype.map.call(li.parentNode.querySelectorAll('[data-ms-feature]'),
              function (el) { return Number(el.getAttribute('data-ms-feature')); });
            api(slug + '/milestones/' + id + '/order', {
              method: 'PUT', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ feature_ids: order })
            }).catch(function (e) { alert(e.message); });
          });
        });
      }
      wireMove('up');
      wireMove('down');

      box.querySelectorAll('[data-ms-close]').forEach(function (btn) {
        btn.addEventListener('click', function () {
          api(slug + '/milestones/' + id + '/close', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cancel: true })
          }).then(function () { renderMilestone(box, slug, id); }).catch(function (e) { alert(e.message); });
        });
      });
    }).catch(function (e) {
      box.innerHTML = '<div class="empty-state"><p class="empty-state-title">Could not load this milestone</p><p>' + esc(e.message) + '</p></div>';
      box.setAttribute('aria-busy', 'false');
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    var roadmap = document.getElementById('roadmap');
    if (roadmap) {
      var slug = roadmap.getAttribute('data-slug') || slugFromPath();
      if (slug) renderRoadmap(roadmap, slug);
      return;
    }
    var box = document.getElementById('milestone');
    if (box) {
      var s = box.getAttribute('data-slug') || slugFromPath();
      var id = box.getAttribute('data-milestone');
      if (s && id) renderMilestone(box, s, id);
    }
  });
})();
