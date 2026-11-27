package commands

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

const (
	auditPageSize = 500
	// auditExportDefaultMax bounds one export so a huge trail cannot fill a disk
	// by accident; --max raises or lowers it.
	auditExportDefaultMax = 100000
)

// NewAuditCommand reads the workspace's tamper-evident audit trail. It needs an
// owner signed in with a password.
func NewAuditCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "audit",
		Short: "List, verify and export the audit trail",
		Long: "Shows who changed what in this workspace, newest first. The trail is append-only and hash-chained; 'verify'\n" +
			"checks the chain and 'export' writes it out as JSON lines together with the head hash. Record the head hash\n" +
			"outside the server to detect later removal of the newest events. Needs an owner signed in with a password.",
		Example: "  machinarchy audit\n  machinarchy audit verify\n  machinarchy audit export --output audit.jsonl",
		Args:    cobra.NoArgs,
		RunE:    listAudit(f),
	}
	command.Flags().String("action", "", "only events with this action, for example member.role_changed")
	command.Flags().Int("limit", 50, "how many events to show (at most 500)")
	command.AddCommand(newAuditVerifyCommand(f), newAuditExportCommand(f))
	markOwner(command)

	return command
}

func auditClient(command *cobra.Command, f *cli.Factory) (api.AuditClient, error) {
	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return nil, err
	}
	audit, ok := client.(api.AuditClient)
	if !ok {
		return nil, errors.New("API client does not support the audit trail")
	}

	return audit, nil
}

func listAudit(f *cli.Factory) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, _ []string) error {
		limit, err := command.Flags().GetInt("limit")
		if err != nil {
			return err
		}
		if limit < 1 || limit > auditPageSize {
			return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("--limit must be between 1 and %d", auditPageSize), "", "machinarchy audit --limit 100")
		}
		action, err := command.Flags().GetString("action")
		if err != nil {
			return err
		}
		client, err := auditClient(command, f)
		if err != nil {
			return err
		}
		events, err := client.ListAuditEvents(command.Context(), action, 0, limit)
		if err != nil {
			return fmt.Errorf("list audit events: %w", err)
		}

		rows := make([][]string, 0, len(events))
		ids := make([]string, 0, len(events))
		for _, e := range events {
			ids = append(ids, strconv.FormatInt(e.ID, 10))
			rows = append(rows, []string{strconv.FormatInt(e.ID, 10), e.OccurredAt.Local().Format("2006-01-02 15:04:05"),
				sanitize.Line(e.ActorUsername), sanitize.Line(e.Action), sanitize.Line(e.TargetType + " " + e.TargetID), sanitize.Line(e.Outcome)})
		}

		return printList(command, f, events, ids, []string{"ID", "TIME", "ACTOR", "ACTION", "TARGET", "OUTCOME"}, rows)
	}
}

func newAuditVerifyCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Check the audit trail's hash chain",
		Long: "Recomputes every event's hash and link on the server. A valid result proves no earlier event was edited, removed\n" +
			"or reordered. It cannot see events removed from the end: keep the printed head hash somewhere outside the server\n" +
			"and compare it next time. Exits non-zero when the chain is broken.",
		Example: "  machinarchy audit verify",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := auditClient(command, f)
			if err != nil {
				return err
			}
			result, err := client.VerifyAudit(command.Context())
			if err != nil {
				return fmt.Errorf("verify audit trail: %w", err)
			}
			printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
			if err != nil {
				return err
			}
			if printer.Format() != output.FormatTable {
				if err := printer.Document(result); err != nil {
					return err
				}
			} else {
				out := command.OutOrStdout()
				if result.Valid {
					_, _ = fmt.Fprintf(out, "Audit trail is intact: %d events.\nHead hash: %s\n", result.Events, sanitize.Line(result.HeadHash))
				} else {
					_, _ = fmt.Fprintf(out, "Audit trail is BROKEN at event %d: %s\n", result.FirstInvalidID, sanitize.Line(result.Reason))
				}
			}
			if !result.Valid {
				return clierr.Detailed(clierr.ExitFailure, errors.New("the audit hash chain is broken"), "", "machinarchy audit export --output audit.jsonl")
			}

			return nil
		},
	}
}

// auditSummary is the last line of an export.
type auditSummary struct {
	Events   int    `json:"events"`
	HeadID   int64  `json:"headId,omitempty"`
	HeadHash string `json:"headHash,omitempty"`
	OldestID int64  `json:"oldestId,omitempty"`
	// ChainLinked is true when every event's prevHash matched the event before
	// it; it is false when a link was broken and omitted when --action filtered
	// the events (a filtered export has gaps by design).
	ChainLinked *bool `json:"chainLinked,omitempty"`
	BrokenAtID  int64 `json:"brokenAtId,omitempty"`
	Truncated   bool  `json:"truncated"`
}

func newAuditExportCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "export",
		Short: "Write the audit trail as JSON lines",
		Long: "Writes every event, newest first, one JSON object per line, then a final {\"summary\":...} line holding the head\n" +
			"hash (the newest event's hash) and whether the chain links held. Store the file and the head hash outside the\n" +
			"server. The export is bounded by --max (default 100000); a truncated export says so. --output refuses to\n" +
			"overwrite an existing file and creates it readable only by you.",
		Example: "  machinarchy audit export --output audit.jsonl\n  machinarchy audit export --action member.role_changed",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			maxEvents, err := command.Flags().GetInt("max")
			if err != nil {
				return err
			}
			if maxEvents < 1 {
				return clierr.Detailed(clierr.ExitUsage, errors.New("--max must be at least 1"), "", "machinarchy audit export --max 1000")
			}
			action, err := command.Flags().GetString("action")
			if err != nil {
				return err
			}
			path, err := command.Flags().GetString("output")
			if err != nil {
				return err
			}
			client, err := auditClient(command, f)
			if err != nil {
				return err
			}

			out := command.OutOrStdout()
			var file *os.File
			if path != "" {
				file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // user-chosen output path
				if err != nil {
					return clierr.Detailed(clierr.ExitFailure, fmt.Errorf("cannot create %s: %w", sanitize.Line(path), err), "an existing file is never overwritten", "")
				}
				out = file
			}
			buffered := bufio.NewWriter(out)
			summary, exportErr := exportAudit(command, client, buffered, action, maxEvents)
			if exportErr == nil {
				exportErr = buffered.Flush()
			}
			if file != nil {
				if closeErr := file.Close(); exportErr == nil {
					exportErr = closeErr
				}
				if exportErr != nil {
					_ = os.Remove(path) // never leave a partial export that looks complete
				}
			}
			if exportErr != nil {
				return exportErr
			}
			if path != "" {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "Exported %d events to %s. Head hash: %s\n", summary.Events, sanitize.Line(path), sanitize.Line(summary.HeadHash))
			}
			if summary.ChainLinked != nil && !*summary.ChainLinked {
				return clierr.Detailed(clierr.ExitFailure, fmt.Errorf("the audit chain is broken at event %d", summary.BrokenAtID), "", "machinarchy audit verify")
			}

			return nil
		},
	}
	command.Flags().String("action", "", "only events with this action (the chain check is skipped)")
	command.Flags().String("output", "", "write to this new file instead of the terminal")
	command.Flags().Int("max", auditExportDefaultMax, "stop after this many events")

	return command
}

func exportAudit(command *cobra.Command, client api.AuditClient, out io.Writer, action string, maxEvents int) (auditSummary, error) {
	var summary auditSummary
	linked := action == ""
	var expectPrev string // the hash the next (older) event must have
	var before int64
	encoder := json.NewEncoder(out)

	for summary.Events < maxEvents {
		page := auditPageSize
		if remaining := maxEvents - summary.Events; remaining < page {
			page = remaining
		}
		events, err := client.ListAuditEvents(command.Context(), action, before, page)
		if err != nil {
			return summary, fmt.Errorf("list audit events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			if before != 0 && e.ID >= before {
				return summary, fmt.Errorf("the server returned event %d out of order", e.ID)
			}
			if summary.Events == 0 {
				summary.HeadID, summary.HeadHash = e.ID, e.Hash
			} else if linked && e.Hash != expectPrev && summary.BrokenAtID == 0 {
				summary.BrokenAtID = e.ID
			}
			expectPrev = e.PrevHash
			summary.Events++
			summary.OldestID = e.ID
			before = e.ID
			if err := encoder.Encode(e); err != nil {
				return summary, err
			}
		}
		if len(events) < page {
			break
		}
	}
	if summary.Events >= maxEvents {
		// Truncated only when older events remain.
		more, err := client.ListAuditEvents(command.Context(), action, before, 1)
		if err != nil {
			return summary, fmt.Errorf("list audit events: %w", err)
		}
		summary.Truncated = len(more) > 0
	}
	if action == "" {
		ok := summary.BrokenAtID == 0
		summary.ChainLinked = &ok
	}

	return summary, encoder.Encode(map[string]auditSummary{"summary": summary})
}
