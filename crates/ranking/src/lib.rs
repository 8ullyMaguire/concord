//! Concord's priority mathematics (spec §6), ported from `internal/ranking`.
//!
//! Pure functions only, no SQL, exactly as the Go package: the queries live in `concord_db`. Two of
//! these are load-bearing in a way that is easy to lose in a port, and both are tested against the
//! numbers rather than against themselves:
//!
//! * [`rate`] is Glicko-2, and the Illinois root-finder has a subtlety the published paper does not
//!   spell out — the chord must be anchored between `A` and `B`, not at `a`, or the iteration
//!   degenerates when `A == a` and never converges. [`tests::the_published_worked_example`] is the
//!   Glickman example from the paper, so a port that gets this wrong fails on numbers, not on style.
//! * [`apply_pairwise_vote`] scales the *whole* update by the voter's weight, RD and volatility
//!   included. The original sentence was "the RD/volatility update is always standard", and §30
//!   superseded it with a measurement: at weight 3e-6 a vote moved the rating by 5e-4 and still
//!   collapsed RD from 350 to 290, and priority is scored on `r - 2·RD`, so a brand-new account moved
//!   the number that ranks by 119 points while looking like it had done nothing.

pub mod composite;
pub mod health;

use std::f64::consts::PI;

/// Glicko-2's scale factor: `r` lives in rating points, `mu` in the paper's units.
pub const SCALE: f64 = 173.7178;

/// The default rating an account or feature starts with.
pub const DEFAULT_R: f64 = 1500.0;

/// The maximum rating deviation — `r - 2·RD` at the cap is the floor of the confidence bound.
pub const MAX_RD: f64 = 350.0;

/// The default starting deviation.
pub const DEFAULT_RD: f64 = 350.0;

/// The default starting volatility.
pub const DEFAULT_VOL: f64 = 0.06;

/// How many days a new account ramps from zero to full vote weight (Sybil defence, §12.3).
pub const ACCOUNT_AGE_GATE_DAYS: f64 = 7.0;

/// Glickman's dampening function.
fn g(phi: f64) -> f64 {
    1.0 / (1.0 + 3.0 * phi * phi / (PI * PI)).sqrt()
}

/// One opponent result within a rating period.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Game {
    /// The opponent's rating.
    pub opponent_r: f64,
    /// The opponent's rating deviation.
    pub opponent_rd: f64,
    /// 1 win, 0.5 draw, 0 loss.
    pub score: f64,
}

/// Run one Glicko-2 rating period. Returns `(r', RD', sigma')`.
///
/// With no games, `r` is unchanged and RD grows toward 350 via `sqrt(RD² + σ²)`, per the paper's
/// idle-period rule — which is what makes an idle feature's confidence bound fall back to the floor
/// rather than keeping a stale certainty.
#[must_use]
pub fn rate(r: f64, rd: f64, sigma: f64, games: &[Game], tau: f64) -> (f64, f64, f64) {
    let mu = (r - DEFAULT_R) / SCALE;
    let phi = rd / SCALE;

    if games.is_empty() {
        let phi_star = (phi * phi + sigma * sigma).sqrt();
        let grown = (SCALE * phi_star).min(MAX_RD);
        return (r, grown, sigma);
    }

    let mut inv_v = 0.0;
    let mut g_sum = 0.0;
    for game in games {
        let mu_j = (game.opponent_r - DEFAULT_R) / SCALE;
        let phi_j = game.opponent_rd / SCALE;
        let gj = g(phi_j);
        let e = 1.0 / (1.0 + (-gj * (mu - mu_j)).exp());
        inv_v += gj * gj * e * (1.0 - e);
        g_sum += gj * (game.score - e);
    }
    let v = 1.0 / inv_v;
    let delta = v * g_sum;

    let a = (sigma * sigma).ln();
    let f = |x: f64| {
        let ex = x.exp();
        let num = ex * (delta * delta - phi * phi - v - ex);
        let den = 2.0 * (phi * phi + v + ex) * (phi * phi + v + ex);
        num / den - (x - a) / (tau * tau)
    };

    let mut big_a = a;
    let mut big_b;
    if delta * delta > phi * phi + v {
        big_b = (delta * delta - phi * phi - v).ln();
    } else {
        let mut k = 1.0;
        while f(big_a - k * tau) < 0.0 {
            k += 1.0;
        }
        big_b = big_a - k * tau;
    }
    let mut f_a = f(big_a);
    let mut f_b = f(big_b);
    // Illinois algorithm (regula falsi with the stuck-side halving trick). The chord is between
    // (A, fA) and (B, fB): C = A − fA·(A−B)/(fA−fB). Anchoring the chord at `a` instead degenerates
    // when A == a and never converges — caught by the published worked example below.
    let mut iterations = 0;
    while (big_b - big_a).abs() > 1e-6 && iterations < 100 {
        let den = f_a - f_b;
        if den == 0.0 {
            break;
        }
        let big_c = big_a - f_a * (big_a - big_b) / den;
        let f_c = f(big_c);
        if f_c * f_b < 0.0 {
            big_a = big_b;
            f_a = f_b;
        } else {
            f_a /= 2.0;
        }
        big_b = big_c;
        f_b = f_c;
        iterations += 1;
    }
    let sigma_p = (big_a / 2.0).exp();

    let phi_star = (phi * phi + sigma_p * sigma_p).sqrt();
    let phi_p = 1.0 / (1.0 / (phi_star * phi_star) + 1.0 / v).sqrt();
    let mu_p = mu + phi_p * phi_p * g_sum;
    (DEFAULT_R + SCALE * mu_p, SCALE * phi_p, sigma_p)
}

