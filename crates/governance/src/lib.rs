//! Concord's collective-decision rules (spec §4–§6), ported from `internal/governance`.
//!
//! Pure logic only — SQL and HTTP live in `concord_db` and the router. Ported rather than
//! re-derived, and the tests are the Go suite's fixtures, because every one of them was written
//! against a bug this code has already had:
//!
//! * **a stand-aside is a reservation, not opposition.** `Decisive()` is `consent + block`. Counting
//!   stand-asides there made 4 consent / 3 stand-aside / 0 block read 4/7 = 0.57 and fail a 0.7
//!   threshold, so the people who withheld consent *because* they had concerns were the ones who
//!   sank the proposal.
//! * **an empty room is not unanimous.** `SupportRatio` and `DecisiveRatio` return 0 on a zero
//!   denominator, not 1. A mutation found this in the Go tree; the fixture is carried across.
//! * **the override is a second window, not a first-window shortcut.** `EvaluateConsensus` never
//!   returns `AcceptedOverridden`; `EvaluateOverride` is the second ballot and it divides by
//!   non-abstain participants, not by the decisive denominator. That difference is the whole point:
//!   a room of 5 consent / 6 stand-aside / 1 block reads 5/6 = 0.83 decisively and would override,
//!   while its support of 5/12 = 0.42 is below the floor an ordinary call must clear — an override
//!   would be easier than a pass.

/// A member's role. Higher ranks outrank lower ones.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Role {
    /// No vote.
    Guest,
    /// The default.
    User,
    /// Has contributed.
    Contributor,
    /// May review.
    Reviewer,
    /// May maintain.
    Maintainer,
    /// Owns the project.
    Owner,
}

impl Role {
    /// The numeric rank; higher outranks lower.
    #[must_use]
    pub fn level(self) -> i32 {
        match self {
            Self::Guest => 0,
            Self::User => 1,
            Self::Contributor => 2,
            Self::Reviewer => 3,
            Self::Maintainer => 4,
            Self::Owner => 5,
        }
    }

    /// Whether this role carries at least `min`'s rank.
    #[must_use]
    pub fn at_least(self, min: Self) -> bool {
        self.level() >= min.level()
    }
}

/// The project's decision regime.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GovernanceModel {
    /// Collaborators decide direction; no role steers alone.
    Collective,
    /// Maintainers may accept features and bypass the merge quorum; every bypass is logged.
    MaintainerLed,
}

impl GovernanceModel {
    /// `None` for an unknown model — the baseline's `charters` row is the source of truth and a
    /// typo there must be a refusal, not a silent switch to the default.
    #[must_use]
    pub fn parse(value: &str) -> Option<Self> {
        match value {
            "collective" => Some(Self::Collective),
            "maintainer_led" => Some(Self::MaintainerLed),
            _ => None,
        }
    }

    /// The stored spelling.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Collective => "collective",
            Self::MaintainerLed => "maintainer_led",
        }
    }
}

/// A project's decision thresholds (§5.5, §6.3, §8).
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Charter {
    /// Share of eligible collaborators needed for quorum.
    pub quorum_ratio: f64,
    /// ... but never fewer than this many.
    pub quorum_min: i64,
    /// Consent over all substantive positions.
    pub support_ratio_min: f64,
    /// Consent over the positions that took a side — the decisive bar.
    pub consent_ratio: f64,
    /// Consent over **non-abstain participants**, for the override window.
    pub override_ratio: f64,
    /// How long a call's window is.
    pub vote_window_days: f64,
    /// Whether merging needs a quorum at all.
    pub merge_requires_quorum: bool,
    /// The merge quorum's floor.
    pub merge_quorum_min: i64,
    /// The merge quorum's ratio.
    pub merge_quorum_ratio: f64,
    /// Whether a reviewer-level technical approval is required to merge.
    pub require_reviewer_approval: bool,
    /// WIP limit for `in_progress`.
    pub wip_in_progress: i64,
    /// WIP limit for `review`.
    pub wip_review: i64,
    /// The `λ` on the pain term.
    pub lam: f64,
    /// The `μ` on the weight term.
    pub mu: f64,
    /// How long a strategic theme lives unless renewed.
    pub theme_lifetime_days: f64,
    /// How fast a complaint's pain decays.
    pub pain_halflife_days: f64,
    /// How fast reputation decays.
    pub rep_halflife_days: f64,
    /// Glicko-2's τ.
    pub glicko_tau: f64,
    /// The ceiling on a voter's weight.
    pub vote_weight_cap: f64,
    /// How many distinct voters a solution call needs.
    pub solution_call_min_voters: i64,
    /// The Glicko-2 win probability a solution call needs over the runner-up **and** the baseline.
    pub solution_call_confidence: f64,
    /// How long the leading position must have held.
    pub solution_stable_hours: f64,
}

