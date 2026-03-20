package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newAgentsPolicyCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage send policies for agent addresses",
	}
	cmd.AddCommand(
		newAgentsPolicyGetCmd(rt),
		newAgentsPolicySetCmd(rt),
		newAgentsPolicyAddCmd(rt),
		newAgentsPolicyRemoveCmd(rt),
	)
	return cmd
}

// axme agents policy get <address>
func newAgentsPolicyGetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "get <address>",
		Short: "Show the send policy for an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])

			status, body, _, err := rt.request(cmd.Context(), ctx, "GET",
				"/v1/agents/"+address+"/policy", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				detail := asString(body["detail"])
				if detail == "" {
					if errObj := asMap(body["error"]); len(errObj) > 0 {
						detail = asString(errObj["message"])
					}
				}
				if detail == "" {
					detail = fmt.Sprintf("HTTP %d", status)
				}
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", detail)}
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(body)
			}

			policy := asMap(body["policy"])
			fmt.Printf("Agent:   %s\n", address)
			fmt.Printf("Mode:    %s\n", asString(policy["policy_type"]))

			entries := asSlice(policy["entries"])
			if len(entries) == 0 {
				fmt.Println("Entries: (none)")
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

// axme agents policy set <address> <mode>
func newAgentsPolicySetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "set <address> <open|allowlist|denylist>",
		Short: "Set the send policy mode for an agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			mode := args[1]

			if mode != "open" && mode != "allowlist" && mode != "denylist" {
				return &cliError{Code: 2, Msg: "mode must be one of: open, allowlist, denylist"}
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "PUT",
				"/v1/agents/"+address+"/policy", nil,
				map[string]interface{}{"policy_type": mode}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", asString(body["detail"]))}
			}

			fmt.Printf("Policy set to %s for %s\n", mode, address)
			return nil
		},
	}
}

// axme agents policy add <address> <sender_pattern>
func newAgentsPolicyAddCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "add <address> <sender_pattern>",
		Short: "Add a sender pattern to the agent's policy",
		Long: `Add a sender pattern to the allowlist or denylist.

Patterns can be exact addresses or use wildcards:
  agent://org/workspace/specific-agent   — exact match
  agent://org/workspace/*                — all agents in workspace
  agent://org/*                          — all agents in org
  *                                      — any sender`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			pattern := args[1]

			status, body, _, err := rt.request(cmd.Context(), ctx, "POST",
				"/v1/agents/"+address+"/policy/entries", nil,
				map[string]interface{}{"sender_pattern": pattern}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", asString(body["detail"]))}
			}

			fmt.Printf("Added pattern %q to %s\n", pattern, address)
			return nil
		},
	}
}

// axme agents policy remove <address> <entry_id>
func newAgentsPolicyRemoveCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <address> <entry_id>",
		Short: "Remove a sender pattern entry from the agent's policy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			address := normalizeAgentAddress(args[0])
			entryID := args[1]

			status, body, _, err := rt.request(cmd.Context(), ctx, "DELETE",
				"/v1/agents/"+address+"/policy/entries/"+entryID, nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", asString(body["detail"]))}
			}

			fmt.Printf("Removed entry %s from %s\n", entryID, address)
			return nil
		},
	}
}

func normalizeAgentAddress(addr string) string {
	if len(addr) > 0 && addr[0] != 'a' {
		// If it doesn't start with "agent://", user likely passed bare name
		return addr
	}
	return addr
}
