package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/ui"
)

// Run executes the CLI and returns the process exit code. It owns error
// presentation so that every failure looks and exits the same way
// (docs/cli/spec/02-commands.md section 2.3):
//
//	Error: <what happened>
//	Why:   <the cause, when it is known>
//	Next:  <the exact command or action>
//
// With -o json the same three parts are written to stderr as one JSON object,
// so a script can read them without scraping text. Passing nil args uses
// os.Args[1:]. The caller decides how to exit, which keeps the whole path
// testable.
func Run(ctx context.Context, options Options, args []string) int {
	if ctx == nil {
		ctx = context.Background()
	}
	options.Streams = normalizedStreams(options.Streams)

	root, factory := newRoot(options)
	if args != nil {
		root.SetArgs(args)
	}

	command, err := root.ExecuteContextC(ctx)
	path := ""
	if command != nil {
		path = command.CommandPath()
	}

	presentation := factory.errorPresentation(root)

	return reportError(options.Streams.ErrOut, err, factory.started, path, presentation)
}

// presentation says how an error is written: the style of the diagnostics
// stream and whether it is JSON.
type presentation struct {
	style ui.Style
	json  bool
}

// errorPresentation works out how to print an error. When the configuration
// was loaded its settings decide; when it was not (a broken config file, a
// usage error) the flags and the environment alone do, so --plain and
// -o json still apply to the error that says the config is broken.
func (f *Factory) errorPresentation(root *cobra.Command) presentation {
	if f.config != nil {
		return presentation{style: f.style, json: f.config.OutputExplicit() == "json"}
	}

	plain := truthy(f.getenv("MACHINARCHY_PLAIN"))
	output := f.getenv("MACHINARCHY_OUTPUT")
	if flags := root.PersistentFlags(); flags != nil {
		if flag := flags.Lookup("plain"); flag != nil && flag.Changed {
			plain = flag.Value.String() == "true"
		}
		if flag := flags.Lookup("output"); flag != nil && flag.Changed {
			output = flag.Value.String()
		}
	}

	return presentation{
		style: ui.DetectStyle(f.getenv, f.Streams.ErrOut, plain),
		json:  strings.EqualFold(strings.TrimSpace(output), "json"),
	}
}

func truthy(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))

	return err == nil && parsed
}

// reportError prints err to w and returns the exit code it maps to.
// started reports whether the selected command began to run; an unclassified
// error before that point came from Cobra rejecting the command line. path is
// the command that failed ("machinarchy logs operation"), so a usage error
// points at that command's help rather than the list of all commands.
func reportError(w io.Writer, err error, started bool, path string, p presentation) int {
	if err == nil {
		return clierr.ExitOK
	}

	code, classified := clierr.Classify(err)
	if !classified && !started {
		code = clierr.ExitUsage
	}
	if clierr.IsReported(err) {
		return code
	}

	if code == clierr.ExitInterrupted {
		_, _ = fmt.Fprintln(w, "Interrupted.")

		return code
	}

	next := clierr.Hint(err)
	if next == "" && code == clierr.ExitUsage {
		next = "machinarchy --help"
		if path != "" && path != "machinarchy" {
			next = path + " --help"
		}
	}

	m := msg.Message{Kind: msg.KindError, Primary: err.Error(), Why: clierr.Why(err), Next: next}
	if p.json {
		writeJSONError(w, code, m)

		return code
	}
	_, _ = fmt.Fprint(w, ui.Render(m, p.style))

	return code
}

// jsonError is the machine-readable form of an error for -o json.
type jsonError struct {
	Error jsonErrorBody `json:"error"`
}

type jsonErrorBody struct {
	Code    string `json:"code"`
	Exit    int    `json:"exit"`
	Message string `json:"message"`
	Why     string `json:"why,omitempty"`
	Next    string `json:"next,omitempty"`
}

func writeJSONError(w io.Writer, code int, m msg.Message) {
	encoded, err := json.Marshal(jsonError{Error: jsonErrorBody{
		Code: clierr.Name(code), Exit: code, Message: m.Primary, Why: m.Why, Next: m.Next,
	}})
	if err != nil { // cannot happen: the value is plain strings and an int
		_, _ = fmt.Fprintf(w, "Error: %s\n", m.Primary)

		return
	}
	_, _ = fmt.Fprintf(w, "%s\n", encoded)
}
