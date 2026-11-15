// One escaping rule, in one place.
//
// Before this, 29 files each defined their own `esc`, in three shapes, and the review's point was that this is where the subtle bugs live:
// an escaping helper that differs by a character between two pages is a page that renders markup where another renders text.
//
// The rule: five characters, replaced once, in a single pass. Single pass matters — a chain of `.replace()` calls that escapes `&` first and
// then `<` re-escapes the ampersands it just produced under a different reading, and the version that escapes `&` LAST double-escapes a
// literal `&amp;` in the input. A character class with a map cannot, because each character is visited exactly once.
//
// `null` and `undefined` render as the empty string rather than "null": every caller here interpolates API fields that are absent by design
// (a project with no license, a complaint with no resolution), and "null" in the markup is a bug the reader sees.
(function () {
  'use strict';

  var MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

  function esc(value) {
    return String(value == null ? '' : value).replace(/[&<>"']/g, function (c) {
      return MAP[c];
    });
  }

  window.ConcordEscape = { esc: esc, escapeHTML: esc };
})();
