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

  // splitSections breaks a body into sections at its top-level headings.
  // Returns [{title, markdown, level}].
  //
  // It splits at H1 *or* H2. Matching H2 alone looked harmless and was not: a
  // document whose sections are H1 came back as ONE section, so a 229 KB body
  // was assigned to innerHTML in a single go -- the exact tab lock this branch
  // exists to prevent. It was worse than that, because the single section was
  // then the only child of the .doc-large grid, landing it in the grid's 200px
  // first column: the whole document rendered 200px wide.
  //
  // H1 is the deeper split when both appear in one document, because a document
  // that opens with an H1 title and then uses H2s for its sections should not
  // become one section per part of a sentence. In practice the two do not mix:
  // the corpus has documents that are all-H1 (a plan with 8, a handoff readme
  // with 4) and documents that are all-H2.
  function splitSections(body) {
    var lines = String(body || '').split('\n');

    // Pick the level to split on: whichever of H1/H2 appears most, H1 winning a
    // tie only if it is also the first heading. Falling back to H2 keeps the
    // previous behaviour for a document with neither.
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

    // Two separate questions, previously conflated behind one size threshold.
    //
    // 1. Does this document need a table of contents? That depends on how many
    //    headings it has, not how many bytes it has. The old single threshold
    //    (200 KB) meant the TOC was only ever built for a document no larger
    //    than the biggest in the corpus -- which is 55 KB -- so it was dead code.
    //    The 36 KB frontend spec has 22 headings and rendered with no nav at all.
    //
    // 2. Does this document need lazy section rendering? That is purely a size
    //    question: 1.7 MB of markdown in one innerHTML locks the tab. Below the
    //    threshold the whole body is assigned at once, which is correct and fast
    //    for a 55 KB document.
    //
    // So a mid-sized document now gets a TOC and renders whole; only a large one
    // gets the section-splitting treatment.
    if (body.length < BIG_DOC_BYTES) {
      // Nothing is deferred below the threshold, so the body is rendered as one
      // article and the TOC is built afterwards from the ids the renderer
      // actually emitted (see buildTOCFromDOM).
      //
      // It used to be built from splitSections' count instead, which is a
      // parallel guess at the same numbering. The two disagreed: splitSections
      // treats the run of text before the first H2 as its own section, so it
      // counts one more section than the document has H2s. That produced a TOC
      // with links to #doc-s-8..13 that point at nothing, and duplicate ids for
      // every heading after the first block. Reading the ids back is the only
      // construction that cannot drift from the markup it links to.
      // data-split-level is the level this document's H2s sit at. Declared
      // rather than left to default so buildTOCFromDOM and the renderer read the
      // same number instead of each applying a fallback.
      return head +
        '<div class="doc-large" data-split-level="2">' +
        '<nav class="doc-toc" aria-label="Sections" hidden></nav>' +
        // Same wrapper as the large-document path, so the article is the second
        // grid cell in both cases rather than the first.
        '<div class="doc-sections"><article class="markdown-body" data-markdown>' +
        escapeTextForAttr(body) + '</article></div></div>';
    }

    // Large document: render section by section on demand, so the first paint
    // does not depend on the size of the file.
    var sections = splitSections(body);
    // Declared before the markup that states it and before the observer closure
    // reads it, so neither has to recompute the level independently.
    var splitLevel = sections.length ? sections[0].level : 2;
    var toc = sections.map(function (s, n) {
      return '<li><a class="doc-toc-link" href="#" data-section="' + n + '">' +
        esc(s.title || 'Introduction') + '</a></li>';
    }).join('');
    var first = '<article class="markdown-body" data-markdown>' +
      escapeTextForAttr(sections[0].lines.join('\n')) + '</article>';
    // The wrappers get a placeholder height so they OCCUPIY SPACE before they
    // are rendered.
    //
    // The first version marked each section `hidden`. That deadlocks: a hidden
    // element has zero height, so it never intersects the viewport, so the
    // observer never fires, so it stays hidden. The document renders its first
    // section and then nothing else, and the page is only 5,886px tall for a
    // 337 KB document -- there is no scroll left to trigger anything.
    //
    // The fix is to reserve height with CSS rather than remove it with an
    // attribute. The placeholder height is a guess; when the section renders,
    // the real content replaces it and the layout shifts once, below the fold.
    // The reserved height is proportional to the section's source length, so the
    // scrollbar approximates the finished document rather than collapsing to the
    // first section. A flat constant is worse than useless here: it puts every
    // pending section at the same height regardless of size, and the last one can
    // sit past the end of a scrollbar that does not yet extend far enough to
    // reach it.
    var placeholder = sections.slice(1).map(function (s, n) {
      var px = Math.max(200, Math.min(4000, Math.round(s.lines.join('\n').length * 0.30)));
      return '<div class="doc-section is-pending" data-section-body="' + (n + 1) +
        '" style="min-height:' + px + 'px">' +
        '<article class="markdown-body" data-markdown>' +
        escapeTextForAttr(s.lines.join('\n')) + '</article></div>';
    }).join('');
    // data-split-level is read back by the renderer so it puts ids on the same
    // heading level this function split on. Stating it in the markup rather than
    // recomputing it is what keeps the two from drifting.
    return head +
      '<div class="doc-large" data-split-level="' + splitLevel + '">' +
      (sections.length > 1
        ? '<nav class="doc-toc" aria-label="Sections"><h2 class="doc-toc-title">Contents</h2>' +
          '<ol>' + toc + '</ol></nav>'
        : '') +
      // The sections are wrapped in their own element so they form ONE grid
      // cell and stack down the page.
      //
      // Without the wrapper they are siblings of the nav inside the 2-column
      // grid, so the browser placed them into successive grid cells: measured on
      // a 229 KB document, section 1 was 33314px tall in the 200px nav column
      // and section 2 sat beside it in the 408px article column at the same
      // scroll offset. Every placeholder shared a top with its neighbour, so
      // clicking a contents entry scrolled to a position 2115px above the
      // section it named, and half the sections were invisible.
      //
      // This was never visible before because splitSections matched H2 only and
      // no document in the corpus exceeded the 200 KB lazy threshold, so the
      // branch had never rendered.
      '<div class="doc-sections">' + first + placeholder + '</div>' +
      '</div>';
  }

  // The renderer reads text from the DOM rather than from a JS string, so the
  // markdown never has to survive an HTML-escaping round trip through an
  // attribute. Using textContent semantics: the element holds escaped text and
  // renderMarkdown reads it back as text.
  function escapeTextForAttr(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // renderMarkdownNodes renders the markdown that is DUE to be rendered.
  //
  // It deliberately skips anything inside a .doc-section.is-pending wrapper --
  // those are the large-document path's job, handled by the observer in wire().
  //
  // The first version selected every [data-markdown] node, which rendered all
  // 40 sections of a 337 KB document eagerly and defeated the lazy path
  // entirely: the placeholder heights existed, the observer existed, and neither
  // was needed because everything was already rendered. It looked like it worked
  // because the content appeared -- and the cost it was avoiding was paid on
  // every load of every large document.
  function renderMarkdownNodes(root) {
    var els = root.querySelectorAll('[data-markdown]');
    Array.prototype.forEach.call(els, function (el) {
      if (el.closest('.doc-section.is-pending')) return;
      var text = el.textContent || '';
      // markdown.js escapes authored HTML itself, so its output is the only
      // innerHTML assignment on this page.
      //
      // idStart: on the large-document path each section is its own [data-markdown]
      // node, rendered by its own render() call. Heading ids are numbered from
      // zero per render, so without the section index every section emits
      // doc-s-0 and the contents list can only ever reach the first one.
      var holder = el.closest('[data-section-body]');
      var idStart = holder ? Number(holder.getAttribute('data-section-body')) : 0;
      // Read from the .doc-large wrapper, not from root: root is the page
      // container, which has no data-split-level, so reading it there silently
      // fell back to 2 and put no ids on an all-H1 document.
      var large = root.querySelector('.doc-large');
      el.innerHTML = window.ConcordMarkdown.render(text, {
        idStart: idStart,
        idLevel: Number(large && large.getAttribute('data-split-level')) || 2
      });
      el.removeAttribute('data-markdown');
    });
    // After rendering, not before: the TOC is built from ids that only exist
    // once the markdown has been rendered.
    buildTOCFromDOM(root);
  }

  // buildTOCFromDOM fills a TOC nav from the H2 ids the renderer emitted.
  //
  // It reads the rendered headings rather than recomputing their numbering, so a
  // link cannot exist without its target. A document with fewer than two
  // headings gets no nav at all -- an empty "Contents" panel is worse than none.
  function buildTOCFromDOM(root) {
    var nav = root.querySelector('nav.doc-toc[hidden]');
    if (!nav) return;
    // Which level carries ids depends on what the document was split on, so it
    // is read from the markup rather than assumed to be h2.
    var level = Number(root.querySelector('.doc-large') &&
      root.querySelector('.doc-large').getAttribute('data-split-level')) || 2;
    var headings = Array.prototype.slice.call(
      root.querySelectorAll('.markdown-body h' + level + '[id^="doc-s-"]'));
    if (headings.length < 2) {
      nav.parentNode && nav.parentNode.removeChild(nav);
      return;
    }
    nav.innerHTML = '<h2 class="doc-toc-title">Contents</h2><ol>' +
      headings.map(function (h) {
        return '<li><a class="doc-toc-link" href="#' + h.id + '">' +
          esc(h.textContent || 'Introduction') + '</a></li>';
      }).join('') + '</ol>';
    nav.removeAttribute('hidden');
  }

  // renderPendingSection fills a placeholder now, rather than waiting for the
  // IntersectionObserver to reach it. Scrolling a still-empty placeholder shows
  // the reader a blank band and reads as a broken link.
  function renderPendingSection(wrap, box) {
    var el = wrap.querySelector('[data-markdown]');
    if (!el || !el.hasAttribute('data-markdown')) return false;
    var large = box.querySelector('.doc-large');
    el.innerHTML = window.ConcordMarkdown.render(el.textContent || '', {
      idStart: Number(wrap.getAttribute('data-section-body')) || 0,
      idLevel: Number(large && large.getAttribute('data-split-level')) || 2
    });
    el.removeAttribute('data-markdown');
    wrap.classList.remove('is-pending');
    wrap.removeAttribute('style');   // drop the reserved height; content sets it
    return true;
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
      // Already rendered (or never needed rendering): nothing to observe.
      if (!wrap.classList.contains('is-pending')) return;
      var observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          if (!e.isIntersecting) return;
          // The same helper the contents list uses, so a section rendered by
          // scrolling and one rendered by clicking are identical. Two copies of
          // this is how they drift: the click path had its own and lost the
          // idStart offset.
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
          // A whole-body document. Its headings carry real ids (doc-s-N), so the
          // link names its target directly. Scrolling the wrapper rather than
          // the heading would land at the section's top edge, which for the
          // first heading is the document header itself.
          var id = (a.getAttribute('href') || '').replace(/^#/, '');
          target = id ? box.querySelector('#' + CSS.escape(id)) : null;
        } else {
          // data-section is the index into the ORIGINAL section list, so entry
          // n names section n. Section 0 is the eagerly-rendered first article;
          // the rest are the placeholders, which are numbered 1..n-1 in the same
          // order. So entry n is placeholder n -- not n+1.
          //
          // The n+1 form was an off-by-one that put every entry one section too
          // far: measured on a 229 KB document, clicking the entry labelled
          // "Heading 2" scrolled to "Heading 3", and the final entry landed on
          // the document header because it asked for a placeholder that does not
          // exist. Both are wrong in opposite directions, which is why neither
          // showed up as an obvious "off by one" -- one was invisible and one
          // looked like a scroll glitch.
          //
          // The section may still be an unrendered placeholder, so it is filled
          // on demand: scrolling to an empty band reads as a broken link.
          var idx = Number(n);
          if (idx === 0) {
            target = box.querySelector('.doc-sections > .markdown-body');
          } else {
            // Render every section from the top of the document down to the
            // target, not just the target.
            //
            // A placeholder is shorter than the section it stands for -- it
            // reserves min-height, while the rendered content is ~14200px against
            // a 4000px reservation. So a section's position on the page is not
            // final until everything above it has rendered, and the target's
            // position moves every time one of them does.
            //
            // Scrolling to an unrendered target therefore lands wherever the
            // target was, and the observer then grows the sections above it and
            // pushes the target down: measured 1743px short after rendering the
            // two sections above it. Re-scrolling in response to those renders
            // was tried and does not work, because the observers fire whenever
            // they fire and a loop that waits for them is guessing at a deadline.
            // Rendering the path to the target makes its position final, and one
            // scroll is then correct.
            //
            // Sections below the target are left pending, so jumping into a long
            // document still does not render all of it.
            box.querySelectorAll('[data-section-body]').forEach(function (w) {
              if (Number(w.getAttribute('data-section-body')) <= idx) {
                renderPendingSection(w, box);
              }
            });
            target = box.querySelector('[data-section-body="' + idx + '"]');
          }
        }
        if (!target) return;

        // One scroll, and it is correct.
        //
        // The loop that used to be here re-checked the position after each
        // render and re-scrolled until it stopped moving. It never settled
        // reliably, because the renders it was waiting for come from
        // IntersectionObservers that fire whenever they fire, and every bound on
        // the loop was a guess: a frame budget expired first and left the reader
        // 871px-4357px short -- always an exact multiple of one section's growth.
        //
        // Rendering the path to the target above removed the race instead of
        // winning it. Nothing above the target is still a placeholder, so the
        // target's position is final by the time this runs, and the sections
        // below it stay pending so the document is still lazy.
        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
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

    box.setAttribute('aria-busy', 'true');
    box.innerHTML = (window.ConcordSkeleton ? window.ConcordSkeleton.rows(5)
      : '<div class="loading-spinner" role="status" aria-label="Loading documents"></div>');

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
          // Prefer the document whose slug is the kind itself -- 'readme'/'readme',
          // 'spec'/'spec'. Within a kind, that is the canonical document: for a
          // readme kind it is the actual README, not the handoff note or the
          // premise, which are also readmes.
          //
          // The first version of this filtered on d.kind === 'readme', which is a
          // no-op: ofKind is ALREADY filtered to the active kind. It therefore
          // took whichever document the API happened to list first, so opening a
          // project landed on the handoff note instead of its README. API order is
          // by id, which is registration order, which is whatever the import
          // happened to do.
          var canonical = ofKind.filter(function (d) { return d.slug === kind; })[0];
          doc = canonical || ofKind[0] || docs[0] || null;
        }

        box.innerHTML =
          '<div class="doc-layout">' +
          rail(kinds, { kind: kind, slug: slug }) +
          '<div class="doc-main">' +
          (ofKind.length ? '<div class="doc-list-col">' + docList(ofKind, doc && doc.id) + '</div>' : '') +
          '<div class="doc-reader">' + reader(doc, slug) + '</div>' +
          '</div></div>';

        renderMarkdownNodes(box);
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
        wire(box, slug, doc || { body: '' });
      })
      .catch(function () {
        box.innerHTML = '<div class="empty-state"><div class="empty-state-icon">\u26A0\uFE0F</div>' +
          '<p class="empty-state-title">Could not load documents</p>' +
          '<p class="empty-state-description">The API did not respond as expected. Try again.</p></div>';
        // The error path is where aria-busy is most often forgotten, and it is
        // the worst place to forget it: aria-busy suppresses live-region
        // updates inside the region, so an error left busy is an error a screen
        // reader never announces. Caught by hand in a browser with the API
        // blocked -- the Go tests assert done() exists in the file, not that
        // every catch calls it.
        if (window.ConcordSkeleton) window.ConcordSkeleton.done(box);
      });
  });
})();