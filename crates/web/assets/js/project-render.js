// Concord project detail — the node-only renderer (D-16, step 2b).
//
// This file owns every tree the project page draws from data it has already fetched.
// The old `render()` was one long HTML string that concatenated strings returned by
// `ConcordPanels.*`, `ConcordWrites.panel()` and `ConcordArena.controls()`. A page
// assembled that way needs one `innerHTML` sink and one missed `esc()` away from an
// XSS. Here every value is a text node or a `setAttribute` value; there is no API
// in this file that parses HTML.
//
// D-14 kept `ConcordArena.controls()` as a string because `project.js` concatenated
// it. D-16 supersedes that: this file renders the controls as nodes, so the string
// contract is no longer composed into this page. The helper modules keep their
// string exports for their own wire code; the project page no longer calls them
// for markup.
(function () {
  'use strict';
  var el = window.ConcordDom.el;

  function sectionHead(title, note, top) {
    var props = { class: 'section-head' };
    if (top) props.style = 'margin-top:2rem;';
    return el('div', props, [
      el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, title),
      el('p', { class: 'section-sub' }, note)
    ]);
  }

  function emptyState(icon, title, description) {
    return el('div', { class: 'empty-state' }, [
      el('div', { class: 'empty-state-icon' }, icon),
      el('p', { class: 'empty-state-title' }, title),
      el('p', { class: 'empty-state-description' }, description)
    ]);
  }

  function panelEmpty(title, description) {
    return el('div', { class: 'empty-state' }, [mutant,
      el('p', { class: 'empty-state-title' }, title),
      el('p', { class: 'empty-state-description' }, description)
    ]);
  }

  var PANEL_ROOTS = {
    'Capabilities': 'panel-capabilities',
    'Field reports': 'panel-field-reports',
    'Solutions': 'panel-solutions',
    'Alternatives': 'panel-alternatives'
  };

  function panelError(what, message) {
    return el('div', { class: 'empty-state panel-error ' + (PANEL_ROOTS[what] || '') }, [
      el('p', { class: 'empty-state-title' }, what + ' unavailable'),
      el('p', { class: 'empty-state-description' }, message)
    ]);
  }

  function fmtDate(unix) {
    if (!unix) return '';
    try { return new Date(unix * 1000).toLocaleDateString(); } catch (e) { return ''; }
  }

  function healthBar(score) {
    if (score == null) {
      return el('p', { class: 'health-label' }, [
        el('span', {}, 'health'), el('span', {}, 'no metrics yet')
      ]);
    }
    var pct = Math.round(score * 100);
    var cls = pct >= 70 ? 'good' : pct >= 40 ? 'mid' : 'poor';
    return [
      el('div', { class: 'health-bar' },
        el('div', { class: 'health-fill health-fill-' + cls, style: 'width:' + pct + '%' })),
      el('p', { class: 'health-label' }, [
        el('span', {}, 'health'), el('span', {}, pct + '%')
      ])
    ];
  }

  function newcomerLine(proj) {
    if (proj.newcomer_response_hours == null) return null;
    return el('p', { class: 'health-label' }, [
      el('span', {}, 'first response to a newcomer'),
      el('span', {}, Number(proj.newcomer_response_hours).toFixed(1) + ' h (n=' +
        Number(proj.newcomer_sample || 0) + ')')
    ]);
  }

  // A disputed claim prints its value WITH the dispute beside it. It never prints
  // as settled, and it is never flattened to "no" — "people disagree" and "this
  // is false" are different claims about a project.
  function capabilityValue(c) {
    if (!c.asserted) return el('span', { class: 'cap-unknown' }, 'no claim yet');
    var children = [el('span', { class: 'cap-value' }, c.value || 'unknown')];
    if (c.state === 'disputed') {
      children.push(' ');
      children.push(el('span', {
        class: 'badge badge-amber',
        title: 'People disagree about this. ' + (c.disputes || 0) +
          ' contested it; the original claim is not rewritten.'
      }, 'disputed'));
    } else if (c.state === 'confirmed') {
      children.push(' ');
      children.push(el('span', {
        class: 'badge badge-green',
        title: 'Confirmed by ' + (c.confirms || 0) + ' independent confirmations.'
      }, 'confirmed'));
    } else {
      children.push(' ');
      children.push(el('span', { class: 'badge badge-slate' }, 'asserted, unconfirmed'));
    }
    return children;
  }

  function capabilitiesPanel(data) {
    if (!data) return panelError('Capabilities', 'The capability matrix could not be loaded.');
    var caps = data.capabilities || [];
    var head = el('div', { class: 'section-head' }, [
      el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, 'Capabilities'),
      el('p', { class: 'section-sub' }, 'What this project claims about itself, and what nobody has ' +
        'claimed yet. ' + (data.unknown_count > 0
          ? data.unknown_count + ' of ' + caps.length + ' have no claim from anyone.'
          : 'Every capability in the catalog has a claim.'))
    ]);
    if (!caps.length) {
      return el('div', { class: 'panel-capabilities' }, [
        head,
        panelEmpty('No capability matrix yet',
          'This instance has no capability catalog, so there is nothing for a project to claim. ' +
          'Capabilities are created instance-wide and then asserted per project.')
      ]);
    }
    var rows = caps.map(function (c) {
      return el('tr', {}, [
        el('td', {}, [
          c.label || c.key,
          el('div', { class: 'cap-key' }, c.key)
        ]),
        el('td', { class: 'cap-cat' }, c.category || ''),
        el('td', {}, [
          capabilityValue(c),
          c.evidence ? el('div', { class: 'cap-evidence' }, c.evidence) : null
        ])
      ]);
    });
    return el('div', { class: 'panel-capabilities' }, [
      head,
      el('div', { class: 'card' }, el('table', { class: 'cap-table' }, [
        el('thead', {}, el('tr', {}, [
          el('th', {}, 'Capability'), el('th', {}, 'Category'), el('th', {}, 'Claim')
        ])),
        el('tbody', {}, rows)
      ]))
    ]);
  }

  function fieldReportForm() {
    function row(label, forId, control) {
      return el('div', { class: 'form-row' }, [
        el('label', { class: 'form-label', 'for': forId }, label), control
      ]);
    }
    function input(id, name, placeholder) {
      return el('input', { class: 'input', id: id, name: name, placeholder: placeholder });
    }
    return el('form', { class: 'card', id: 'field-report-form', style: 'margin-top:1rem;' }, [
      el('div', { class: 'card-title' }, 'File a field report'),
      el('p', { class: 'card-text' }, 'What happened when you used this, in the terms the outcome rate ' +
        'counts. A version, an environment, and an outcome are what make one report comparable with the next.'),
      row('Outcome', 'fr-outcome', el('select', { class: 'input', id: 'fr-outcome', name: 'outcome' }, [
        el('option', { value: 'worked' }, 'worked'),
        el('option', { value: 'worked-with-caveats' }, 'worked, with caveats'),
        el('option', { value: 'abandoned' }, 'abandoned'),
        el('option', { value: 'migrated-away' }, 'migrated away')
      ])),
      row('Version', 'fr-version', input('fr-version', 'version', '1.4.2')),
      row('Environment', 'fr-environment', input('fr-environment', 'environment', 'arm64, docker, bare metal')),
      row('Use case', 'fr-use-case', input('fr-use-case', 'use_case', 'what you were trying to do')),
      row('Migrated to (required only for the migrated-away outcome)', 'fr-migrated-to',
        input('fr-migrated-to', 'migrated_to', 'the successor')),
      row('What happened', 'fr-caveats', el('textarea', {
        class: 'input', id: 'fr-caveats', name: 'caveats', rows: '3'
      })),
      row('Advice for the next person', 'fr-advice', el('textarea', {
        class: 'input', id: 'fr-advice', name: 'advice', rows: '2'
      })),
      el('button', { type: 'submit', class: 'btn', 'data-file-field-report': '' }, 'File this report'),
      el('p', { class: 'form-status', 'data-fr-error': '', hidden: '' })
    ]);
  }

  function fieldReportsPanel(data) {
    if (!data) return panelError('Field reports', 'Field reports could not be loaded.');
    var reports = data.reports || [];
    var head = el('div', { class: 'section-head', style: 'margin-top:2rem;' }, [
      el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, 'Field reports'),
      el('p', { class: 'section-sub' }, 'Structured experience records: version, environment, and what actually happened.')
    ]);
    // The summary is computed BEFORE the empty check and included in both returns,
    // so the `outcome_rate != null` guard is the thing that decides whether a rate appears.
    var summary = null;
    if (data.outcome_rate != null) {
      var pct = Math.round(data.outcome_rate * 100);
      summary = el('div', { class: 'fr-summary' }, [
        el('strong', {}, pct + '%'), ' of ' + data.sample_size + ' reporter' +
          (data.sample_size === 1 ? '' : 's') + ' report this working, weighted by reporter reputation.'
      ]);
    }
    var envs = data.by_environment || {};
    var envKeys = Object.keys(envs);
    var env = envKeys.length
      ? el('div', { class: 'pill-row', style: 'margin-top:0.5rem;' }, envKeys.map(function (k) {
          return el('span', { class: 'badge badge-slate' }, k + ' ' + Math.round(envs[k] * 100) + '%');
        }))
      : null;
    if (!reports.length) {
      return el('div', { class: 'panel-field-reports' }, [
        head, summary, env,
        panelEmpty('No field reports yet',
          'Nobody has recorded using this. Until someone does, there is no evidence here to weigh ' +
          'against the project\'s claims.'),
        fieldReportForm()
      ]);
    }
    var cards = reports.map(function (r) {
      var body = [];
      if (r.caveats) body.push(el('p', { class: 'card-text' }, r.caveats));
      if (r.advice) body.push(el('p', { class: 'card-text' }, [el('strong', {}, 'Advice: '), r.advice]));
      var responses = (r.responses || []).map(function (resp) {
        return el('div', { class: 'fr-response' }, [
          el('span', { class: 'badge ' + (resp.is_owner ? 'badge-indigo' : 'badge-slate') },
            resp.is_owner ? 'owner' : 'reply'),
          ' ', resp.body
        ]);
      });
      var id = String(r.id);
      responses.push(el('form', { class: 'fr-reply-form', 'data-respond-report': id }, [
        el('label', { class: 'form-label', 'for': 'fr-reply-' + id }, 'Reply'),
        el('input', { class: 'input', id: 'fr-reply-' + id, 'data-response-body': '',
          placeholder: 'the owner can answer here; anyone signed in can say they saw it too' }),
        el('button', { type: 'submit', class: 'btn', 'data-submit-response': '' }, 'Reply'),
        el('p', { class: 'form-status', 'data-fr-error': '', hidden: '' })
      ]));
      var pills = [el('span', { class: 'badge badge-purple' }, r.outcome)];
      if (r.version) pills.push(el('span', { class: 'badge badge-slate' }, r.version));
      if (r.environment) pills.push(el('span', { class: 'badge badge-slate' }, r.environment));
      return el('div', { class: 'card fr-card' }, [
        el('div', { class: 'card-title' }, r.use_case || 'field report'),
        el('div', { class: 'pill-row' }, pills),
        body, responses
      ]);
    });
    var omitted = data.omitted > 0
      ? el('p', { class: 'section-sub' }, 'Showing the newest ' + data.shown + '; ' + data.omitted +
          ' older report' + (data.omitted === 1 ? '' : 's') + ' not shown.')
      : null;
    return el('div', { class: 'panel-field-reports' }, [
      head, summary, env, omitted,
      el('div', { class: 'grid-responsive-2', style: 'margin-top:1rem;' }, cards),
      fieldReportForm()
    ]);
  }

  function solutionsPanel(data) {
    if (!data) return panelError('Solutions', 'Solution standings could not be loaded.');
    var groups = data.features || [];
    var head = el('div', { class: 'section-head', style: 'margin-top:2rem;' }, [
      el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, 'Solution standings'),
      el('p', { class: 'section-sub' }, 'Ranked proposals per feature. Scores are computed against the other ' +
        'proposals for that feature, so they are not comparable across features.')
    ]);
    if (!groups.length) {
      return el('div', { class: 'panel-solutions' }, [
        head,
        panelEmpty('No roadmap yet',
          'This project has no features, so there is nothing for solutions to address. ' +
          'Features come from validated complaints.')
      ]);
    }
    var withSol = data.features_with_solutions || 0;
    var without = data.features_without || 0;
    var tally = el('div', { class: 'fr-summary', style: 'border-left-color: var(--slate-400);' }, [
      el('strong', {}, String(withSol)), ' of ' + groups.length + ' feature' +
        (groups.length === 1 ? '' : 's') + ' ' + (withSol === 1 ? 'has' : 'have') + ' proposals' +
        (without > 0 ? '; ' + without + ' ' + (without === 1 ? 'has' : 'have') + ' none.' : '.')
    ]);
    var blocks = groups.map(function (g) {
      var sols = g.solutions || [];
      if (!sols.length) {
        return el('div', { class: 'sol-group sol-empty' }, [
          el('div', { class: 'sol-feature' }, g.feature_title),
          el('p', { class: 'card-text' }, 'No proposals yet. ' +
            (g.feature_id ? 'Feature ' + g.feature_id + ' is open for one.' : ''))
        ]);
      }
      var rows = sols.map(function (sol, i) {
        return el('li', { class: sol.is_baseline ? 'sol-row sol-baseline' : 'sol-row' }, [
          el('span', { class: 'sol-rank' }, String(i + 1)),
          el('span', { class: 'sol-title' }, sol.title || 'proposal ' + sol.solution_id),
          el('span', { class: 'sol-coverage',
            title: 'Coverage against this feature\'s criteria' },
            Math.round((sol.coverage || 0) * 100) + '%'),
          sol.rating != null ? sol.rating.toFixed(2) : '\u2014',
          sol.is_baseline ? el('span', { class: 'badge badge-slate' }, 'do nothing') : null
        ]);
      });
      return el('div', { class: 'sol-group' }, [
        el('div', { class: 'sol-feature' }, g.feature_title),
        el('ul', { class: 'sol-list' }, rows),
        g.omitted > 0 ? el('p', { class: 'section-sub' }, g.omitted + ' more proposal' +
          (g.omitted === 1 ? '' : 's') + ' not shown.') : null
      ]);
    });
    var calls = data.consensus_calls || [];
    var consensusLink = calls.length
      ? el('p', { class: 'panel-consensus-link' },
          el('a', { href: '/projects/' + encodeURIComponent(data.slug || '') +
            '/consensus?call=' + encodeURIComponent(calls[0].id) }, 'Open the call'))
      : null;
    return el('div', { class: 'panel-solutions' }, [
      head, tally, consensusLink, el('div', { class: 'sol-groups' }, blocks)
    ]);
  }

  // The arena controls, rendered as nodes. D-14 left them as a string because
  // the old page concatenated it; D-16 removes that reason.
  function arenaControls(a) {
    var id = String(a.arena_id);
    function button(attr, label) {
      var props = { type: 'button', class: 'btn btn-quiet' };
      props['data-arena-' + attr] = id;
      return el('button', props, label);
    }
    return [
      el('div', { class: 'arena-loop' }, [
        button('pair', 'Judge the next pair'),
        button('log', 'Vote log'),
        button('verify', 'Verify ranking')
      ]),
      el('div', { class: 'arena-pair', 'data-arena-pair-out': id, role: 'status', 'aria-live': 'polite' }),
      el('div', { class: 'arena-log', 'data-arena-log-out': id }),
      el('div', { class: 'arena-verify', 'data-arena-verify-out': id })
    ];
  }

  function alternativesPanel(data) {
    if (!data) return panelError('Alternatives', 'Alternative project rankings could not be loaded.');
    var arenas = data.arenas || [];
    var head = el('div', { class: 'section-head', style: 'margin-top:2rem;' }, [
      el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, 'Alternatives'),
      el('p', { class: 'section-sub' }, 'Competing projects, ranked for a use case you name. Each use case is ' +
        'ranked on its own, so scores are not comparable between them.')
    ]);
    if (!arenas.length) {
      return el('div', { class: 'panel-alternatives' }, [
        head,
        panelEmpty('No use cases compared yet',
          'This project has not compared itself against other projects for a particular use case. ' +
          'Those comparisons are added per use case, and each one is ranked separately.')
      ]);
    }
    var blocks = arenas.map(function (a) {
      var entries = a.entries || [];
      var list = entries.length
        ? el('ul', { class: 'alt-list' }, entries.map(function (entry) {
            return el('li', { class: 'alt-row' }, [
              el('span', { class: 'alt-rank' }, entry.rank),
              el('a', { class: 'alt-title', href: '/projects/' + entry.slug }, entry.title),
              el('span', { class: 'alt-rating' },
                entry.rating.toFixed ? entry.rating.toFixed(0) : entry.rating)
            ]);
          }))
        : el('p', { class: 'card-text' }, 'No projects entered yet. This use case has been posed and ' +
            'nothing has been compared against it.');
      var pair = null;
      if (entries.length) {
        pair = el('p', { class: 'alt-pair' }, [
          el('span', {}, 'Better for: ' + entries[0].better_for),
          entries[0].worse_for ? el('span', {}, entries[0].worse_for) : null
        ]);
      }
      return el('div', { class: 'alt-arena' }, [
        el('div', { class: 'alt-usecase' }, a.use_case),
        pair, list, arenaControls(a)
      ]);
    });
    return el('div', { class: 'panel-alternatives' }, [head, blocks]);
  }

  function complaintCards(list, slug) {
    return (list || []).map(function (c) {
      var pills = [
        el('span', { class: 'badge ' + (c.status === 'validated' ? 'badge-green' : 'badge-slate') },
          c.status || 'open'),
        el('span', { class: 'badge badge-slate' }, 'severity ' + c.severity)
      ];
      if (c.status === 'open') {
        pills.push(el('button', { class: 'btn btn-quiet', 'data-validate': c.id }, 'Validate'));
        pills.push(el('button', { class: 'btn btn-quiet', 'data-reject': c.id }, 'Reject as spam'));
      }
      return el('div', { class: 'card' }, [
        el('div', { class: 'card-title' },
          el('a', { href: '/projects/' + slug + '/complaints/' + c.id }, c.title)),
        c.body ? el('p', { class: 'card-text' }, c.body) : null,
        el('div', { class: 'pill-row' }, pills)
      ]);
    });
  }

  function linkCard(href, title, body, status) {
    return el('article', { class: 'card' }, [
      el('h3', {}, el('a', { href: href }, title)),
      body ? el('p', { class: 'card-text' }, body) : null,
      el('p', { class: 'card-meta' },
        el('span', { class: 'badge ' + (status === 'open' ? 'badge-blue' : 'badge-slate') },
          status || 'open'))
    ]);
  }

  function listSection(lists, p) {
    var head = sectionHead('Lists',
      'Curated collections whose entries are ranked by pairwise comparison, not by who edited the markdown last.', true);
    if (!lists || !lists.length) {
      return [head, emptyState('\u{1F4CB}', 'No lists yet', 'Nothing has been curated here.')];
    }
    return [head, el('div', { class: 'grid-responsive-2' }, lists.map(function (l) {
      return linkCard('/projects/' + encodeURIComponent(p.slug) + '/lists/' + encodeURIComponent(l.id),
        l.name, l.description, l.status);
    }))];
  }

  function requestSection(requests, p) {
    var head = sectionHead('Requests',
      'Structured questions, not threads. Projects answer them with a fit rationale, and the community ranks ' +
      'those answers against each other.', true);
    if (!requests || !requests.length) {
      return [head, emptyState('\u{1F4DC}', 'No open requests', 'Nothing has been asked here yet.')];
    }
    return [head, el('div', { class: 'grid-responsive-2' }, requests.map(function (r) {
      return linkCard('/projects/' + encodeURIComponent(p.slug) + '/requests/' + encodeURIComponent(r.id),
        r.title, r.body, r.status || 'open');
    }))];
  }

  function workItemsSection(items, p) {
    var list = items || [];
    var session = window.ConcordSession && window.ConcordSession.instance();
    var signedIn = !!(session && session.signedIn());
    var rows = list.map(function (w) {
      var badges = [];
      if (w.kind === 'complaint') {
        badges.push(el('span', { class: 'badge badge-amber' }, 'needs validating'));
      } else {
        badges.push(el('span', { class: 'badge badge-slate' }, w.status));
        if (w.time_budget_minutes) {
          badges.push(el('span', { class: 'badge badge-indigo' }, String(w.time_budget_minutes) + ' min'));
        }
        if (w.difficulty) {
          badges.push(el('span', { class: 'badge badge-purple' }, 'difficulty ' + w.difficulty));
        }
        if (w.needs_mentor) {
          badges.push(el('span', { class: 'badge badge-emerald' }, 'mentor offered'));
        }
      }
      var action;
      if (w.kind === 'feature') {
        if (w.claimed_by) {
          action = el('span', { class: 'meta-item', 'data-claimed': w.id }, 'claimed by another contributor');
        } else if (!signedIn) {
          action = el('a', { class: 'btn', href: '/login' }, 'Sign in to claim');
        } else {
          action = el('button', { type: 'button', class: 'btn', 'data-claim': w.id }, 'Claim this');
        }
      } else {
        action = el('a', { class: 'btn', href: '/projects/' + p.slug + '/complaints' }, 'Read it');
      }
      var label = null;
      if (w.kind === 'feature' && signedIn) {
        var options = [el('option', { value: '' }, 'difficulty?')];
        ['S', 'M', 'L'].forEach(function (d) {
          options.push(el('option', { value: d, selected: w.difficulty === d ? '' : null }, d));
        });
        label = el('form', { class: 'inline-form', 'data-work-item-form': w.id }, [
          el('select', { name: 'difficulty', 'aria-label': 'Difficulty' }, options),
          el('input', { name: 'minutes', type: 'number', min: '1', step: '1',
            'aria-label': 'Time budget in minutes', placeholder: 'minutes',
            value: w.time_budget_minutes ? String(w.time_budget_minutes) : '' }),
          el('label', { class: 'meta-item' }, [
            el('input', { type: 'checkbox', name: 'mentor', checked: w.needs_mentor ? '' : null }), ' mentor'
          ]),
          el('button', { type: 'submit', class: 'btn' }, 'Save'),
          el('span', { class: 'meta-item', 'data-work-item-status': w.id })
        ]);
      }
      return el('div', { class: 'card', 'data-work-item': w.kind + '-' + w.id }, [
        el('div', { class: 'card-title' }, w.title),
        el('div', { class: 'pill-row' }, badges),
        action, label
      ]);
    });
    return [
      sectionHead('Work items', 'What you can pick up, cheapest first. One live claim per item, and a claim ' +
        'does not move the feature \u2014 the board does that.', true),
      list.length
        ? el('div', { class: 'grid-responsive-2' }, rows)
        : emptyState('\u{1F9F0}', 'Nothing to pick up yet',
            'Work items appear when a feature is sized and moved to ready, or when a complaint is filed ' +
            'that still needs validating.')
    ];
  }

  function signedOutForms() {
    return emptyState(null, 'Sign in to take part', [
      'Filing a complaint, proposing a feature and ranking all need an account. ',
      el('a', { href: '/login?next=' + encodeURIComponent(window.location.pathname) }, 'Sign in'),
      ' or ', el('a', { href: '/register' }, 'register'), '.'
    ]);
  }

  function field(label, control) {
    return el('label', { class: 'field' }, [el('span', {}, label), control]);
  }

  // The duplicate panel's markup, owned here rather than read from
  // `ConcordWrites.panel()`: that string contract was the last reason for the
  // project page to concatenate HTML. The wire helper finds it by data attribute.
  function dupPanel(kind) {
    return el('div', { class: 'dup-panel', 'data-dup-panel': kind,
      role: 'status', 'aria-live': 'polite' });
  }

  function forms(p) {
    var session = window.ConcordSession && window.ConcordSession.instance();
    if (!session || !session.signedIn()) return signedOutForms();
    var mode = window.ConcordPanels.intakeMode(p);
    var complaints = window.__complaints || [];
    var options = [el('option', { value: '' }, mode === 'complaint_first' ? 'Choose\u2026' : 'None — file it as an idea')];
    complaints.forEach(function (c) {
      options.push(el('option', { value: c.id }, c.title));
    });
    return el('div', { class: 'form-grid' }, [
      el('form', { class: 'card', id: 'complaint-form', method: 'post', action: '#' }, [
        el('h3', { class: 'form-title' }, 'File a complaint'),
        el('p', { class: 'form-note' }, 'A specific problem, with the harm it causes. Vague complaints ' +
          'cannot be validated and therefore cannot be solved.'),
        field('Title', el('input', { name: 'title', required: '', maxlength: '200',
          placeholder: 'Search returns nothing for an empty query' })),
        dupPanel('complaint'),
        field('What happens, and to whom', el('textarea', { name: 'body', rows: '3', required: '',
          placeholder: 'I searched for\u2026 and got\u2026' })),
        el('div', { class: 'field-row' }, [
          field('Severity 1-5', el('input', { name: 'severity', type: 'number', min: '1', max: '5', value: '3' })),
          field('How often', el('input', { name: 'frequency', type: 'number', min: '0', step: '0.1', value: '1' }))
        ]),
        el('button', { class: 'btn btn-primary', type: 'submit' }, 'File it'),
        el('p', { class: 'form-status', 'data-status': '', hidden: '' })
      ]),
      el('form', { class: 'card', id: 'feature-form', method: 'post', action: '#', 'data-intake-mode': mode }, [
        el('h3', { class: 'form-title' }, 'Propose a feature'),
        el('p', { class: 'form-note' }, window.ConcordPanels.intakeNote(p)),
        field('Title', el('input', { name: 'title', required: '', maxlength: '200',
          placeholder: 'Search falls back to substring matching' })),
        dupPanel('feature'),
        field('What it would do', el('textarea', { name: 'body', rows: '3', required: '' })),
        field('Answers complaint' + (mode === 'complaint_first' ? '' : ' (optional)'),
          el('select', { name: 'complaint_id' }, options)),
        el('button', { class: 'btn btn-primary', type: 'submit' }, 'Propose it'),
        el('p', { class: 'form-status', 'data-status': '', hidden: '' })
      ])
    ]);
  }

  function nextMilestone(roadmap) {
    var ms = (roadmap && roadmap.milestones) || [];
    var open = ms.filter(function (m) { return m.status === 'planned' || m.status === 'active'; });
    if (!open.length) return null;
    open.sort(function (a, b) {
      if (a.target_date && b.target_date) return a.target_date - b.target_date;
      if (a.target_date) return -1;
      if (b.target_date) return 1;
      return a.id - b.id;
    });
    return open[0];
  }

  function render(p, features, complaints, documents, panels, requests, lists, workItems, roadmap) {
    panels = panels || {};
    var slug = p.slug;
    var badges = [el('span', { class: 'badge badge-indigo' }, p.governance_model || 'governed')];
    if (p.license) badges.push(el('span', { class: 'badge badge-slate' }, p.license));

    var next = nextMilestone(roadmap);
    var nextCard = next ? el('a', { class: 'card ms-next',
      href: '/projects/' + slug + '/milestones/' + next.id }, [
      el('div', { class: 'card-title' }, 'Next milestone: ' + next.title),
      el('p', { class: 'card-text' }, [
        next.target_date ? 'target ' + fmtDate(next.target_date) : 'no date set',
        ' \u00b7 ' + (next.open || 0) + ' open of ' + (next.total || 0),
        next.at_risk ? ' \u00b7 ' : null,
        next.at_risk ? el('strong', {}, 'at risk') : null
      ])
    ]) : null;

    var meta = el('div', { class: 'meta-row' }, [
      p.created_at ? el('span', { class: 'meta-item' }, 'created ' + fmtDate(p.created_at)) : null,
      p.updated_at ? el('span', { class: 'meta-item' }, 'updated ' + fmtDate(p.updated_at)) : null
    ]);

    var featureCards = (features || []).map(function (f) {
      var rating = f.elo_r == null ? 'not compared' : 'rating ' + Math.round(f.elo_r) +
        (f.elo_rd != null ? ' \u00b1' + Math.round(f.elo_rd) : '');
      return el('div', { class: 'card' }, [
        el('div', { class: 'card-title' },
          el('a', { href: '/projects/' + slug + '/features/' + f.id }, f.title)),
        f.body ? el('p', { class: 'card-text' }, f.body) : null,
        el('div', { class: 'pill-row' }, [
          el('span', { class: 'badge badge-purple' }, rating),
          el('span', { class: 'badge badge-slate' }, f.status || 'proposed')
        ])
      ]);
    });

    var intake = window.ConcordPanels.intakeMode(p) === 'complaint_first'
      ? 'A feature needs at least one validated complaint behind it before it can reach ready.'
      : 'A feature may be filed as an idea here; evidence is required before ready.';
    var featureActions = [
      el('a', { class: 'btn btn-primary', href: '/projects/' + slug + '/rank' }, 'Rank features'),
      el('a', { class: 'btn', href: '/projects/' + slug + '/ranking' }, 'See the ranking'),
      el('a', { class: 'btn', href: '/projects/' + slug + '/settings' }, 'Settings')
    ];

    return [
      el('div', { class: 'card mb-6' }, [
        el('div', { class: 'pill-row', style: 'margin-top:0.75rem;' }, badges),
        healthBar(p.health_score), newcomerLine(p), meta
      ]),
      nextCard,
      sectionHead('Complaints', 'The problems being solved. ' + intake),
      complaints && complaints.length
        ? el('div', { class: 'grid-responsive-2' }, complaintCards(complaints, slug))
        : emptyState('\u{1F4CC}', 'No complaints filed',
            'Nothing has been reported yet. A feature cannot be ranked until there is a problem for it to solve.'),
      el('div', { class: 'section-head', style: 'margin-top:2rem;' }, [
        el('h2', { class: 'section-title', style: 'font-size:1.25rem;' }, 'Features'),
        el('p', { class: 'section-sub' }, 'Proposed solutions ranked by pairwise comparison.'),
        el('div', { class: 'pill-row' }, featureActions)
      ]),
      features && features.length
        ? el('div', { class: 'grid-responsive-2', id: 'feature-list' }, featureCards)
        : emptyState('\u2728', 'No features yet', 'Features are proposed from validated complaints.'),
      workItemsSection(workItems, p),
      requestSection(requests, p),
      listSection(lists, p),
      capabilitiesPanel(panels.capabilities),
      fieldReportsPanel(panels.fieldReports),
      solutionsPanel(panels.solutions),
      alternativesPanel(panels.alternatives),
      forms(p)
    ];
  }

  window.ConcordProjectRender = { render: render };
})();
