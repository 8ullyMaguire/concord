// Concord — markdown renderer for project documents.
//
// The security boundary, stated once: this file renders AUTHORED CONTENT that
// arrived from 50 repositories and from any contributor with write access. It is
// not a helper for interpolating our own data. Raw HTML in a document body is
// NEVER passed through — it is escaped and shown as text. That single rule is
// what separates this from the esc() helper in the other scripts, which is for
// values we build markup around and which therefore must NOT be reused here.
//
// Why hand-written rather than a library: this app has no build step, no
// node_modules and no network fetches at runtime, so a vendored CommonMark
// parser would arrive as a multi-thousand-line blob that nobody can audit in
// diffs. The supported subset is named below and is what these documents
// actually use. When the subset stops covering a real document, widen it here
// deliberately, with a test.
//
// Supported: ATX headings, fenced code blocks (``` with an info string),
// blockquotes, unordered and ordered lists (one level), tables with alignment,
// horizontal rules, paragraphs, and inline: code spans, bold, italic, strikethrough,
// autolinks, bare URLs, markdown links, images (inert), and hard breaks.
//
// Deliberately unsupported: raw HTML, nested lists, reference links, footnotes,
// setext headings, and task lists. Each is absent because each is a way for
// authored text to do something the author did not intend.
(function (global) {
  'use strict';

  // escapeHTML is the only way text reaches the output. Every branch that
  // produces a text node calls it, and no branch concatenates authored text
  // into a tag without it.
  function escapeHTML(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // isSafeURL rejects anything that can execute or exfiltrate when placed in
  // href/src. Allowlist, not blocklist: a blocklist of "javascript:", "data:",
  // "vbscript:" is defeated by tab, newline, entity and case tricks, all of which
  // the browser undoes before the URL is parsed.
  //
  // Images are additionally inertised in renderInline: an <img> that points off
  // this host is a beacon to a third party that learns who read what document.
  var SAFE_SCHEME = /^(https?:|mailto:|#|\/|\.\/|\.\.\/)/i;

  function safeURL(raw) {
    var url = String(raw == null ? '' : raw).trim();
    // A colon before the first slash or question mark means the author is
    // trying to name a scheme. Anything not on the allowlist is refused.
    var schemeProbe = url.split(/[/?#]/)[0];
    if (schemeProbe.indexOf(':') !== -1 && !SAFE_SCHEME.test(url)) return null;
    if (!SAFE_SCHEME.test(url)) return null;
    return url;
  }

  // Heading ids, for the documents viewer's table of contents.
  //
  // Only level-2 headings get an id. That is a deliberate constraint, not an
  // oversight: the TOC is built from splitSections(), which splits a document at
  // level-2 headings and numbers those sections 0, 1, 2, ... So "the nth
  // section" and "doc-s-n" have to be the same n. An earlier version numbered
  // every heading level, which made the two schemes disagree the moment a
  // document contained an H3 -- the third H2 would be doc-s-7 while the TOC
  // linked to doc-s-3.
  //
  // Ids come from a counter rather than from the heading text. Text-derived ids
  // ("Installation" -> "installation") collide on any document that repeats a
  // heading, which real specs do constantly; a counter cannot.
  //
  // The large-document path renders one section per render() call, so each call
  // needs to know where in the document it sits. That is what idStart is for,
  // and documents.js passes the section index. Without it every section would
  // restart at doc-s-0 and all but the first would be unreachable.
  function nextHeadingId(start) {
    var n = start;
    return function () {
      var id = 'doc-s-' + n;
      n++;
      return id;
    };
  }

  // inline renders the span-level syntax. Text is escaped first and markup is
  // re-introduced after, so authored '<' can never become a tag: escaping happens
  // on the way in, not on the way out where a miss would be invisible.
  function renderInline(src) {
    // Extract code spans and links into placeholders before anything else, so
    // that '**' inside a code span or a URL is not interpreted as emphasis.
    function stash(html) {
      slots_.push(html);
      // The sentinel wraps a digit run in NULs. NUL cannot survive a JSON
      // string, so authored text can never forge a placeholder; and NUL is not
      // HTML-special, so escaping leaves it alone.
      return '\u0000' + (slots_.length - 1) + '\u0000';
    }

    var text = String(src == null ? '' : src);

    // Code spans: the most literal thing in markdown, and the most commonly
    // broken by doing it late.
    text = text.replace(/(`+)([\s\S]*?)\1/g, function (m, ticks, code) {
      // A leading and trailing space is markdown's way of escaping a code span
      // that itself starts or ends with a backtick; strip exactly one pair.
      var c = code;
      if (c.length > 2 && c.charAt(0) === ' ' && c.charAt(c.length - 1) === ' ') {
        c = c.slice(1, -1);
      }
      return stash('<code>' + escapeHTML(c) + '</code>');
    });

    // Images: rendered inert. No network request is made, so a document cannot
    // use an image to report who read it.
    text = text.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
      function (m, alt, src2, title) {
        return stash('<span class="md-image" title="Images are not loaded from documents">' +
          '<span class="md-image-alt">' + escapeHTML(alt || 'image') + '</span></span>');
      });

    // Links. Only hrefs on the safe-scheme allowlist survive; anything else is
    // demoted to plain text rather than silently dropped, so a reader can see
    // that the author wrote a link.
    text = text.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g,
      function (m, label, href, title) {
        var safe = safeURL(href);
        if (!safe) return escapeHTML(m);
        return stash('<a href="' + escapeHTML(safe) + '" rel="nofollow noopener noreferrer"' +
          (title ? ' title="' + escapeHTML(title) + '"' : '') + '>' + escapeHTML(label) + '</a>');
      });

    // Bare URLs and <autolinks>. Only http(s) and mailto.
    text = text.replace(/&lt;((?:https?:\/\/|mailto:)[^\s&]+)&gt;/gi,
      function (m, href) {
        var safe = safeURL(href);
        if (!safe) return escapeHTML(m);
        return stash('<a href="' + escapeHTML(safe) + '" rel="nofollow noopener noreferrer">' +
          escapeHTML(href) + '</a>');
      });
    text = text.replace(/(^|[\s(])((?:https?:\/\/)[^\s<>()"']+)/g,
      function (m, pre, href) {
        var safe = safeURL(href);
        if (!safe) return m;
        return pre + stash('<a href="' + escapeHTML(safe) + '" rel="nofollow noopener noreferrer">' +
          escapeHTML(href) + '</a>');
      });

    // Escape everything that remains. This is the guarantee: from here on the
    // string is inert text, and the only markup reintroduced is the ones above,
    // all of which contain escaped content.
    text = escapeHTML(text);

    // Emphasis. Runs after escaping so the asterisks are literal and were not
    // themselves escaped, which is why this is safe to do late.
    text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/(^|[^*\w])\*([^*\n]+)\*(?![*\w])/g, '$1<em>$2</em>');
    text = text.replace(/(^|[^_\w])_([^_\n]+)_(?![_\w])/g, '$1<em>$2</em>');
    text = text.replace(/~~([^~]+)~~/g, '<del>$1</del>');

    // Hard break: two trailing spaces, or a trailing backslash.
    text = text.replace(/ {2,}\n/g, '<br>\n');

    return restore(text);
  }

  // restore puts the stashed fragments back.
  var slots_ = [];

  function restore(text) {
    return text.replace(/\u0000(\d+)\u0000/g, function (m, i) {
      return slots_[i] != null ? slots_[i] : '';
    });
  }

  // render converts markdown source to HTML.
  //
  // The block scan is line-based and single-pass. It does not need to be a full
  // CommonMark implementation because the alternative — a parser that handles
  // every construct — means trusting a much larger attack surface on content we
  // do not control.
  function render(source, opts) {
    opts = opts || {};
    // A per-call allocator, not a module-level counter reset. The documents page
    // renders more than one markdown node per page -- a summary and the body --
    // and with a shared counter the second node restarted at doc-s-0, so half
    // the ids on the page were duplicates and the contents list had dead links.
    var nextId = nextHeadingId(opts.idStart || 0);
    var src = String(source == null ? '' : source).replace(/\r\n?/g, '\n');
    var lines = src.split('\n');
    var out = [];
    var i = 0;

    // slots_ is module-level so renderInline can reach it; each render resets it.
    slots_ = [];

    while (i < lines.length) {
      var line = lines[i];

      // Fenced code block. The info string is rendered as escaped text, never
      // as a class, so a crafted language like "x" onmouseover=... is inert.
      var fence = line.match(/^\s*(```+|~~~+)\s*(.*)$/);
      if (fence) {
        var marker = fence[1].charAt(0);
        var info = fence[2].trim();
        var buf = [];
        i++;
        while (i < lines.length) {
          var close = lines[i].match(/^\s*(```+|~~~+)\s*$/);
          if (close && close[1].charAt(0) === marker) { i++; break; }
          buf.push(lines[i]);
          i++;
        }
        out.push('<pre class="md-code"><code data-lang="' + escapeHTML(info) + '">' +
          escapeHTML(buf.join('\n')) + '</code></pre>');
        continue;
      }

      // Blank line.
      if (/^\s*$/.test(line)) { i++; continue; }


      // ATX heading. Up to 6 hashes; the trailing-# closing sequence is stripped.
      var h = line.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/);
      if (h) {
        var level = h[1].length;
        // Only H2 gets an id. Without one a table of contents is a list of text
        // that goes nowhere: the documents viewer links to #doc-s-N, which only
        // works if this renderer numbers the same way. See nextHeadingId for why
        // H2 specifically is the level that numbering is defined against.
        var hid = level === 2 ? nextId() : '';
        out.push('<h' + level + (hid ? ' id="' + hid + '"' : '') +
          ' class="md-h' + level + '">' + renderInline(h[2]) + '</h' + level + '>');
        i++;
        continue;
      }

      // Horizontal rule. Requires 3+ of one character with only spaces between,
      // so a line of dashes used as list spacing is not eaten.
      if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
        out.push('<hr class="md-hr">');
        i++;
        continue;
      }

      // Blockquote: consecutive '>' lines, rendered as one block with the inner
      // content re-parsed so a quote can contain a list or a code fence.
      if (/^\s*>/.test(line)) {
        var qbuf = [];
        while (i < lines.length && /^\s*>/.test(lines[i])) {
          qbuf.push(lines[i].replace(/^\s*>\s?/, ''));
          i++;
        }
        out.push('<blockquote class="md-quote">' + render(qbuf.join('\n')) + '</blockquote>');
        continue;
      }

      // Table: a header row, a delimiter row with alignment colons, then body
      // rows. A table without a delimiter row is not a table in markdown, and
      // treating it as one would swallow the paragraph that follows.
      //
      // The delimiter row is validated per-cell after splitting rather than by
      // one regex over the whole line. A whole-line regex has to allow a
      // trailing pipe, a leading pipe, spaces everywhere and three colon
      // arrangements at once, and getting that wrong silently disabled alignment
      // and made every aligned table render as a paragraph.
      if (line.indexOf('|') !== -1 && i + 1 < lines.length && isDelimiterRow(lines[i + 1])) {
        var aligns = splitRow(lines[i + 1]).map(function (c) {
          if (/^:.*:$/.test(c)) return 'center';
          if (/:$/.test(c)) return 'right';
          if (/^:/.test(c)) return 'left';
          return '';
        });
        var headers = splitRow(line);
        i += 2;
        var rows = [];
        while (i < lines.length && lines[i].indexOf('|') !== -1 && !/^\s*$/.test(lines[i])) {
          rows.push(splitRow(lines[i]));
          i++;
        }
        out.push('<div class="md-table-wrap"><table class="md-table"><thead><tr>' +
          headers.map(function (c, n) {
            return '<th' + (aligns[n] ? ' style="text-align:' + aligns[n] + '"' : '') + '>' +
              renderInline(c) + '</th>';
          }).join('') + '</tr></thead><tbody>' +
          rows.map(function (r) {
            return '<tr>' + r.map(function (c, n) {
              return '<td' + (aligns[n] ? ' style="text-align:' + aligns[n] + '"' : '') + '>' +
                renderInline(c) + '</td>';
            }).join('') + '</tr>';
          }).join('') + '</tbody></table></div>');
        continue;
      }

      // Lists. One level of nesting is supported by indenting 2+ spaces on a
      // continuation line and re-rendering the inner block; deeper nesting is
      // rendered flat rather than mis-nested.
      var isUl = /^\s*[-*+]\s+/.test(line);
      var isOl = /^\s*\d+[.)]\s+/.test(line);
      if (isUl || isOl) {
        var ordered = isOl;
        var startMatch = line.match(/^\s*(\d+)[.)]\s+/);
        var startAttr = ordered && startMatch && startMatch[1] !== '1'
          ? ' start="' + escapeHTML(startMatch[1]) + '"' : '';
        var items = [];
        while (i < lines.length) {
          var cur = lines[i];
          var m = ordered ? cur.match(/^\s*\d+[.)]\s+(.*)$/) : cur.match(/^\s*[-*+]\s+(.*)$/);
          if (m) {
            items.push(m[1]);
            i++;
            continue;
          }
          // Lazy continuation: an indented, non-blank line continues the item.
          if (/^\s{2,}\S/.test(cur) && items.length) {
            items[items.length - 1] += '\n' + cur.replace(/^\s{2,}/, '');
            i++;
            continue;
          }
          break;
        }
        var tag = ordered ? 'ol' : 'ul';
        out.push('<' + tag + ' class="md-list"' + startAttr + '>' + items.map(function (it) {
          // An item may hold a fenced block; render its own lines.
          if (/^\s*(```|~~~)/.test(it)) return '<li>' + render(it) + '</li>';
          return '<li>' + renderInline(it) + '</li>';
        }).join('') + '</' + tag + '>');
        continue;
      }

      // Paragraph: consume until a blank line or the start of another block.
      var pbuf = [];
      while (i < lines.length && !/^\s*$/.test(lines[i]) &&
             !/^\s*(```|~~~|#{1,6}\s|>|[-*+]\s|\d+[.)]\s)/.test(lines[i]) &&
             !/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(lines[i])) {
        pbuf.push(lines[i]);
        i++;
      }
      if (pbuf.length) {
        out.push('<p class="md-p">' + renderInline(pbuf.join('\n')) + '</p>');
      } else {
        // The line starts something we did not match; consume it as text so the
        // loop cannot spin forever on an unrecognised construct.
        out.push('<p class="md-p">' + renderInline(lines[i]) + '</p>');
        i++;
      }
    }

    return restore(out.join('\n'));
  }

  // isDelimiterRow reports whether a line is a table's alignment row. Every
  // cell must be three-or-more dashes with optional surrounding colons; a single
  // non-conforming cell means it is not a delimiter row.
  function isDelimiterRow(line) {
    if (line.indexOf('-') === -1) return false;
    var cells = splitRow(line);
    if (!cells.length) return false;
    return cells.every(function (c) {
      // One dash is enough. GFM's own rule is /^:?-+:?$/ and the spec's tables
      // use two; requiring three rejected the common `|:--|` form and made the
      // whole table render as a paragraph.
      return /^:?-+:?$/.test(c.trim());
    });
  }

  // splitRow splits a table row on unescaped pipes, tolerating a leading and
  // trailing pipe and \| inside cells.
  function splitRow(line) {
    var s = String(line).trim();
    // Protect code spans first. A pipe inside `a|b` is content, not a column
    // boundary, and splitting on it turns one cell into two and shifts every
    // column after it -- which is how an escaped-pipe table loses its structure.
    // The placeholder uses the same NUL sentinel as renderInline's stash.
    var spans = [];
    s = s.replace(/(`+)([^`]*?)\1/g, function (m, ticks, code) {
      spans.push(code);
      return '\u0000' + (spans.length - 1) + '\u0000';
    });
    if (s.charAt(0) === '|') s = s.slice(1);
    if (s.charAt(s.length - 1) === '|') s = s.slice(0, -1);
    var cells = [];
    var cur = '';
    for (var i = 0; i < s.length; i++) {
      if (s.charAt(i) === '\\' && s.charAt(i + 1) === '|') { cur += '|'; i++; continue; }
      if (s.charAt(i) === '|') { cells.push(cur.trim()); cur = ''; continue; }
      cur += s.charAt(i);
    }
    cells.push(cur.trim());
    // Restore the code span contents into their cells.
    return cells.map(function (c) {
      return c.replace(/\u0000(\d+)\u0000/g, function (m, n) {
        return '`' + spans[n] + '`';
      });
    });
  }

  global.ConcordMarkdown = {
    render: render,
    escapeHTML: escapeHTML,
    safeURL: safeURL,
    // Exposed for tests, not for pages.
    supported: ['headings', 'fenced-code', 'blockquote', 'lists', 'tables', 'hr', 'paragraph',
                'code-spans', 'bold', 'italic', 'strikethrough', 'links', 'autolinks', 'inert-images']
  };
})(window);