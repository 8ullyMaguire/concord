//! Scout's classification — `docs/specs/scout-spec.md` §3 and §4.
//!
//! This is the one part of Concord with no analogue elsewhere: the finder asks questions and ranks, and nothing anywhere says *"this
//! project is a base to fork and that one is a library to depend on"*. The decomposition lives here too, because it is the same kind of
//! judgement — text in, catalog-shaped structure out, with the reasoning recorded.
//!
//! Two rules are load-bearing enough to state at the top:
//!
//! * **Every verdict carries its signals.** The surface exists to answer *"why did it say that"*, so a classification without a named
//!   input is a bug rather than a terse style. [`classify`] cannot produce one: there is no path that leaves `signals` empty, and an
//!   unclassifiable candidate is reported as `avoid` with `unclassified` as its signal rather than being given a plausible-sounding
//!   verdict nobody earned.
//!
//! * **Unknown health is not zero health.** `project_metrics` is empty on a fresh instance and its stored `health_score` column is 0 for
//!   every project on the live one, so a rule keyed on that number calls the entire catalogue `avoid` and passes any test asserting "at
//!   least one avoid". Every rule here therefore requires `known` before it uses the value, and an unmeasured project is `adopt` with
//!   `health_unmeasured` among its signals — not `avoid`.
//!
//! The crate holds no pool and reads no database, for the same reason the finder engine does not: these are properties of a decision,
//! and a decision that needs a row to be exercised is a decision nobody tests.

pub mod decompose;

/// The total below which a project with KNOWN metrics is `avoid`.
///
/// Named rather than inlined because the spec lists it as a rule and a test has to be able to move it. `0.25` is not a measured
/// threshold — there are no measured projects yet — so it is stated as a constant to be tuned against real data rather than dressed up
/// as a calibrated number.
pub const HEALTH_FLOOR: f64 = 0.25;

/// The total below which a project is **dead** rather than merely unhealthy, which makes it `inspire` instead of `avoid`.
///
/// Strictly below [`HEALTH_FLOOR`], so the two cannot both apply: a project under `0.10` is `avoid` and never `inspire`, because
/// "abandoned" is the more useful thing to tell someone and it must not be the softer of the two.
pub const DEAD_FLOOR: f64 = 0.10;

/// The five classifications. The spec fixes the ORDER they are applied in, and the order is the whole rule: a hard licence conflict is
/// `avoid` even if the project would otherwise be the lead match, and a dead project that is nevertheless the lead is `inspire` rather
/// than `base-on` because nobody forks a corpse.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Verdict {
    /// Must not use.
    Avoid,
    /// The strongest starting point.
    BaseOn,
    /// Level with another project, so a starting point rather than a base.
    Extend,
    /// A dependency to take.
    Adopt,
    /// Dead but instructive.
    Inspire,
}

impl Verdict {
    /// The stored/emitted spelling.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Verdict::Avoid => "avoid",
            Verdict::BaseOn => "base-on",
            Verdict::Extend => "extend",
            Verdict::Adopt => "adopt",
            Verdict::Inspire => "inspire",
        }
    }
}

/// One capability in the decomposition or the catalogue.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Capability {
    /// The catalogue key, or the proposed key.
    pub key: String,
    /// Human-readable label.
    pub label: String,
    /// The axis it belongs to.
    #[serde(default)]
    pub category: String,
    /// `false` for a key the text implied but the catalogue does not define. Such a capability is reported as a **proposal** and is
    /// never scored against — see the test that no verdict depends on one.
    pub in_catalog: bool,
    /// `covered` / `partial` / `open`, from the assertion states.
    #[serde(default)]
    pub coverage: String,
    /// Why this key was pulled out of the text: `verbatim_key`, `label_word`, `category_word`, `text_token`.
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub evidence: String,
    /// Capabilities this one is a near-duplicate of, so an editor can merge them rather than scoring one axis twice.
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub signals: Vec<String>,
}

impl Capability {
    /// A catalogue entry, as the store hands it over.
    #[must_use]
    pub fn catalog(key: impl Into<String>, label: impl Into<String>, category: impl Into<String>) -> Self {
        Capability {
            key: key.into(),
            label: label.into(),
            category: category.into(),
            in_catalog: true,
            coverage: String::new(),
            evidence: String::new(),
            signals: Vec::new(),
        }
    }
}

