// Concord — loading placeholders.
//
// One documented pattern instead of a centred spinner on every page. Measured
// 2026-10-02: eleven pages each rendered .loading-spinner, a blocking 24px dot.
// A skeleton matches the shape of what is arriving, so the layout does not jump
// when the data lands, and it is honest about size -- a page with 12 documents
// should show roughly 12 rows of placeholder.
//
// Static by design. A shimmer on forty placeholders is more distracting than a
// flat block, and these pages are small enough that the skeleton is on screen
// for well under a second. See the .skeleton rules in style.css.
//
// Accessibility: every helper here returns markup, and the caller owns the
// live-region announcement. These placeholders must NOT carry role="status" --
// announcing "loading" is the caller's job via the status line it already has,
// and duplicating it makes a screen reader say it twice.
(function (global) {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function lines(n, cls) {
    var out = '';
    for (var i = 0; i < n; i++) {
      out += '<span class="skeleton ' + esc(cls || 'skeleton-text') + '"></span>';
    }
    return '<div class="skeleton-lines">' + out + '</div>';
  }

  // text: a paragraph block. n lines (default 3).
  function text(n) {
    return lines(n || 3, 'skeleton-text');
  }

  // title: one heading-height bar.
  function title() {
    return '<div class="skeleton skeleton-title"></div>';
  }

  // rows: n card-shaped rows, each a leading square plus lines of text. This is
  // the default for list pages -- complaints, features, documents.
  function rows(n) {
    var out = '';
    for (var i = 0; i < n; i++) {
      out += '<div class="skeleton-card skeleton-row">' +
        '<span class="skeleton" style="width:2rem;height:2rem;border-radius:6px"></span>' +
        '<div class="skeleton-col">' + title() + lines(2, 'skeleton-text') + '</div>' +
        '</div>';
    }
    return out;
  }

  // article: a reading pane -- heading, then a paragraph block.
  function article(lineCount) {
    return '<div class="skeleton-card">' + title() + text(lineCount || 6) + '</div>';
  }

  // board: n columns, each with a few card placeholders. Matches the kanban
  // column shape so the page does not resize when the cards arrive.
  function board(columns, perColumn) {
    var out = '<div class="grid-responsive-2">';
    for (var c = 0; c < (columns || 3); c++) {
      out += '<div class="card">' +
        '<div class="skeleton skeleton-title" style="width:40%"></div>';
      for (var i = 0; i < (perColumn || 2); i++) {
        out += lines(2, 'skeleton-text');
      }
      out += '</div>';
    }
    return out + '</div>';
  }

  // sidebar: the project page's right-hand column.
  function sidebar(blocks) {
    var out = '';
    for (var i = 0; i < (blocks || 3); i++) {
      out += '<div class="card">' + title() + lines(2, 'skeleton-text') + '</div>';
    }
    return out;
  }

  // done clears aria-busy once real content is in the region.
  //
  // Setting aria-busy="true" without ever clearing it leaves the region
  // permanently announced as "busy" to assistive technology, and aria-busy also
  // suppresses live-region updates inside it -- so a page that sets it and
  // forgets would silently stop announcing its own results. Every skeleton entry
  // point therefore pairs with a done() call at the render site, including the
  // error and empty paths.
  function done(el) {
    if (el && el.setAttribute) el.setAttribute('aria-busy', 'false');
    return el;
  }

  global.ConcordSkeleton = {
    text: text, title: title, rows: rows,
    article: article, board: board, sidebar: sidebar,
    done: done
  };
})(window);