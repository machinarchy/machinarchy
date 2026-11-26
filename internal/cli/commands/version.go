package commands

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/buildinfo"
	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/output"
)

// versionInfo is the document printed by `machinarchy version -o json|yaml`.
type versionInfo struct {
	Version   string `json:"version" yaml:"version"`
	Commit    string `json:"commit" yaml:"commit"`
	BuildTime string `json:"buildTime" yaml:"buildTime"`
	GoVersion string `json:"goVersion" yaml:"goVersion"`
	Platform  string `json:"platform" yaml:"platform"`
}

// NewVersionCommand prints the CLI build information. It needs no config file
// or server, so it works on a fresh or damaged installation.
func NewVersionCommand(factory *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print the CLI version",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{cli.AnnotationSkipConfig: "true"},
		Example:     "  machinarchy version\n  machinarchy version -o json",
		RunE: func(command *cobra.Command, _ []string) error {
			printer, err := factory.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}

			info := versionInfo{
				Version:   buildinfo.Version,
				Commit:    buildinfo.Commit,
				BuildTime: buildinfo.BuildTime,
				GoVersion: runtime.Version(),
				Platform:  runtime.GOOS + "/" + runtime.GOARCH,
			}

			if printer.Format() != output.FormatTable {
				return printer.Document(info)
			}

			_, err = fmt.Fprintf(command.OutOrStdout(),
				factory.Logotype(command.OutOrStdout())+"machinarchy %s\ncommit:     %s\nbuilt:      %s\ngo:         %s\nplatform:   %s\n",
				info.Version, info.Commit, info.BuildTime, info.GoVersion, info.Platform)

			return err
		},
	}
}