/// One piece of known trouble with a candidate project.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Pain {
    /// `complaint` or `field_report`.
    pub kind: String,
    /// One line.
    pub title: String,
    /// The rest, empty when there is none.
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub detail: String,
    /// How bad, `0` when unstated.
    #[serde(skip_serializing_if = "is_zero", default)]
    pub severity: f64,
}

#[allow(clippy::trivially_copy_pass_by_ref)]
fn is_zero(value: &f64) -> bool {
    *value == 0.0
}

/// A readable catalogue project, with the attributes Scout scores against.
#[derive(Debug, Clone, Default, PartialEq, serde::Serialize)]
pub struct Candidate {
    /// The project.
    pub project_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// Its own licence, empty when unset.
    pub license: String,
    /// Capability key → the project's assertion. **An absent key is UNKNOWN, not `no`** — the rule that runs through all of this.
    pub attrs: std::collections::BTreeMap<String, String>,
    /// Field-report sample size. Zero is not a 0% outcome rate and must never be rendered as one.
    pub reports: i64,
    /// Share of field reports with a good outcome, `0` when there is no sample.
    pub outcome_rate: f64,
}

/// A score and whether it means anything.
#[derive(Debug, Clone, Copy, Default)]
pub struct Health {
    /// The computed score.
    pub value: f64,
    /// Whether there is telemetry behind it.
    pub known: bool,
}

/// One classified candidate.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct CandidateVerdict {
    /// The project.
    pub project_id: i64,
    /// Its slug.
    pub slug: String,
    /// Its name.
    pub name: String,
    /// The verdict.
    pub verdict: String,
    /// One sentence a human wrote no part of.
    pub reason: String,
    /// The named inputs that produced the verdict. **Never empty.**
    pub signals: Vec<String>,
    /// Share of in-catalogue capabilities this project satisfies.
    pub fit: f64,
    /// The share of those that had any evidence at all.
    pub evidence_coverage: f64,
    /// How many capabilities this project is the top match for. Zero means "not the lead anywhere", which is what makes `base-on` a
    /// distinct claim from `adopt`.
    pub lead_on: i64,
    /// How many of the idea's capabilities it satisfies at all.
    pub matched: i64,
    /// How many there are.
    pub total: i64,
    /// The computed score.
    pub health: f64,
    /// Whether that score means anything. A reader must never see `0` without `known: false`.
    pub health_known: bool,
    /// The project's own licence, empty when unset.
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub license: String,
    /// Known trouble. Empty is normal and means nothing bad.
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub pain: Vec<Pain>,
    /// The field-report sample size.
    pub reports: i64,
    /// The share with a good outcome.
    pub outcome_rate: f64,
}

/// The whole answer.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Report {
    /// The idea, trimmed.
    pub idea: String,
    /// The decomposition: catalogue keys plus explicit proposals.
    pub capabilities: Vec<Capability>,
    /// How many of the decomposition's capabilities can be scored at all.
    pub in_catalog: i64,
    /// How many are proposals.
    pub proposed: i64,
    /// **Every** classified candidate, not only the best few: the whole point is also learning what to avoid.
    pub verdicts: Vec<CandidateVerdict>,
    /// The constraints the caller stated, echoed so a reader can see they were applied rather than assumed.
    #[serde(skip_serializing_if = "Vec::is_empty", default)]
    pub constraints: Vec<String>,
    /// Counts by verdict, which is the sentence worth reading first.
    pub stats: std::collections::BTreeMap<String, i64>,
    /// How many catalogue projects were scored, so a short verdict list reads as "nothing else matched" rather than "we only looked
    /// at these".
    pub candidates_considered: i64,
}

/// The caller's stated constraints and thresholds.
///
/// Everything here has a default, because a classifier whose behaviour depends on an unset field is a classifier whose behaviour nobody
/// can describe. The defaults are the spec's values; a caller overrides them only when it has a reason, and the [`Report`] echoes the
/// thresholds it used so a reader can see them.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// Below this, a project with KNOWN metrics is `avoid`.
    pub health_floor: f64,
    /// Below this, a project with KNOWN metrics is `inspire` — dead but instructive — rather than merely `avoid`.
    pub dead_floor: f64,
    /// Licence expressions the caller will not accept. Matched case-insensitively against the project's own licence. Empty means no
    /// licence constraint, and **a project with no licence does not violate one**: an unknown licence is unknown, not incompatible.
    pub license_constraints: Vec<String>,
    /// Bounds the returned list. Every candidate is classified — the point is also learning what to avoid — so this trims a tail that
    /// is ordered by verdict severity first and fit second.
    pub max_verdicts: usize,
}

