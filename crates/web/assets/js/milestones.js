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
//
// Rendering (step 2b): el() trees only; hostile titles and error sentences are
// text nodes or setAttribute values.
(function () {
  'use strict';
  var el = window.ConcordDom.el;

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
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
    // One client (review item 9b). `ApiError.message` carries the server's sentence — including a plain-text one, which
    // is why the old code had a `JSON.parse` attempt inside a `try` — and a 204 resolves to `null` there too.
    return window.ConcordApi.request(o.method || 'GET', url, o.body === undefined ? null : o.body);
  }

  function dateText(ts) {
    if (!ts) return el('span', { class: 'muted' }, 'no date');
    var d = new Date(ts * 1000);
    return d.toISOString().slice(0, 10);
  }

  function statusTag(m) {
    var nodes = [el('span', { class: 'ms-status ms-' + m.status }, m.status)];
    if (m.at_risk) {
      // A target date in the past with open features. The badge names the reason
      // rather than just colouring the row: "at risk" alone is an adjective.
      nodes.push(' ', el('span', { class: 'ms-risk', title: 'target date passed with features still open' }, 'at risk'));
    }
    return nodes;
  }

  function progress(m) {
    var total = m.total || 0, shipped = m.shipped || 0;
    var pct = total ? Math.round((shipped / total) * 100) : 0;
    var prov = '';
    if (m.seeded) prov += ' · ' + m.seeded + ' seeded';
    if (m.unshipped) prov += ' · ' + m.unshipped + ' unshipped';
    return [
      el('div', { class: 'ms-progress', title: pct + '% shipped' },
        el('span', { style: 'width:' + pct + '%' })),
      el('div', { class: 'ms-counts' },
        shipped + '/' + total + ' shipped · ' + (m.open || 0) + ' open' + prov)
    ];
  }

  function slipText(row) {
    if (row.slip_days != null) {
      var d = Math.round(row.slip_days);
      return d > 0 ? ('slipped ' + d + 'd') : (d < 0 ? ('early by ' + (-d) + 'd') : 'on time (0d)');
    }
    if (row.oldest_open_days != null) return 'oldest open ' + Math.round(row.oldest_open_days) + 'd';
    return 'not measured';
  }

  function errorState(title, message) {
    return el('div', { class: 'empty-state' }, [
      el('p', { class: 'empty-state-title' }, title),
      el('p', {}, message)
    ]);
  }

  function renderRoadmap(box, slug) {
    api(slug + '/roadmap').then(function (data) {
      var ms = data.milestones || [];
      var slip = {};
      (data.slippage || []).forEach(function (s) { slip[s.milestone_id] = s; });
      var children = [el('div', { class: 'roadmap-totals' }, [
        el('span', {}, (data.totals.total || 0) + ' features in milestones'),
        el('span', {}, (data.totals.open || 0) + ' open'),
        el('span', {}, (data.totals.shipped || 0) + ' shipped')
      ])];

      var titleInput = el('input', {
        name: 'title', placeholder: 'Milestone title', 'aria-label': 'Milestone title', required: true
      });
      var dateInput = el('input', { name: 'target_date', type: 'date', 'aria-label': 'Target date' });
      var err = el('span', { class: 'ms-error', role: 'alert' });
      var form = el('form', { class: 'ms-create', 'data-ms-create': '' }, [
        titleInput, dateInput,
        el('button', { type: 'submit', class: 'btn' }, 'Create milestone'), err
      ]);
      children.push(form);

      if (!ms.length) {
        children.push(el('div', { class: 'empty-state' }, [
          el('p', { class: 'empty-state-title' }, 'No milestones yet'),
          el('p', {}, 'A milestone groups features towards a date. The features no milestone claims are the backlog.')
        ]));
      } else {
        var columns = el('div', { class: 'roadmap-timeline', 'data-roadmap-column': '' }, ms.map(function (m) {
          var row = slip[m.id] || {};
          var closed = m.status === 'done' || m.status === 'cancelled';
          return el('div', { class: 'roadmap-column' }, [
            el('div', { class: 'roadmap-head' }, [
              el('a', {
                class: 'roadmap-title',
                href: '/projects/' + encodeURIComponent(slug) + '/milestones/' + m.id
              }, m.title),
              ' ', statusTag(m)
            ]),
            el('div', { class: 'roadmap-date' }, [
              dateText(m.target_date), ' · ', slipText(row)
            ]),
            progress(m),
            el('button', { class: 'btn btn-quiet', 'data-ms-close': m.id, disabled: closed ? true : null }, 'Close')
          ]);
        }));
        children.push(columns);
      }
      box.replaceChildren.apply(box, children);
      box.setAttribute('aria-busy', 'false');

      form.addEventListener('submit', function (event) {
        // The inline `onsubmit="return false;"` this replaced was load-bearing: without it the browser navigates and the
        // milestone the fetch just created is shown by a full page load instead of by `renderRoadmap` below.
        event.preventDefault();
        var title = titleInput.value.trim();
        var date = dateInput.value;
        if (!title) return;
        var body = { title: title };
        if (date) body.target_date = Date.parse(date + 'T00:00:00Z') / 1000;
        api(slug + '/milestones', {
          method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
        }).then(function () { renderRoadmap(box, slug); }).catch(function (e) {
          err.textContent = e.message;
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
      box.replaceChildren(errorState('Could not load the roadmap', e.message));
      box.setAttribute('aria-busy', 'false');
    });
  }

  function renderMilestone(box, slug, id) {
    // The candidates for bulk assign come from the project's feature list, read
    // tolerantly: a reader who cannot list features still sees this milestone.
    Promise.all([
      api(slug + '/milestones/' + id),
      api(slug + '/features').catch(function () { return null; })
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
        var subChildren = statusTag(m);
        subChildren.push(' · ', dateText(m.target_date));
        if (m.body) subChildren.push(' · ', m.body);
        sub.replaceChildren.apply(sub, subChildren);
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

      var children = progress(m).slice();
      groups.forEach(function (g) {
        if (!g[1].length) return;
        children.push(el('h2', { class: 'ms-group' }, [
          g[0], ' ', el('span', { class: 'muted' }, '(' + g[1].length + ')')
        ]));
        var list = el('ul', { class: 'ms-features' }, g[1].map(function (f, i) {
          // Order inside the milestone (§6): up/down rather than drag, because a drag
          // has no keyboard path and the order is a list, not a canvas.
          var pos = (g[0] === 'open') ? [
            el('button', { class: 'btn btn-quiet', 'data-ms-up': f.id, disabled: i === 0 ? true : null }, '↑'),
            el('button', {
              class: 'btn btn-quiet', 'data-ms-down': f.id,
              disabled: i === g[1].length - 1 ? true : null
            }, '↓')
          ] : [];
          return el('li', { 'data-ms-feature': f.id }, [
            el('a', { href: '/projects/' + encodeURIComponent(slug) + '/features/' + f.id }, f.title),
            ' ', el('span', { class: 'muted' }, '#' + f.id + ' · ' + f.status), ' ', pos
          ]);
        }));
        children.push(list);
      });
      // §6: a selection into the milestone. The candidate list is every feature the
      // project has that is not already in it -- a number input would require the
      // reader to know an id the page is already holding.
      var select = el('select', {
        'data-ms-add': '', multiple: true, size: 6, 'aria-label': 'Features to add'
      }, candidates.length ? candidates.map(function (f) {
        return el('option', { value: f.id }, '#' + f.id + ' ' + f.title + ' (' + f.status + ')');
      }) : [el('option', { disabled: true }, 'none available')]);
      var assignStatus = el('p', { class: 'form-status', 'data-ms-assign-status': '', hidden: true });
      children.push(el('div', { class: 'ms-panel' }, [
        el('h2', {}, 'Add features'),
        el('p', { class: 'muted' }, 'Open features not in this milestone. Selecting several adds them in one act.'),
        el('div', { class: 'ms-row' }, [
          select,
          el('button', { class: 'btn', 'data-ms-assign': '', disabled: candidates.length ? null : true }, 'Add selected')
        ]),
        assignStatus
      ]));

      // The close control names BOTH paths: a successor moves the unfinished work,
      // and cancel closes without moving it. One button that quietly did the wrong
      // one of those is how a milestone loses its features.
      children.push(el('div', { class: 'ms-actions' }, [
        el('button', {
          class: 'btn', 'data-ms-close': m.id,
          disabled: m.status === 'done' || m.status === 'cancelled' ? true : null
        }, 'Close as cancelled'),
        el('span', { class: 'muted' },
          'Closing with a successor moves the open features; cancelling leaves them where they are.')
      ]));
      box.replaceChildren.apply(box, children);
      box.setAttribute('aria-busy', 'false');
      var assignBtn = box.querySelector('[data-ms-assign]');
      if (assignBtn) {
        assignBtn.addEventListener('click', function () {
          var sel = box.querySelector('[data-ms-add]');
          var ids = (sel.selectedOptions || []).map(function (o) { return Number(o.value); });
          if (!ids.length) return;
          api(slug + '/milestones/' + id + '/features', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ feature_ids: ids })
          }).then(function () { renderMilestone(box, slug, id); })
            .catch(function (e) { assignStatus.textContent = e.message; });
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
            var order = li.parentNode.querySelectorAll('[data-ms-feature]').map(function (e) {
              return Number(e.getAttribute('data-ms-feature'));
            });
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
      box.replaceChildren(errorState('Could not load this milestone', e.message));
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
      // A missing milestone must say so; the loading sentence is not an answer (D-211).
      setTimeout(function () {
        if ((box.innerText || box.textContent || '').indexOf('Loading milestone') !== -1) {
          box.replaceChildren(errorState('No such milestone', 'This milestone could not be loaded.'));
          box.setAttribute('aria-busy', 'false');
        }
      }, 8000);
    }
  });
})();
