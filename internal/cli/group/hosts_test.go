package group

import (
	"math/rand"
	"strings"
	"testing"
)

func TestNaturalLess(t *testing.T) {
	t.Parallel()

	ordered := []string{"a", "app-1", "app-2", "app-10", "app-010", "app-11", "db-01", "db-primary", "web", "web-1", "web-01x"}
	for i := range ordered {
		for j := range ordered {
			want := i < j
			if got := NaturalLess(ordered[i], ordered[j]); got != want {
				t.Errorf("NaturalLess(%q, %q) = %v, want %v", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestSortNaturalDeduplicates(t *testing.T) {
	t.Parallel()

	got := SortNatural([]string{"web-10", "web-2", "web-2", "web-1"})
	want := []string{"web-1", "web-2", "web-10"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SortNatural = %v, want %v", got, want)
	}
}

// G-10 and G-11.
func TestCompressHosts(t *testing.T) {
	t.Parallel()

	seq := func(prefix string, width int, nums ...int) []string {
		out := make([]string, 0, len(nums))
		for _, n := range nums {
			out = append(out, hostName(prefix, n, width))
		}

		return out
	}
	rangeOf := func(lo, hi int) []int {
		var out []int
		for i := lo; i <= hi; i++ {
			out = append(out, i)
		}

		return out
	}

	tests := []struct {
		name  string
		hosts []string
		width int
		want  string
	}{
		{"padded range with a gap", seq("web", 2, append(rangeOf(1, 12), 14)...), 0, "web-[01-12,14]"},
		{"unpadded numbers", []string{"app-1", "app-2", "app-10"}, 0, "app-[1-2,10]"},
		{"single host stays whole", []string{"web-01"}, 0, "web-01"},
		{"names without numbers", []string{"db-primary", "a"}, 0, "a, db-primary"},
		{"mixed patterns and whole names", []string{"a", "db-primary", "db-01", "web-01", "web-02", "web-03"}, 0, "a, db-01, db-primary, web-[01-03]"},
		{"non consecutive pair", []string{"web-01", "web-03"}, 0, "web-[01,03]"},
		{"padded and unpadded of the same prefix", []string{"web-1", "web-01", "web-02"}, 0, "web-1, web-[01-02]"},
		{"suffix after the number", []string{"n1.example", "n2.example", "n3.example"}, 0, "n[1-3].example"},
		{"two families", []string{"a-1", "a-2", "b-1", "b-2", "b-3"}, 0, "a-[1-2], b-[1-3]"},
		{"order and duplicates do not matter", []string{"web-03", "web-01", "web-02", "web-02"}, 0, "web-[01-03]"},
		{"large range is short", seq("app", 4, rangeOf(1, 5000)...), 20, "app-[0001-5000]"},
		{"empty", nil, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := CompressHosts(tc.hosts, tc.width); got != tc.want {
				t.Fatalf("CompressHosts(%v) = %q, want %q", tc.hosts, got, tc.want)
			}
		})
	}
}

func TestCompressHostsCutsAtWidth(t *testing.T) {
	t.Parallel()

	var hosts []string
	for i := 1; i <= 40; i += 2 { // 20 hosts, no two consecutive
		hosts = append(hosts, hostName("web", i, 2))
	}

	got := CompressHosts(hosts, 40)
	if len(got) > 40 {
		t.Fatalf("result %q is %d long, want at most 40", got, len(got))
	}
	if !strings.HasPrefix(got, "web-[01,03,05") || !strings.HasSuffix(got, " more)") {
		t.Fatalf("unexpected cut %q", got)
	}
	if !strings.Contains(got, "(+") {
		t.Fatalf("a cut list must say how many hosts are left out: %q", got)
	}
}

func TestCompressHostsCutsBetweenItemsAndCountsHosts(t *testing.T) {
	t.Parallel()

	var hosts []string
	for i := 1; i <= 60; i++ {
		hosts = append(hosts, hostName("db", i, 2), hostName("web", i, 2), hostName("cache", i, 2))
	}
	hosts = append(hosts, "zzz-1", "zzz-5")

	got := CompressHosts(hosts, 40)
	if len(got) > 40 {
		t.Fatalf("result %q is %d long", got, len(got))
	}
	// 182 hosts: cache and db are shown (120), web and zzz are counted.
	if got != "cache-[01-60], db-[01-60] (+62 more)" {
		t.Fatalf("unexpected cut %q", got)
	}
}

func TestCompressHostsAlwaysShowsSomething(t *testing.T) {
	t.Parallel()

	got := CompressHosts([]string{"a-very-long-host-name-that-does-not-fit-1", "a-very-long-host-name-that-does-not-fit-3"}, 10)
	if got == "" || !strings.HasPrefix(got, "a-very-long-host-name-that-does-not-fit-[") {
		t.Fatalf("got %q", got)
	}
}

func TestCompressHostsIsOrderIndependent(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(7)) // #nosec G404 -- deterministic test data
	var hosts []string
	for i := 1; i <= 300; i++ {
		if i%7 != 0 {
			hosts = append(hosts, hostName("node", i, 3))
		}
	}
	want := CompressHosts(hosts, 60)
	for range 20 {
		rng.Shuffle(len(hosts), func(i, j int) { hosts[i], hosts[j] = hosts[j], hosts[i] })
		if got := CompressHosts(hosts, 60); got != want {
			t.Fatalf("order changed the result:\n%q\n%q", got, want)
		}
	}
}
