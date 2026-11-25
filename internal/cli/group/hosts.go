package group

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// NaturalLess orders names the way people read them: runs of digits compare by
// value, so "web-2" sorts before "web-10". Ties are broken so the order is
// total and deterministic.
func NaturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigit(ca) && isDigit(cb) {
			ie, je := i, j
			for ie < len(a) && isDigit(a[ie]) {
				ie++
			}
			for je < len(b) && isDigit(b[je]) {
				je++
			}
			na, nb := strings.TrimLeft(a[i:ie], "0"), strings.TrimLeft(b[j:je], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			if ie-i != je-j { // same value: fewer leading zeros first
				return ie-i < je-j
			}
			i, j = ie, je

			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	if len(a)-i != len(b)-j {
		return len(a)-i < len(b)-j
	}

	return a < b
}

// SortNatural sorts names in natural order and removes duplicates.
func SortNatural(names []string) []string {
	out := append([]string(nil), names...)
	sort.Slice(out, func(i, j int) bool { return NaturalLess(out[i], out[j]) })

	unique := out[:0]
	for i, name := range out {
		if i == 0 || name != out[i-1] {
			unique = append(unique, name)
		}
	}

	return unique
}

// CompressHosts writes a host list as short as it can be while staying exact:
// names that differ only in one number become a bracketed range list, so
// web-01 ... web-12 and web-14 read web-[01-12,14]. Zero padding is kept,
// names that do not follow a pattern are listed whole, and the list is cut at
// width (when width > 0) with the number of hosts left out: "(+37 more)".
//
// The result does not depend on the order of names.
func CompressHosts(names []string, width int) string {
	items := buildItems(SortNatural(names))
	if len(items) == 0 {
		return ""
	}

	total := 0
	texts := make([]string, len(items))
	for i, it := range items {
		total += it.hosts
		texts[i] = it.text()
	}
	if full := strings.Join(texts, ", "); width <= 0 || len(full) <= width {
		return full
	}

	var parts []string
	used, shown := 0, 0
	for _, it := range items {
		sep := 0
		if len(parts) > 0 {
			sep = 2
		}

		text, hosts := it.text(), it.hosts
		if used+sep+len(text)+len(more(total-shown-hosts)) > width {
			// Shorten a range list so at least part of this item fits.
			text, hosts = "", 0
			for k := len(it.ranges) - 1; it.pattern() && k >= 1; k-- {
				candidate, count := it.partial(k)
				if used+sep+len(candidate)+len(more(total-shown-count)) <= width {
					text, hosts = candidate, count

					break
				}
			}
			if text == "" && len(parts) == 0 { // always show something
				text, hosts = it.text(), it.hosts
				if it.pattern() {
					text, hosts = it.partial(1)
				}
			}
			if text == "" {
				break
			}
			parts = append(parts, text)
			shown += hosts

			break
		}

		parts = append(parts, text)
		used += sep + len(text)
		shown += hosts
	}

	return strings.Join(parts, ", ") + more(total-shown)
}

func more(n int) string {
	if n <= 0 {
		return ""
	}

	return fmt.Sprintf(" (+%d more)", n)
}

type numRange struct{ lo, hi int }

// item is one entry of a compressed list: a whole name or a pattern with
// ranges.
type item struct {
	first          string // smallest member, for ordering
	hosts          int
	whole          string
	prefix, suffix string
	pad            int
	ranges         []numRange
}

func (it item) pattern() bool { return it.whole == "" }

func (it item) text() string {
	if !it.pattern() {
		return it.whole
	}
	text, _ := it.partial(len(it.ranges))

	return text
}

// partial renders the first k ranges and the number of hosts they cover.
func (it item) partial(k int) (string, int) {
	pieces := make([]string, 0, k)
	count := 0
	for _, r := range it.ranges[:k] {
		if r.lo == r.hi {
			pieces = append(pieces, it.format(r.lo))
			count++

			continue
		}
		pieces = append(pieces, it.format(r.lo)+"-"+it.format(r.hi))
		count += r.hi - r.lo + 1
	}

	return it.prefix + "[" + strings.Join(pieces, ",") + "]" + it.suffix, count
}

func (it item) format(n int) string {
	if it.pad > 0 {
		return fmt.Sprintf("%0*d", it.pad, n)
	}

	return strconv.Itoa(n)
}

type member struct {
	name   string
	digits string
	num    int
}

type patternKey struct{ prefix, suffix string }

func buildItems(sorted []string) []item {
	var items []item
	groups := map[patternKey][]member{}
	var keys []patternKey

	for _, name := range sorted {
		start, end := lastDigitRun(name)
		if start < 0 || end-start > 15 {
			items = append(items, item{first: name, hosts: 1, whole: name})

			continue
		}
		digits := name[start:end]
		num, err := strconv.Atoi(digits)
		if err != nil {
			items = append(items, item{first: name, hosts: 1, whole: name})

			continue
		}
		key := patternKey{prefix: name[:start], suffix: name[end:]}
		if _, seen := groups[key]; !seen {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], member{name: name, digits: digits, num: num})
	}

	for _, key := range keys {
		items = append(items, groupItems(key, groups[key])...)
	}
	sort.Slice(items, func(i, j int) bool { return NaturalLess(items[i].first, items[j].first) })

	return items
}

// groupItems turns the members that share a prefix and suffix into items. A
// zero-padded family (01, 02, ... 12) keeps its padding; numbers without
// padding form their own family; anything that fits neither is listed whole.
func groupItems(key patternKey, members []member) []item {
	pad := 0
	for _, m := range members {
		if len(m.digits) > 1 && m.digits[0] == '0' {
			pad = max(pad, len(m.digits))
		}
	}

	var padded, plain, odd []member
	for _, m := range members {
		leadingZero := len(m.digits) > 1 && m.digits[0] == '0'
		switch {
		case pad > 0 && len(m.digits) == pad:
			padded = append(padded, m)
		case !leadingZero:
			plain = append(plain, m)
		default:
			odd = append(odd, m)
		}
	}

	var items []item
	for _, m := range odd {
		items = append(items, item{first: m.name, hosts: 1, whole: m.name})
	}
	items = append(items, family(key, padded, pad)...)
	items = append(items, family(key, plain, 0)...)

	return items
}

func family(key patternKey, members []member, pad int) []item {
	switch len(members) {
	case 0:
		return nil
	case 1:
		return []item{{first: members[0].name, hosts: 1, whole: members[0].name}}
	}

	sort.Slice(members, func(i, j int) bool { return members[i].num < members[j].num })

	var ranges []numRange
	for _, m := range members {
		if n := len(ranges); n > 0 && m.num == ranges[n-1].hi+1 {
			ranges[n-1].hi = m.num

			continue
		}
		ranges = append(ranges, numRange{lo: m.num, hi: m.num})
	}

	return []item{{first: members[0].name, hosts: len(members), prefix: key.prefix, suffix: key.suffix, pad: pad, ranges: ranges}}
}

// lastDigitRun returns the bounds of the last run of digits in name, or -1.
func lastDigitRun(name string) (int, int) {
	end := len(name)
	for end > 0 && !isDigit(name[end-1]) {
		end--
	}
	if end == 0 {
		return -1, -1
	}
	start := end
	for start > 0 && isDigit(name[start-1]) {
		start--
	}

	return start, end
}
