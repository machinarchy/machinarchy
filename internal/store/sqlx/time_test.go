package sqlx

import (
	"sort"
	"testing"
	"time"
)

// Stored times are compared as text in SQL, so the text must sort like the
// times (AUDIT F14-01: a whole second sorted after the half second following it).
func TestTimeTextSortsLikeTime(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(time.Nanosecond), base.Add(500 * time.Millisecond), base.Add(time.Second),
		base.Add(time.Second + time.Microsecond), base.Add(time.Minute), base.Add(24 * time.Hour)}
	texts := make([]string, len(times))
	for i, v := range times {
		texts[i] = Time(v)
	}
	if !sort.StringsAreSorted(texts) {
		t.Fatalf("text order differs from time order: %v", texts)
	}
	for i, v := range times {
		got, err := ParseTime(texts[i])
		if err != nil || !got.Equal(v) {
			t.Fatalf("ParseTime(%q) = %v, %v; want %v", texts[i], got, err, v)
		}
	}
	// Times written before the fixed width are still readable.
	if got, err := ParseTime("2026-11-25T10:00:00.5Z"); err != nil || !got.Equal(base.Add(500*time.Millisecond)) {
		t.Fatalf("old variable-width form = %v, %v", got, err)
	}
}
