// Package ranking implements Concord's priority mathematics (spec §6):
// Glicko-2 pairwise ratings, complaint pain scores, feature priority, and
// vote weight. All functions are pure — SQL wiring lives in internal/store.
package ranking

import "math"

const scale = 173.7178 // Glicko-2 scale factor

// g(phi) is Glickman's dampening function.
func g(phi float64) float64 {
	return 1.0 / math.Sqrt(1.0+3.0*phi*phi/(math.Pi*math.Pi))
}

// Game is one opponent result within a rating period.
type Game struct {
	OpponentR  float64
	OpponentRD float64
	Score      float64 // 1 win, 0.5 draw, 0 loss
}

// Rate runs one Glicko-2 rating period (Glickman, glicko2.pdf). It returns
// (r', RD', sigma'). With no games, r is unchanged and RD grows toward 350
// via sqrt(RD² + σ²), per the paper's idle-period rule.
func Rate(r, rd, sigma float64, games []Game, tau float64) (float64, float64, float64) {
	mu := (r - 1500.0) / scale
	phi := rd / scale

	if len(games) == 0 {
		phiStar := math.Sqrt(phi*phi + sigma*sigma)
		grown := scale * phiStar
		if grown > 350.0 {
			grown = 350.0
		}
		return r, grown, sigma
	}

	invV := 0.0
	gSum := 0.0 // Σ g(phi_j) * (s_j - E_j)
	for _, gm := range games {
		muJ := (gm.OpponentR - 1500.0) / scale
		phiJ := gm.OpponentRD / scale
		gj := g(phiJ)
		e := 1.0 / (1.0 + math.Exp(-gj*(mu-muJ)))
		invV += gj * gj * e * (1.0 - e)
		gSum += gj * (gm.Score - e)
	}
	v := 1.0 / invV
	delta := v * gSum

	a := math.Log(sigma * sigma)

	f := func(x float64) float64 {
		ex := math.Exp(x)
		num := ex * (delta*delta - phi*phi - v - ex)
		den := 2.0 * (phi*phi + v + ex) * (phi*phi + v + ex)
		return num/den - (x-a)/(tau*tau)
	}

	bigA := a
	var bigB float64
	if delta*delta > phi*phi+v {
		bigB = math.Log(delta*delta - phi*phi - v)
	} else {
		k := 1.0
		for f(bigA-k*tau) < 0 {
			k++
		}
		bigB = bigA - k*tau
	}
	fA, fB := f(bigA), f(bigB)
	// Illinois algorithm (regula falsi with the stuck-side halving trick).
	// The chord is between (A, fA) and (B, fB): C = A − fA·(A−B)/(fA−fB).
	// (Anchoring the chord at `a` instead degenerates when A == a and
	// never converges — caught by the Glickman worked-example test.)
	for iterations := 0; abs(bigB-bigA) > 1e-6 && iterations < 100; iterations++ {
		den := fA - fB
		if den == 0 {
			break
		}
		bigC := bigA - fA*(bigA-bigB)/den
		fC := f(bigC)
		if fC*fB < 0 {
			bigA, fA = bigB, fB
		} else {
			fA /= 2.0
		}
		bigB, fB = bigC, fC
	}
	sigmaP := math.Exp(bigA / 2.0)

	phiStar := math.Sqrt(phi*phi + sigmaP*sigmaP)
	phiP := 1.0 / math.Sqrt(1.0/(phiStar*phiStar)+1.0/v)
	muP := mu + phiP*phiP*gSum
	return 1500.0 + scale*muP, scale * phiP, sigmaP
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