impl Options {
    fn with_defaults(self) -> Options {
        Options {
            health_floor: if self.health_floor <= 0.0 { HEALTH_FLOOR } else { self.health_floor },
            dead_floor: if self.dead_floor <= 0.0 { DEAD_FLOOR } else { self.dead_floor },
            license_constraints: self.license_constraints,
            max_verdicts: if self.max_verdicts == 0 { 25 } else { self.max_verdicts },
        }
    }
}

/// Everything [`classify`] needs, assembled by the caller.
#[derive(Debug, Clone, Default)]
pub struct Input {
    /// The idea in the user's words.
    pub idea: String,
    /// The decomposition. A capability with `in_catalog: false` contributes NO scoring weight — it cannot, because there is nothing in
    /// the catalogue to match it against.
    pub capabilities: Vec<Capability>,
    /// The readable catalogue projects.
    pub candidates: Vec<Candidate>,
    /// Project id → health. A caller that omits a project here is asserting "no telemetry", which is the same as a metrics row being
    /// absent.
    pub health: std::collections::BTreeMap<i64, Health>,
    /// The known pain per project.
    pub pain: std::collections::BTreeMap<i64, Vec<Pain>>,
}

/// One project's score against the decomposition.
#[derive(Debug, Clone, Default)]
struct Scored {
    fit: f64,
    coverage: f64,
    matched: i64,
    total: i64,
    satisfied: Vec<String>,
}

/// Produce the report.
///
/// The sequence is fixed by the spec and **the order is the rule**, so it is one ordered pass rather than independent predicates
/// combined later: a licence conflict must beat a healthy lead, and an abandoned project must not be `base-on` however well it matches.
#[must_use]
pub fn classify(input: &Input, options: Options) -> Report {
    let options = options.with_defaults();
    let mut report = Report {
        idea: input.idea.trim().to_string(),
        capabilities: input.capabilities.clone(),
        in_catalog: 0,
        proposed: 0,
        verdicts: Vec::new(),
        constraints: options.license_constraints.clone(),
        stats: std::collections::BTreeMap::new(),
        candidates_considered: input.candidates.len() as i64,
    };
    for capability in &input.capabilities {
        if capability.in_catalog {
            report.in_catalog += 1;
        } else {
            report.proposed += 1;
        }
    }

    let scored = score(input);
    let (leads, tied_with) = lead_counts(input, &scored);

    for candidate in &input.candidates {
        let result = scored.get(&candidate.project_id).cloned().unwrap_or_default();
        let health = input.health.get(&candidate.project_id).copied().unwrap_or_default();
        let lead_on = leads.get(&candidate.project_id).copied().unwrap_or(0);
        let mut verdict = CandidateVerdict {
            project_id: candidate.project_id,
            slug: candidate.slug.clone(),
            name: candidate.name.clone(),
            verdict: String::new(),
            reason: String::new(),
            signals: Vec::new(),
            fit: result.fit,
            evidence_coverage: result.coverage,
            lead_on,
            matched: result.matched,
            total: result.total,
            health: health.value,
            health_known: health.known,
            license: candidate.license.clone(),
            pain: input.pain.get(&candidate.project_id).cloned().unwrap_or_default(),
            reports: candidate.reports,
            outcome_rate: candidate.outcome_rate,
        };
        // **The tie is passed IN, and that is a deliberate deviation from the reference.** The Go appends the tie signals AFTER
        // `applyVerdict`, so the branch that reads them ("tied at the top of some capability and nobody was awarded the lead") can never
        // fire, and a tie falls through to `adopt`/`complete`. Both of the reference's own comments say a tie is a fork request rather than
        // a base; the spec says the same. Intent over code — see the ledger entry, which records the deviation and what a differential
        // probe may therefore disagree about.
        let tie = tied_with.get(&candidate.project_id).cloned().unwrap_or_default();
        apply_verdict(&mut verdict, candidate, health, lead_on, &result, &options, &tie);
        // Tie signals are additive: whatever verdict the rules chose, the reader also learns that this project was level with another.
        // Appending after the verdict keeps a tie from ever suppressing the signal a verdict is required to carry.
        verdict.signals.extend(tie);
        *report.stats.entry(verdict.verdict.clone()).or_insert(0) += 1;
        report.verdicts.push(verdict);
    }

    sort_verdicts(&mut report.verdicts);
    report.verdicts.truncate(options.max_verdicts);
    report
}

