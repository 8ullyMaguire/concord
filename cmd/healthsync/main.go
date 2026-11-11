// Command healthsync measures each project's upstream repository and writes the result
// through the EXISTING store.UpdateProjectMetrics (R9, spec §4.5.7.1–.2).
//
// Three rules decide its shape:
//
//  1. ONE scorer. The health weights, the last_commit_at derivation and the computed_at stamp
//     live in discovery/health.go and the store. This command fetches; it does not score.
//  2. A FAILED FETCH WRITES NOTHING. Unknown health is not zero health -- a transient 502 that
//     wrote zeros would rank the whole catalog `avoid` for an hour.
//  3. A FIELD THAT CANNOT BE MEASURED SKIPS THE PROJECT. `Metrics.MedianReviewHours == 0` means
//     "reviewed instantly", which is the best possible band; writing it because no review could
//     be read is a fabricated number that inflates the score. So a repository with no review
//     sample is reported as SKIP, not synced with a zero.
//
// It is a one-shot command under a systemd timer rather than a goroutine in the server: the
// server restarts on every deploy, and a background loop inside it would lose its schedule and
// re-fetch the whole catalog on each restart.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// snapshot is what one repository reported. Every field is REQUIRED: the fetch returns an error
// rather than leaving one at zero, so the caller can never write a zero it did not measure.
type snapshot struct {
	Stars, Forks, OpenIssues int
	Contributors             int
	Releases90d              int
	Commits                  int
	License                  string
	PushedAt                 time.Time
	MedianReviewHours        float64
	ReviewSample             int
	// PRs is how many pull requests were sampled. It is what separates "no PRs, so nothing to
	// measure" from "PRs exist and nobody answered", which look identical in ReviewSample.
	PRs int
}

