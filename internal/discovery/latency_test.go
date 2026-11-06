package discovery

import "testing"

func ep(start, end float64, hasStart, hasEnd bool) Episode {
	return Episode{StartedAt: start, EndedAt: end, HasStart: hasStart, HasEnd: hasEnd}
}

// The spec fixes the lower-middle for even samples. With {10h, 20h} the median is
// 10, not 15 -- the published number must be a duration the instance produced.
func TestMeasureMedianIsLowerMiddleForEvenSamples(t *testing.T) {
	lat := Measure([]Episode{
		ep(0, 10*3600, true, true),
		ep(0, 20*3600, true, true),
	})
	if lat.N != 2 {
		t.Fatalf("N = %d, want 2", lat.N)
	}
	if lat.MedianHours != 10 {
		t.Fatalf("MedianHours = %v, want 10 (lower-middle, not the mean 15)", lat.MedianHours)
	}
	if lat.Unit != "hours" {
		t.Fatalf("Unit = %q, want hours", lat.Unit)
	}

	lat = Measure([]Episode{
		ep(0, 5*3600, true, true),
		ep(0, 10*3600, true, true),
		ep(0, 20*3600, true, true),
		ep(0, 30*3600, true, true),
	})
	if lat.MedianHours != 10 {
		t.Fatalf("MedianHours = %v, want 10 for {5,10,20,30}", lat.MedianHours)
	}
}

// An unfinished row must not enter the sample. If it did, with a zero duration,
// the median of {0, 100} would be 0 -- reporting a still-open complaint as a
// zero-hour triage, which is the "zero-day" lie spec §3.2 forbids.
func TestMeasureUnfinishedIsNotZero(t *testing.T) {
	lat := Measure([]Episode{
		ep(0, 100*3600, true, true),
		ep(0, 0, true, false),
	})
	if lat.N != 1 {
		t.Fatalf("N = %d, want 1", lat.N)
	}
	if lat.MedianHours != 100 {
		t.Fatalf("MedianHours = %v, want 100", lat.MedianHours)
	}
	if lat.Unfinished != 1 {
		t.Fatalf("Unfinished = %d, want 1", lat.Unfinished)
	}
}

// A finished row with no recorded start is NOT unfinished. Counting it as
// unfinished would report a shipped feature as still in progress.
func TestMeasureUnmeasuredIsNotUnfinished(t *testing.T) {
	lat := Measure([]Episode{
		ep(0, 50*3600, true, true),
		ep(0, 99, false, true),
	})
	if lat.N != 1 {
		t.Fatalf("N = %d, want 1", lat.N)
	}
	if lat.Unmeasured != 1 {
		t.Fatalf("Unmeasured = %d, want 1", lat.Unmeasured)
	}
	if lat.Unfinished != 0 {
		t.Fatalf("Unfinished = %d, want 0 -- a finished row is not unfinished", lat.Unfinished)
	}
}

// A row with neither end is not a fact about anything, so it lands in no bucket.
func TestMeasureNeitherEndIsCounted(t *testing.T) {
	lat := Measure([]Episode{{}})
	if lat.N != 0 || lat.Unfinished != 0 || lat.Unmeasured != 0 {
		t.Fatalf("empty episode counted: %+v", lat)
	}
}

// An empty instance publishes zeros and n=0, never a fabricated median.
func TestMeasureEmptyIsZero(t *testing.T) {
	lat := Measure(nil)
	if lat.MedianHours != 0 || lat.N != 0 {
		t.Fatalf("empty Measure = %+v, want zeros", lat)
	}
	if lat.Unit != "hours" {
		t.Fatalf("Unit = %q, want hours even when empty", lat.Unit)
	}
}