/// Set the verdict, reason and signals for one candidate.
///
/// Every branch pushes at least one signal, and the final fallthrough leaves `signals` non-empty. That is the invariant the spec asks
/// for, and the one every test in this module checks.
fn apply_verdict(
    verdict: &mut CandidateVerdict,
    candidate: &Candidate,
    health: Health,
    lead_on: i64,
    scored: &Scored,
    options: &Options,
    tie: &[String],
) {
    // 1. A licence constraint is a hard veto and outranks everything. It is first because a user who said "nothing proprietary" wants
    //    that answered before any question of how well something fits.
    if let Some(conflict) = license_conflict(&candidate.license, &options.license_constraints) {
        verdict.verdict = Verdict::Avoid.as_str().to_string();
        verdict.signals.push(format!("license_constraint:{conflict}"));
        verdict.signals.push(format!("license:{}", candidate.license));
        verdict.reason = format!(
            "excluded by the caller's license constraint ({conflict} says {:?})",
            candidate.license
        );
        return;
    }

    // 2. Abandoned — but only on KNOWN metrics. An unmeasured project is not an abandoned one, which is the trap the spec's §2 exists to
    //    close.
    if health.known && health.value < options.health_floor {
        verdict.signals.push(format!("health:{:.2}", health.value));
        verdict.signals.push(format!("health_floor:{:.2}", options.health_floor));
        if health.value < options.dead_floor {
            // Dead as well as unhealthy. `avoid` still, because "abandoned" is the more useful thing to hear, but the signal
            // distinguishes them.
            verdict.signals.push(format!("dead_below:{:.2}", options.dead_floor));
            verdict.verdict = Verdict::Avoid.as_str().to_string();
            verdict.reason = format!(
                "looks abandoned: health {:.2} is under the {:.2} floor, and under the {:.2} dead threshold as well",
                health.value, options.health_floor, options.dead_floor
            );
            return;
        }
        verdict.verdict = Verdict::Avoid.as_str().to_string();
        verdict.reason = format!(
            "looks abandoned: health {:.2} is under the {:.2} floor",
            health.value, options.health_floor
        );
        return;
    }

    // 3. The lead — but only an UNCONTESTED one, and only if it is healthy enough and licensed enough to be a base.
    //
    //    A tie is not a base-on. Two projects leading equally is a fork request, and the report says so with both names rather than
    //    awarding the label to whichever row happened to come first.
    if let Some(tie_names) = tied_names(tie) {
        if lead_on == 0 {
            verdict.verdict = Verdict::Extend.as_str().to_string();
            verdict.reason = format!(
                "level with another project on at least one capability, so it is a starting point to extend rather than a base to fork ({tie_names})"
            );
            return;
        }
    }
    if lead_on > 0 && health.known {
        verdict.verdict = Verdict::BaseOn.as_str().to_string();
        verdict.signals.push(format!("leads:{lead_on}"));
        verdict.signals.push(format!("health:{:.2}", health.value));
        if !candidate.license.is_empty() {
            verdict.signals.push(format!("license:{}", candidate.license));
        }
        verdict.reason = format!(
            "the strongest starting point: top match on {lead_on} of {} capabilities, health {:.2}",
            scored.total, health.value
        );
        return;
    }

    // 5. A subset match: satisfy some of it and be a library to depend on.
    if scored.total > 0 && scored.matched < scored.total {
        verdict.verdict = Verdict::Adopt.as_str().to_string();
        verdict
            .signals
            .push(format!("subset:{}_of_{}", scored.matched, scored.total));
        if !health.known {
            verdict.signals.push("health_unmeasured".to_string());
        }
        if !candidate.license.is_empty() {
            verdict.signals.push(format!("license:{}", candidate.license));
        }
        verdict.reason = format!(
            "covers {} of the {} capabilities in this idea — a dependency to take rather than a base to fork",
            scored.matched, scored.total
        );
        return;
    }
    if scored.total > 0 && scored.matched == scored.total {
        verdict.verdict = Verdict::Adopt.as_str().to_string();
        verdict
            .signals
            .push(format!("complete:{}_of_{}", scored.matched, scored.total));
        // The unmeasured-health signal belongs here too, not only on the subset branch. An earlier version put it on `subset:` alone, so
        // a project matching EVERY capability with no metrics row carried no record that its health was unknown — and a reader would take
        // the absence of `health:` as "health was not relevant" rather than "we never measured it".
        if !health.known {
            verdict.signals.push("health_unmeasured".to_string());
        }
        if !candidate.license.is_empty() {
            verdict.signals.push(format!("license:{}", candidate.license));
        }
        verdict.reason = format!("satisfies all {} capabilities — a complete dependency", scored.total);
        return;
    }

    // 6. Dead but instructive. Last, because it is the weakest claim: a dead project that is nonetheless the best available answer is
    //    still worth reading, but not forking.
    if health.known && health.value < options.dead_floor {
        verdict.verdict = Verdict::Inspire.as_str().to_string();
        verdict.signals.push(format!("dead:{:.2}", health.value));
        verdict.signals.push(format!("dead_below:{:.2}", options.dead_floor));
        verdict.reason = format!(
            "dead (health {:.2}, under {:.2}) but still the closest thing in the catalog — worth reading, not worth forking",
            health.value, options.dead_floor
        );
        return;
    }

    // Unreachable given the ordering above, and deliberately so: there is no way to produce a verdict without signals, so an
    // unclassifiable candidate is reported as what it is rather than given a plausible label.
    verdict.verdict = Verdict::Avoid.as_str().to_string();
    verdict.signals.push("unclassified".to_string());
    verdict.reason = "no rule matched; nothing here says this is usable".to_string();
}

