package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newQuotaCmd returns the "axme quota" command group for regular users.
// No platform_admin JWT required — uses the standard API key from context.
func newQuotaCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "View your workspace limits and request a tier upgrade",
		Long: `View your current quota limits and usage, or request a higher-tier upgrade.

Alpha tiers:
  unverified      50 intents/day  ·  5 actors   ·  2 service accounts
  email_verified  500 intents/day ·  20 actors  ·  10 service accounts
  corporate       5000 intents/day · 200 actors ·  50 service accounts

Verify your email to move from unverified → email_verified automatically.
To request corporate limits, run: axme quota upgrade-request`,
	}
	cmd.AddCommand(
		newQuotaShowCmd(rt),
		newQuotaSetCmd(rt),
		newQuotaUpgradeRequestCmd(rt),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// axme quota show
// ---------------------------------------------------------------------------

func newQuotaShowCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show current quota limits and usage for your workspace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			if c.OrgID == "" || c.WorkspaceID == "" {
				return fmt.Errorf("org_id and workspace_id must be set in the active context (run 'axme whoami' to check)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			status, body, raw, err := rt.request(
				ctx,
				c,
				"GET",
				"/v1/portal/enterprise/overview",
				map[string]string{
					"org_id":       c.OrgID,
					"workspace_id": c.WorkspaceID,
				},
				nil,
				false,
			)
			if err != nil {
				return err
			}
			if status >= 400 {
				return fmt.Errorf("%s", httpErrorMessage(status, raw))
			}
			if rt.outputJSON {
				fmt.Println(raw)
				return nil
			}

			quotaPolicy := asMap(asMap(body["overview"])["quota_policy"])
			usageSummary := asMap(asMap(body["overview"])["usage_summary"])
			dims := asMap(quotaPolicy["dimensions"])
			usageDims := asMap(usageSummary["dimensions"])

			if len(dims) == 0 {
				fmt.Println("No quota policy found for this workspace.")
				fmt.Println("To request limits, email hello@axme.ai with your org email and usage scenario.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DIMENSION\tUSED\tLIMIT\tUSAGE%")
			for key, limitVal := range dims {
				limit := int64(0)
				switch v := limitVal.(type) {
				case float64:
					limit = int64(v)
				case int64:
					limit = v
				case int:
					limit = int64(v)
				}

				used := int64(0)
				if ud := asMap(usageDims[key]); ud != nil {
					switch v := ud["used"].(type) {
					case float64:
						used = int64(v)
					case int64:
						used = v
					case int:
						used = int64(v)
					}
				}

				pctStr := "-"
				if limit > 0 {
					pct := used * 100 / limit
					bar := ""
					if pct >= 90 {
						bar = " ⚠"
					} else if pct >= 70 {
						bar = " !"
					}
					pctStr = fmt.Sprintf("%d%%%s", pct, bar)
				}
				dimLabel := strings.ReplaceAll(key, "_", " ")
				fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", dimLabel, used, limit, pctStr)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			fmt.Println()
			overage := asString(quotaPolicy["overage_mode"])
			hard := quotaPolicy["hard_enforcement"]
			fmt.Printf("overage_mode=%s  hard_enforcement=%v\n", overage, hard)
			fmt.Println()
			fmt.Println("To request higher limits, email hello@axme.ai with your org email and a description of your use case.")
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// axme quota set
// ---------------------------------------------------------------------------

func newQuotaSetCmd(rt *runtime) *cobra.Command {
	var intentsPerDay int
	var actorsTotal int
	var serviceAccountsPerWorkspace int
	var overageMode string
	var hardEnforcement bool

	cmd := &cobra.Command{
		Use:    "set",
		Short:  "Set quota limits for your workspace (operator only — requires platform key)",
		Hidden: true, // operator-only; not exposed in public CLI help
		Long: `Update quota limits for your workspace.

This command is intended for platform operators only and requires a platform API key.
Regular users should use 'axme quota upgrade-request' to request higher limits.

Dimensions:
  intents-per-day              max intents created per calendar day (UTC)
  actors-total                 max total org members
  service-accounts-per-workspace  max service accounts in workspace`,
		Example: `  axme quota set --intents-per-day 500 --overage-mode block
  axme quota set --actors-total 20 --service-accounts-per-workspace 10`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			if c.OrgID == "" || c.WorkspaceID == "" {
				return fmt.Errorf("org_id and workspace_id must be set in the active context")
			}

			dimensions := map[string]any{}
			if cmd.Flags().Changed("intents-per-day") {
				dimensions["intents_per_day"] = intentsPerDay
			}
			if cmd.Flags().Changed("actors-total") {
				dimensions["actors_total"] = actorsTotal
			}
			if cmd.Flags().Changed("service-accounts-per-workspace") {
				dimensions["service_accounts_per_workspace"] = serviceAccountsPerWorkspace
			}
			if len(dimensions) == 0 {
				return fmt.Errorf("specify at least one dimension flag (--intents-per-day, --actors-total, --service-accounts-per-workspace)")
			}

			actorID := c.OwnerAgent
			if actorID == "" {
				actorID = "cli"
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			payload := map[string]any{
				"org_id":               c.OrgID,
				"workspace_id":         c.WorkspaceID,
				"dimensions":           dimensions,
				"overage_mode":         overageMode,
				"hard_enforcement":     hardEnforcement,
				"updated_by_actor_id":  actorID,
			}

			status, body, raw, err := rt.request(ctx, c, "PATCH", "/v1/quotas", nil, payload, false)
			if err != nil {
				return err
			}
			if status >= 400 {
				return fmt.Errorf("%s", httpErrorMessage(status, raw))
			}
			if rt.outputJSON {
				fmt.Println(raw)
				return nil
			}

			policy := asMap(body["quota_policy"])
			fmt.Println("Quota updated.")
			if policy != nil {
				dims := asMap(policy["dimensions"])
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "DIMENSION\tLIMIT")
				for k, v := range dims {
					fmt.Fprintf(w, "%s\t%v\n", strings.ReplaceAll(k, "_", " "), v)
				}
				_ = w.Flush()
				fmt.Printf("\noverage_mode=%s  hard_enforcement=%v\n", asString(policy["overage_mode"]), policy["hard_enforcement"])
			}
			fmt.Println("\nRun `axme quota show` to verify.")
			return nil
		},
	}
	cmd.Flags().IntVar(&intentsPerDay, "intents-per-day", 0, "Max intents per calendar day")
	cmd.Flags().IntVar(&actorsTotal, "actors-total", 0, "Max total actors in org")
	cmd.Flags().IntVar(&serviceAccountsPerWorkspace, "service-accounts-per-workspace", 0, "Max service accounts in workspace")
	cmd.Flags().StringVar(&overageMode, "overage-mode", "block", "Overage mode: block, grace, bill_overage")
	cmd.Flags().BoolVar(&hardEnforcement, "hard-enforcement", true, "Block requests when limit reached (default: true)")
	return cmd
}

// ---------------------------------------------------------------------------
// axme quota upgrade-request
// ---------------------------------------------------------------------------

func newQuotaUpgradeRequestCmd(rt *runtime) *cobra.Command {
	var company string
	var justification string
	var tier string

	cmd := &cobra.Command{
		Use:   "upgrade-request",
		Short: "Request a corporate-tier quota upgrade for your workspace",
		Long: `Submit a quota upgrade request to the AXME platform team.

The request will be reviewed within 1 business day.
On approval, your workspace quota is automatically upgraded — no further action needed.

Valid upgrade tiers:
  corporate  — 5 000 intents/day, 200 actors, 50 service accounts (default)`,
		Example: `  axme quota upgrade-request \
    --company "Acme Corp" \
    --justification "Running a production pilot with ~50 AI agents"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			company = strings.TrimSpace(company)
			justification = strings.TrimSpace(justification)
			tier = strings.TrimSpace(tier)
			if tier == "" {
				tier = "corporate"
			}

			if len(company) < 2 {
				return fmt.Errorf("--company is required (at least 2 characters)")
			}
			if len(justification) < 10 {
				return fmt.Errorf("--justification is required (at least 10 characters)")
			}
			validTiers := map[string]bool{"email_verified": true, "corporate": true}
			if !validTiers[tier] {
				return fmt.Errorf("--tier must be one of: email_verified, corporate")
			}

			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			requesterID := c.OwnerAgent
			if requesterID == "" {
				requesterID = "cli"
			}

			payload := map[string]any{
				"request_type":       "quota_upgrade",
				"requester_actor_id": requesterID,
				"requested_tier":     tier,
				"company_name":       company,
				"justification":      justification,
			}
			if c.OrgID != "" {
				payload["org_id"] = c.OrgID
			}
			if c.WorkspaceID != "" {
				payload["workspace_id"] = c.WorkspaceID
			}

			status, body, raw, err := rt.request(ctx, c, "POST", "/v1/access-requests", nil, payload, false)
			if err != nil {
				return err
			}
			if status >= 400 {
				return fmt.Errorf("%s", httpErrorMessage(status, raw))
			}
			if rt.outputJSON {
				fmt.Println(raw)
				return nil
			}

			ar := asMap(body["access_request"])
			reqID := asString(ar["access_request_id"])
			state := asString(ar["state"])
			requestedTier := asString(ar["requested_tier"])

			fmt.Printf("Quota upgrade request submitted.\n\n")
			fmt.Printf("  Request ID:     %s\n", reqID)
			fmt.Printf("  State:          %s\n", state)
			fmt.Printf("  Requested tier: %s\n", requestedTier)
			fmt.Printf("  Company:        %s\n", company)
			fmt.Println()
			fmt.Println("The platform team will review your request within 1 business day.")
			fmt.Println("When approved, your quota will be upgraded automatically — no action needed on your side.")
			return nil
		},
	}
	cmd.Flags().StringVar(&company, "company", "", "Company or project name (required, min 2 chars)")
	cmd.Flags().StringVar(&justification, "justification", "", "Why you need higher limits (required, min 10 chars)")
	cmd.Flags().StringVar(&tier, "tier", "corporate", "Tier to request: corporate (default) or email_verified")
	_ = cmd.MarkFlagRequired("company")
	_ = cmd.MarkFlagRequired("justification")
	return cmd
}
