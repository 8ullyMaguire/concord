// The renderer that cannot emit markup by accident (step 2b).
//
// The old idiom was `'<p>' + esc(text) + '</p>'` assigned to innerHTML. One missed `esc()` was an XSS, and with a bearer
// token in localStorage it was a full account takeover. This builder has no string-sink at all: every text child becomes a
// DOM text node, and every attribute goes through setAttribute. There is no API here that parses HTML, so a value that
// contains `<img onerror=...>` is displayed as those characters rather than executed.
//
// el(tag, props, children)
//   props.class / props.className -> class attribute
//   props.text                    -> a text node (for leaf nodes built without children)
//   props.on                      -> { click: fn } event listeners
//   every other key               -> an attribute, e.g. { type: 'button', 'data-read': id }
//   children                      -> a node, a string/number, or an array of either; falsy entries are skipped
(function () {
  'use strict';

  function append(node, child) {
    if (child == null || child === false || child === true) return;
    if (typeof child === 'object' && child.nodeType) {
      node.appendChild(child);
    } else {
      node.appendChild(document.createTextNode(String(child)));
    }
  }

  function el(tag, props, children) {
    var node = document.createElement(tag);
    var given = props || {};
    Object.keys(given).forEach(function (key) {
      var value = given[key];
      if (value == null || value === false) return;
      if (key === 'class' || key === 'className') {
        node.setAttribute('class', String(value));
      } else if (key === 'text') {
        append(node, value);
      } else if (key === 'on') {
        Object.keys(value).forEach(function (event) {
          node.addEventListener(event, value[event]);
        });
      } else {
        node.setAttribute(key, value === true ? '' : String(value));
      }
    });
    var kids = children == null ? [] : (Array.isArray(children) ? children : [children]);
    kids.forEach(function (child) {
      if (Array.isArray(child)) {
        child.forEach(function (grandchild) { append(node, grandchild); });
      } else {
        append(node, child);
      }
    });
    return node;
  }

  window.ConcordDom = { el: el };
})();
