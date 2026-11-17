// Concord — project documents.
//
// Reachable at /projects/{slug}/documents. Before this page the whole document
// surface was API-only: 1,087 documents were stored, searchable with curl, and
// invisible on the site. This is the viewer.
//
// Markdown is rendered by window.ConcordMarkdown, which escapes raw HTML. Since
// step 2b the only HTML sink is inside that sanitizer (renderInto); every other
// node on this page is built by dom.js's text-only el().

(function () {
  'use strict';
  var el = window.ConcordDom.el;

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
    (docs || []).forEach(function (d) { counts[d.kind] = (counts[d.kind] || 0) + 1; });
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

  // A document this large cannot be rendered in one go: 1.7 MB of markdown
  // builds a very large DOM and the tab locks for seconds. Split by top-level
  // heading into sections, and load sections on demand.
  var BIG_DOC_BYTES = 200 * 1024;

  // splitSections breaks a body into sections at its top-level headings.
  function splitSections(body) {
    var lines = String(body || '').split('\n');
    var h1 = 0, h2 = 0, firstIsH1 = false, seen = false;
    lines.forEach(function (line) {
      var a = line.match(/^#\s+(.*)$/);
      var b = line.match(/^##\s+(.*)$/);
      if (a) { h1++; if (!seen) { firstIsH1 = true; seen = true; } }
      else if (b) { h2++; seen = true; }
    });
    var level = (h1 > h2 || (h1 === h2 && h1 > 0 && firstIsH1)) ? 1 : 2;
    var re = level === 1 ? /^#\s+(.*)$/ : /^##\s+(.*)$/;
    var sections = [];
    var cur = { title: '', level: level, lines: [] };
    lines.forEach(function (line) {
      var m = line.match(re);
      if (m) {
        if (cur.lines.join('\n').trim() || cur.title) sections.push(cur);
        cur = { title: m[1], level: level, lines: [line] };
      } else {
        cur.lines.push(line);
      }
    });
    if (cur.lines.join('\n').trim() || cur.title) sections.push(cur);
    return sections;
  }

  function emptyState(icon, title, description) {
    return el('div', { class: 'empty-state' }, [
      icon ? el('div', { class: 'empty-state-icon' }, icon) : null,
      el('p', { class: 'empty-state-title' }, title),
      el('p', { class: 'empty-state-description' }, description)
    ]);
  }

  function docList(docs, activeID) {
    if (!docs.length) {
      return emptyState(null, 'Nothing here yet', 'No document of this kind has been added.');
    }
    return el('ul', { class: 'doc-list' }, docs.map(function (d) {
      return el('li', {}, el('a', {
        class: 'doc-item' + (d.id === activeID ? ' is-active' : ''),
        href: '/projects/' + encodeURIComponent(d.__slug) + '/documents?kind=' +
          encodeURIComponent(d.kind) + '&doc=' + d.id
      }, [
        el('span', { class: 'doc-item-title' }, d.title || d.slug || ('document ' + d.id)),
        el('span', { class: 'doc-item-meta' }, [
          fmtBytes((d.body || '').length),
          d.revision > 1 ? ' · rev ' + d.revision : null,
          d.updated_at ? ' · ' + fmtDate(d.updated_at) : null
        ])
      ]));
    }));
  }

  function rail(kinds, active) {
    return el('nav', { class: 'doc-rail', 'aria-label': 'Document kinds' }, kinds.map(function (k) {
      var label = [k.label];
      if (k.count) label.push(el('span', { class: 'doc-kind-count' }, k.count));
      return el('a', {
        class: 'doc-kind' + (k.kind === active.kind ? ' is-active' : ''),
        href: '/projects/' + encodeURIComponent(active.slug) + '/documents?kind=' + encodeURIComponent(k.kind)
      }, [
        el('span', { class: 'doc-kind-label' }, label),
        k.blurb ? el('span', { class: 'doc-kind-blurb' }, k.blurb) : null
      ]);
    }));
  }

  function reader(doc, projectSlug) {
    if (!doc) {
      return emptyState('📄', 'No document selected',
        'Choose one from the list. Everything a project publishes lives here.');
    }
    var body = doc.body || '';
    var head = el('div', { class: 'doc-head' }, [
      el('div', {}, [
        el('h1', { class: 'page-title' }, doc.title || doc.slug),
        el('p', { class: 'page-subtitle' }, [
          doc.kind + ' · rev ' + doc.revision,
          doc.updated_at ? ' · updated ' + fmtDate(doc.updated_at) : null,
          ' · ' + fmtBytes(body.length)
        ])
      ]),
      el('div', { class: 'detail-actions' }, [
        el('button', { class: 'btn', 'data-copy': '' }, 'Copy markdown'),
        el('a', { class: 'btn btn-secondary', href: '/projects/' + encodeURIComponent(projectSlug) },
          'Back to project')
      ])
    ]);
    var parts = [head];
    if (!body.trim()) {
      parts.push(emptyState(null, 'This document is empty', 'It exists, but has no content yet.'));
      return parts;
    }

    // A TOC depends on heading count, lazy rendering on byte count. A mid-sized
    // document gets a TOC and renders whole; only a large one gets splitting.
    if (body.length < BIG_DOC_BYTES) {
      parts.push(el('div', { class: 'doc-large', 'data-split-level': '2' }, [
        el('nav', { class: 'doc-toc', 'aria-label': 'Sections', hidden: '' }),
        el('div', { class: 'doc-sections' },
          el('article', { class: 'markdown-body', 'data-markdown': '' }, body))
      ]));
      return parts;
    }

    var sections = splitSections(body);
    var splitLevel = sections.length ? sections[0].level : 2;
    var toc = sections.length > 1 ? el('nav', { class: 'doc-toc', 'aria-label': 'Sections' }, [
      el('h2', { class: 'doc-toc-title' }, 'Contents'),
      el('ol', {}, sections.map(function (s, n) {
        return el('li', {}, el('a', {
          class: 'doc-toc-link', href: '#', 'data-section': n
        }, s.title || 'Introduction'));
      }))
    ]) : null;

    // Placeholders reserve height without being hidden: a hidden element has
    // zero height, never intersects the viewport, and the observer never fires.
    var placeholders = sections.slice(1).map(function (s, n) {
      var px = Math.max(200, Math.min(4000, Math.round(s.lines.join('\n').length * 0.30)));
      return el('div', {
        class: 'doc-section is-pending', 'data-section-body': n + 1, style: 'min-height:' + px + 'px'
      }, el('article', { class: 'markdown-body', 'data-markdown': '' }, s.lines.join('\n')));
    });
    var first = el('article', { class: 'markdown-body', 'data-markdown': '' }, sections[0].lines.join('\n'));
    parts.push(el('div', { class: 'doc-large', 'data-split-level': splitLevel }, [
      toc, el('div', { class: 'doc-sections' }, [first].concat(placeholders))
    ]));
    return parts;
  }

  // renderMarkdownNodes renders the markdown that is DUE to be rendered.
  function renderMarkdownNodes(root) {
    var nodes = root.querySelectorAll('[data-markdown]');
    Array.prototype.forEach.call(nodes, function (node) {
      if (node.closest('.doc-section.is-pending')) return;
      var holder = node.closest('[data-section-body]');
      var idStart = holder ? Number(holder.getAttribute('data-section-body')) : 0;
      var large = root.querySelector('.doc-large');
      window.ConcordMarkdown.renderInto(node, node.textContent || '', {
        idStart: idStart,
        idLevel: Number(large && large.getAttribute('data-split-level')) || 2
      });
    });
    // After rendering: the TOC is built from ids that only exist once rendered.
    buildTOCFromDOM(root);
  }

  function buildTOCFromDOM(root) {
    var nav = root.querySelector('nav.doc-toc[hidden]');
    if (!nav) return;
    var level = Number(root.querySelector('.doc-large') &&
      root.querySelector('.doc-large').getAttribute('data-split-level')) || 2;
    var headings = Array.prototype.slice.call(
      root.querySelectorAll('.markdown-body h' + level + '[id^="doc-s-"]'));
    if (headings.length < 2) {
      nav.parentNode && nav.parentNode.removeChild(nav);
      return;
    }
    nav.replaceChildren(
      el('h2', { class: 'doc-toc-title' }, 'Contents'),
      el('ol', {}, headings.map(function (h) {
        return el('li', {}, el('a', { class: 'doc-toc-link', href: '#' + h.id },
          h.textContent || 'Introduction'));
      }))
    );
    nav.removeAttribute('hidden');
  }

  function renderPendingSection(wrap, box) {
    var node = wrap.querySelector('[data-markdown]');
    if (!node || !node.hasAttribute('data-markdown')) return false;
    var large = box.querySelector('.doc-large');
    window.ConcordMarkdown.renderInto(node, node.textContent || '', {
      idStart: Number(wrap.getAttribute('data-section-body')) || 0,
      idLevel: Number(large && large.getAttribute('data-split-level')) || 2
    });
    wrap.classList.remove('is-pending');
    wrap.removeAttribute('style');
    return true;
  }

  // A local skeleton built with el() so the loading state needs no HTML string.
  function loadingRows(n) {
    return el('div', {}, Array.apply(null, Array(n)).map(function () {
      return el('div', { class: 'skeleton-card skeleton-row' }, [
        el('span', { class: 'skeleton', style: 'width:2rem;height:2rem;border-radius:6px' }),
        el('div', { class: 'skeleton-col' }, [
          el('div', { class: 'skeleton skeleton-title' }),
          el('div', { class: 'skeleton skeleton-text' }),
          el('div', { class: 'skeleton skeleton-text' })
        ])
      ]);
    }));
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

    Array.prototype.forEach.call(box.querySelectorAll('[data-section-body]'), function (wrap) {
      if (!wrap.classList.contains('is-pending')) return;
      var observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          if (!e.isIntersecting) return;
          renderPendingSection(e.target, box);
          observer.unobserve(e.target);
        });
      }, { rootMargin: '400px' });
      observer.observe(wrap);
    });

    Array.prototype.forEach.call(box.querySelectorAll('.doc-toc-link'), function (a) {
      a.addEventListener('click', function (ev) {
        ev.preventDefault();
        var n = a.getAttribute('data-section');
        var target;
        if (n === null) {
          var id = (a.getAttribute('href') || '').replace(/^#/, '');
          target = id ? box.querySelector('#' + CSS.escape(id)) : null;
        } else {
          var idx = Number(n);
          if (idx === 0) {
            target = box.querySelector('.doc-sections > .markdown-body');
          } else {
            box.querySelectorAll('[data-section-body]').forEach(function (w) {
              if (Number(w.getAttribute('data-section-body')) <= idx) renderPendingSection(w, box);
            });
            target = box.querySelector('[data-section-body="' + idx + '"]');
          }
        }
        if (!target) return;
        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      });
    });
  }

  // GET /projects/{slug}/documents/search?q= is a real FTS5 index. The response
  // is a bare ARRAY; each hit links to its own kind's page.
  function wireSearch(box, slug, state) {
    var form = document.getElementById('doc-search');
    var input = document.getElementById('doc-search-input');
    if (!form || !input) return;
    var status = form.querySelector('[data-search-status]');
    var seq = 0;
    var timer = null;

    function say(msg, bad) {
      if (!status) return;
      status.textContent = msg || '';
      status.className = bad ? 'error-state' : 'form-note';
    }
    function column() { return box.querySelector('.doc-list-col'); }
    function restore() {
      var col = column();
      if (col) col.replaceChildren(docList(state.ofKind, state.doc && state.doc.id));
      say('');
    }
    function run(q) {
      var mine = ++seq;
      say('Searching…');
      window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/documents/search?q=' +
            encodeURIComponent(q) + '&limit=20')
        .then(function (hits) {
          if (mine !== seq) return;
          hits = hits || [];
          hits.forEach(function (d) { d.__slug = slug; });
          var col = column();
          if (col) col.replaceChildren(docList(hits, state.doc && state.doc.id));
          say(hits.length
            ? hits.length + (hits.length === 1 ? ' result' : ' results') + ' for “' + q + '”'
            : 'No document in this project matches “' + q + '”.');
        })
        .catch(function (error) {
          if (mine !== seq) return;
          if (error && error.status === 400) {
            say('The search could not run: ' + error.message, true);
          } else if (error && error.status) {
            say('The search failed (' + error.status + '). The list below is unchanged.', true);
          } else {
            say('The search could not reach the server.', true);
          }
        });
    }
    function go(q) {
      try {
        var url = new URL(window.location.href);
        if (q) { url.searchParams.set('q', q); } else { url.searchParams.delete('q'); }
        window.history.replaceState(null, '', url.pathname + url.search);
      } catch (e) { /* no URL/history is not worth failing over */ }
      if (!q) { restore(); return; }
      run(q);
    }
    input.addEventListener('input', function () {
      var q = input.value.trim();
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { go(q); }, 300);
    });
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      if (timer) clearTimeout(timer);
      go(input.value.trim());
    });
    var initial = new URLSearchParams(window.location.search).get('q');
    if (initial) { input.value = initial; run(initial); }
  }

  document.addEventListener('DOMContentLoaded', function () {
    var box = document.getElementById('project-documents');
    if (!box) return;
    var slug = slugFromPath();
    if (!slug) { box.replaceChildren(el('p', { class: 'muted' }, 'No project.')); return; }

    var kind = currentKind();
    var wanted = new URLSearchParams(window.location.search).get('doc');
    box.setAttribute('aria-busy', 'true');
    box.replaceChildren(window.ConcordSkeleton ? loadingRows(5)
      : el('div', { class: 'loading-spinner', role: 'status', 'aria-label': 'Loading documents' }));

    // Documents are public: a read of the list returns 200 without a token.
    window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/documents?fields=meta')
      .catch(function () { return []; })
      .then(function (list) {
        var docs = (list || []).map(function (d) { return d; });
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
          var canonical = ofKind.filter(function (d) { return d.slug === kind; })[0];
          doc = canonical || ofKind[0] || docs[0] || null;
        }
        var bodyReady = doc && !doc.body
          ? window.ConcordApi.get('/api/v1/projects/' + encodeURIComponent(slug) + '/documents/' +
              encodeURIComponent(String(doc.id)))
              .catch(function () { return null; })
              .then(function (full) {
                if (full && typeof full.body === 'string') doc.body = full.body;
                return doc;
              })
          : Promise.resolve(doc);

        return bodyReady.then(function () {
          box.replaceChildren(el('div', { class: 'doc-layout' }, [
            rail(kinds, { kind: kind, slug: slug }),
            el('div', { class: 'doc-main' }, [
              el('div', { class: 'doc-list-col' }, docList(ofKind, doc && doc.id)),
              el('div', { class: 'doc-reader' }, reader(doc, slug))
            ])
          ]));
          renderMarkdownNodes(box);
          if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
          box.setAttribute('aria-busy', 'false');
          wire(box, slug, doc || { body: '' });
          wireSearch(box, slug, { docs: docs, ofKind: ofKind, kind: kind, doc: doc });
        });
      })
      .catch(function () {
        box.replaceChildren(emptyState('⚠️', 'Could not load documents',
          'The API did not respond as expected. Try again.'));
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
      });
  });
})();
