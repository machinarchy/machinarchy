package commands

import (
	"fmt"
	"strings"

	"github.com/machinarchy/machinarchy/internal/cli/api"
	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/spf13/cobra"
)

func resolveProfile(command *cobra.Command, client api.Client, nameOrID string) (string, error) {
	profiles, err := collect(func(cursor string) (api.Page[api.AccessProfile], error) {
		return client.ListAccessProfiles(command.Context(), cursor)
	})
	if err != nil {
		return "", fmt.Errorf("list access profiles: %w", err)
	}
	for _, profile := range profiles {
		if profile.ID == nameOrID || strings.EqualFold(profile.Name, nameOrID) {
			return profile.ID, nil
		}
	}
	return "", clierr.Newf(clierr.ExitNotFound, "", "access profile %q was not found", nameOrID)
}

// parseLabels splits KEY=VALUE assignments from KEY- removals.
func parseLabels(args []string) (map[string]string, []string, error) {
	set := map[string]string{}
	var remove []string
	for _, arg := range args {
		if key, value, ok := strings.Cut(arg, "="); ok {
			if key = strings.TrimSpace(key); key == "" {
				return nil, nil, clierr.Usagef("label %q has an empty key", arg)
			}
			set[key] = value
			continue
		}
		if key, ok := strings.CutSuffix(arg, "-"); ok && key != "" {
			remove = append(remove, key)
			continue
		}
		return nil, nil, clierr.Usagef("label %q must be KEY=VALUE or KEY-", arg)
	}
	return set, remove, nil
}