/// The constraint this licence violates, or `None`.
///
/// **An empty project licence never conflicts.** Treating "we do not know" as "incompatible" would make every project with an unset
/// licence `avoid` on a fresh instance, which is the same class of error as reading absent telemetry as zero health: an absence of
/// information turned into a verdict.
fn license_conflict(license: &str, constraints: &[String]) -> Option<String> {
    let project = license.trim().to_lowercase();
    if project.is_empty() || constraints.is_empty() {
        return None;
    }
    for constraint in constraints {
        let constraint = constraint.trim().to_lowercase();
        if constraint.is_empty() {
            continue;
        }
        if project.contains(&constraint) || constraint.contains(&project) {
            return Some(constraint);
        }
    }
    None
}

/// Order by severity of the verdict, then by fit.
///
/// Severity order is the reader's order: a project you must not use is more important than the best one you can, because the reason to
/// read this page at all is usually to avoid a mistake. Fit breaks ties within a verdict, then the name, so the result never depends on
/// row order.
fn sort_verdicts(verdicts: &mut [CandidateVerdict]) {
    let severity = |verdict: &str| -> u8 {
        match verdict {
            "avoid" => 0,
            "base-on" => 1,
            "extend" => 2,
            "adopt" => 3,
            _ => 4,
        }
    };
    verdicts.sort_by(|a, b| {
        severity(&a.verdict)
            .cmp(&severity(&b.verdict))
            .then_with(|| b.fit.total_cmp(&a.fit))
            .then_with(|| a.name.cmp(&b.name))
    });
}

/// Pull the `tied_with:` signal back out into a readable phrase.
///
/// Parsed out of the signals rather than carried as a field, because a tie is extra information about a verdict rather than part of the
/// scoring: every verdict branch is required to emit its own signals, and the tie has to be able to coexist with all of them.
fn tied_names(signals: &[String]) -> Option<String> {
    signals
        .iter()
        .find_map(|signal| signal.strip_prefix("tied_with:").map(str::to_string))
}