/// The result of one pairwise comparison (§5.4).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    /// The left feature won.
    A,
    /// The right feature won.
    B,
    /// Both — a draw.
    Both,
    /// Neither is worth building.
    Neither,
    /// The voter skipped.
    Skip,
}

impl Outcome {
    /// Parse the stored spelling; anything unknown is not a vote.
    #[must_use]
    pub fn parse(value: &str) -> Option<Self> {
        match value {
            "a" => Some(Self::A),
            "b" => Some(Self::B),
            "both" => Some(Self::Both),
            "neither" => Some(Self::Neither),
            "skip" => Some(Self::Skip),
            _ => None,
        }
    }

    /// Whether the outcome moves ratings at all.
    #[must_use]
    pub fn applies_rating_change(self) -> bool {
        matches!(self, Self::A | Self::B | Self::Both)
    }

    /// The standard Glicko-2 scores `(sA, sB)`.
    #[must_use]
    pub fn score_pair(self) -> (f64, f64) {
        match self {
            Self::A => (1.0, 0.0),
            Self::B => (0.0, 1.0),
            Self::Both => (0.5, 0.5),
            Self::Neither | Self::Skip => (0.0, 0.0),
        }
    }
}

/// A feature's rating state.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Rated {
    /// Rating.
    pub r: f64,
    /// Rating deviation.
    pub rd: f64,
    /// Volatility.
    pub vol: f64,
    /// The maintainer-set weight, §6.2's `μ` input beside the tag-matched theme weight.
    pub strategic_weight: f64,
}

/// Update both features for one pairwise vote, scaled by the voter's weight.
///
/// A weight of zero is a complete no-op on rating, RD **and** volatility, and the vote is still
/// recorded. That case is the account-age gate refusing a brand-new account's influence, and it needs
/// the whole update skipped: leaving RD and volatility at their standard update would let a mass of
/// throwaway accounts shrink the uncertainty on chosen features, and priority is scored on
/// `r - 2·RD`, so collapsing RD is itself a way to move a feature up the board without shifting its
/// rating.
pub fn apply_pairwise_vote(
    a: Rated,
    b: Rated,
    outcome: Outcome,
    weight: f64,
    tau: f64,
) -> (Rated, Rated) {
    if !outcome.applies_rating_change() || weight <= 0.0 {
        return (a, b);
    }
    let (s_a, s_b) = outcome.score_pair();
    let (ra, rd_a, vol_a) = rate(
        a.r,
        a.rd,
        a.vol,
        &[Game {
            opponent_r: b.r,
            opponent_rd: b.rd,
            score: s_a,
        }],
        tau,
    );
    let (rb, rd_b, vol_b) = rate(
        b.r,
        b.rd,
        b.vol,
        &[Game {
            opponent_r: a.r,
            opponent_rd: a.rd,
            score: s_b,
        }],
        tau,
    );
    (
        Rated {
            r: a.r + weight * (ra - a.r),
            rd: a.rd + weight * (rd_a - a.rd),
            vol: a.vol + weight * (vol_a - a.vol),
            strategic_weight: a.strategic_weight,
        },
        Rated {
            r: b.r + weight * (rb - b.r),
            rd: b.rd + weight * (rd_b - b.rd),
            vol: b.vol + weight * (vol_b - b.vol),
            strategic_weight: b.strategic_weight,
        },
    )
}

