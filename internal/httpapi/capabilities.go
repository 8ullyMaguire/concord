package httpapi

import (
	"net/http"

	"git.polarisocial.xyz/concord/concord/internal/store"
)

// The capability panel's read model (spec §4.10 "Capabilities", phase3-spec §4).
//
// Two shapes of not-knowing are on this table and the endpoint must keep them
// apart, because the whole contribution loop depends on it:
//
//   - a capability with NO assertion for this project: nobody has said anything
//   - an assertion whose value is literally "unknown": somebody recorded that
//     nobody knows
//
// `ListCapabilitiesForSet` returns only rows that exist, so the first case is
// the absence of a key rather than a row, and this handler fills the gap from
// the catalog so `unknown_count` can be computed. Filling it in with the string
// "unknown" instead would make the two indistinguishable — which is exactly what
// that store method's doc comment says it exists to prevent.
type projectCapabilitiesRes struct {
	ProjectID int64 `json:"project_id"`
	// Capabilities is every catalog capability, each carrying this project's
	// state where one exists. The page renders the whole catalog deliberately:
	// a capability nobody has mentioned is the contribution queue, and a panel
	// that hid it would remove the reason the panel exists.
	Capabilities []capabilityRow `json:"capabilities"`
	// UnknownCount is how many of them have no assertion for this project.
	UnknownCount int `json:"unknown_count"`
	// AssertedCount is how many have one, whatever its state. A disputed claim
	// is an assertion, not an absence.
	AssertedCount int `json:"asserted_count"`
}

// capabilityRow is one capability as this project stands on it.
type capabilityRow struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	// Asserted reports whether this project has a claim at all. False means
	// nobody has said anything — NOT that the answer is "no".
	Asserted bool `json:"asserted"`
	// Value is empty when !Asserted. Never substituted with "unknown".
	Value string `json:"value,omitempty"`
	// State is asserted | confirmed | disputed, and is empty when !Asserted.
	//
	// A disputed claim keeps its ORIGINAL value. `ConfirmCapability` records a
	// dispute as a negative confirmation and leaves the value alone, because
	// substituting "no" would turn "people disagree" into "this is false" — a
	// different claim resting on a different evidence base.
	State       string   `json:"state,omitempty"`
	Confirms    int      `json:"confirms,omitempty"`
	Disputes    int      `json:"disputes,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	ConfirmedBy []string `json:"confirmed_by,omitempty"`
}

func (s *Server) handleListProjectCapabilities(w http.ResponseWriter, r *http.Request) {
	// requireProjectID resolves the slug in {project_id} to a numeric id AND
	// enforces visibility, answering 404 rather than 403 so this route cannot be
	// used to enumerate which private projects exist.
	projectID, ok := s.requireProjectID(w, r)
	if !ok {
		return
	}

	caps, err := s.Store.ListCapabilities(r.Context(), "")
	if err != nil {
		mapError(w, err)
		return
	}
	assertions, err := s.Store.ListCapabilityAssertions(r.Context(), projectID)
	if err != nil {
		mapError(w, err)
		return
	}

	// Index by capability key. The store returns at most one assertion per
	// (project, capability) — AssertCapability upserts — so a plain map cannot
	// drop a row, and two rows would be a store bug rather than an input.
	byKey := make(map[string]store.CapabilityAssertion, len(assertions))
	for _, a := range assertions {
		byKey[a.Capability] = a
	}

	res := projectCapabilitiesRes{
		ProjectID:    projectID,
		Capabilities: make([]capabilityRow, 0, len(caps)),
	}
	for _, c := range caps {
		row := capabilityRow{Key: c.Key, Label: c.Label, Category: c.Category}
		if a, ok := byKey[c.Key]; ok {
			row.Asserted = true
			row.Value = a.Value
			row.State = a.State
			row.Confirms = a.Confirms
			row.Disputes = a.Disputes
			row.Evidence = a.Evidence
			res.AssertedCount++
		} else {
			res.UnknownCount++
		}
		res.Capabilities = append(res.Capabilities, row)
	}

	writeJSON(w, http.StatusOK, res)
}
