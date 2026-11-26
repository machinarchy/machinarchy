package ui

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// redrawEvery is the shortest time between two redraws of the live line.
	redrawEvery = 500 * time.Millisecond
	// plainEvery is the longest time between two progress lines in plain mode.
	plainEvery = 10 * time.Second
	// barWidth is the number of cells of the progress bar.
	barWidth = 20
)

// Counts is the state of a run that progress shows.
type Counts struct {
	// Done is the number of hosts that reached a final state.
	Done int
	// Total is the number of hosts in the run.
	Total int
	// OK is the number of hosts that succeeded.
	OK int
	// Failed is the number of hosts that did not succeed and are final.
	Failed int
	// Running is the number of hosts still working.
	Running int
}

// Progress shows how far a run has come (docs/cli/spec/03-ux.md 7.2 rule 9).
//
// On a terminal in rich mode it redraws one line, at most twice a second. On a
// pipe, in a log, or with --plain it never redraws: it prints a new line every
// ten seconds and each time another tenth of the hosts is done, so a CI log or
// a screen reader reads linear text. Progress goes to the writer it is given,
// which the caller makes stderr; the result goes to stdout.
type Progress struct {
	out    io.Writer
	style  Style
	live   bool
	clock  func() time.Time
	start  time.Time
	last   time.Time
	decile int
	shown  Counts
	drawn  bool
	closed bool
}

// NewProgress creates a Progress. tty says whether out is an interactive
// terminal; the live redraw needs a terminal and rich mode. clock is time.Now
// in production.
func NewProgress(out io.Writer, style Style, tty bool, clock func() time.Time) *Progress {
	return &Progress{out: out, style: style, live: tty && !style.Plain, clock: clock}
}

// Update reports the current counts. It prints only when the rules above say
// it is time.
func (p *Progress) Update(c Counts) {
	if p.closed {
		return
	}

	now := p.clock()
	if p.start.IsZero() {
		p.start = now
		p.last = now
	}

	if p.live {
		if p.drawn && now.Sub(p.last) < redrawEvery {
			return
		}
		p.last = now
		p.draw(c)

		return
	}

	decile := 0
	if c.Total > 0 {
		decile = c.Done * 10 / c.Total
	}
	if decile <= p.decile && now.Sub(p.last) < plainEvery {
		return
	}
	p.decile = max(p.decile, decile)
	p.last = now
	p.emit(c)
}

// Finish prints the final counts and ends the line. It is safe to call more
// than once.
func (p *Progress) Finish(c Counts) {
	if p.closed {
		return
	}
	p.closed = true

	if p.live {
		p.draw(c)
		_, _ = fmt.Fprintln(p.out)

		return
	}
	if p.shown != c || p.shown == (Counts{}) {
		p.emit(c)
	}
}

func (p *Progress) draw(c Counts) {
	p.shown = c
	p.drawn = true
	_, _ = fmt.Fprint(p.out, "\r"+p.liveLine(c)+"\x1b[K")
}

func (p *Progress) emit(c Counts) {
	p.shown = c
	_, _ = fmt.Fprintln(p.out, plainLine(c))
}

// plainLine is the linear form: no colour, no symbols, no redraw.
func plainLine(c Counts) string {
	return fmt.Sprintf("progress %d/%d ok=%d failed=%d running=%d", c.Done, c.Total, c.OK, c.Failed, c.Running)
}

// liveLine is the redrawn form:
//
//	████████░░░░░░░░░░░░ 120/200  ✔ 112  ✖ 3  ● 85 running
func (p *Progress) liveLine(c Counts) string {
	filled := 0
	if c.Total > 0 {
		filled = min(barWidth, c.Done*barWidth/c.Total)
	}

	on, off := "#", "-"
	if p.style.Unicode {
		on, off = "█", "░"
	}
	bar := strings.Repeat(on, filled) + strings.Repeat(off, barWidth-filled)

	return fmt.Sprintf("%s %d/%d  %s %d  %s %d  %s %d running",
		bar, c.Done, c.Total,
		p.style.Marker(SymOK), c.OK,
		p.style.Marker(SymFail), c.Failed,
		p.style.Marker(SymRunning), c.Running)
}
