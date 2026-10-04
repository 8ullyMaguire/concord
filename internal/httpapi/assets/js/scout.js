// Concord Scout: free-text idea in, catalog verdicts out.
//
// Vanilla JS, no build step, matching assets/js/finder.js. It renders what
// /api/v1/scout returns and computes nothing about classification — a client-side
// verdict would be a second implementation of the same rule, and the two would
// disagree the moment a threshold changed.
//
// Three rules this file exists to keep, and each cost a defect somewhere else in
// the codebase to learn:
//
//   - **Signals are rendered, not summarised.** The API returns `signals` beside
//     every verdict; a UI that showed only the label would make the label
//     unfalsifiable, which is the same failure as a bare fit number over unknown
//     evidence.
//   - **Coverage is three states and `open` is a real answer.** A capability
//     nobody has asserted anything about is rendered as `open`, never as `0%` and
//     never as a dash, because "we have not looked" is the finding.
//   - **`reports == 0` is not `0%`.** Same rule as finder.js: an unmeasured
//     outcome rate is not a zero rate.
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function pct(x) {
    if (!isFinite(x)) return '—';
    return Math.round(x * 100) + '%';
  }

  var root = document.getElementById('scout-root');
  if (!root) return;

  var seed = document.getElementById('scout-seed');
  var report = document.getElementById('scout-report');
  var errBox = document.getElementById('scout-error');
  var input = document.getElementById('scout-seed-text');
  var exclude = document.getElementById('scout-exclude');
  var caps = document.getElementById('scout-capabilities');
  var verdicts = document.getElementById('scout-verdicts');
  var decompSummary = document.getElementById('scout-decomp-summary');
  var statsLine = document.getElementById('scout-stats');

  function show(el, on) { if (el) el.hidden = !on; }

  function fail(msg) {
    if (!errBox) return;
    errBox.textContent = msg;
    show(errBox, true);
  }

  // --- rendering ---------------------------------------------------------

  var VERDICT_ORDER = ['avoid', 'base-on', 'extend', 'adopt', 'inspire'];

  var VERDICT_NOTE = {
    // The label alone does not say what to DO, so each carries one line. The API
    // already sends a reason per verdict; this is the short form for the badge.
    'avoid': 'do not build on this',
    'base-on': 'fork this',
    'extend': 'start here and extend',
    'adopt': 'depend on this',
    'inspire': 'worth reading'
  };

  function renderCapabilities(list) {
    caps.innerHTML = '';
    var inCatalog = 0, proposed = 0;
    list.forEach(function (c) {
      if (c.in_catalog) inCatalog++; else proposed++;
      var li = document.createElement('li');
      // One element per capability, class on the `li` only. The panel-class rule
      // from the project page applies here: a class on both the `li` and an inner
      // span makes every strict-mode locator ambiguous.
      li.className = 'scout-cap' + (c.in_catalog ? '' : ' scout-cap-proposed');
      li.setAttribute('data-key', c.key);
      li.setAttribute('data-coverage', c.coverage || 'open');

      var label = document.createElement('span');
      label.className = 'scout-cap-label';
      label.textContent = c.label || c.key;
      li.appendChild(label);

      // Coverage is stated for every capability, including `open`. A dash here
      // would read as "no data about the data", which is the confusion this
      // feature exists to avoid.
      var cov = document.createElement('span');
      cov.className = 'scout-cap-coverage';
      cov.textContent = c.coverage || 'open';
      li.appendChild(cov);

      if (!c.in_catalog) {
        var tag = document.createElement('span');
        tag.className = 'scout-cap-tag';
        tag.textContent = 'proposed';
        tag.title = 'Not in the catalog, so it cannot be scored — reported, never matched.';
        li.appendChild(tag);
      }

      caps.appendChild(li);
    });

    var parts = [];
    if (inCatalog) parts.push(inCatalog + ' from the catalog');
    if (proposed) {
      parts.push(proposed + ' proposed from your text — reported, but not scored ' +
        'against anything');
    }
    if (!parts.length) {
      parts.push('Nothing in your idea matched a capability, so there was nothing ' +
        'to score against. Naming a need like "offline" or "self-hosted" gets you ' +
        'a real answer.');
    }
    decompSummary.textContent = parts.join(' · ');
  }

  function renderVerdict(v) {
    var row = document.createElement('div');
    row.className = 'scout-verdict scout-verdict-' + v.verdict;
    row.setAttribute('data-verdict', v.verdict);
    row.setAttribute('data-slug', v.slug);

    var head = document.createElement('div');
    head.className = 'scout-verdict-head';

    var badge = document.createElement('span');
    badge.className = 'scout-badge';
    badge.textContent = v.verdict;
    badge.title = VERDICT_NOTE[v.verdict] || '';
    head.appendChild(badge);

    var name = document.createElement('a');
    name.className = 'scout-verdict-name';
    name.href = '/projects/' + encodeURIComponent(v.slug);
    name.textContent = v.name || v.slug;
    head.appendChild(name);

    if (v.lead_on > 0) {
      var lead = document.createElement('span');
      lead.className = 'scout-badge scout-badge-lead';
      lead.textContent = 'leads ' + v.lead_on +
        (v.lead_on === 1 ? ' capability' : ' capabilities');
      head.appendChild(lead);
    }

    row.appendChild(head);

    var reason = document.createElement('p');
    reason.className = 'scout-verdict-reason';
    reason.textContent = v.reason || '';
    row.appendChild(reason);

    // The numbers, always together. A fit with no coverage beside it is the
    // misleading report the Finder fix already established; evidence_coverage is
    // what makes fit honest, so it is never omitted.
    var facts = document.createElement('p');
    facts.className = 'scout-verdict-facts';
    var bits = [];
    if (v.total > 0) {
      bits.push(v.matched + ' of ' + v.total + ' capabilities (' +
        pct(v.fit) + ', evidence ' + pct(v.evidence_coverage) + ')');
    } else {
      bits.push('no capabilities to score against');
    }
    // health_known gates the number. Rendering `health 0.00` for an unmeasured
    // project would be the exact lie §6.3 forbids.
    bits.push(v.health_known ? 'health ' + v.health.toFixed(2) : 'health not measured');
    if (v.license) bits.push('license ' + v.license);
    // reports == 0 renders as absent, never as 0%.
    if (v.reports > 0) {
      bits.push('field reports ' + pct(v.outcome_rate) + ' of ' + v.reports);
    } else if (v.reports === 0) {
      bits.push('no field reports yet');
    }
    facts.textContent = bits.join(' · ');
    row.appendChild(facts);

    if (v.signals && v.signals.length) {
      var sig = document.createElement('ul');
      sig.className = 'scout-signals';
      v.signals.forEach(function (s) {
        var li = document.createElement('li');
        li.textContent = s;
        sig.appendChild(li);
      });
      row.appendChild(sig);
    }

    if (v.pain && v.pain.length) {
      var pain = document.createElement('div');
      pain.className = 'scout-pain';
      var ph = document.createElement('h3');
      ph.textContent = 'Known complaints';
      pain.appendChild(ph);
      var ul = document.createElement('ul');
      v.pain.forEach(function (p) {
        var li = document.createElement('li');
        li.textContent = (p.title || p.summary || p.id);
        ul.appendChild(li);
      });
      pain.appendChild(ul);
      row.appendChild(pain);
    }

    return row;
  }

  function renderReport(data) {
    renderCapabilities(data.capabilities || []);

    var considered = data.candidates_considered || 0;
    var counts = [];
    VERDICT_ORDER.forEach(function (k) {
      if (data.stats && data.stats[k]) counts.push(data.stats[k] + ' ' + k);
    });
    statsLine.textContent = considered === 0
      ? 'No projects in the catalog were visible to you, so there was nothing to compare against.'
      : considered + (considered === 1 ? ' project considered' : ' projects considered') +
        (counts.length ? ' — ' + counts.join(', ') : '');

    verdicts.innerHTML = '';
    var list = data.verdicts || [];
    if (!list.length) {
      var empty = document.createElement('p');
      empty.className = 'scout-empty';
      empty.textContent = 'Nothing to report.';
      verdicts.appendChild(empty);
    }
    list.forEach(function (v) { verdicts.appendChild(renderVerdict(v)); });

    show(seed, false);
    show(report, true);
  }

  // --- fetch -------------------------------------------------------------

  function submit(e) {
    if (e) e.preventDefault();
    show(errBox, false);
    var idea = (input.value || '').trim();
    if (!idea) {
      fail('Say what you want to build first.');
      return;
    }
    var q = 'idea=' + encodeURIComponent(idea);
    var ex = (exclude.value || '').trim();
    if (ex) q += '&exclude_licenses=' + encodeURIComponent(ex);

    root.setAttribute('aria-busy', 'true');
    fetch('/api/v1/scout?' + q, { headers: { 'Accept': 'application/json' } })
      .then(function (r) {
        return r.text().then(function (txt) {
          var data = {};
          try { data = txt ? JSON.parse(txt) : {}; } catch (err) { data = {}; }
          if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
          return data;
        });
      })
      .then(renderReport)
      .catch(function (err) { fail(err.message || 'Scout could not answer.'); })
      .then(function () { root.setAttribute('aria-busy', 'false'); });
  }

  var form = document.getElementById('scout-seed-form');
  if (form) form.addEventListener('submit', submit);

  var again = document.getElementById('scout-again');
  if (again) {
    again.addEventListener('click', function () {
      show(report, false);
      show(seed, true);
      if (input) input.focus();
    });
  }

  // --- boot --------------------------------------------------------------

  show(seed, true);
  show(report, false);
  root.setAttribute('aria-busy', 'false');
  if (input) input.focus();
})();