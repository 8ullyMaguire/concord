package httpapi

// The Solutions panel's read model (spec §4.10 "Roadmap: solution standings",
// phase3-spec §4.1 item 3, solutions-panel-spec.md).
//
// This is the third of S1's three panels, and the only one whose data already had
// a read path: `handleListSolutions` serves ONE feature's standings under
// `/features/{id}/solutions`. What was missing is a project-level view, because
// the project page does not know which feature the reader cares about and adding
// a feature picker would be a fourth surface that §4.2's "no new page" rule
// makes expensive.
//
// Two decisions recorded in the spec and load-bearing here:
//
//   - Standings are grouped PER FEATURE, never merged into one project-wide
//     leaderboard. A solution's score is computed against the arena entries
//     competing with it, so "best solution in the project" ranks numbers that
//     were never comparable. `SolutionScore` carries `IsBaseline` for exactly
//     this reason: a baseline entry is ranked but never selectable, and a merged
//     list would let a project's "do nothing" for one feature outrank a real
//     proposal for another.
//
//   - Features with no solutions are INCLUDED with an empty list, and counted in
//     `FeaturesWithout`. "One feature has proposals and four do not" is the useful
//     sentence; a panel that omits the four reads as if one feature were the whole
//     project.
import (
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// SolutionStandingsPerFeature bounds the solutions rendered for ONE feature.
//
// Per feature, not per project: truncating a project's whole solution list would
// hide some features entirely, and truncating within a feature hides that
// feature's runner-ups, which is the thing standings exist to show. Five is the
// same bound the Finder uses for a candidate list.
const SolutionStandingsPerFeature = 5

type projectSolutionsRes struct {
	ProjectID int64              `json:"project_id"`
	Features  []featureStandings `json:"features"`
	// FeaturesWithSolutions and FeaturesWithout are the counts the panel leads
	// with. Both present even when one is zero, because "no feature has any
	// proposals" is a statement about the project worth making.
	FeaturesWithSolutions int `json:"features_with_solutions"`
	FeaturesWithout       int `json:"features_without"`
}

type featureStandings struct {
	FeatureID    int64                 `json:"feature_id"`
	FeatureTitle string                `json:"feature_title"`
	Solutions    []store.SolutionScore `json:"solutions"`
	// Shown and Omitted bound the list for this feature. The page says how many
	// it left out; silently truncating reads as "there are five".
	Shown   int `json:"shown"`
	Omitted int `json:"omitted"`
}

func (s *Server) handleListProjectSolutions(w http.ResponseWriter, r *http.Request) {
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	// Status "" is every status. A shipped feature whose proposals were all
	// withdrawn is still part of the project's roadmap, and a standings panel
	// that hides closed work is a panel that erases it.
	features, err := s.Store.ListFeatures(r.Context(), projectID, "")
	if err != nil {
		mapError(w, err)
		return
	}

	res := projectSolutionsRes{
		ProjectID: projectID,
		// Never nil: "no features" and "null features" are the same answer to a
		// client and different code to the page.
		Features: make([]featureStandings, 0, len(features)),
	}

	for _, f := range features {
		scores, err := s.Store.ListSolutions(r.Context(), f.ID, SolutionStandingsPerFeature)
		if err != nil {
			mapError(w, err)
			return
		}
		// The omitted count comes from an explicit COUNT rather than from
		// asking for limit+1 rows and looking for an extra one. The first version
		// did the latter and reported `omitted = 1` for a feature with seven
		// solutions: ListSolutions applies its limit inside ArenaLeaderboard and
		// then skips arena entries whose solution row is gone, so "one more row"
		// is not a witness of "one more solution". See store.CountSolutions.
		total, err := s.Store.CountSolutions(r.Context(), f.ID)
		if err != nil {
			mapError(w, err)
			return
		}

		row := featureStandings{
			FeatureID:    f.ID,
			FeatureTitle: f.Title,
			// Never nil, same reason as Features: a feature nobody has proposed
			// for is a normal state, and the comment on the per-feature handler
			// says an empty array is the honest answer where a 404 reads as
			// breakage.
			//
			// Assigned here rather than being left to a nil slice: ListSolutions
			// returns nil for a feature with no arena, and a nil slice marshals
			// to `null`, which the page then has to special-case.
			Solutions: []store.SolutionScore{},
		}
		// Truncate, then derive shown from the TRUNCATED length, then omitted
		// from the count.
		//
		// Two ordering bugs lived here and both produced a wrong omitted count
		// rather than an obvious nil:
		//
		//   - the first version assigned `scores` before truncating, so Shown
		//     counted every solution the leaderboard returned and omitted was
		//     `total - Shown` = 7 - 7 = 0 while five were actually dropped. It
		//     is the same class of mistake as the nil-slice one below: assign
		//     from the value you just changed.
		//   - for a feature with no arena ListSolutions returns nil, and a nil
		//     slice marshals to `null`, not `[]`, so `solutions` came back null
		//     for exactly the case the struct comment says must be an array.
		if len(scores) > SolutionStandingsPerFeature {
			scores = scores[:SolutionStandingsPerFeature]
		}
		if len(scores) == 0 {
			scores = []store.SolutionScore{}
		}
		row.Solutions = scores
		row.Shown = len(scores)
		// Computed from the count, not from the slice length, and floored at
		// zero: a stale arena entry can make the leaderboard shorter than the
		// count, and a negative `omitted` would be nonsense to a reader.
		if total > row.Shown {
			row.Omitted = total - row.Shown
		}

		if len(row.Solutions) == 0 {
			res.FeaturesWithout++
		} else {
			res.FeaturesWithSolutions++
		}
		res.Features = append(res.Features, row)
	}

	writeJSON(w, http.StatusOK, res)
}
