package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/msg"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// NewMembersCommand lists the people who have access to the workspace, changes
// their role and removes them. It needs an owner signed in with a password.
func NewMembersCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "members",
		Short: "List, change and remove people with access",
		Long: "Shows who has access to this server's workspace and their role (reader, operator or owner). Owners can change a\n" +
			"role or remove a person. A workspace always keeps at least one owner. Managing members needs an owner signed in\n" +
			"with a password.",
		Example: "  machinarchy members\n  machinarchy members role alice operator\n  machinarchy members remove alice",
		Args:    cobra.NoArgs,
		RunE:    listMembers(f),
	}
	command.AddCommand(newMembersRoleCommand(f), newMembersRemoveCommand(f))
	markOwner(command)

	return command
}

func membersClient(command *cobra.Command, f *cli.Factory) (api.MembersClient, error) {
	client, err := f.NewAPIClient(command.Context())
	if err != nil {
		return nil, err
	}
	members, ok := client.(api.MembersClient)
	if !ok {
		return nil, errors.New("API client does not support members")
	}

	return members, nil
}

func listMembers(f *cli.Factory) func(*cobra.Command, []string) error {
	return func(command *cobra.Command, _ []string) error {
		client, err := membersClient(command, f)
		if err != nil {
			return err
		}
		members, err := client.ListMembers(command.Context())
		if err != nil {
			return fmt.Errorf("list members: %w", err)
		}

		rows := make([][]string, 0, len(members))
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, strconv.Itoa(m.ID))
			rows = append(rows, []string{strconv.Itoa(m.ID), sanitize.Line(m.Username), sanitize.Line(m.Email), m.Role,
				m.CreatedAt.Local().Format("2006-01-02")})
		}

		return printList(command, f, members, ids, []string{"ID", "USER", "EMAIL", "ROLE", "SINCE"}, rows)
	}
}

// findMember picks one member by username or numeric ID.
func findMember(members []api.Member, who string) (api.Member, error) {
	who = strings.TrimSpace(who)
	for _, m := range members {
		if m.Username == who {
			return m, nil
		}
	}
	if id, err := strconv.Atoi(who); err == nil {
		for _, m := range members {
			if m.ID == id {
				return m, nil
			}
		}
	}

	return api.Member{}, clierr.Detailed(clierr.ExitNotFound, fmt.Errorf("there is no member %q", sanitize.Line(who)), "", "machinarchy members")
}

func newMembersRoleCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "role USER reader|operator|owner",
		Short: "Change a member's role",
		Long: "Changes what a member may do: reader (look), operator (look and run) or owner (manage people, tokens and access).\n" +
			"USER is a username or an ID. The last owner cannot be demoted.",
		Example: "  machinarchy members role alice operator",
		Args:    cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			role := strings.ToLower(strings.TrimSpace(args[1]))
			if role != "reader" && role != "operator" && role != "owner" {
				return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is not a role", sanitize.Line(args[1])),
					"the roles are reader, operator and owner", "machinarchy members role "+args[0]+" operator")
			}
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			members, err := client.ListMembers(command.Context())
			if err != nil {
				return fmt.Errorf("list members: %w", err)
			}
			member, err := findMember(members, args[0])
			if err != nil {
				return err
			}
			if _, err := client.SetMemberRole(command.Context(), member.ID, role); err != nil {
				return fmt.Errorf("change role: %w", err)
			}

			return printMessage(command, f, msg.Get("member.role_changed").With(msg.Vars{"user": sanitize.Line(member.Username), "role": role}))
		},
	}
}

func newMembersRemoveCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove USER",
		Short: "Remove a member's access",
		Long: "Removes a person's access to the workspace and deletes their API tokens at once. Their account is kept and they\n" +
			"can sign in, but see nothing. You confirm by typing their name (in a script: --confirm NAME); --yes is not\n" +
			"enough. An owner can remove themselves while another owner remains; the last owner cannot be removed.",
		Example: "  machinarchy members remove alice\n  machinarchy members remove alice --confirm alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			members, err := client.ListMembers(command.Context())
			if err != nil {
				return fmt.Errorf("list members: %w", err)
			}
			member, err := findMember(members, args[0])
			if err != nil {
				return err
			}
			what := fmt.Sprintf("Remove %q (%s) from server %s? Their API tokens stop working at once.",
				sanitize.Line(member.Username), member.Role, f.Config().ResolveServerName())
			if err := f.ConfirmTyped(what, member.Username); err != nil {
				return err
			}
			if err := client.RemoveMember(command.Context(), member.ID); err != nil {
				return fmt.Errorf("remove member: %w", err)
			}

			return printMessage(command, f, msg.Get("member.removed").With(msg.Vars{"user": sanitize.Line(member.Username)}))
		},
	}
}