func main() {
	var (
		dbPath = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		dryRun = flag.Bool("dry-run", false, "fetch and print, write nothing")
		only   = flag.String("only", "", "comma-separated slugs")
		rate   = flag.Duration("rate", time.Second, "minimum gap between requests")
	)
	flag.Parse()

	cfg := config.Load()
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		fatal("open db %s: %v", cfg.DBPath, err)
	}
	defer sqlDB.Close()
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		fatal("migrate: %v", err)
	}
	st := store.New(sqlDB)
	ctx := context.Background()

	projects, err := st.ListProjectsWithRepo(ctx)
	if err != nil {
		fatal("%v", err)
	}
	// A green run over zero repositories is the failure mode this command exists to make
	// visible: it would print "synced 0" and every downstream surface would still be blank.
	if len(projects) == 0 {
		fatal("no project has a stated repository; run cmd/forgeseed first (a sync of nothing is not a sync)")
	}
	if *only != "" {
		want := map[string]bool{}
		for _, s := range strings.Split(*only, ",") {
			want[strings.TrimSpace(s)] = true
		}
		filtered := projects[:0]
		for _, p := range projects {
			if want[p.Slug] {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
		if len(projects) == 0 {
			fatal("-only named no project that has a repository")
		}
	}

	f := newFetcher(os.Getenv("CONCORD_FORGE_TOKEN"), *rate)
	synced, skipped, failed := 0, 0, 0
	for _, p := range projects {
		fields, err := store.ParseRepoURL(p.RepoURL)
		if err != nil {
			// The column is written through ParseRepoURL, so this is a corrupt row rather
			// than a user error: report it, do not guess a repository.
			fmt.Printf("FAILED %s: stored repo_url does not parse: %v\n", p.Slug, err)
			failed++
			continue
		}
		snap, err := f.snapshot(ctx, p.RepoHost, fields)
		if err != nil {
			fmt.Printf("FAILED %s: %v\n", p.Slug, err)
			failed++
			continue
		}
		// No review sample is NOT a reason to skip the project: recency, breadth and cadence
		// were measured, and they are three quarters of the score. The responsiveness term is
		// passed as unknown so HealthScore excludes it and renormalises, rather than writing a
		// zero that would read as "reviewed instantly" -- which is what this command did until
		// the first real run showed five of six repositories with NO pull requests at all.
		reviewHours := snap.MedianReviewHours
		verdict := "review latency measured"
		if snap.ReviewSample == 0 {
			reviewHours = discovery.UnknownReviewHours
			verdict = fmt.Sprintf("review latency unknown (%d pull request(s) sampled, none answered)",
				snap.PRs)
		}
		m := discovery.Metrics{
			LastCommitAgeDays: time.Since(snap.PushedAt).Hours() / 24,
			MedianReviewHours: reviewHours,
			Contributors:      snap.Contributors,
			Releases90d:       snap.Releases90d,
		}
		// R12 / §4.5.7.2: the newcomer responsiveness is computed from the LOCAL
		// event log, not from the forge. It is read BEFORE the write so a database
		// error skips the project entirely rather than leaving half a measurement
		// landed -- the same rule as a failed fetch writing nothing.
		events, err := st.ListForgeEvents(ctx, fields.Full)
		if err != nil {
			fmt.Printf("FAILED %s: read forge events: %v\n", p.Slug, err)
			failed++
			continue
		}
		newcomerHours, newcomerN, newcomerOK := discovery.NewcomerResponse(events)
		var published *float64
		if newcomerOK {
			published = &newcomerHours
		}
		// Both the median and its sample are stated: a project at 3 of the required
		// 5 is ABSENT from the leaderboard, and the sample is what explains why. A
		// bare NULL would be indistinguishable from a project whose webhook has
		// never fired.
		newcomerVerdict := fmt.Sprintf("newcomer response unknown (n=%d, floor=%d)",
			newcomerN, discovery.NewcomerMinSample)
		if newcomerOK {
			newcomerVerdict = fmt.Sprintf("newcomer response %.1fh (n=%d)", newcomerHours, newcomerN)
		}
		fmt.Printf("%s %s %s last_commit_age=%.1fd contributors=%d releases90d=%d %s; %s\n",
			p.Slug, p.RepoHost, fields.Full, m.LastCommitAgeDays, m.Contributors, m.Releases90d,
			verdict, newcomerVerdict)
		if *dryRun {
			synced++
			continue
		}
		if _, err := st.UpdateProjectMetrics(ctx, p.Slug, m,
			snap.Stars, snap.Forks, snap.OpenIssues, snap.Commits, snap.License); err != nil {
			fmt.Printf("FAILED %s: write: %v\n", p.Slug, err)
			failed++
			continue
		}
		if err := st.UpdateProjectNewcomer(ctx, p.Slug, published, newcomerN); err != nil {
			fmt.Printf("FAILED %s: write newcomer: %v\n", p.Slug, err)
			failed++
			continue
		}
		synced++
	}
	fmt.Printf("synced %d, skipped %d, failed %d of %d\n", synced, skipped, failed, len(projects))
	if failed > 0 {
		os.Exit(1)
	}
	if synced == 0 {
		// Nothing measured is not success. Exit non-zero so the timer's journal shows it.
		fmt.Fprintln(os.Stderr, "no project could be measured")
		os.Exit(1)
	}
}

type fetcher struct {
	client *http.Client
	token  string
	rate   time.Duration
	last   time.Time
}

func newFetcher(token string, rate time.Duration) *fetcher {
	return &fetcher{
		client: &http.Client{Timeout: 30 * time.Second},
		token:  token,
		rate:   rate,
	}
}

// get performs one GET with the politeness delay and the token, and decodes into out.
func (f *fetcher) get(ctx context.Context, url string, out any) error {
	if f.rate > 0 {
		if wait := f.rate - time.Since(f.last); wait > 0 {
			time.Sleep(wait)
		}
	}
	f.last = time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "token "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s -> %s: %s", url, resp.Status, firstLine(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s: decode: %w", url, err)
		}
	}
	return nil
}

// getTotal reads a collection's total from the headers each forge uses, falling back to the
// length of the page it was given.
func (f *fetcher) getTotal(ctx context.Context, url string) (int, error) {
	if f.rate > 0 {
		if wait := f.rate - time.Since(f.last); wait > 0 {
			time.Sleep(wait)
		}
	}
	f.last = time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "token "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s -> %s", url, resp.Status)
	}
	if n := resp.Header.Get("X-Total-Count"); n != "" {
		return strconv.Atoi(n)
	}
	// GitHub answers the count in the Link rel="last" page number.
	for _, part := range strings.Split(resp.Header.Get("Link"), ",") {
		if !strings.Contains(part, `rel="last"`) {
			continue
		}
		start := strings.Index(part, "page=")
		if start < 0 {
			continue
		}
		rest := part[start+len("page="):]
		if end := strings.IndexAny(rest, "&>"); end >= 0 {
			rest = rest[:end]
		}
		if n, err := strconv.Atoi(rest); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s: no total in headers", url)
}

func (f *fetcher) snapshot(ctx context.Context, host string, r store.RepoFields) (snapshot, error) {
	switch host {
	case store.RepoHostGithub:
		return f.github(ctx, r)
	case store.RepoHostForgejo, store.RepoHostCodeberg:
		if host == store.RepoHostForgejo {
			return f.gitea(ctx, "https://git.polarisocial.xyz", r)
		}
		return f.gitea(ctx, "https://codeberg.org", r)
	}
	return snapshot{}, fmt.Errorf("no fetcher for host %q", host)
}

