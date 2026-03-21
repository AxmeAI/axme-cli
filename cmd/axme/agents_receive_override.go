package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newAgentsReceiveOverrideCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receive-override",
		Short: "Manage per-agent receive policy override for cross-org intents",
	}
	cmd.AddCommand(
		newAgentsReceiveOverrideGetCmd(rt),
		newAgentsReceiveOverrideSetCmd(rt),
		newAgentsReceiveOverrideAddCmd(rt),
		newAgentsReceiveOverrideRemoveCmd(rt),
	)
	return cmd
}

// axme agents receive-override get <address>
func newAgentsReceiveOverrideGetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "get <address>",
		Short: "Show the receive override for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])

			status, body, _, err := rt.request(cmd.Context(), ctx, "GET",
				"/v1/agents/"+address+"/receive-override", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(body)
			}

			override := asMap(body["override"])
			fmt.Printf("Agent:    %s\n", address)
			fmt.Printf("Override: %s\n", asString(override["override_type"]))

			entries := asSlice(override["entries"])
			if len(entries) == 0 {
				fmt.Println("Entries:  (none)")
			} else {
				fmt.Println("Entries:")
				for _, e := range entries {
					entry := asMap(e)
					fmt.Printf("  - %s  (id: %s)\n", asString(entry["sender_pattern"]), asString(entry["entry_id"]))
				}
			}
			return nil
		},
	}
}

// axme agents receive-override set <address> <open|allowlist|closed|use_org_default>
func newAgentsReceiveOverrideSetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "set <address> <open|allowlist|closed|use_org_default>",
		Short: "Set the receive override for an agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			mode := args[1]

			if mode != "open" && mode != "allowlist" && mode != "closed" && mode != "use_org_default" {
				return &cliError{Code: 2, Msg: "mode must be one of: open, allowlist, closed, use_org_default"}
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "PUT",
				"/v1/agents/"+address+"/receive-override", nil,
				map[string]interface{}{"override_type": mode}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Receive override set to %s for %s\n", mode, address)
			return nil
		},
	}
}

// axme agents receive-override add <address> <sender_pattern>
func newAgentsReceiveOverrideAddCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "add <address> <sender_pattern>",
		Short: "Add a sender pattern to the agent's receive override allowlist",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			pattern := args[1]

			status, body, _, err := rt.request(cmd.Context(), ctx, "POST",
				"/v1/agents/"+address+"/receive-override/entries", nil,
				map[string]interface{}{"sender_pattern": pattern}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Added pattern %q to %s receive override\n", pattern, address)
			return nil
		},
	}
}

// axme agents receive-override remove <address> <entry_id>
func newAgentsReceiveOverrideRemoveCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <address> <entry_id>",
		Short: "Remove a sender pattern entry from the agent's receive override",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			entryID := args[1]

			status, body, _, err := rt.request(cmd.Context(), ctx, "DELETE",
				"/v1/agents/"+address+"/receive-override/entries/"+entryID, nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Removed entry %s from %s receive override\n", entryID, address)
			return nil
		},
	}
}