impl Charter {
    /// The spec's defaults, adjusted per governance model — a maintainer-led project may merge
    /// without quorum, and can re-enable it.
    #[must_use]
    pub fn default_for(model: GovernanceModel) -> Self {
        Self {
            quorum_ratio: 0.2,
            quorum_min: 3,
            // §6.6: support >= 0.50 and decisive >= 0.70. The support floor is what a consent ratio
            // alone cannot express — a call can clear 70% of the decisive positions while being
            // carried by a handful of people against a room full of reservations.
            consent_ratio: 0.7,
            support_ratio_min: 0.5,
            override_ratio: 0.8,
            vote_window_days: 7.0,
            merge_requires_quorum: model == GovernanceModel::Collective,
            merge_quorum_min: 2,
            merge_quorum_ratio: 0.25,
            solution_call_min_voters: 3,
            solution_call_confidence: 0.80,
            solution_stable_hours: 72.0,
            require_reviewer_approval: true,
            wip_in_progress: 3,
            wip_review: 4,
            lam: 20.0,
            mu: 100.0,
            theme_lifetime_days: 90.0,
            pain_halflife_days: 90.0,
            rep_halflife_days: 180.0,
            glicko_tau: 0.5,
            vote_weight_cap: 3.0,
        }
    }

    /// The WIP limit for a phase; 0 means unlimited.
    #[must_use]
    pub fn wip_for(&self, phase: &str) -> i64 {
        match phase {
            "in_progress" => self.wip_in_progress,
            "review" => self.wip_review,
            _ => 0,
        }
    }
}

/// `max(quorum_min, ceil(ratio·eligible))`, capped by the eligible count; 0 when nobody is eligible.
#[must_use]
pub fn quorum_threshold(eligible: i64, charter: &Charter) -> i64 {
    if eligible <= 0 {
        return 0;
    }
    let need = charter
        .quorum_min
        .max((charter.quorum_ratio * eligible as f64).ceil() as i64);
    eligible.min(need)
}

/// [`quorum_threshold`] with the merge settings.
#[must_use]
pub fn merge_quorum_threshold(eligible: i64, charter: &Charter) -> i64 {
    if eligible <= 0 {
        return 0;
    }
    let need = charter
        .merge_quorum_min
        .max((charter.merge_quorum_ratio * eligible as f64).ceil() as i64);
    eligible.min(need)
}

/// A consensus-call stance (§5.5).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Stance {
    /// In favour.
    Consent,
    /// Neutral.
    Abstain,
    /// Has concerns, will not block.
    StandAside,
    /// Opposed.
    Block,
}

impl Stance {
    /// `None` for anything that is not a stance.
    #[must_use]
    pub fn parse(value: &str) -> Option<Self> {
        match value {
            "consent" => Some(Self::Consent),
            "abstain" => Some(Self::Abstain),
            "stand_aside" => Some(Self::StandAside),
            "block" => Some(Self::Block),
            _ => None,
        }
    }

    /// The stored spelling.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Consent => "consent",
            Self::Abstain => "abstain",
            Self::StandAside => "stand_aside",
            Self::Block => "block",
        }
    }
}

/// A call's cast positions, with the two denominators a verdict needs.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ConsensusCounts {
    /// Consents.
    pub consent: i64,
    /// Abstentions.
    pub abstain: i64,
    /// Reservations.
    pub stand_aside: i64,
    /// Blocks.
    pub block: i64,
    /// Distinct position rows.
    pub participants: i64,
    /// Contributor-and-above members.
    pub eligible: i64,
    /// Objections still open.
    pub open_objections: i64,
}

impl ConsensusCounts {
    /// `consent + stand_aside + block` — participation, reservations included.
    #[must_use]
    pub fn non_abstain(&self) -> i64 {
        self.consent + self.stand_aside + self.block
    }

    /// `consent + block` — the denominator of the decisive ratio.
    ///
    /// A stand-aside is a reservation, not opposition: §6.6 defines it as "I have concerns, but I
    /// will not block". Counting it here made a stand-aside vote *against* the proposal, which is the
    /// opposite of what the position means.
    #[must_use]
    pub fn decisive(&self) -> i64 {
        self.consent + self.block
    }