// ---------------------------------------------------------------- github

func (f *fetcher) github(ctx context.Context, r store.RepoFields) (snapshot, error) {
	var snap snapshot
	base := "https://api.github.com/repos/" + r.Full

	var repo struct {
		Stars    int       `json:"stargazers_count"`
		Forks    int       `json:"forks_count"`
		Issues   int       `json:"open_issues_count"`
		PushedAt time.Time `json:"pushed_at"`
		License  *struct {
			SPDX string `json:"spdx_id"`
		} `json:"license"`
	}
	if err := f.get(ctx, base, &repo); err != nil {
		return snap, err
	}
	snap.Stars, snap.Forks, snap.OpenIssues, snap.PushedAt = repo.Stars, repo.Forks, repo.Issues, repo.PushedAt
	if repo.License != nil && repo.License.SPDX != "NOASSERTION" {
		snap.License = repo.License.SPDX
	}

	var contributors []struct {
		Login string `json:"login"`
	}
	if err := f.get(ctx, base+"/contributors?per_page=100&anon=0", &contributors); err != nil {
		return snap, fmt.Errorf("contributors: %w", err)
	}
	// The page is capped at 100 by the API, so a full page is a FLOOR, not a count. The scorer
	// rewards breadth, so publishing 100 for a 3,000-contributor project understates it -- which
	// is the safe direction, and it is recorded rather than silently rounded.
	snap.Contributors = len(contributors)

	var releases []struct {
		PublishedAt *time.Time `json:"published_at"`
	}
	if err := f.get(ctx, base+"/releases?per_page=100", &releases); err != nil {
		return snap, fmt.Errorf("releases: %w", err)
	}
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	for _, rel := range releases {
		if rel.PublishedAt != nil && rel.PublishedAt.After(cutoff) {
			snap.Releases90d++
		}
	}

	commits, err := f.getTotal(ctx, base+"/commits?per_page=1")
	if err != nil {
		return snap, fmt.Errorf("commits: %w", err)
	}
	snap.Commits = commits

	n := 3
	if f.token != "" {
		n = 10
	}
	hours, sample, prs, err := f.githubReviewHours(ctx, base, n)
	if err != nil {
		return snap, err
	}
	snap.MedianReviewHours, snap.ReviewSample, snap.PRs = hours, sample, prs
	return snap, nil
}

