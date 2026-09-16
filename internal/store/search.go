package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Search is Concord's first-class discovery surface (spec §15). This file
// implements the v1 SQL shape: FTS5 free text + structured filters +
// always-on facets. The query-language parser (tag:, language:, NOT, …)
// is a PLAN.md milestone and will compile down to these filters.

type SearchFilters struct {
	Tags       []string `json:"tags"`         // all must match (AND)
	Languages  []string `json:"languages"`    // all must match (AND)
	MinLangPct float64  `json:"min_lang_pct"` // applies to each Language filter
	MinHealth  float64  `json:"min_health"`   // 0 = any
	Model      string   `json:"model"`        // "" = any (collective | maintainer_led)
	License    string   `json:"license"`      // "" = any
}

type ProjectHit struct {
	Project
	Score float64 `json:"score"` // FTS bm25 rank (negative = better), 0 for filter-only searches
}

type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type Facets struct {
	Tags      []FacetCount `json:"tags"`
	Languages []FacetCount `json:"languages"`
	Licenses  []FacetCount `json:"licenses"`
	Models    []FacetCount `json:"models"`
}

type SearchResult struct {
	Query   string        `json:"query"`
	Filters SearchFilters `json:"filters"`
	Total   int           `json:"total"`
	Sort    string        `json:"sort"`
	Results []ProjectHit  `json:"results"`
	Facets  Facets        `json:"facets"`
}

// Sort options: ""/"relevance" (FTS rank, or updated when q is empty),
// "updated", "health", "newest".
func (d *DB) SearchProjects(ctx context.Context, q string, f SearchFilters, sort string) (SearchResult, error) {
	res := SearchResult{Query: q, Filters: f, Sort: sort, Results: []ProjectHit{}}

	var joins, wheres []string
	var args []any

	if q != "" {
		joins = append(joins, `JOIN projects_fts fts ON fts.slug = p.slug`)
		wheres = append(wheres, `projects_fts MATCH ?`)
		args = append(args, ftsQuery(q))
	}
	for _, tag := range f.Tags {
		wheres = append(wheres, `EXISTS (SELECT 1 FROM project_tags pt
			JOIN tags t ON t.id = pt.tag_id WHERE pt.project_id = p.id AND t.name = ?)`)
		args = append(args, strings.ToLower(strings.TrimSpace(tag)))
	}
	for _, lang := range f.Languages {
		wheres = append(wheres, `EXISTS (SELECT 1 FROM project_languages pl
			WHERE pl.project_id = p.id AND pl.language = ? AND pl.pct >= ?)`)
		args = append(args, strings.TrimSpace(lang), f.MinLangPct)
	}
	if f.MinHealth > 0 {
		wheres = append(wheres, `COALESCE(m.health_score, 0) >= ?`)
		args = append(args, f.MinHealth)
	}
	if f.Model != "" {
		wheres = append(wheres, `p.governance_model = ?`)
		args = append(args, f.Model)
	}
	if f.License != "" {
		wheres = append(wheres, `p.license = ?`)
		args = append(args, f.License)
	}

	sqlStr := `SELECT ` + projectColumns
	if q != "" {
		sqlStr += `, fts.rank`
	} else {
		sqlStr += `, 0`
	}
	sqlStr += ` FROM projects p
		LEFT JOIN project_metrics m ON m.project_id = p.id`
	for _, j := range joins {
		sqlStr += " " + j
	}
	if len(wheres) > 0 {
		sqlStr += " WHERE " + strings.Join(wheres, " AND ")
	}
	switch sort {
	case "updated":
		sqlStr += ` ORDER BY p.updated_at DESC`
	case "health":
		sqlStr += ` ORDER BY COALESCE(m.health_score, 0) DESC, p.updated_at DESC`
	case "newest":
		sqlStr += ` ORDER BY p.created_at DESC`
	default: // relevance
		if q != "" {
			sqlStr += ` ORDER BY fts.rank`
		} else {
			sqlStr += ` ORDER BY p.updated_at DESC`
		}
	}

	rows, err := d.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return res, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var hit ProjectHit
		var health sql.NullFloat64
		var rank float64
		if err := rows.Scan(&hit.ID, &hit.Slug, &hit.Name, &hit.Description,
			&hit.GovernanceModel, &hit.License, &hit.CreatedAt, &hit.UpdatedAt,
			&health, &rank); err != nil {
			rows.Close()
			return res, err
		}
		if health.Valid {
			h := health.Float64
			hit.HealthScore = &h
		}
		hit.Score = rank
		res.Results = append(res.Results, hit)
		ids = append(ids, hit.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, err
	}
	// Release the result cursor BEFORE issuing facet queries: the pool is
	// capped at one connection, and an open cursor would deadlock them.
	rows.Close()
	res.Total = len(res.Results)

	facets, err := d.facetsFor(ctx, ids)
	if err != nil {
		return res, err
	}
	res.Facets = facets
	return res, nil
}

// ftsQuery tokenizes free text into quoted AND terms so arbitrary user
// input can never inject FTS operators. The full query language (§15)
// will parse tag:/language:/NOT/grouping into SearchFilters instead.
func ftsQuery(q string) string {
	words := strings.Fields(q)
	if len(words) == 0 {
		return `""`
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + strings.ReplaceAll(w, `"`, ``) + `"`
	}
	return strings.Join(quoted, " AND ")
}

func (d *DB) facetsFor(ctx context.Context, ids []int64) (Facets, error) {
	var facets Facets
	if len(ids) == 0 {
		return facets, nil
	}
	in := placeholders(len(ids))
	args := idsToAny(ids)

	tagRows, err := d.QueryContext(ctx, `
		SELECT t.name, COUNT(*) FROM project_tags pt
		JOIN tags t ON t.id = pt.tag_id
		WHERE pt.project_id IN (`+in+`)
		GROUP BY t.name ORDER BY COUNT(*) DESC, t.name LIMIT 25`, args...)
	if err != nil {
		return facets, err
	}
	facets.Tags, err = scanFacets(tagRows)
	if err != nil {
		return facets, err
	}

	langRows, err := d.QueryContext(ctx, `
		SELECT language, COUNT(*) FROM project_languages
		WHERE project_id IN (`+in+`)
		GROUP BY language ORDER BY COUNT(*) DESC, language LIMIT 25`, args...)
	if err != nil {
		return facets, err
	}
	facets.Languages, err = scanFacets(langRows)
	if err != nil {
		return facets, err
	}

	licRows, err := d.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(license,''), 'unknown') AS lic, COUNT(*)
		FROM projects WHERE id IN (`+in+`)
		GROUP BY lic ORDER BY COUNT(*) DESC, lic LIMIT 25`, args...)
	if err != nil {
		return facets, err
	}
	facets.Licenses, err = scanFacets(licRows)
	if err != nil {
		return facets, err
	}

	modelRows, err := d.QueryContext(ctx, `
		SELECT governance_model, COUNT(*) FROM projects
		WHERE id IN (`+in+`)
		GROUP BY governance_model ORDER BY COUNT(*) DESC, governance_model`, args...)
	if err != nil {
		return facets, err
	}
	facets.Models, err = scanFacets(modelRows)
	return facets, err
}

func scanFacets(rows *sql.Rows) ([]FacetCount, error) {
	defer rows.Close()
	var out []FacetCount
	for rows.Next() {
		var fc FacetCount
		if err := rows.Scan(&fc.Value, &fc.Count); err != nil {
			return nil, err
		}
		out = append(out, fc)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func idsToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