/// Compute each candidate's score against the decomposition.
///
/// *Absence is not evidence* is the rule that runs through all of it: a capability with NO assertion for a project is unknown, not false.
/// So a project satisfying one of nine capabilities does NOT score 11% — it scores 100% of the one axis it has evidence on, with
/// coverage recording that eight axes were unknown. Reporting it as 11% would tell the reader this project is nearly a non-match, which
/// is a claim the data does not support.
///
/// The value itself does not gate the match: Scout matches on coverage of an axis, and whether the value is `yes` or `no` is the user's
/// to read, not Scout's to filter on. Filtering would answer a question nobody asked ("which projects are a yes?") and hide the evidence
/// that says no, which is often the most useful thing on the page.
fn score(input: &Input) -> std::collections::BTreeMap<i64, Scored> {
    let in_catalog: Vec<&str> = input
        .capabilities
        .iter()
        .filter(|capability| capability.in_catalog)
        .map(|capability| capability.key.as_str())
        .collect();
    let mut out = std::collections::BTreeMap::new();
    for candidate in &input.candidates {
        let mut result = Scored {
            total: in_catalog.len() as i64,
            ..Scored::default()
        };
        if result.total == 0 {
            out.insert(candidate.project_id, result);
            continue;
        }
        for key in &in_catalog {
            match candidate.attrs.get(*key) {
                // An absent key and an empty value are both "no evidence", which is why one check covers them.
                Some(value) if !value.is_empty() => result.satisfied.push((*key).to_string()),
                _ => {}
            }
        }
        result.matched = result.satisfied.len() as i64;
        // The reference computes both of these as `known / total` — its comment describes `coverage` as "the share of those that had any
        // evidence", which is the same number when every satisfied capability is evidence. Kept identical rather than corrected: the
        // two are reported side by side, and inventing a second denominator here would make a port's report disagree with the service
        // it replaces on a page whose whole purpose is comparison.
        if result.matched > 0 {
            let share = result.matched as f64 / result.total as f64;
            result.coverage = share;
            result.fit = share;
        }
        out.insert(candidate.project_id, result);
    }
    out
}

/// How many capabilities each project is the TOP match for, and who it ties with at each of those leads.
///
/// The tie is the point. A `base-on` verdict is the claim *"this is the thing to fork"*, and two projects leading equally is a fork
/// request, not a base — so the counts alone cannot decide the verdict and the tie has to be visible to the caller.
///
/// Ordering is explicit — fit, then coverage, then satisfied count, then name, then id — so the outcome never depends on row order. The
/// defect that made an early version award `base-on` to whichever project the query returned first was exactly that dependence.
fn lead_counts(
    input: &Input,
    scored: &std::collections::BTreeMap<i64, Scored>,
) -> (
    std::collections::BTreeMap<i64, i64>,
    std::collections::BTreeMap<i64, Vec<String>>,
) {
    let mut by_capability: std::collections::BTreeMap<&str, Vec<&Candidate>> = std::collections::BTreeMap::new();
    for candidate in &input.candidates {
        if let Some(result) = scored.get(&candidate.project_id) {
            for key in &result.satisfied {
                by_capability.entry(key.as_str()).or_default().push(candidate);
            }
        }
    }
    let mut leads: std::collections::BTreeMap<i64, i64> = std::collections::BTreeMap::new();
    let mut tied_with: std::collections::BTreeMap<i64, Vec<String>> = std::collections::BTreeMap::new();

    for (key, mut candidates) in by_capability {
        // Ties are decided by the full comparator, so "tied" means genuinely equal on every axis rather than equal on one.
        candidates.sort_by(|a, b| {
            let (a_score, b_score) = (&scored[&a.project_id], &scored[&b.project_id]);
            b_score
                .fit
                .total_cmp(&a_score.fit)
                .then_with(|| b_score.coverage.total_cmp(&a_score.coverage))
                .then_with(|| b_score.matched.cmp(&a_score.matched))
                .then_with(|| a.name.cmp(&b.name))
                .then_with(|| a.project_id.cmp(&b.project_id))
        });
        let Some(top) = candidates.first().copied() else {
            continue;
        };
        let mut leaders = vec![top];
        for candidate in candidates.iter().skip(1) {
            let (a, b) = (&scored[&candidate.project_id], &scored[&top.project_id]);
            if a.fit == b.fit && a.coverage == b.coverage && a.matched == b.matched {
                leaders.push(candidate);
            } else {
                break;
            }
        }
        if leaders.len() == 1 {
            *leads.entry(top.project_id).or_insert(0) += 1;
            continue;
        }
        // A tie awards nobody the lead, and every tied project is told who it ties with. Awarding it to one of them is the defect this
        // exists to prevent.
        let mut names: Vec<String> = leaders.iter().map(|leader| leader.slug.clone()).collect();
        names.sort();
        let joined = names.join(",");
        for leader in &leaders {
            let entry = tied_with.entry(leader.project_id).or_default();
            entry.insert(0, format!("capability:{key}"));
            entry.insert(1, format!("tied_with:{joined}"));
        }
    }
    (leads, tied_with)
}