    /// `consent / non_abstain`, 0 on an empty room.
    ///
    /// Zero, not one: an empty room is not unanimous. A mutation found this in the Go tree — the
    /// 0/0 branch returned 1 and an empty call read as a unanimous pass.
    #[must_use]
    pub fn support_ratio(&self) -> f64 {
        match self.non_abstain() {
            0 => 0.0,
            n => self.consent as f64 / n as f64,
        }
    }

    /// `consent / (consent + block)`, 0 on an empty room.
    #[must_use]
    pub fn decisive_ratio(&self) -> f64 {
        match self.decisive() {
            0 => 0.0,
            n => self.consent as f64 / n as f64,
        }
    }

    /// More reservations than consents: the decision passes but has not actually been agreed.
    #[must_use]
    pub fn reluctant(&self) -> bool {
        self.stand_aside > self.consent
    }
}

/// The outcome of a consensus call.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Result {
    /// Cleared both thresholds with no open block.
    Accepted,
    /// Reached the decisive bar after an override window.
    AcceptedOverridden,
    /// Below a threshold.
    Rejected,
    /// An unresolved objection.
    Blocked,
    /// Not enough participants for the room to speak.
    InsufficientQuorum,
    /// The override window's own verdict: the block was set aside.
    Overridden,
    /// The override window's own verdict: the block stands.
    Upheld,
}

impl Result {
    /// The stored spelling.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Accepted => "accepted",
            Self::AcceptedOverridden => "accepted_overridden",
            Self::Rejected => "rejected",
            Self::Blocked => "blocked",
            Self::InsufficientQuorum => "insufficient_quorum",
            Self::Overridden => "overridden",
            Self::Upheld => "upheld",
        }
    }
}

/// §6.6's decision rule, with no side effects.
///
/// An unresolved block is **never** overridden here: §6.6's pass conditions end with "no unresolved
/// block", and its override is "a second window", evaluated by [`evaluate_override`]. Until the
/// seventh pass the Go function compared the decisive ratio against the override ratio and returned
/// `accepted_overridden` from a first-window close, which made the second window redundant and the
/// override easier than an ordinary pass.
#[must_use]
pub fn evaluate_consensus(counts: &ConsensusCounts, charter: &Charter) -> Result {
    if counts.participants < quorum_threshold(counts.eligible, charter) {
        return Result::InsufficientQuorum;
    }
    if counts.open_objections > 0 {
        return Result::Blocked;
    }
    if counts.support_ratio() < charter.support_ratio_min {
        return Result::Rejected;
    }
    if counts.decisive_ratio() >= charter.consent_ratio {
        return Result::Accepted;
    }
    Result::Rejected
}

/// §6.6's override: `consent / (consent + stand_aside + block) >= override_ratio`, applied to the
/// **second** window's counts.
///
/// The denominator is every non-abstain participant — stand-asides included. The alternative
/// (the decisive denominator) is incoherent rather than merely different: with 5 consent, 6
/// stand-aside and 1 block, consent/(consent+block) reads 0.83 and would override a block while the
/// support ratio of 5/12 = 0.42 is below the floor an ordinary call must clear. An override would be
/// easier than a pass. The consequence is that a reluctant consensus can never override anything,
/// which is the point of §6.6 having two thresholds.
#[must_use]
pub fn evaluate_override(counts: &ConsensusCounts, charter: &Charter) -> Result {
    if counts.participants < quorum_threshold(counts.eligible, charter) {
        return Result::InsufficientQuorum;
    }
    if counts.support_ratio() >= charter.override_ratio {
        return Result::Overridden;
    }
    Result::Upheld
}

/// The merge-confirmation verdict (§5.7).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct MergeGate {
    /// Confirming collaborators.
    pub approvals: i64,
    /// How many were needed; 0 when the charter disabled the quorum.
    pub quorum_needed: i64,
    /// Whether the quorum is met.
    pub quorum_ok: bool,
    /// Whether a reviewer-level approval is present or not required.
    pub reviewer_ok: bool,
    /// Whether the charter requires a merge quorum at all.
    pub merge_requires_quorum: bool,
}

impl MergeGate {
    /// Whether the merge may proceed.
    #[must_use]
    pub fn passed(&self) -> bool {
        self.quorum_ok && self.reviewer_ok
    }
}

