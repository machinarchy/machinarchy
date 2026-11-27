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
	"github.com/machinarchy/machinarchy/internal/cli/output"
	"github.com/machinarchy/machinarchy/internal/cli/sanitize"
)

// NewMembersCommand lists the people who have access to the workspace, changes
// their role and removes them. It needs an owner signed in with a password.
func NewMembersCommand(f *cli.Factory) *cobra.Command {
	command := &cobra.Command{
		Use:   "members",
		Short: "List, invite, change and remove people with access",
		Long: "Shows who has access to this server's workspace and their role (reader, operator or owner). Owners can change a\n" +
			"role or remove a person. A workspace always keeps at least one owner. Managing members needs an owner signed in\n" +
			"with a password.",
		Example: "  machinarchy members\n  machinarchy members role alice operator\n  machinarchy members remove alice\n  machinarchy members invite new.person@example.com --role operator",
		Args:    cobra.NoArgs,
		RunE:    listMembers(f),
	}
	command.AddCommand(newMembersRoleCommand(f), newMembersRemoveCommand(f),
		newMembersInviteCommand(f), newMembersInvitesCommand(f), newMembersRevokeCommand(f),
		newMembersAddCommand(f), newMembersLinkCommand(f))
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

func newMembersInviteCommand(f *cli.Factory) *cobra.Command {
	var role string
	command := &cobra.Command{
		Use:   "invite EMAIL",
		Short: "Invite a person by email",
		Long: "Emails a one-time invitation token that lets the person choose their own username and password. It works once\n" +
			"and expires after 7 days. The role is reader (default) or operator; owners are promoted afterwards with\n" +
			"'members role'. The server must have mail configured. If the address already has an account, nothing is sent\n" +
			"and the answer looks the same.",
		Example: "  machinarchy members invite new.person@example.com\n  machinarchy members invite new.person@example.com --role operator",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			role = strings.ToLower(strings.TrimSpace(role))
			if role != "reader" && role != "operator" {
				return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is not a role you can invite", sanitize.Line(role)),
					"an invitation gives reader or operator; promote to owner afterwards", "machinarchy members invite "+args[0]+" --role operator")
			}
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			result, err := client.InviteMember(command.Context(), strings.TrimSpace(args[0]), role)
			if err != nil {
				return fmt.Errorf("invite: %w", err)
			}

			return printMessage(command, f, msg.Get("member.invited").With(msg.Vars{
				"email": sanitize.Line(result.Email), "role": result.Role, "expires": result.ExpiresAt.Local().Format("2006-01-02 15:04")}))
		},
	}
	command.Flags().StringVar(&role, "role", "reader", "Role to give: reader or operator")

	return command
}

func newMembersInvitesCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "invites",
		Short:   "List pending invitations",
		Long:    "Shows invitations that have not been accepted, revoked or expired. Tokens are never shown.",
		Example: "  machinarchy members invites",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			invitations, err := client.ListInvitations(command.Context())
			if err != nil {
				return fmt.Errorf("list invitations: %w", err)
			}
			rows := make([][]string, 0, len(invitations))
			ids := make([]string, 0, len(invitations))
			for _, i := range invitations {
				ids = append(ids, strconv.Itoa(i.ID))
				rows = append(rows, []string{strconv.Itoa(i.ID), sanitize.Line(i.Email), i.Role, sanitize.Line(i.InvitedBy),
					i.ExpiresAt.Local().Format("2006-01-02 15:04")})
			}

			return printList(command, f, invitations, ids, []string{"ID", "EMAIL", "ROLE", "INVITED BY", "EXPIRES"}, rows)
		},
	}
}

func newMembersRevokeCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "revoke INVITATION_ID",
		Short:   "Cancel a pending invitation",
		Long:    "Cancels an invitation so its token stops working at once. Find the ID with 'members invites'.",
		Example: "  machinarchy members revoke 4",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			id, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil || id <= 0 {
				return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is not an invitation ID", sanitize.Line(args[0])), "", "machinarchy members invites")
			}
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			if err := client.RevokeInvitation(command.Context(), id); err != nil {
				return fmt.Errorf("revoke invitation: %w", err)
			}

			return printMessage(command, f, msg.Get("member.invite_revoked").With(msg.Vars{"id": strconv.Itoa(id)}))
		},
	}
}

func newMembersAddCommand(f *cli.Factory) *cobra.Command {
	var role, email string
	command := &cobra.Command{
		Use:   "add USERNAME",
		Short: "Add a person without email",
		Long: "Creates an account for a person who cannot be invited by email. The account has no password and cannot sign in\n" +
			"until its one-time token is used: you get the token once (valid 24 hours) and give the person the username and\n" +
			"the token, over a channel you trust. They choose their own password with 'machinarchy login SERVER --invite'.\n" +
			"The role is reader (default) or operator. If the token is lost, 'members link USER' issues a new one.",
		Example: "  machinarchy members add alice\n  machinarchy members add alice --role operator --email alice@example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			role = strings.ToLower(strings.TrimSpace(role))
			if role != "reader" && role != "operator" {
				return clierr.Detailed(clierr.ExitUsage, fmt.Errorf("%q is not a role you can add", sanitize.Line(role)),
					"an added person gets reader or operator; promote to owner afterwards", "machinarchy members add "+args[0]+" --role operator")
			}
			client, err := membersClient(command, f)
			if err != nil {
				return err
			}
			link, err := client.AddMember(command.Context(), strings.TrimSpace(args[0]), strings.TrimSpace(email), role)
			if err != nil {
				return fmt.Errorf("add member: %w", err)
			}

			return printLink(command, f, "member.added", link, link.Username, link.Role)
		},
	}
	command.Flags().StringVar(&role, "role", "reader", "Role to give: reader or operator")
	command.Flags().StringVar(&email, "email", "", "Email address to record for the account (nothing is sent)")

	return command
}

func newMembersLinkCommand(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "link USER",
		Short: "Issue a new one-time token for a person who has not activated",
		Long: "Replaces the one-time token of a person added with 'members add' who has not chosen a password yet; the old\n" +
			"token stops working. The server refuses it for an account that already has a password, so it cannot be used to\n" +
			"take over a working account. USER is a username or an ID.",
		Example: "  machinarchy members link alice",
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
			link, err := client.ReissueMemberLink(command.Context(), member.ID)
			if err != nil {
				return fmt.Errorf("issue link: %w", err)
			}

			return printLink(command, f, "member.added", link, member.Username, member.Role)
		},
	}
}

// printLink shows a one-time token: as a document for -o json/yaml, otherwise a
// message and the token on its own line (stdout only, never saved).
func printLink(command *cobra.Command, f *cli.Factory, id string, link *api.MemberLink, user, role string) error {
	printer, err := f.Printer(command.OutOrStdout(), output.FormatTable)
	if err != nil {
		return err
	}
	if printer.Format() != output.FormatTable {
		return printer.Document(link)
	}
	if err := printMessage(command, f, msg.Get(id).With(msg.Vars{"user": sanitize.Line(user), "role": role,
		"expires": link.ExpiresAt.Local().Format("2006-01-02 15:04")})); err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "\n  Username: %s\n  Token:    %s\n", sanitize.Line(user), link.Token)

	return err
}
