package group

import "strings"

// Normalise replaces the parts of a command's output that differ between hosts
// for no interesting reason, so that "the same result" compares equal
// (docs/cli/spec/03-ux.md 7.2 rule 3):
//
//   - ISO-8601 timestamps, syslog-style timestamps and HH:MM:SS times -> <time>
//   - UUIDs -> <uuid>
//   - process IDs written pid=N, "pid N" or [N] -> pid=<pid>, [<pid>]
//   - 0x... addresses of four or more hex digits -> <addr>
//   - trailing whitespace at the end of a line, and CR before LF
//
// It deliberately does not mask any other digits: version numbers, counts and
// load figures stay visible, because a difference in them is information.
func Normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	normalise(s, func(piece string) { b.WriteString(piece) })

	return b.String()
}

// normalise scans s once and calls emit with the pieces of the normalised
// text in order. It never allocates the whole result, so the aggregator can
// hash it directly.
func normalise(s string, emit func(string)) {
	start := 0
	flush := func(end int) {
		if end > start {
			emit(s[start:end])
		}
	}

	for i := 0; i < len(s); {
		c := s[i]

		switch {
		case c == ' ' || c == '\t' || c == '\r':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r') {
				j++
			}
			if j == len(s) || s[j] == '\n' {
				flush(i)
				start = j
			}
			i = j

			continue

		case isDigit(c) || isHexLetter(c):
			if boundaryBefore(s, i) {
				if end, repl := matchNumberish(s, i); end > i {
					flush(i)
					emit(repl)
					i, start = end, end

					continue
				}
				// A, D and F are hex letters and month initials (Apr, Dec, Feb).
				if isMonthInitial(c) {
					if end := matchSyslog(s, i); end > i {
						flush(i)
						emit("<time>")
						i, start = end, end

						continue
					}
				}
			}

		case c == 'p' || c == 'P':
			if boundaryBefore(s, i) {
				if end := matchPID(s, i); end > i {
					flush(i)
					emit("pid=<pid>")
					i, start = end, end

					continue
				}
			}

		case c == '[':
			if end := matchBracketPID(s, i); end > i {
				flush(i)
				emit("[<pid>]")
				i, start = end, end

				continue
			}

		case isMonthInitial(c):
			if boundaryBefore(s, i) {
				if end := matchSyslog(s, i); end > i {
					flush(i)
					emit("<time>")
					i, start = end, end

					continue
				}
			}
		}

		i++
	}

	flush(len(s))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexLetter(c byte) bool { return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

func isHex(c byte) bool { return isDigit(c) || isHexLetter(c) }

func isWord(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isMonthInitial(c byte) bool {
	switch c {
	case 'J', 'F', 'M', 'A', 'S', 'O', 'N', 'D':
		return true
	}

	return false
}

func boundaryBefore(s string, i int) bool { return i == 0 || !isWord(s[i-1]) }

func boundaryAfter(s string, j int) bool { return j >= len(s) || !isWord(s[j]) }

// matchNumberish tries the patterns that start with a digit or a hex letter at
// a word boundary: UUID, ISO timestamp, HH:MM:SS time and 0x address. It
// returns the end of the match and the replacement, or end <= i for no match.
func matchNumberish(s string, i int) (int, string) {
	if end := matchUUID(s, i); end > i {
		return end, "<uuid>"
	}
	if !isDigit(s[i]) {
		return i, ""
	}
	if end := matchISO(s, i); end > i {
		return end, "<time>"
	}
	if end := matchClock(s, i); end > i && boundaryAfter(s, end) {
		return end, "<time>"
	}
	if end := matchAddress(s, i); end > i {
		return end, "<addr>"
	}

	return i, ""
}

// matchUUID matches 8-4-4-4-12 hex digits.
func matchUUID(s string, i int) int {
	if len(s)-i < 36 {
		return i
	}
	for k, c := range []byte(s[i : i+36]) {
		switch k {
		case 8, 13, 18, 23:
			if c != '-' {
				return i
			}
		default:
			if !isHex(c) {
				return i
			}
		}
	}
	if !boundaryAfter(s, i+36) {
		return i
	}

	return i + 36
}

// matchClock matches dd:dd:dd with an optional fraction, without checking the
// boundary after it.
func matchClock(s string, i int) int {
	if len(s)-i < 8 || !isDigit(s[i]) || !isDigit(s[i+1]) || s[i+2] != ':' ||
		!isDigit(s[i+3]) || !isDigit(s[i+4]) || s[i+5] != ':' || !isDigit(s[i+6]) || !isDigit(s[i+7]) {
		return i
	}
	end := i + 8
	if end+1 < len(s) && (s[end] == '.' || s[end] == ',') && isDigit(s[end+1]) {
		end++
		for end < len(s) && isDigit(s[end]) {
			end++
		}
	}

	return end
}

// matchISO matches yyyy-mm-dd, a T or a space, a clock time, and an optional
// zone (Z, +hh, +hhmm or +hh:mm).
func matchISO(s string, i int) int {
	if len(s)-i < 19 {
		return i
	}
	for k := range 10 {
		c := s[i+k]
		if k == 4 || k == 7 {
			if c != '-' {
				return i
			}
		} else if !isDigit(c) {
			return i
		}
	}
	if s[i+10] != 'T' && s[i+10] != ' ' {
		return i
	}
	end := matchClock(s, i+11)
	if end == i+11 {
		return i
	}

	switch {
	case end < len(s) && s[end] == 'Z':
		end++
	case end+2 < len(s) && (s[end] == '+' || s[end] == '-') && isDigit(s[end+1]) && isDigit(s[end+2]):
		end += 3
		if end < len(s) && s[end] == ':' {
			end++
		}
		for k := 0; k < 2 && end < len(s) && isDigit(s[end]); k++ {
			end++
		}
	}
	if !boundaryAfter(s, end) {
		return i
	}

	return end
}

// matchAddress matches 0x followed by four or more hex digits.
func matchAddress(s string, i int) int {
	if len(s)-i < 6 || s[i] != '0' || (s[i+1] != 'x' && s[i+1] != 'X') {
		return i
	}
	end := i + 2
	for end < len(s) && isHex(s[end]) {
		end++
	}
	if end-(i+2) < 4 || !boundaryAfter(s, end) {
		return i
	}

	return end
}

// matchPID matches "pid" in any case, then "=" or a space, then digits.
func matchPID(s string, i int) int {
	if len(s)-i < 5 {
		return i
	}
	if (s[i+1] != 'i' && s[i+1] != 'I') || (s[i+2] != 'd' && s[i+2] != 'D') {
		return i
	}
	if s[i+3] != '=' && s[i+3] != ' ' {
		return i
	}
	end := i + 4
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == i+4 || !boundaryAfter(s, end) {
		return i
	}

	return end
}

// matchBracketPID matches [digits], as in "sshd[1234]".
func matchBracketPID(s string, i int) int {
	end := i + 1
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == i+1 || end-(i+1) > 10 || end >= len(s) || s[end] != ']' {
		return i
	}

	return end + 1
}

var months = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// matchSyslog matches "Mon  2 15:04:05" style timestamps.
func matchSyslog(s string, i int) int {
	if len(s)-i < 13 {
		return i
	}
	found := false
	for _, month := range months {
		if s[i:i+3] == month {
			found = true

			break
		}
	}
	if !found {
		return i
	}

	j := i + 3
	spaces := 0
	for j < len(s) && s[j] == ' ' {
		j++
		spaces++
	}
	if spaces == 0 || j >= len(s) || !isDigit(s[j]) {
		return i
	}
	j++
	if j < len(s) && isDigit(s[j]) {
		j++
	}
	spaces = 0
	for j < len(s) && s[j] == ' ' {
		j++
		spaces++
	}
	if spaces == 0 {
		return i
	}
	end := matchClock(s, j)
	if end == j || !boundaryAfter(s, end) {
		return i
	}

	return end
}