/// Distinct collaborator confirmations ≥ [`merge_quorum_threshold`] (unless the charter disabled the
/// quorum) plus, when required, at least one reviewer-or-above technical approval.
#[must_use]
pub fn check_merge_gate(
    approvals: i64,
    eligible: i64,
    reviewer_approval: bool,
    charter: &Charter,
) -> MergeGate {
    let needs_quorum = charter.merge_requires_quorum;
    let needed = if needs_quorum {
        merge_quorum_threshold(eligible, charter)
    } else {
        0
    };
    MergeGate {
        approvals,
        quorum_needed: needed,
        quorum_ok: !needs_quorum || approvals >= needed,
        reviewer_ok: !charter.require_reviewer_approval || reviewer_approval,
        merge_requires_quorum: needs_quorum,
    }
}

/// The fixed kanban phases, in advance order (§5.6).
pub const BOARD_PHASES: [&str; 9] = [
    "inbox",
    "triaged",
    "solution_draft",
    "consensus",
    "ready",
    "in_progress",
    "review",
    "done",
    "rejected",
];

/// How far an unmet quorum may push a call's deadline (§5.5: "silence cannot decide").
///
/// Silence not deciding is a rule about *evaluation*, not about eternity. With no cap, a call nobody
/// joins stays open forever and accumulates a board of zombies; with a cap, a call that still has no
/// quorum when the window runs out expires — and expiry is deliberately distinct from rejection.
/// Nobody voted against it; it simply ran out of attention.
pub const MAX_WINDOW_EXTENSIONS: i64 = 2;

/// A consensus call's open/close bookkeeping.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Deadline {
    /// When the call opened.
    pub opens_at: f64,
    /// When it closes.
    pub closes_at: f64,
    /// How many extensions have been applied.
    pub extensions: i64,
}

impl Deadline {
    /// Whether the call is past its final deadline and can no longer be extended.
    ///
    /// A call inside its window, or one that has used every extension and still has time left,
    /// returns false — running out of extensions freezes the deadline rather than expiring the call
    /// on the spot. A frozen call can still be closed by an explicit decision; an expired one is over.
    #[must_use]
    pub fn expired(&self, now: f64) -> bool {
        if now < self.closes_at {
            return false;
        }
        self.extensions >= MAX_WINDOW_EXTENSIONS
    }

    /// Whether an unmet-quorum call may still push its deadline.
    #[must_use]
    pub fn extendable(&self) -> bool {
        self.extensions < MAX_WINDOW_EXTENSIONS
    }

