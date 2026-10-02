// Concord — project documents.
//
// Reachable at /projects/{slug}/documents. Before this page the whole document
// surface was API-only: 1,087 documents were stored, searchable with curl, and
// invisible on the site. This is the viewer.
//
// Markdown is rendered by window.ConcordMarkdown, which escapes raw HTML. The
// esc() helper below is the ordinary one for interpolating our own values, and
// the two are not interchangeable — see the comment at the top of markdown.js.
(function () {
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // KIND_META drives the rail. The order is reading order for a project: what it
  // is, what it must do, how it is being built, why it was built that way.
  var KIND_META = [
    { kind: 'readme',    label: 'README',    blurb: 'What this project is' },
    { kind: 'spec',      label: 'Spec',      blurb: 'What it must do' },
    { kind: 'plan',      label: 'Plan',      blurb: 'How it is being built' },
    { kind: 'adr',       label: 'Decisions', blurb: 'Why it was built that way' },
    { kind: 'changelog', label: 'Changelog', blurb: 'What changed' },
    { kind: 'wiki',      label: 'Wiki',      blurb: 'Everything else' }
  ];

  function slugFromPath() {
    var parts = window.location.pathname.split('/').filter(Boolean);
    return parts.length >= 2 && parts[0] === 'projects' ? decodeURIComponent(parts[1]) : '';
  }

  function fmtDate(unix) {
    if (!unix) return '';
    try { return new Date(unix * 1000).toLocaleDateString(); } catch (e) { return ''; }
  }

  function fmtBytes(n) {
    if (!n) return '0 B';
    var u = ['B', 'KB', 'MB', 'GB'], i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(1)) + ' ' + u[i];
  }

  // KINDS with a count and a slug, in the order above, plus any kind the server
  // knows that this file does not. A new kind in the CHECK constraint must not
  // silently vanish from the rail.
  function kindsWithCounts(docs) {
    var counts = {};
    (docs || []).forEach(function (d) {
      counts[d.kind] = (counts[d.kind] || 0) + 1;
    });
    var known = KIND_META.map(function (k) {
      return { kind: k.kind, label: k.label, blurb: k.blurb, count: counts[k.kind] || 0 };
    });
    var seen = {};
    known.forEach(function (k) { seen[k.kind] = true; });
    Object.keys(counts).forEach(function (k) {
      if (!seen[k]) known.push({ kind: k, label: k, blurb: '', count: counts[k] });
    });
    return known;
  }

  function currentKind() {
    var m = window.location.search.match(/[?&]kind=([^&]+)/);
    return m ? decodeURIComponent(m[1]) : 'readme';
  }

  // A document this large cannot be assigned to innerHTML in one go: 1.7 MB of
  // markdown builds a very large DOM and the tab locks for seconds. The corpus
  // contains such a document (a vendored CHANGELOG). Split by top-level heading
  // into sections, and load sections on demand.
  var BIG_DOC_BYTES = 200 * 1024;

  // splitSections breaks a body at level-2 headings so a huge document can be
  // rendered piecewise. Returns [{title, markdown}].
  function splitSections(body) {
    var lines = String(body || '').split('\n');
    var sections = [];
    var cur = { title: '', lines: [] };
    lines.forEach(function (line) {
      var m = line.match(/^##\s+(.*)$/);
      if (m) {
        if (cur.lines.join('\n').trim() || cur.title) sections.push(cur);
        cur = { title: m[1], lines: [line] };
      } else {
        cur.lines.push(line);
      }
    });
    if (cur.lines.join('\n').trim() || cur.title) sections.push(cur);
    return sections;
  }

  function docList(docs, activeID) {
    if (!docs.length) {
      return '<div class="empty-state"><p class="empty-state-title">Nothing here yet</p>' +
        '<p class="empty-state-description">No document of this kind has been added.</p></div>';
    }
    return '<ul class="doc-list">' + docs.map(function (d) {
      return '<li><a class="doc-item' + (d.id === activeID ? ' is-active' : '') + '" ' +
        'href="/projects/' + encodeURIComponent(d.__slug) + '/documents?kind=' +
        encodeURIComponent(d.kind) + '&amp;doc=' + d.id + '">' +
        '<span class="doc-item-title">' + esc(d.title || d.slug || ('document ' + d.id)) + '</span>' +
        '<span class="doc-item-meta">' + esc(fmtBytes((d.body || '').length)) +
        (d.revision > 1 ? ' · rev ' + d.revision : '') +
        (d.updated_at ? ' · ' + esc(fmtDate(d.updated_at)) : '') + '</span></a></li>';
    }).join('') + '</ul>';
  }

  function rail(kinds, active) {
    return '<nav class="doc-rail" aria-label="Document kinds">' +
      kinds.map(function (k) {
        return '<a class="doc-kind' + (k.kind === active.kind ? ' is-active' : '') + '" ' +
          'href="/projects/' + encodeURIComponent(active.slug) + '/documents?kind=' +
          encodeURIComponent(k.kind) + '">' +
          '<span class="doc-kind-label">' + esc(k.label) +
          (k.count ? ' <span class="doc-kind-count">' + k.count + '</span>' : '') + '</span>' +
          (k.blurb ? '<span class="doc-kind-blurb">' + esc(k.blurb) + '</span>' : '') +
          '</a>';
      }).join('') + '</nav>';
  }

  function reader(doc, projectSlug) {
    if (!doc) {
      return '<div class="empty-state"><div class="empty-state-icon">📄</div>' +
        '<p class="empty-state-title">No document selected</p>' +
        '<p class="empty-state-description">Choose one from the list. ' +
        'Everything a project publishes lives here.</p></div>';
    }
    var body = doc.body || '';
    var head = '<div class="doc-head">' +
      '<div><h1 class="page-title">' + esc(doc.title || doc.slug) + '</h1>' +
      '<p class="page-subtitle">' + esc(doc.kind) + ' · rev ' + esc(doc.revision) +
      (doc.updated_at ? ' · updated ' + esc(fmtDate(doc.updated_at)) : '') +
      ' · ' + esc(fmtBytes(body.length)) + '</p></div>' +
      '<div class="detail-actions">' +
      '<button class="btn" data-copy>Copy markdown</button>' +
      '<a class="btn btn-secondary" href="/projects/' + encodeURIComponent(projectSlug) +
      '">Back to project</a></div></div>';

    if (!body.trim()) {
      return head + '<div class="empty-state"><p class="empty-state-title">This document is empty</p>' +
        '<p class="empty-state-description">It exists, but has no content yet.</p></div>';
    }

    if (body.length < BIG_DOC_BYTES) {
      return head + '<article class="markdown-body" data-markdown>' +
        escapeTextForAttr(body) + '</article>';
    }

    // Large document: render section by section on demand, so the first paint
    // does not depend on the size of the file.
    var sections = splitSections(body);
    var toc = sections.map(function (s, n) {
      return '<li><a class="doc-toc-link" href="#" data-section="' + n + '">' +
        esc(s.title || 'Introduction') + '</a></li>';
    }).join('');
    var first = '<article class="markdown-body" data-markdown>' +
      escapeTextForAttr(sections[0].lines.join('\n')) + '</article>';
    var placeholder = sections.slice(1).map(function (s, n) {
      return '<div class="doc-section" data-section-body="' + (n + 1) + '">' +
        '<article class="markdown-body" data-markdown hidden>' +
        escapeTextForAttr(s.lines.join('\n')) + '</article></div>';
    }).join('');
    return head +
      '<div class="doc-large">' +
      (sections.length > 1
        ? '<nav class="doc-toc" aria-label="Sections"><h2 class="doc-toc-title">Contents</h2>' +
          '<ol>' + toc + '</ol></nav>'
        : '') +
      first + placeholder + '</div>';
  }

  // The renderer reads text from the DOM rather than from a JS string, so the
  // markdown never has to survive an HTML-escaping round trip through an
  // attribute. Using textContent semantics: the element holds escaped text and
  // renderMarkdown reads it back as text.
  function escapeTextForAttr(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function renderMarkdownNodes(root) {
    var els = root.querySelectorAll('[data-markdown]');
    Array.prototype.forEach.call(els, function (el) {
      var text = el.textContent || '';
      // markdown.js escapes authored HTML itself, so its output is the only
      // innerHTML assignment on this page.
      el.innerHTML = window.ConcordMarkdown.render(text);
      el.removeAttribute('data-markdown');
    });
  }

  function wire(box, slug, doc) {
    var copy = box.querySelector('[data-copy]');
    if (copy) {
      copy.addEventListener('click', function () {
        var btn = copy;
        var original = btn.textContent;
        var done = function (ok) {
          btn.textContent = ok ? 'Copied' : 'Copy failed';
          setTimeout(function () { btn.textContent = original; }, 1500);
        };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(doc.body || '').then(function () { done(true); },
                                                          function () { done(false); });
        } else {
          // execCommand is deprecated but remains the only synchronous path on a
          // non-secure origin, which is what 127.0.0.1 over plain http is.
          var ta = document.createElement('textarea');
          ta.value = doc.body || '';
          ta.style.position = 'fixed';
          ta.style.opacity = '0';
          document.body.appendChild(ta);
          ta.select();
          var ok = false;
          try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
          document.body.removeChild(ta);
          done(ok);
        }
      });
    }

    // Sections in a large document render on first view.
    Array.prototype.forEach.call(box.querySelectorAll('[data-section-body]'), function (wrap) {
      var inner = wrap.querySelector('[data-markdown]');
      if (inner && inner.hasAttribute('data-markdown')) return;
      var observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          if (!e.isIntersecting) return;
          var el = e.target.querySelector('[data-markdown]');
          if (el && el.hasAttribute('data-markdown')) {
            el.innerHTML = window.ConcordMarkdown.render(el.textContent || '');
            el.removeAttribute('data-markdown');
            el.removeAttribute('hidden');
          }
          observer.unobserve(e.target);
        });
      }, { rootMargin: '400px' });
      observer.observe(wrap);
    });

    Array.prototype.forEach.call(box.querySelectorAll('.doc-toc-link'), function (a) {
      a.addEventListener('click', function (ev) {
        ev.preventDefault();
        var n = a.getAttribute('data-section');
        var target = n === '0'
          ? box.querySelector('.doc-large > .markdown-body')
          : box.querySelector('[data-section-body="' + n + '"]');
        if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      });
    });
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-documents');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.innerHTML = '<p class="muted">No project.</p>'; return; }

    var kind = currentKind();
    var wanted = new URLSearchParams(window.location.search).get('doc');

    box.innerHTML = '<div class="loading-spinner" role="status" aria-label="Loading documents"></div>';

    // Documents are public: a read of the list returns 200 without a token.
    fetch('/api/v1/projects/' + encodeURIComponent(slug) + '/documents')
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) {
        var docs = (list || []).map(function (d) { return d; });
        // The list response carries project_id but not the slug, and the links
        // need the slug. Carry it explicitly rather than reading it back out of
        // the URL inside a template.
        docs.forEach(function (d) { d.__slug = slug; });

        var kinds = kindsWithCounts(docs);
        var known = kinds.some(function (k) { return k.kind === kind; });
        if (!known && kinds.length) kind = kinds[0].kind;

        var ofKind = docs.filter(function (d) { return d.kind === kind; });
        var doc = null;
        if (wanted) {
          doc = docs.filter(function (d) { return String(d.id) === String(wanted); })[0] || null;
        }
        if (!doc) {
          // Default to the README when there is one, because that is the
          // document a reader almost always wants first.
          var preferred = ofKind.filter(function (d) { return d.kind === 'readme'; })[0];
          doc = preferred || ofKind[0] || docs[0] || null;
        }

        box.innerHTML =
          '<div class="doc-layout">' +
          rail(kinds, { kind: kind, slug: slug }) +
          '<div class="doc-main">' +
          (ofKind.length ? '<div class="doc-list-col">' + docList(ofKind, doc && doc.id) + '</div>' : '') +
          '<div class="doc-reader">' + reader(doc, slug) + '</div>' +
          '</div></div>';

        renderMarkdownNodes(box);
        wire(box, slug, doc || { body: '' });
      })
      .catch(function () {
        box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">⚠️</div>' +
          '<p class="empty-state-title">Could not load documents</p>' +
          '<p class="empty-state-description">The API did not respond as expected. Try again.</p></div>';
      });
  });
})();