// githubReviewHours returns the median hours from a PR being opened to its FIRST review, over
// the most recently updated PRs. A PR with no review is not a fast response, it is no response:
// it contributes nothing, and the sample size is reported so the median can be read honestly.
func (f *fetcher) githubReviewHours(ctx context.Context, base string, n int) (float64, int, int, error) {
	var prs []struct {
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := f.get(ctx, fmt.Sprintf("%s/pulls?state=all&per_page=%d&sort=updated", base, n), &prs); err != nil {
		return 0, 0, 0, fmt.Errorf("pulls: %w", err)
	}
	hours := []float64{}
	for _, pr := range prs {
		var reviews []struct {
			SubmittedAt time.Time `json:"submitted_at"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := f.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews?per_page=100", base, pr.Number), &reviews); err != nil {
			return 0, 0, 0, fmt.Errorf("reviews of #%d: %w", pr.Number, err)
		}
		// A REVIEW is not the only way somebody answers a pull request; on most projects of this
		// size the first response is a COMMENT on the PR. Counting only formal reviews left five
		// of six repositories unmeasurable on the live instance -- the metric was not missing, the
		// fetch was looking in one place. Comments are read from the issue endpoint, which is
		// where PR comments live in the GitHub API.
		var comments []struct {
			CreatedAt time.Time `json:"created_at"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := f.get(ctx, fmt.Sprintf("%s/issues/%d/comments?per_page=100", base, pr.Number), &comments); err != nil {
			return 0, 0, 0, fmt.Errorf("comments of #%d: %w", pr.Number, err)
		}

		// The EARLIEST response by SOMEONE ELSE. A self-comment is not a response, and counting
		// it would publish a responsiveness no contributor experienced.
		var first time.Time
		consider := func(at time.Time, who string) {
			if at.IsZero() || at.Before(pr.CreatedAt) || who == "" || who == pr.User.Login {
				return
			}
			if first.IsZero() || at.Before(first) {
				first = at
			}
		}
		for _, rv := range reviews {
			consider(rv.SubmittedAt, rv.User.Login)
		}
		for _, c := range comments {
			consider(c.CreatedAt, c.User.Login)
		}
		if first.IsZero() {
			continue
		}
		hours = append(hours, first.Sub(pr.CreatedAt).Hours())
	}
	return median(hours), len(hours), len(prs), nil
}

// ---------------------------------------------------------------- forgejo / codeberg

func (f *fetcher) gitea(ctx context.Context, root string, r store.RepoFields) (snapshot, error) {
	var snap snapshot
	base := root + "/api/v1/repos/" + r.Full

	var repo struct {
		Stars    int       `json:"stars_count"`
		Forks    int       `json:"forks_count"`
		Issues   int       `json:"open_issues_count"`
		Updated  time.Time `json:"updated_at"`
		PushedAt time.Time `json:"pushed_at"`
	}
	if err := f.get(ctx, base, &repo); err != nil {
		return snap, err
	}
	snap.Stars, snap.Forks, snap.OpenIssues = repo.Stars, repo.Forks, repo.Issues
	snap.PushedAt = repo.PushedAt
	if snap.PushedAt.IsZero() {
		snap.PushedAt = repo.Updated
	}

	// Gitea has no contributors endpoint on every version, so distinct commit authors is the
	// measure: it is the same population the scorer means by "breadth", read from commits.
	var commitsPage []struct {
		Author struct {
			Login string `json:"login"`
			Name  string `json:"name"`
		} `json:"author"`
	}
	if err := f.get(ctx, base+"/commits?limit=50", &commitsPage); err != nil {
		return snap, fmt.Errorf("commits: %w", err)
	}
	authors := map[string]bool{}
	for _, c := range commitsPage {
		who := c.Author.Login
		if who == "" {
			who = c.Author.Name
		}
		if who != "" {
			authors[who] = true
		}
	}
	snap.Contributors = len(authors)

	var releases []struct {
		PublishedAt *time.Time `json:"published_at"`
		CreatedAt   time.Time  `json:"created_at"`
	}
	if err := f.get(ctx, base+"/releases?limit=50", &releases); err != nil {
		return snap, fmt.Errorf("releases: %w", err)
	}
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	for _, rel := range releases {
		at := rel.CreatedAt
		if rel.PublishedAt != nil {
			at = *rel.PublishedAt
		}
		if at.After(cutoff) {
			snap.Releases90d++
		}
	}

	commits, err := f.getTotal(ctx, base+"/commits?limit=1")
	if err != nil {
		return snap, fmt.Errorf("commit count: %w", err)
	}
	snap.Commits = commits

	n := 3
	if f.token != "" {
		n = 10
	}
	hours, sample, prs, err := f.giteaReviewHours(ctx, base, n)
	if err != nil {
		return snap, err
	}
	snap.MedianReviewHours, snap.ReviewSample, snap.PRs = hours, sample, prs
	return snap, nil
}

func (f *fetcher) giteaReviewHours(ctx context.Context, base string, n int) (float64, int, int, error) {
	var prs []struct {
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := f.get(ctx, fmt.Sprintf("%s/pulls?state=all&limit=%d", base, n), &prs); err != nil {
		return 0, 0, 0, fmt.Errorf("pulls: %w", err)
	}
	hours := []float64{}
	for _, pr := range prs {
		var reviews []struct {
			SubmittedAt time.Time `json:"submitted_at"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := f.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews", base, pr.Number), &reviews); err != nil {
			return 0, 0, 0, fmt.Errorf("reviews of #%d: %w", pr.Number, err)
		}
		// Same reason as the GitHub fetch: the first answer to a PR on a small forge is a
		// comment far more often than a formal review.
		var comments []struct {
			CreatedAt time.Time `json:"created_at"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := f.get(ctx, fmt.Sprintf("%s/issues/%d/comments", base, pr.Number), &comments); err != nil {
			return 0, 0, 0, fmt.Errorf("comments of #%d: %w", pr.Number, err)
		}
		var first time.Time
		consider := func(at time.Time, who string) {
			if at.IsZero() || at.Before(pr.CreatedAt) || who == "" || who == pr.User.Login {
				return
			}
			if first.IsZero() || at.Before(first) {
				first = at
			}
		}
		for _, rv := range reviews {
			consider(rv.SubmittedAt, rv.User.Login)
		}
		for _, c := range comments {
			consider(c.CreatedAt, c.User.Login)
		}
		if first.IsZero() {
			continue
		}
		hours = append(hours, first.Sub(pr.CreatedAt).Hours())
	}
	return median(hours), len(hours), len(prs), nil
}

// median takes the lower-middle of an even sample, deliberately: interpolating between two
// response times invents a duration nobody experienced, and this number is published.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[(len(s)-1)/2]
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
