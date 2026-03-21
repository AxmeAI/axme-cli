package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newOrgReceivePolicyCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receive-policy",
		Short: "Manage org-level receive policy for cross-org intents",
	}
	cmd.AddCommand(
		newOrgReceivePolicyGetCmd(rt),
		newOrgReceivePolicySetCmd(rt),
		newOrgReceivePolicyAddCmd(rt),
		newOrgReceivePolicyRemoveCmd(rt),
	)
	return cmd
}

// axme org receive-policy get
func newOrgReceivePolicyGetCmd(rt *runtime) *cobra.Command {
	var orgID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the receive policy for the current organization",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			resolvedOrgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(orgID))
			if err != nil {
				return err
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "GET",
				"/v1/organizations/"+resolvedOrgID+"/receive-policy", nil, nil, true)
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

			policy := asMap(body["policy"])
			fmt.Printf("Org:     %s\n", resolvedOrgID)
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
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization ID (defaults to selected org)")
	return cmd
}

// axme org receive-policy set <open|allowlist|closed>
func newOrgReceivePolicySetCmd(rt *runtime) *cobra.Command {
	var orgID string
	cmd := &cobra.Command{
		Use:   "set <open|allowlist|closed>",
		Short: "Set the receive policy mode for the organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := args[0]
			if mode != "open" && mode != "allowlist" && mode != "closed" {
				return &cliError{Code: 2, Msg: "mode must be one of: open, allowlist, closed"}
			}

			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			resolvedOrgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(orgID))
			if err != nil {
				return err
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "PUT",
				"/v1/organizations/"+resolvedOrgID+"/receive-policy", nil,
				map[string]interface{}{"policy_type": mode}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Receive policy set to %s for org %s\n", mode, resolvedOrgID)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization ID (defaults to selected org)")
	return cmd
}

// axme org receive-policy add <sender_pattern>
func newOrgReceivePolicyAddCmd(rt *runtime) *cobra.Command {
	var orgID string
	cmd := &cobra.Command{
		Use:   "add <sender_pattern>",
		Short: "Add a sender pattern to the org receive policy allowlist",
		Long: `Add a sender pattern to the receive policy allowlist.

Patterns can be exact addresses or use wildcards:
  agent://org/workspace/specific-agent   — exact match
  agent://org/workspace/*                — all agents in workspace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]

			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			resolvedOrgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(orgID))
			if err != nil {
				return err
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "POST",
				"/v1/organizations/"+resolvedOrgID+"/receive-policy/entries", nil,
				map[string]interface{}{"sender_pattern": pattern}, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Added pattern %q to org %s receive policy\n", pattern, resolvedOrgID)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization ID (defaults to selected org)")
	return cmd
}

// axme org receive-policy remove <entry_id>
func newOrgReceivePolicyRemoveCmd(rt *runtime) *cobra.Command {
	var orgID string
	cmd := &cobra.Command{
		Use:   "remove <entry_id>",
		Short: "Remove a sender pattern entry from the org receive policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entryID := args[0]

			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			resolvedOrgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(orgID))
			if err != nil {
				return err
			}

			status, body, _, err := rt.request(cmd.Context(), ctx, "DELETE",
				"/v1/organizations/"+resolvedOrgID+"/receive-policy/entries/"+entryID, nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed: %s", extractErrorDetail(body))}
			}

			fmt.Printf("Removed entry %s from org %s receive policy\n", entryID, resolvedOrgID)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization ID (defaults to selected org)")
	return cmd
}

// extractErrorDetail extracts a human-readable error message from an API response body.
func extractErrorDetail(body map[string]interface{}) string {
	detail := asString(body["detail"])
	if detail == "" {
		if errObj := asMap(body["error"]); len(errObj) > 0 {
			detail = asString(errObj["message"])
		}
	}
	if detail == "" {
		detail = "unknown error"
	}
	return detail
}
