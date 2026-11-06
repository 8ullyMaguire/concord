package discovery

import "sort"

// Latency is a duration distribution, never a mean. Spec §3.2 requires the median
// because a mean is moved by one outlier -- a single complaint that sat for a
// year would drag the average up and make a fast instance look slow, and the
// number exists to tell the reader how fast the instance actually is.
//
// THREE counts, not two. The plan's STAT-2 wrote `N` and `Unfinished`, which
// forces a row that is finished but was never measured (a seeded feature inserted
// directly as `shipped`, with no audit row recording its transition) into one of
// the two buckets. Putting it in `Unfinished` reports a shipped feature as still
// in progress -- the same class of lie §2 forbids, one row-shape over. So it gets
// its own count and is visible as such.
type Latency struct {
	MedianHours float64 `json:"median_hours"`
	N           int     `json:"n"`
	Unfinished  int     `json:"unfinished"`
	Unmeasured  int     `json:"unmeasured"`
	Unit        string  `json:"unit"`
}

// Episode is one entity's own timeline, with the two ends optional.
//
// The optionals are the whole point. A row can be:
//
//	HasStart && HasEnd    measured     -- contributes to N
//	HasStart && !HasEnd   unfinished   -- still open, contributes to Unfinished
//	!HasStart && HasEnd   unmeasured   -- finished, but no recorded start
//	!HasStart && !HasEnd  nothing      -- a row with neither end; not counted,
//	                                     because there is no fact to report
//
// The last case is why this is a struct and not two slices: the caller decides
// which of the four a row is, from the audit trail, rather than from whether a
// value happened to be NULL.
type Episode struct {
	StartedAt float64
	EndedAt   float64
	HasStart  bool
	HasEnd    bool
}

// Measure reduces episodes to a Latency.
//
// Rules the spec fixes and this enforces:
//
//   - An even sample uses the LOWER-middle value, never an interpolation. With
//     two samples {10, 20} the median is 10, not 15: the published number must be
//     a value the instance actually produced, because a reader comparing it to
//     their own instance expects a duration that occurred, not an average of two.
//   - A row with no start is never a zero-hour episode. Absent is not fast.
//   - A negative duration is clamped to zero and counted. It cannot arise from a
//     consistent audit trail (it would mean an entity finished before it started);
//     clamping keeps one corrupt row from dragging the median negative, and the
//     count still shows in N so the row is not hidden.
func Measure(episodes []Episode) Latency {
	lat := Latency{Unit: "hours"}
	durations := make([]float64, 0, len(episodes))
	for _, e := range episodes {
		switch {
		case e.HasStart && e.HasEnd:
			h := (e.EndedAt - e.StartedAt) / 3600.0
			if h < 0 {
				h = 0
			}
			durations = append(durations, h)
		case e.HasStart && !e.HasEnd:
			lat.Unfinished++
		case !e.HasStart && e.HasEnd:
			lat.Unmeasured++
		}
	}
	lat.N = len(durations)
	if lat.N > 0 {
		lat.MedianHours = MedianLowerMiddle(durations)
	}
	return lat
}

// MedianLowerMiddle returns the lower of the two middle values of an even-length
// sample. The input is sorted in place.
func MedianLowerMiddle(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	sort.Float64s(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return xs[n/2-1]
}
