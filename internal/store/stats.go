package store

import "context"

// Census is the composition of the instance: what exists, not what happened.
//
// Every slice is a still frame, which is the only kind of aggregate a seeded
// instance can publish without lying: a batch import changes a total, and it
// changes a "per week" series.
//
// THE SPLIT IS THREE-WAY, AND THE THIRD COLUMN IS THE POINT. Migration 0030
// backfilled every pre-existing feature and complaint to origin='unknown', so
// 'unknown' is not "organic" -- it is the population that predates the
// backfill. A two-way split that lumps it into "other" reports the seed
// population as organic, which is the exact lie this milestone exists to
// prevent (spec §2: an aggregate that cannot produce the split is not
// published).
type Census struct {
	// Tables that carry `origin` publish the split.
	FeatureStatus []ProvenanceCount `json:"features_by_status"`
	FeatureEffort []ProvenanceCount `json:"features_by_effort"`
	Complaints    []ProvenanceCount `json:"complaints_by_status"`

	// Tables that do not carry `origin` publish a plain count. Projects are
	// created by people (CreateProject requires a user), and members, documents
	// and arenas are human actions, so none of these is an import chart. Spec §2
	// withholds an aggregate only where the rows can be created by an import;
	// these cannot.
	Projects   []FacetCount `json:"projects_by_language"`
	Tags       []FacetCount `json:"projects_by_tag"`
	Licenses   []FacetCount `json:"projects_by_license"`
	Governance []FacetCount `json:"projects_by_governance"`
	Visibility []FacetCount `json:"projects_by_visibility"`
	Members    []FacetCount `json:"members_by_role"`
	Documents  []FacetCount `json:"documents_by_kind"`
	Arenas     []FacetCount `json:"arenas_by_type"`
}

// ProvenanceCount carries the split on the same struct, not as a second query:
// a caller that has to remember to fetch the split is a caller that will print
// a total without one.
//
// Total = Organic + Seeded + Unknown, always. The invariant is arithmetic and
// asserted in TestCensusProvenanceRowsSum over a fixture holding all three.
type ProvenanceCount struct {
	Value   string `json:"value"`
	Total   int    `json:"total"`
	Organic int    `json:"organic"` // origin IN ('api','ui')
	Seeded  int    `json:"seeded"`  // origin IN ('seed','import')
	Unknown int    `json:"unknown"` // origin = 'unknown' (predates the 0030 backfill)
}

// Census computes the instance census. It reads six tables and issues nine
// grouped queries; there is no join that could multiply a facet, which is the
// defect the AC names (a total larger than count(*)).
func (d *DB) Census(ctx context.Context) (Census, error) {
	var c Census

	featureStatus, err := d.provenanceFacet(ctx,
		`SELECT status, COUNT(*),
		        SUM(CASE WHEN origin IN ('api','ui') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin IN ('seed','import') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin = 'unknown' THEN 1 ELSE 0 END)
		 FROM features GROUP BY status ORDER BY status`)
	if err != nil {
		return c, err
	}
	c.FeatureStatus = featureStatus

	featureEffort, err := d.provenanceFacet(ctx,
		`SELECT effort, COUNT(*),
		        SUM(CASE WHEN origin IN ('api','ui') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin IN ('seed','import') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin = 'unknown' THEN 1 ELSE 0 END)
		 FROM features GROUP BY effort ORDER BY effort`)
	if err != nil {
		return c, err
	}
	c.FeatureEffort = featureEffort

	complaints, err := d.provenanceFacet(ctx,
		`SELECT status, COUNT(*),
		        SUM(CASE WHEN origin IN ('api','ui') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin IN ('seed','import') THEN 1 ELSE 0 END),
		        SUM(CASE WHEN origin = 'unknown' THEN 1 ELSE 0 END)
		 FROM complaints GROUP BY status ORDER BY status`)
	if err != nil {
		return c, err
	}
	c.Complaints = complaints

	// Plain facets. Each is a grouped COUNT over one table (or one join to a
	// lookup), so no row multiplication is possible.
	if c.Projects, err = d.countFacet(ctx,
		`SELECT language, COUNT(*) FROM project_languages
		 GROUP BY language ORDER BY COUNT(*) DESC, language LIMIT 25`); err != nil {
		return c, err
	}
	if c.Tags, err = d.countFacet(ctx,
		`SELECT t.name, COUNT(*) FROM project_tags pt
		 JOIN tags t ON t.id = pt.tag_id
		 GROUP BY t.name ORDER BY COUNT(*) DESC, t.name LIMIT 25`); err != nil {
		return c, err
	}
	if c.Licenses, err = d.countFacet(ctx,
		`SELECT COALESCE(NULLIF(license,''), 'unknown'), COUNT(*) FROM projects
		 GROUP BY 1 ORDER BY COUNT(*) DESC, 1 LIMIT 25`); err != nil {
		return c, err
	}
	if c.Governance, err = d.countFacet(ctx,
		`SELECT governance_model, COUNT(*) FROM projects
		 GROUP BY governance_model ORDER BY COUNT(*) DESC, governance_model`); err != nil {
		return c, err
	}
	if c.Visibility, err = d.countFacet(ctx,
		`SELECT visibility, COUNT(*) FROM projects
		 GROUP BY visibility ORDER BY COUNT(*) DESC, visibility`); err != nil {
		return c, err
	}
	if c.Members, err = d.countFacet(ctx,
		`SELECT role, COUNT(*) FROM members
		 GROUP BY role ORDER BY COUNT(*) DESC, role`); err != nil {
		return c, err
	}
	// The table is project_documents, not documents: the plan said "documents"
	// and a query against that name is a runtime error, not a typo the compiler
	// catches.
	if c.Documents, err = d.countFacet(ctx,
		`SELECT kind, COUNT(*) FROM project_documents
		 GROUP BY kind ORDER BY COUNT(*) DESC, kind`); err != nil {
		return c, err
	}
	if c.Arenas, err = d.countFacet(ctx,
		`SELECT type, COUNT(*) FROM arenas
		 GROUP BY type ORDER BY COUNT(*) DESC, type`); err != nil {
		return c, err
	}

	return c, nil
}

func (d *DB) provenanceFacet(ctx context.Context, query string, args ...any) ([]ProvenanceCount, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvenanceCount
	for rows.Next() {
		var p ProvenanceCount
		if err := rows.Scan(&p.Value, &p.Total, &p.Organic, &p.Seeded, &p.Unknown); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) countFacet(ctx context.Context, query string, args ...any) ([]FacetCount, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanFacets(rows)
}
