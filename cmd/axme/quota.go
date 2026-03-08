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

			status, body, raw, err := rt.request(ctx, c, "GET", "/v1/portal/enterprise/overview", nil, nil, false)
			if err != nil {
				return err
			}
			if status >= 400 {
				return fmt.Errorf("server returned %d: %s", status, raw)
			}
			if rt.outputJSON {
				fmt.Println(raw)
				return nil
			}

			quotaPolicy := asMap(body["quota_policy"])
			usageSummary := asMap(body["usage_summary"])
			dims := asMap(quotaPolicy["dimensions"])
			usageDims := asMap(usageSummary["dimensions"])

			if len(dims) == 0 {
				fmt.Println("No quota policy found for this workspace.")
				fmt.Println("Verify your email to activate limits: the email was sent at onboarding.")
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
			fmt.Println("To request higher limits: axme quota upgrade-request --company \"...\" --justification \"...\"")
			return nil
		},
	}
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

			payload := map[string]any{
				"request_type":    "quota_upgrade",
				"requester_actor_id": c.OwnerAgent,
				"requested_tier":  tier,
				"company_name":    company,
				"justification":   justification,
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
				return fmt.Errorf("server returned %d: %s", status, raw)
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