/// A member role's influence multiplier (§4). A guest has no vote, and is gated before weight matters.
#[must_use]
pub fn role_multiplier(role: &str) -> f64 {
    match role {
        "owner" => 2.0,
        "maintainer" => 1.75,
        "reviewer" => 1.5,
        "contributor" => 1.25,
        _ => 1.0,
    }
}

/// `min(cap, (1 + log10(1 + reputation)) * roleMultiplier)`.
#[must_use]
pub fn vote_weight(reputation: f64, role_mult: f64, cap: f64) -> f64 {
    let raw = (1.0 + (1.0 + reputation).log10()) * role_mult;
    cap.min(raw)
}

/// Ramp a voter's weight from 0 to 1 across their first [`ACCOUNT_AGE_GATE_DAYS`] on the instance.
///
/// A ramp rather than a cliff on purpose: a cliff makes the day the gate opens an event worth waiting
/// for, and a two-contributor project whose members registered on the same day then has no way to
/// reach quorum at all. A ramp lets a new account take part immediately at reduced weight: it cannot
/// buy influence with a throwaway account, and it is not locked out of the conversation it came to
/// join. A negative age (clock skew) reads as 0 days rather than as an error — a voter must not be
/// penalised because our clock is wrong.
#[must_use]
pub fn age_gate_multiplier(account_age_days: f64) -> f64 {
    let days = if account_age_days < 0.0 {
        0.0
    } else {
        account_age_days
    };
    (days / ACCOUNT_AGE_GATE_DAYS).min(1.0)
}

/// A stored `created_at` (unix seconds) and now, as an age in days.
#[must_use]
pub fn account_age_days(created_at: f64, now: f64) -> f64 {
    (now - created_at) / 86_400.0
}

/// Decay points with an exponential half-life: `points · 0.5^(age/halflife)`.
#[must_use]
pub fn decay_points(points: f64, age_days: f64, halflife_days: f64) -> f64 {
    if halflife_days <= 0.0 {
        return points;
    }
    points * 0.5f64.powf(age_days / halflife_days)
}

/// §6.1: `severity · frequency · ln(1 + affected) · strategicMult`, decayed from creation.
///
/// `affected` is reputation-weighted upstream: 1 for the reporter plus the per-impact contribution.
#[must_use]
pub fn pain_score(
    severity: i64,
    frequency: f64,
    strategic_mult: f64,
    affected: f64,
    age_days: f64,
    halflife_days: f64,
) -> f64 {
    let base = severity as f64 * frequency * (1.0 + affected).ln() * strategic_mult;
    decay_points(base, age_days, halflife_days)
}