    /// One window-length later, or unchanged and `false` when no extension remains.
    ///
    /// The window length is the original `opens_at → closes_at` span, so extending does not silently
    /// shorten the window on a call created with a custom duration.
    #[must_use]
    pub fn extend(&self) -> (Self, bool) {
        if !self.extendable() {
            return (*self, false);
        }
        let span = if self.closes_at - self.opens_at <= 0.0 {
            86_400.0 // a degenerate window still gets a day, not zero
        } else {
            self.closes_at - self.opens_at
        };
        (
            Self {
                opens_at: self.opens_at,
                closes_at: self.closes_at + span,
                extensions: self.extensions + 1,
            },
            true,
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn charter() -> Charter {
        // quorum min 3, ratio 0.2; consent 0.7; support 0.5; override 0.8.
        Charter::default_for(GovernanceModel::Collective)
    }

    #[test]
    fn roles_rank_and_compare() {
        assert!(Role::Owner.at_least(Role::Maintainer));
        assert!(Role::Reviewer.at_least(Role::Reviewer));
        assert!(!Role::Contributor.at_least(Role::Reviewer));
        assert!(!Role::Guest.at_least(Role::User));
        assert_eq!(Role::Owner.level(), 5);
        assert_eq!(GovernanceModel::parse("nonsense"), None);
        assert_eq!(GovernanceModel::parse("maintainer_led"), Some(GovernanceModel::MaintainerLed));
    }

    #[test]
    fn the_quorum_floor_wins_over_a_small_ratio() {
        let c = charter();
        for (eligible, want) in [(0, 0), (3, 3), (4, 3), (10, 3), (20, 4), (100, 20)] {
            assert_eq!(
                quorum_threshold(eligible, &c),
                want,
                "quorum for {eligible} eligible"
            );
        }
        // A merge quorum has its own floor and ratio.
        assert_eq!(merge_quorum_threshold(0, &c), 0);
        assert_eq!(merge_quorum_threshold(4, &c), 2);
        assert_eq!(merge_quorum_threshold(20, &c), 5);
    }

    #[test]
    fn a_call_is_accepted_only_with_quorum_no_block_and_both_thresholds() {
        let c = charter();
        let accepted = ConsensusCounts { consent: 5, abstain: 1, participants: 6, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_consensus(&accepted, &c), Result::Accepted);
        // Unanimous consent without quorum is still not a decision.
        let tiny = ConsensusCounts { consent: 2, participants: 2, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_consensus(&tiny, &c), Result::InsufficientQuorum);
        // One open objection blocks regardless of the ratios.
        let blocked = ConsensusCounts { consent: 9, participants: 10, eligible: 10, open_objections: 1, ..Default::default() };
        assert_eq!(evaluate_consensus(&blocked, &c), Result::Blocked);
        // The support floor catches a thin majority: decisive 3/3 = 1.0 passes, support
        // 3/(3+5) = 0.375 fails. This is the case a single consent ratio cannot express, and the
        // reason `support_ratio_min` exists. Note 4 consent / 3 stand-aside / 1 block is NOT this
        // case: support 4/8 = 0.50 exactly clears the floor and the call is accepted.
        let reservations = ConsensusCounts {
            consent: 3, stand_aside: 5, participants: 8, eligible: 10, ..Default::default()
        };
        assert_eq!(evaluate_consensus(&reservations, &c), Result::Rejected);
        let exactly_half = ConsensusCounts {
            consent: 4, stand_aside: 3, block: 1, participants: 8, eligible: 10, ..Default::default()
        };
        assert_eq!(evaluate_consensus(&exactly_half, &c), Result::Accepted);
        // Stand-asides do not count against consent: 2 consent / 1 stand-aside is decisive 2/2.
        let one_reservation = ConsensusCounts { consent: 2, stand_aside: 1, participants: 4, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_consensus(&one_reservation, &c), Result::Accepted);
        // Abstains count toward quorum but not toward either ratio.
        let abstained = ConsensusCounts { consent: 3, abstain: 3, participants: 6, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_consensus(&abstained, &c), Result::Accepted);
    }

    #[test]
    fn the_first_window_never_overrides_a_block() {
        let c = charter();
        // 8 consent / 1 stand-aside with an open objection: the override ratio would carry this,
        // and the first window must still say blocked. Otherwise the second window is decorative.
        let counts = ConsensusCounts {
            consent: 8, stand_aside: 1, participants: 9, eligible: 10, open_objections: 1, ..Default::default()
        };
        assert_eq!(evaluate_consensus(&counts, &c), Result::Blocked);
        assert_eq!(evaluate_override(&counts, &c), Result::Overridden);
    }

    #[test]
    fn the_override_divides_by_non_abstain_participants() {
        let c = charter();
        // The discriminating fixture: 4 consent / 3 stand-aside / 1 block. Non-abstain 4/8 = 0.50,
        // decisive 4/5 = 0.80 — the only shape here where the two candidate denominators disagree.
        let counts = ConsensusCounts {
            consent: 4, stand_aside: 3, block: 1, participants: 8, eligible: 10, open_objections: 1,
            ..Default::default()
        };
        assert!((counts.support_ratio() - 0.5).abs() < 1e-12);
        assert!((counts.decisive_ratio() - 0.8).abs() < 1e-12);
        assert_eq!(evaluate_override(&counts, &c), Result::Upheld);

        // Two reservations are exactly 0.80 and carry; one block on top is 0.727 and does not.
        let exactly = ConsensusCounts { consent: 8, stand_aside: 2, participants: 10, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_override(&exactly, &c), Result::Overridden);
        let short = ConsensusCounts { consent: 8, stand_aside: 2, block: 1, participants: 11, eligible: 11, ..Default::default() };
        assert_eq!(evaluate_override(&short, &c), Result::Upheld);

        // Abstains are the only position outside the denominator: 4 consent / 6 abstain is 4/4 = 1.0.
        let abstained = ConsensusCounts { consent: 4, abstain: 6, participants: 10, eligible: 10, open_objections: 1, ..Default::default() };
        assert_eq!(evaluate_override(&abstained, &c), Result::Overridden);

        // A reluctant consensus can never override: more reservations than consents puts the ratio
        // under a half, below any threshold above it.
        let reluctant = ConsensusCounts { consent: 5, stand_aside: 6, block: 1, participants: 12, eligible: 12, open_objections: 1, ..Default::default() };
        assert!(reluctant.reluctant());
        assert_eq!(evaluate_override(&reluctant, &c), Result::Upheld);

        // The second window needs quorum like the first.
        let tiny = ConsensusCounts { consent: 2, participants: 2, eligible: 10, ..Default::default() };
        assert_eq!(evaluate_override(&tiny, &c), Result::InsufficientQuorum);
    }

    #[test]
    fn an_empty_room_is_not_unanimous() {
        let empty = ConsensusCounts::default();
        assert_eq!(empty.support_ratio(), 0.0, "0/0 must be 0, not a unanimous 1");
        assert_eq!(empty.decisive_ratio(), 0.0);
        // And the verdict an empty room gets, measured rather than assumed: with nobody eligible the
        // quorum threshold is 0, so the quorum branch does not fire and the support floor rejects it.
        // `insufficient_quorum` would read better and would be a different answer from the service
        // this replaces, so the port keeps `rejected` and this test pins it.
        assert_eq!(evaluate_consensus(&empty, &charter()), Result::Rejected);
        assert_eq!(quorum_threshold(0, &charter()), 0, "an empty room has no threshold to miss");
    }

    #[test]
    fn the_merge_gate_needs_a_quorum_and_a_reviewer() {
        let c = charter();
        let no_approvals = check_merge_gate(0, 10, false, &c);
        assert!(!no_approvals.passed());
        assert_eq!(no_approvals.quorum_needed, 3, "max(2, ceil(0.25·10))");
        let approved = check_merge_gate(3, 10, true, &c);
        assert!(approved.passed());
        // Quorum but no reviewer is still a refusal while the charter requires one.
        assert!(!check_merge_gate(3, 10, false, &c).passed());
        // A maintainer-led project merges without a quorum, and still needs the reviewer.
        let led = Charter::default_for(GovernanceModel::MaintainerLed);
        assert!(!led.merge_requires_quorum);
        let bypass = check_merge_gate(0, 10, true, &led);
        assert!(bypass.passed());
        assert_eq!(bypass.quorum_needed, 0);
        assert!(check_merge_gate(0, 10, true, &led).merge_requires_quorum == false);
    }

    #[test]
    fn a_deadline_freezes_then_expires() {
        let start = Deadline { opens_at: 0.0, closes_at: 100.0, extensions: 0 };
        assert!(!start.expired(50.0));
        assert!(start.extendable());
        let (once, ok) = start.extend();
        assert!(ok);
        assert_eq!(once.closes_at, 200.0);
        assert_eq!(once.extensions, 1);
        let (twice, ok) = once.extend();
        assert!(ok);
        // 400, not 300: `span` is recomputed from the deadline being extended, so the window doubles
        // on each extension rather than growing by the original length. That is what the Go
        // `Deadline.Extend` does — its comment says "the window length is the original
        // opens_at→closes_at span", which is the span *of the receiver* — and it is carried across
        // unchanged rather than quietly corrected, because the two services have to agree on when a
        // call closes.
        assert_eq!(twice.closes_at, 400.0);
        assert!(!twice.extendable());
        // Used up but still inside the window: frozen, not expired.
        assert!(!twice.expired(250.0));
        assert!(!twice.expired(399.0));
        // Past the deadline with no extensions left: over.
        assert!(twice.expired(400.0));
        let (same, ok) = twice.extend();
        assert!(!ok);
        assert_eq!(same, twice);
        // A degenerate window still gets a day rather than zero.
        let degenerate = Deadline { opens_at: 0.0, closes_at: 0.0, extensions: 0 };
        assert_eq!(degenerate.extend().0.closes_at, 86_400.0);
    }

    #[test]
    fn the_board_phases_are_in_advance_order_and_carry_the_wip_limits() {
        let c = charter();
        assert_eq!(BOARD_PHASES[0], "inbox");
        assert_eq!(BOARD_PHASES[BOARD_PHASES.len() - 1], "rejected");
        assert_eq!(c.wip_for("in_progress"), 3);
        assert_eq!(c.wip_for("review"), 4);
        assert_eq!(c.wip_for("inbox"), 0, "unlimited phases say zero");
        assert_eq!(Stance::parse("stand_aside"), Some(Stance::StandAside));
        assert_eq!(Stance::parse("nope"), None);
    }
}
