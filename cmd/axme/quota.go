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

Tiers:
  Starter (default — applied on first login):
    500 intents/day · 120 req/min · 128 KB payload · 1 GB storage
    20 agents · 20 SSE streams

  Business (request via: axme quota upgrade-request):
    5000 intents/day · 600 req/min · 512 KB payload · 10 GB storage
    50 agents · 100 SSE streams`,
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

			// Ordered dimension display with human-readable labels
			dimOrder := []struct{ key, label string }{
				{"intents_per_day", "Intents / day"},
				{"rate_limit_per_minute", "Rate limit / min"},
				{"payload_max_bytes", "Max payload"},
				{"storage_bytes", "Storage"},
				{"actors_total", "Team members"},
				{"service_accounts_per_workspace", "Agents"},
				{"sse_streams_max", "SSE streams"},
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DIMENSION\tUSED\tLIMIT\tUSAGE%")
			for _, dim := range dimOrder {
				limitVal, exists := dims[dim.key]
				if !exists {
					continue
				}
				limit := toInt64(limitVal)
				used := int64(0)
				if ud := asMap(usageDims[dim.key]); ud != nil {
					used = toInt64(ud["used"])
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

				// Format bytes dimensions as human-readable
				usedStr := fmt.Sprintf("%d", used)
				limitStr := fmt.Sprintf("%d", limit)
				if dim.key == "payload_max_bytes" || dim.key == "storage_bytes" {
					usedStr = formatBytes(used)
					limitStr = formatBytes(limit)
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", dim.label, usedStr, limitStr, pctStr)
			}
			// Show any extra dimensions not in our ordered list
			for key, limitVal := range dims {
				found := false
				for _, dim := range dimOrder {
					if dim.key == key {
						found = true
						break
					}
				}
				if !found {
					dimLabel := strings.ReplaceAll(key, "_", " ")
					fmt.Fprintf(w, "%s\t%d\t%d\t-\n", dimLabel, int64(0), toInt64(limitVal))
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			fmt.Println()
			// Display tier name (map API names to user-friendly)
			tierName := asString(quotaPolicy["tier"])
			friendlyTier := map[string]string{
				"email_verified": "Starter", "corporate": "Business",
			}
			if ft, ok := friendlyTier[tierName]; ok {
				tierName = ft
			}
			if tierName != "" {
				fmt.Printf("Tier: %s\n", tierName)
			}
			overage := asString(quotaPolicy["overage_mode"])
			hard := quotaPolicy["hard_enforcement"]
			fmt.Printf("overage_mode=%s  hard_enforcement=%v\n", overage, hard)
			fmt.Println()
			fmt.Println("To request higher limits: axme quota upgrade-request --company <name> --justification <reason>")
			return nil
		},
	}
	return cmd
}

func toInt64(v interface{}) int64 {
	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case int:
		return int64(val)
	}
	return 0
}

func formatBytes(b int64) string {
	switch {
	case b >= 1073741824:
		return fmt.Sprintf("%.1f GB", float64(b)/1073741824)
	case b >= 1048576:
		return fmt.Sprintf("%.1f MB", float64(b)/1048576)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	case b == 0:
		return "0"
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// quota set removed — users must not set their own quotas.
// Quota management is server-side only (admin API / direct DB).

// ---------------------------------------------------------------------------
// axme quota upgrade-request
// ---------------------------------------------------------------------------

func newQuotaUpgradeRequestCmd(rt *runtime) *cobra.Command {
	var company string
	var justification string
	var tier string

	cmd := &cobra.Command{
		Use:   "upgrade-request",
		Short: "Request a Business-tier quota upgrade for your workspace",
		Long: `Submit a quota upgrade request to the AXME platform team.

The request will be reviewed within 1 business day.
On approval, your workspace quota is automatically upgraded — no further action needed.

Valid upgrade tiers:
  business  — 5000 intents/day · 600 req/min · 512 KB payload · 10 GB storage · 50 agents · 100 SSE streams`,
		Example: `  axme quota upgrade-request \
    --company "Acme Corp" \
    --justification "Running a production pilot with ~50 AI agents"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			company = strings.TrimSpace(company)
			justification = strings.TrimSpace(justification)
			tier = strings.TrimSpace(tier)
			if tier == "" {
				tier = "business"
			}

			if len(company) < 2 {
				return fmt.Errorf("--company is required (at least 2 characters)")
			}
			if len(justification) < 10 {
				return fmt.Errorf("--justification is required (at least 10 characters)")
			}
			// Map user-friendly tier names to API values
			tierMap := map[string]string{
				"starter": "email_verified", "email_verified": "email_verified",
				"business": "corporate", "corporate": "corporate",
			}
			apiTier, ok := tierMap[strings.ToLower(tier)]
			if !ok {
				return fmt.Errorf("--tier must be one of: starter, business")
			}
			tier = apiTier

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
	cmd.Flags().StringVar(&tier, "tier", "business", "Tier to request: business (default) or starter")
	_ = cmd.MarkFlagRequired("company")
	_ = cmd.MarkFlagRequired("justification")
	return cmd
}