/// §6.2: `(r − 2·RD) + λ·ln(1 + painSum) + μ·themeWeight`.
///
/// The lower confidence bound keeps new, uncertain features from dominating the list.
#[must_use]
pub fn priority_score(r: f64, rd: f64, pain_sum: f64, theme_weight: f64, lam: f64, mu: f64) -> f64 {
    (r - 2.0 * rd) + lam * (1.0 + pain_sum).ln() + mu * theme_weight
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Glickman's published worked example (March 2022): r=1500, RD=200, σ=0.06, τ=0.5 over one
    /// period with opponents 1400 (RD 30, win), 1550 (RD 100, loss), 1700 (RD 300, loss).
    ///
    /// This is the test that proves the Illinois iteration, and it is the reason the port cannot
    /// drift silently: a wrong chord anchor or a wrong initial bracket fails on these three numbers.
    #[test]
    fn the_published_worked_example() {
        let games = [
            Game { opponent_r: 1400.0, opponent_rd: 30.0, score: 1.0 },
            Game { opponent_r: 1550.0, opponent_rd: 100.0, score: 0.0 },
            Game { opponent_r: 1700.0, opponent_rd: 300.0, score: 0.0 },
        ];
        let (r, rd, sigma) = rate(1500.0, 200.0, 0.06, &games, 0.5);
        assert!((r - 1464.06).abs() <= 0.1, "new rating = {r:.4}, want ~1464.06");
        assert!((rd - 151.52).abs() <= 0.1, "new RD = {rd:.4}, want ~151.52");
        assert!((sigma - 0.05999).abs() <= 0.0001, "new sigma = {sigma:.6}, want ~0.05999");
    }

    /// Cross-implementation reference values, computed to full precision by an independent Python
    /// transliteration of the same algorithm and asserted to 1e-9.
    ///
    /// The published worked example above is rounded to two decimals, which is wide enough for a
    /// mutation that shifts the rating by 0.05 to survive. These are not rounded: every one of these
    /// numbers is a float64 the algorithm either reproduces or does not, and the mutation gate needs
    /// that resolution to tell a surviving mutant from an equivalent one.
    #[test]
    fn the_full_precision_reference_values() {
        let cases: [(&[Game], f64, f64, f64, f64, [f64; 3]); 4] = [
            (
                &[Game { opponent_r: 1500.0, opponent_rd: 200.0, score: 1.0 }],
                1500.0, 200.0, 0.06, 0.5,
                [1578.801716729907, 180.078290804417, 0.059999627543],
            ),
            (
                &[Game { opponent_r: 1500.0, opponent_rd: 200.0, score: 0.5 }],
                1500.0, 200.0, 0.06, 0.5,
                [1500.0, 180.078280469121, 0.059998055070],
            ),
            (
                &[Game { opponent_r: 1750.0, opponent_rd: 50.0, score: 0.0 }],
                1600.0, 120.0, 0.05, 0.5,
                [1577.601622343057, 114.817166636690, 0.049999110285],
            ),
            (
                &[
                    Game { opponent_r: 1300.0, opponent_rd: 60.0, score: 1.0 },
                    Game { opponent_r: 1700.0, opponent_rd: 100.0, score: 0.5 },
                ],
                1450.0, 180.0, 0.07, 0.3,
                [1528.696113067678, 153.763053956230, 0.069999403850],
            ),
        ];
        for (games, r, rd, sigma, tau, want) in cases {
            let (got_r, got_rd, got_sigma) = rate(r, rd, sigma, games, tau);
            assert!(
                (got_r - want[0]).abs() < 1e-9,
                "r' = {got_r:.12}, want {:.12} (from r={r}, rd={rd}, sigma={sigma}, tau={tau})",
                want[0]
            );
            assert!((got_rd - want[1]).abs() < 1e-9, "RD' = {got_rd:.12}, want {:.12}", want[1]);
            assert!(
                (got_sigma - want[2]).abs() < 1e-12,
                "sigma' = {got_sigma:.12}, want {:.12}",
                want[2]
            );
        }
        // The published example again, at the same resolution, so the rounded check above is not the
        // only thing standing between the port and a 0.04 drift.
        let games = [
            Game { opponent_r: 1400.0, opponent_rd: 30.0, score: 1.0 },
            Game { opponent_r: 1550.0, opponent_rd: 100.0, score: 0.0 },
            Game { opponent_r: 1700.0, opponent_rd: 300.0, score: 0.0 },
        ];
        let (r, rd, sigma) = rate(1500.0, 200.0, 0.06, &games, 0.5);
        assert!((r - 1464.050670539301).abs() < 1e-9, "r' = {r:.12}");
        assert!((rd - 151.516524123857).abs() < 1e-9, "RD' = {rd:.12}");
        assert!((sigma - 0.059995984286).abs() < 1e-12, "sigma' = {sigma:.12}");
    }

    #[test]
    fn a_win_raises_the_rating_and_a_loss_lowers_it() {
        let win = rate(1500.0, 200.0, 0.06, &[Game { opponent_r: 1500.0, opponent_rd: 200.0, score: 1.0 }], 0.5);
        assert!(win.0 > 1500.0, "r' = {:.2} after a win vs an equal player", win.0);
        let loss = rate(1500.0, 200.0, 0.06, &[Game { opponent_r: 1500.0, opponent_rd: 200.0, score: 0.0 }], 0.5);
        assert!(loss.0 < 1500.0, "r' = {:.2} after a loss", loss.0);
        let draw = rate(1500.0, 200.0, 0.06, &[Game { opponent_r: 1500.0, opponent_rd: 200.0, score: 0.5 }], 0.5);
        assert!((draw.0 - 1500.0).abs() <= 1e-6, "a symmetric draw moved r to {:.4}", draw.0);
        assert!(win.1 < 200.0, "RD' = {:.2} should shrink after playing", win.1);
    }

    #[test]
    fn an_idle_period_grows_rd_and_caps_at_the_ceiling() {
        let (r, rd, _) = rate(1500.0, MAX_RD, 0.06, &[], 0.5);
        assert_eq!(rd, MAX_RD, "an idle period at the cap must stay at the cap");
        assert_eq!(r, 1500.0, "an idle period must not move the rating");
        // Exactly, not "greater than": the growth is `SCALE * sqrt((rd/SCALE)^2 + sigma^2)`, and the
        // only assertion here used to be `> 100`, which `SCALE / phi_star` (300.2) and
        // `SCALE + phi_star` (174.3, then clamped to the cap) both satisfy. Two mutants lived in
        // this line until these two numbers were written down.
        let (_, grown, _) = rate(1500.0, 100.0, 0.06, &[], 0.5);
        assert!((grown - 100.541734352122).abs() < 1e-9, "RD' = {grown:.12}, want 100.541734352122");
        let (_, grown, _) = rate(1500.0, 50.0, 0.02, &[], 0.5);
        assert!((grown - 50.120566134220).abs() < 1e-9, "RD' = {grown:.12}, want 50.120566134220");
    }

    #[test]
    fn a_zero_weight_vote_changes_nothing_but_is_still_an_outcome() {
        let a = Rated { r: 1500.0, rd: 350.0, vol: 0.06, strategic_weight: 1.0 };
        let b = Rated { r: 1400.0, rd: 100.0, vol: 0.06, strategic_weight: 1.0 };
        let (a2, b2) = apply_pairwise_vote(a, b, Outcome::A, 0.0, 0.5);
        assert_eq!(a2, a, "a zero-weight vote must leave A untouched, RD and volatility included");
        assert_eq!(b2, b, "a zero-weight vote must leave B untouched");
    }

    #[test]
    fn neither_and_skip_leave_the_ratings_alone() {
        let a = Rated { r: 1500.0, rd: 200.0, vol: 0.06, strategic_weight: 1.0 };
        let b = Rated { r: 1600.0, rd: 200.0, vol: 0.06, strategic_weight: 1.0 };
        for outcome in [Outcome::Neither, Outcome::Skip] {
            let (a2, b2) = apply_pairwise_vote(a, b, outcome, 1.0, 0.5);
            assert_eq!(a2, a);
            assert_eq!(b2, b);
        }
        assert_eq!(Outcome::parse("both"), Some(Outcome::Both));
        assert_eq!(Outcome::parse("nonsense"), None);
    }

    #[test]
    fn the_weight_scales_the_whole_update() {
        let a = Rated { r: 1500.0, rd: 200.0, vol: 0.06, strategic_weight: 1.0 };
        let b = Rated { r: 1500.0, rd: 200.0, vol: 0.06, strategic_weight: 1.0 };
        let (full, _) = apply_pairwise_vote(a, b, Outcome::A, 1.0, 0.5);
        let (half, _) = apply_pairwise_vote(a, b, Outcome::A, 0.5, 0.5);
        // Half weight moves the rating half as far — the interpolation is on the delta, not on the
        // resulting rating.
        assert!((half.r - 1500.0 - (full.r - 1500.0) / 2.0).abs() < 1e-9);
        // And it moves RD half as far, which is the part §30 superseded: a weight near zero must not
        // still collapse the uncertainty.
        assert!((half.rd - 200.0 - (full.rd - 200.0) / 2.0).abs() < 1e-9);
    }

    #[test]
    fn the_age_gate_ramps_and_never_penalises_a_skewed_clock() {
        assert_eq!(age_gate_multiplier(-3.0), 0.0);
        assert_eq!(age_gate_multiplier(0.0), 0.0);
        assert!((age_gate_multiplier(3.5) - 0.5).abs() < 1e-9);
        assert_eq!(age_gate_multiplier(7.0), 1.0);
        assert_eq!(age_gate_multiplier(700.0), 1.0);
        assert!((account_age_days(0.0, 86_400.0) - 1.0).abs() < 1e-9);
    }

    #[test]
    fn the_weight_is_capped_and_grows_with_reputation() {
        assert_eq!(vote_weight(0.0, 1.0, 5.0), 1.0);
        assert!(vote_weight(99.0, 1.0, 5.0) > 1.0);
        assert_eq!(vote_weight(1e9, 2.0, 5.0), 5.0, "the cap is the cap");
        assert_eq!(role_multiplier("owner"), 2.0);
        assert_eq!(role_multiplier("guest"), 1.0);
    }

    #[test]
    fn decay_halves_at_the_halflife_and_refuses_a_zero_halflife() {
        assert!((decay_points(8.0, 0.0, 30.0) - 8.0).abs() < 1e-12);
        assert!((decay_points(8.0, 30.0, 30.0) - 4.0).abs() < 1e-12);
        assert!((decay_points(8.0, 60.0, 30.0) - 2.0).abs() < 1e-12);
        // A halflife of zero would divide by it; the Go function returns the points untouched.
        assert_eq!(decay_points(8.0, 60.0, 0.0), 8.0);
    }

    #[test]
    fn pain_and_priority_are_the_spec_formulas() {
        // ln(1 + 1) = 0.6931…, so severity 3 × frequency 2 × mult 1 × that = 4.1588…
        let pain = pain_score(3, 2.0, 1.0, 1.0, 0.0, 30.0);
        assert!((pain - 3.0 * 2.0 * (2.0f64).ln()).abs() < 1e-12);
        // (1500 - 2·350) + 1.0·ln(1 + pain) + 1.0·0
        let priority = priority_score(1500.0, 350.0, pain, 0.0, 1.0, 1.0);
        assert!((priority - (800.0 + (1.0 + pain).ln())).abs() < 1e-12);
        // The theme weight is the μ term and nothing else.
        assert!((priority_score(1500.0, 350.0, 0.0, 2.0, 1.0, 1.5) - 800.0 - 3.0).abs() < 1e-12);
    }
}

/// §5.1's displayed score, `r - 2*RD`.
///
/// Not a display detail: it is what the leaderboard orders on, so an entry with no games (RD 350) cannot
/// outrank a well-attested one on the strength of not having lost yet.
#[must_use]
pub fn conservative(r: f64, rd: f64) -> f64 {
    r - 2.0 * rd
}

/// §5.1's expected score, `E(r1, r2, RD1, RD2)` — "A is better with 87% confidence".
///
/// Glickman eq. 2.1 with a twist the Go file records: `E` uses the *rating* player's own deviation, not
/// the opponent's, so `rd2` does not enter the formula. It is in the signature because every caller has it
/// and dropping it would make the arguments asymmetric — but pretending it mattered would make a
/// high-RD leader look harder to beat than it is.
#[must_use]
pub fn win_probability(r1: f64, rd1: f64, r2: f64, _rd2: f64) -> f64 {
    let mu1 = (r1 - 1500.0) / SCALE;
    let mu2 = (r2 - 1500.0) / SCALE;
    1.0 / (1.0 + (-g(rd1 / SCALE) * (mu1 - mu2)).exp())
}
