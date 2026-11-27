package commands

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// auditArchiveLineLimit bounds one line of an archive; an event is far smaller.
const auditArchiveLineLimit = 1 << 20

func newAuditPruneCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "prune",
		Short: "Remove old audit events once an export of them exists",
		Long: "Removes the events below --before-id from the server, leaving a checkpoint so the rest of the trail still\n" +
			"verifies. First make an unfiltered export with 'audit export --output FILE' and keep it: pruned events cannot\n" +
			"be restored. --archive reads that file and sends only the head hash and event count of the range; the server\n" +
			"recomputes both from its own copy and refuses on any difference, if the chain is broken, or if the newest event\n" +
			"to remove is younger than 30 days. Confirm by typing the --before-id value, or pass --confirm ID in a script.",
		Example:     "  machinarchy audit export --output audit.jsonl\n  machinarchy audit prune --before-id 1200 --archive audit.jsonl --confirm 1200",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{cli.AnnotationDryRun: "true"},
		RunE: func(command *cobra.Command, _ []string) error {
			before, err := command.Flags().GetInt64("before-id")
			if err != nil {
				return err
			}
			path, err := command.Flags().GetString("archive")
			if err != nil {
				return err
			}
			if before < 2 || path == "" {
				return clierr.Detailed(clierr.ExitUsage, errors.New("--before-id and --archive are required"), "events with an ID below --before-id are removed",
					"machinarchy audit prune --before-id 1200 --archive audit.jsonl")
			}
			request, err := auditPruneFromArchive(path, before)
			if err != nil {
				return err
			}
			if f.DryRun() {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "Would remove %d events below %d (newest hash %s). Nothing was changed.\n",
					request.ArchiveCount, before, sanitize.Line(request.ArchiveHeadHash))

				return nil
			}
			what := fmt.Sprintf("This permanently removes %d audit events below %d from the server. Keep %s: it is the only copy afterwards.",
				request.ArchiveCount, before, sanitize.Line(path))
			if err := f.ConfirmTyped(what, strconv.FormatInt(before, 10)); err != nil {
				return err
			}
			client, err := auditClient(command, f)
			if err != nil {
				return err
			}
			checkpoint, err := client.PruneAudit(command.Context(), request)
			if err != nil {
				return fmt.Errorf("prune audit trail: %w", err)
			}
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			if printer.Format() != output.FormatTable {
				return printer.Document(checkpoint)
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "Removed %d events up to event %d.\nCheckpoint hash: %s\nRecord the hash outside the server with your archive.\n",
				checkpoint.EventsRemoved, checkpoint.LastEventID, sanitize.Line(checkpoint.LastHash))

			return nil
		},
	}
	command.Flags().Int64("before-id", 0, "remove events with an ID below this one")
	command.Flags().String("archive", "", "the unfiltered 'audit export' file that holds those events")
	markOwner(command)

	return command
}

// auditPruneFromArchive reads an export file and derives the proof for the
// events below before: their count and the hash of the newest. It checks what
// the file itself can show: an unfiltered export whose events link to each
// other. The server still recomputes everything from its own data.
func auditPruneFromArchive(path string, before int64) (api.AuditPruneRequest, error) {
	var request api.AuditPruneRequest
	file, err := os.Open(path) //nolint:gosec // user-chosen archive path
	if err != nil {
		return request, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("cannot read the archive %s: %w", sanitize.Line(path), err), "", "machinarchy audit export --output audit.jsonl")
	}
	defer func() { _ = file.Close() }()

	invalid := func(reason string) (api.AuditPruneRequest, error) {
		return request, clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%s is not usable as an archive: %s", sanitize.Line(path), reason), "",
			"machinarchy audit export --output audit.jsonl")
	}

	var events []api.AuditEvent
	var summary *auditSummary
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), auditArchiveLineLimit)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Summary *auditSummary `json:"summary"`
		}
		if err := json.Unmarshal(line, &envelope); err == nil && envelope.Summary != nil {
			summary = envelope.Summary

			continue
		}
		var event api.AuditEvent
		if err := json.Unmarshal(line, &event); err != nil || event.ID <= 0 || event.Hash == "" {
			return invalid("a line is not an audit event")
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return invalid("it could not be read completely: " + err.Error())
	}
	if summary == nil {
		return invalid("the final summary line is missing (an incomplete or edited export)")
	}
	if summary.ChainLinked == nil {
		return invalid("it is a filtered export; use an unfiltered 'audit export'")
	}
	if !*summary.ChainLinked {
		return invalid("its chain was already broken when it was exported (run 'audit verify')")
	}

	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	var below []api.AuditEvent
	for _, event := range events {
		if event.ID >= before {
			break
		}
		if n := len(below); n > 0 {
			if event.ID == below[n-1].ID {
				return invalid(fmt.Sprintf("event %d appears twice", event.ID))
			}
			if event.PrevHash != below[n-1].Hash {
				return invalid(fmt.Sprintf("event %d does not link to the event before it", event.ID))
			}
		}
		below = append(below, event)
	}
	if len(below) == 0 {
		return invalid(fmt.Sprintf("it holds no events below %d", before))
	}
	request.BeforeID = before
	request.ArchiveCount = len(below)
	request.ArchiveHeadHash = below[len(below)-1].Hash

	return request, nil
}
