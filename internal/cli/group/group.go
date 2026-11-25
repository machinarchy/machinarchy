// Package group turns the per-host results of a run into the grouped output of
// docs/cli/spec/03-ux.md section 7: hosts are grouped by outcome, identical
// results collapse into one group, host lists are compressed, volatile data
// such as timestamps and PIDs is ignored when comparing output, and a few
// hosts that differ from a dominant result are flagged as drift.
//
// The package has two halves. The Aggregator and Summary are pure: no I/O, no
// terminal, no clock. They stream: for each host the aggregator keeps a hash
// of the normalised output, a bounded sample, and an integer index, never the
// output text, so 5,000 hosts with 15 KiB each use a few hundred kilobytes.
// Render (render.go) draws a Summary for a terminal, plain text or JSON.
package group

import (
	"crypto/sha256"
	"sort"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// Default sample bounds: the first lines and characters of a group's output.
const (
	DefaultSampleLines = 5
	DefaultSampleChars = 300

	// sampleSource bounds how much of an output is looked at to build a
	// sample, so choosing a sample never costs more than a few kilobytes.
	sampleSource = 4096
)

// Controller target states the aggregator understands (docs/operations.md).
const (
	StateSucceeded   = "Succeeded"
	StateFailed      = "Failed"
	StateTimedOut    = "TimedOut"
	StateCancelled   = "Cancelled"
	StateInterrupted = "Interrupted"
	StateLost        = "Lost"
	StateRunning     = "Running"
	StatePending     = "Pending"
)

// Result is one host's outcome as the CLI learned it.
type Result struct {
	// Host is the host name.
	Host string
	// State is the controller's target state, for example "Succeeded".
	State string
	// ExitCode is the remote exit code, when there is one.
	ExitCode *int
	// ErrorClass is the controller's error class, for example "unreachable".
	ErrorClass string
	// Stdout and Stderr are the captured output, already cut to the
	// controller's window.
	Stdout, Stderr string
}

// Class is the outcome class of a host. The numeric order is the display
// order: what the reader must act on comes first.
type Class int

// Outcome classes.
const (
	ClassFailed Class = iota
	ClassTimedOut
	ClassUnknown
	ClassCancelled
	ClassRunning
	ClassPending
	ClassSucceeded
	classCount
)

var classNames = [classCount]string{"failed", "timedOut", "unknown", "cancelled", "running", "pending", "succeeded"}

// String returns the stable name used in JSON output.
func (c Class) String() string {
	if c >= 0 && c < classCount {
		return classNames[c]
	}

	return "unknown"
}

// ClassOf maps a controller target state to an outcome class. A host whose
// outcome cannot be known (Lost, Interrupted, or a state this CLI does not
// know) is "unknown": the command may or may not have run there.
func ClassOf(state string) Class {
	switch state {
	case StateSucceeded:
		return ClassSucceeded
	case StateFailed:
		return ClassFailed
	case StateTimedOut:
		return ClassTimedOut
	case StateCancelled:
		return ClassCancelled
	case StateRunning:
		return ClassRunning
	case StatePending:
		return ClassPending
	default:
		return ClassUnknown
	}
}

// Options configure an Aggregator. Zero values select the defaults.
type Options struct {
	// SampleLines and SampleChars bound the sample kept for each group.
	SampleLines, SampleChars int
}

type groupKey struct {
	class    Class
	exit     int // -1 when there is no exit code
	errClass string
	digest   [16]byte
}

type bucket struct {
	class      Class
	exit       *int
	errClass   string
	hosts      []int32
	sampleHost int32 // the naturally first host, whose output is the sample
	stdout     string
	stderr     string
	stdoutMore int
	stderrMore int
}

// Aggregator groups Results as they arrive. It is not safe for concurrent use.
type Aggregator struct {
	opts    Options
	names   []string
	index   map[string]int32
	buckets map[groupKey]*bucket
}

// New creates an Aggregator.
func New(opts Options) *Aggregator {
	if opts.SampleLines <= 0 {
		opts.SampleLines = DefaultSampleLines
	}
	if opts.SampleChars <= 0 {
		opts.SampleChars = DefaultSampleChars
	}

	return &Aggregator{opts: opts, index: make(map[string]int32), buckets: make(map[groupKey]*bucket)}
}

// Add records one host's result. A host that was already added is ignored, so
// feeding a result twice cannot change the counts.
//
// The output text is read once to hash it and, when the host becomes the
// group's sample, a bounded prefix of it is copied. It is never retained.
func (a *Aggregator) Add(r Result) {
	if _, seen := a.index[r.Host]; seen {
		return
	}
	id := int32(len(a.names)) // #nosec G115 -- bounded by the number of hosts in a run
	a.names = append(a.names, r.Host)
	a.index[r.Host] = id

	class := ClassOf(r.State)
	key := groupKey{class: class, exit: -1, errClass: r.ErrorClass, digest: digest(r)}
	if r.ExitCode != nil {
		key.exit = *r.ExitCode
	}

	b, ok := a.buckets[key]
	if !ok {
		b = &bucket{class: class, errClass: r.ErrorClass, sampleHost: -1}
		if r.ExitCode != nil {
			exit := *r.ExitCode
			b.exit = &exit
		}
		a.buckets[key] = b
	}
	b.hosts = append(b.hosts, id)

	// The sample is the output of the naturally first host, so the result does
	// not depend on the order in which hosts finished.
	if b.sampleHost < 0 || NaturalLess(r.Host, a.names[b.sampleHost]) {
		b.sampleHost = id
		b.stdout, b.stdoutMore = sample(r.Stdout, a.opts)
		b.stderr, b.stderrMore = sample(r.Stderr, a.opts)
	}
}

// Len returns the number of hosts added.
func (a *Aggregator) Len() int { return len(a.names) }

// digest hashes the normalised stdout and stderr without building them.
func digest(r Result) [16]byte {
	if r.Stdout == "" && r.Stderr == "" {
		return [16]byte{}
	}

	h := sha256.New()
	write := func(piece string) { _, _ = h.Write([]byte(piece)) }
	normalise(r.Stdout, write)
	_, _ = h.Write([]byte{0})
	normalise(r.Stderr, write)

	var out [16]byte
	copy(out[:], h.Sum(nil))

	return out
}

// sample returns the first lines of text, bounded, sanitised, and the number
// of lines that were left out.
func sample(text string, opts Options) (string, int) {
	if text == "" {
		return "", 0
	}
	if len(text) > sampleSource {
		text = text[:sampleSource]
	}
	text = strings.Clone(sanitize.Text(text))
	text = strings.TrimRight(strings.ReplaceAll(text, "\r", ""), "\n \t")

	lines := strings.Split(text, "\n")
	left := 0
	if len(lines) > opts.SampleLines {
		left = len(lines) - opts.SampleLines
		lines = lines[:opts.SampleLines]
	}

	out := strings.Join(lines, "\n")
	if runes := []rune(out); len(runes) > opts.SampleChars {
		out = string(runes[:opts.SampleChars]) + "..."
	}

	return out, left
}

// View is one group of hosts with identical results.
type View struct {
	// Hosts are the host names in natural order.
	Hosts []string
	// Count is len(Hosts).
	Count int
	// ExitCode is the shared remote exit code, when there is one.
	ExitCode *int
	// ErrorClass is the shared controller error class.
	ErrorClass string
	// Stdout and Stderr are samples of the shared output.
	Stdout, Stderr string
	// StdoutMore and StderrMore count the sample lines that were left out.
	StdoutMore, StderrMore int
	// Differs marks a succeeded group that differs from the dominant one.
	Differs bool
}

// ClassView is every group of one outcome class.
type ClassView struct {
	Class  Class
	Hosts  int
	Groups []View
}

// DriftKind says how the succeeded hosts compare with each other.
type DriftKind int

// Drift kinds.
const (
	// DriftNone: all succeeded hosts printed the same, or fewer than two
	// groups exist.
	DriftNone DriftKind = iota
	// DriftDominant: one group has two or more hosts and others differ from it.
	DriftDominant
	// DriftAllDifferent: every succeeded host printed something different
	// (normal for time-dependent output; not a warning).
	DriftAllDifferent
	// DriftMixed: several groups of two or more hosts and none holds a
	// majority, so no group can be called the odd one out.
	DriftMixed
)

// String returns the stable name used in JSON output.
func (k DriftKind) String() string {
	switch k {
	case DriftDominant:
		return "dominant"
	case DriftAllDifferent:
		return "allDifferent"
	case DriftMixed:
		return "mixed"
	case DriftNone:
	}

	return "none"
}

// Drift is the comparison of succeeded hosts (spec 7.2 rule 8).
type Drift struct {
	Kind DriftKind
	// Deviant is the number of succeeded hosts outside the dominant group.
	Deviant int
	// Majority is the size of the dominant group (or the largest group when
	// the kind is DriftMixed).
	Majority int
	// Groups is the number of different outputs among the succeeded hosts.
	Groups int
}

// Summary is the grouped result of a run.
type Summary struct {
	// Total is the number of hosts.
	Total int
	// Classes holds the non-empty outcome classes in display order.
	Classes []ClassView
	// Drift compares the succeeded hosts.
	Drift Drift
}

// Count returns the number of hosts in class c.
func (s Summary) Count(c Class) int {
	for _, view := range s.Classes {
		if view.Class == c {
			return view.Hosts
		}
	}

	return 0
}

// GroupCount returns the number of groups over all classes.
func (s Summary) GroupCount() int {
	n := 0
	for _, view := range s.Classes {
		n += len(view.Groups)
	}

	return n
}

// FirstHost returns the naturally first host of the whole run, or "".
func (s Summary) FirstHost() string {
	first := ""
	for _, view := range s.Classes {
		for _, g := range view.Groups {
			if len(g.Hosts) > 0 && (first == "" || NaturalLess(g.Hosts[0], first)) {
				first = g.Hosts[0]
			}
		}
	}

	return first
}

// ExitFor returns the process exit code of a finished run: 0 when every host
// succeeded, 9 when none did, 8 otherwise (docs/cli/spec/02-commands.md
// 3.10.5). An empty run exits 0.
func ExitFor(s Summary) int {
	ok := s.Count(ClassSucceeded)
	switch {
	case s.Total == 0 || ok == s.Total:
		return 0
	case ok == 0:
		return 9
	default:
		return 8
	}
}

// Summary builds the grouped view. It does not change the aggregator, so it
// can be called repeatedly while results are still arriving. The result is
// identical for any order in which the same results were added.
func (a *Aggregator) Summary() Summary {
	byClass := make([][]View, classCount)

	for _, b := range a.buckets {
		hosts := make([]string, len(b.hosts))
		for i, id := range b.hosts {
			hosts[i] = a.names[id]
		}
		sort.Slice(hosts, func(i, j int) bool { return NaturalLess(hosts[i], hosts[j]) })

		byClass[b.class] = append(byClass[b.class], View{
			Hosts: hosts, Count: len(hosts), ExitCode: b.exit, ErrorClass: b.errClass,
			Stdout: b.stdout, Stderr: b.stderr, StdoutMore: b.stdoutMore, StderrMore: b.stderrMore,
		})
	}

	summary := Summary{Total: len(a.names)}
	for class := range classCount {
		groups := byClass[class]
		if len(groups) == 0 {
			continue
		}
		sort.Slice(groups, func(i, j int) bool { return lessGroup(groups[i], groups[j]) })

		hosts := 0
		for _, g := range groups {
			hosts += g.Count
		}
		summary.Classes = append(summary.Classes, ClassView{Class: class, Hosts: hosts, Groups: groups})
	}

	summary.Drift = markDrift(&summary)

	return summary
}

// lessGroup orders groups: larger first, then by their first host, then by the
// shared result so the order is total.
func lessGroup(a, b View) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	if a.Hosts[0] != b.Hosts[0] {
		return NaturalLess(a.Hosts[0], b.Hosts[0])
	}
	if a.ErrorClass != b.ErrorClass {
		return a.ErrorClass < b.ErrorClass
	}

	return a.Stdout < b.Stdout
}

// markDrift compares the succeeded hosts (spec 7.2 rule 8) and marks the
// groups that differ from the dominant one. A group is dominant when it has
// two or more hosts and more than half of all succeeded hosts: "3 hosts
// printed something different from the other 11" is a statement about a
// majority. When no group holds a majority the result is DriftMixed and no
// group is singled out.
func markDrift(s *Summary) Drift {
	for i := range s.Classes {
		view := &s.Classes[i]
		if view.Class != ClassSucceeded || len(view.Groups) < 2 {
			continue
		}

		majority := view.Groups[0].Count // groups are sorted, largest first
		switch {
		case majority < 2:
			return Drift{Kind: DriftAllDifferent, Groups: len(view.Groups)}
		case majority*2 <= view.Hosts:
			return Drift{Kind: DriftMixed, Majority: majority, Groups: len(view.Groups)}
		}
		for j := 1; j < len(view.Groups); j++ {
			view.Groups[j].Differs = true
		}

		return Drift{Kind: DriftDominant, Deviant: view.Hosts - majority, Majority: majority, Groups: len(view.Groups)}
	}

	return Drift{}
}
