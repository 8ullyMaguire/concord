package store

import (
	"context"
	"fmt"
	"strings"
)

// §6.4's expertise weighting needs a reputation figure that is scoped to a tag:
// "a Rust-heavy design is weighed by Rust-reputed voters".
//
// It cannot come from GetReputation, which sums every event a user has in a
// project. That number says how much work someone has done, not what it was
// work on, and using it would make a generalist with a long history heavier on a
// Rust design than a Rust expert with a shorter one -- the opposite of what
// §8.3 means by "expertise is tag-scoped".
//
// So this is a separate derivation over the same underlying contributions, keyed
// by tag. The points per contribution match what AddReputation already awards for
// the same act, deliberately: a tag-reputation of 90 and a project reputation of
// 90 then mean the same amount of work, and the multiplier's saturation points
// are calibrated in units a reader already understands rather than in an
// arbitrary scale that exists only here.
//
// It is derived rather than stored because there is no table to store it in, and
// adding one would mean a second representation of the contribution history that
// has to be kept in step with the first. The cost is that a vote is now two
// queries it was one before, which is why the inputs are narrow.

const (
	// tagRepValidatedComplaint matches the "complaint_validated" reputation event.
	tagRepValidatedComplaint = 5.0
	// tagRepShippedSolution matches "submit_feature": filing a solution that
	// reaches consensus is the tag-scoped equivalent of a shipped complaint.
	tagRepShippedSolution = 5.0
	// tagRepMergedPR matches "create_merge_request".
	tagRepMergedPR = 3.0
	// tagRepArenaVote matches "vote": §8.3 counts "good-faith consensus
	// participation", and a non-skip vote in a solution's arena is the
	// participation this table can actually see.
	tagRepArenaVote = 1.0
)

// TagReputation returns a user's §8.3 reputation per tag within one project.
//
// Tags are returned lowercased and de-duplicated, matching
// ranking.ParseExpertiseTags, so a lookup against a solution's expertise_tags
// either finds the voter's tag or correctly finds nothing -- a case difference
// must never read as "no expertise" when the same word is written elsewhere in
// the other case.
func (d *DB) TagReputation(ctx context.Context, projectID, userID int64) (map[string]float64, error) {
	out := map[string]float64{}
	add := func(tag string, points float64) {
		t := strings.ToLower(strings.TrimSpace(tag))
		if t == "" {
			return
		}
		out[t] += points
	}

	// Validated complaints, by the tag on the complaint.
	if err := d.forEachTagSum(ctx, `
		SELECT LOWER(t.name) AS tag, SUM(?) AS pts
		FROM complaints c
		JOIN complaint_tags ct ON ct.complaint_id = c.id
		JOIN tags t ON t.id = ct.tag_id
		WHERE c.project_id = ? AND c.author_id = ?
		  AND c.status IN ('validated','linked','closed')
		GROUP BY LOWER(t.name)`, tagRepValidatedComplaint, projectID, userID, out); err != nil {
		return nil, err
	}

	// Merged merge requests, by the tags on the feature they implement. An
	// unmerged PR is worth nothing here: §8.3 counts merged work, and counting
	// openings would let a member farm tag-reputation by filing and abandoning.
	if err := d.forEachTagSum(ctx, `
		SELECT LOWER(t.name) AS tag, SUM(?) AS pts
		FROM merge_requests m
		JOIN feature_tags ft ON ft.feature_id = m.feature_id
		JOIN tags t ON t.id = ft.tag_id
		WHERE m.project_id = ? AND m.author_id = ? AND m.status = 'merged'
		GROUP BY LOWER(t.name)`, tagRepMergedPR, projectID, userID, out); err != nil {
		return nil, err
	}

	// Solutions that reached consensus, by their own declared expertise tags.
	//
	// The status list is the same one §6.5 walks a solution through: draft and
	// discussion are proposals, consensus onward are decisions. A rejected
	// solution is excluded, so a member cannot file something, lose the argument,
	// and keep the credit.
	solTags, err := d.solutionTagReputation(ctx, projectID, userID, tagRepShippedSolution)
	if err != nil {
		return nil, err
	}
	for _, st := range solTags {
		add(st.tag, st.points)
	}

	// Votes cast on solutions carrying those tags. A skip is excluded because
	// §5.1's skip is a statement that the write-up is unclear -- it is not
	// participation in the decision.
	voteTags, err := d.solutionVoteTagReputation(ctx, projectID, userID, tagRepArenaVote)
	if err != nil {
		return nil, err
	}
	for _, st := range voteTags {
		add(st.tag, st.points)
	}

	return out, nil
}

