package group

import (
	"encoding/json"
	"io"
)

// Document is the stable machine-readable form of a Summary (-o json and
// -o yaml). Field names are camelCase like the API's. Every key is always
// present, arrays are never null, and hosts are in natural order, so the same
// run always produces the same bytes. New fields may be added in later
// releases; none is renamed or removed (docs/cli/spec/08-migration.md).
type Document struct {
	Total   int             `json:"total" yaml:"total"`
	Exit    int             `json:"exit" yaml:"exit"`
	Counts  DocumentCounts  `json:"counts" yaml:"counts"`
	Drift   DocumentDrift   `json:"drift" yaml:"drift"`
	Classes []DocumentClass `json:"classes" yaml:"classes"`
}

// DocumentCounts is the number of hosts per outcome class.
type DocumentCounts struct {
	Succeeded int `json:"succeeded" yaml:"succeeded"`
	Failed    int `json:"failed" yaml:"failed"`
	TimedOut  int `json:"timedOut" yaml:"timedOut"`
	Unknown   int `json:"unknown" yaml:"unknown"`
	Cancelled int `json:"cancelled" yaml:"cancelled"`
	Running   int `json:"running" yaml:"running"`
	Pending   int `json:"pending" yaml:"pending"`
}

// DocumentDrift is the comparison of succeeded hosts.
type DocumentDrift struct {
	Kind     string `json:"kind" yaml:"kind"`
	Deviant  int    `json:"deviant" yaml:"deviant"`
	Majority int    `json:"majority" yaml:"majority"`
	Groups   int    `json:"groups" yaml:"groups"`
}

// DocumentClass is one outcome class with all its groups.
type DocumentClass struct {
	Class  string          `json:"class" yaml:"class"`
	Hosts  int             `json:"hosts" yaml:"hosts"`
	Groups []DocumentGroup `json:"groups" yaml:"groups"`
}

// DocumentGroup is one group of hosts with the same result. Hosts lists every
// host, never a compressed form.
type DocumentGroup struct {
	Hosts      []string `json:"hosts" yaml:"hosts"`
	Count      int      `json:"count" yaml:"count"`
	ExitCode   *int     `json:"exitCode,omitempty" yaml:"exitCode,omitempty"`
	ErrorClass string   `json:"errorClass,omitempty" yaml:"errorClass,omitempty"`
	Stdout     string   `json:"stdout,omitempty" yaml:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty" yaml:"stderr,omitempty"`
	StdoutMore int      `json:"stdoutMoreLines,omitempty" yaml:"stdoutMoreLines,omitempty"`
	StderrMore int      `json:"stderrMoreLines,omitempty" yaml:"stderrMoreLines,omitempty"`
	Differs    bool     `json:"differs" yaml:"differs"`
}

// Document converts the summary to its stable form.
func (s Summary) Document() Document {
	doc := Document{
		Total: s.Total,
		Exit:  ExitFor(s),
		Counts: DocumentCounts{
			Succeeded: s.Count(ClassSucceeded), Failed: s.Count(ClassFailed), TimedOut: s.Count(ClassTimedOut),
			Unknown: s.Count(ClassUnknown), Cancelled: s.Count(ClassCancelled),
			Running: s.Count(ClassRunning), Pending: s.Count(ClassPending),
		},
		Drift:   DocumentDrift{Kind: s.Drift.Kind.String(), Deviant: s.Drift.Deviant, Majority: s.Drift.Majority, Groups: s.Drift.Groups},
		Classes: make([]DocumentClass, 0, len(s.Classes)),
	}

	for _, view := range s.Classes {
		class := DocumentClass{Class: view.Class.String(), Hosts: view.Hosts, Groups: make([]DocumentGroup, 0, len(view.Groups))}
		for _, g := range view.Groups {
			class.Groups = append(class.Groups, DocumentGroup(g))
		}
		doc.Classes = append(doc.Classes, class)
	}

	return doc
}

// RenderJSON writes the summary as indented JSON with a trailing newline. The
// output is the same for any order in which the results were added.
func RenderJSON(w io.Writer, s Summary) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")

	return encoder.Encode(s.Document())
}