type tagPoints struct {
	tag    string
	points float64
}

// forEachTagSum runs a (tag, points) aggregate and merges it into out.
func (d *DB) forEachTagSum(ctx context.Context, query string, points float64, projectID, userID int64, out map[string]float64) error {
	rows, err := d.QueryContext(ctx, query, points, projectID, userID)
	if err != nil {
		return fmt.Errorf("tag reputation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		var pts float64
		if err := rows.Scan(&tag, &pts); err != nil {
			return fmt.Errorf("tag reputation scan: %w", err)
		}
		if t := strings.ToLower(strings.TrimSpace(tag)); t != "" {
			out[t] += pts
		}
	}
	return rows.Err()
}

// solutionTagReputation attributes a user's shipped solutions to their declared
// expertise tags.
//
// solutions.expertise_tags is a comma-separated list, so the rows come back
// un-split and are expanded here. Doing it in SQL with instr/replace tricks would
// be faster and would also make the tag grammar a query language, which is worse
// than one Go loop over the handful of solutions a person has shipped.
func (d *DB) solutionTagReputation(ctx context.Context, projectID, userID int64, points float64) ([]tagPoints, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT s.expertise_tags
		FROM solutions s
		JOIN features f ON f.id = s.feature_id
		WHERE f.project_id = ? AND s.author_id = ?
		  AND s.status IN ('consensus','ready','in_progress','review','shipped')
		  AND s.expertise_tags != ''`, projectID, userID)
	if err != nil {
		return nil, fmt.Errorf("solution tag reputation: %w", err)
	}
	defer rows.Close()
	var out []tagPoints
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("solution tag reputation scan: %w", err)
		}
		for _, t := range splitTagList(raw) {
			out = append(out, tagPoints{tag: t, points: points})
		}
	}
	return out, rows.Err()
}

// solutionVoteTagReputation attributes a user's votes to the tags of the
// solutions they judged.
func (d *DB) solutionVoteTagReputation(ctx context.Context, projectID, userID int64, points float64) ([]tagPoints, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT s.expertise_tags
		FROM pairwise_votes v
		JOIN arenas ar ON ar.id = v.arena_id
		JOIN solutions s ON s.id = v.a
		WHERE ar.project_id = ? AND v.voter_id = ? AND v.outcome != 'skip'
		  AND s.expertise_tags != ''`, projectID, userID)
	if err != nil {
		return nil, fmt.Errorf("solution vote tag reputation: %w", err)
	}
	defer rows.Close()
	var out []tagPoints
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("solution vote tag reputation scan: %w", err)
		}
		for _, t := range splitTagList(raw) {
			out = append(out, tagPoints{tag: t, points: points})
		}
	}
	return out, rows.Err()
}

// splitTagList splits a comma-separated tag list into normalised names, dropping
// empties and duplicates.
//
// Duplicates matter here for the same reason they do in ranking.ParseExpertiseTags,
// and here it is arithmetic rather than lookup: "rust,rust" would otherwise
// contribute its points twice and double one tag's reputation, so a solution that
// happened to repeat its first tag would outrank one that did not.
func splitTagList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		t := strings.ToLower(strings.TrimSpace(p))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
