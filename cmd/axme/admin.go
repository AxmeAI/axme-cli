package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newAdminCmd returns the top-level "axme admin" command.
// All sub-commands require a platform_admin JWT in the active context
// (bearer_token or AXME_BEARER_TOKEN env var) alongside the platform API key.
func newAdminCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "AXME Cloud operator commands (platform_admin only)",
		Long: `Platform-owner commands for managing AXME Cloud.
All sub-commands require a platform_admin JWT.
Set bearer_token in the active context or AXME_BEARER_TOKEN env var.`,
	}
	cmd.AddCommand(
		newAdminUsersCmd(rt),
		newAdminQuotaCmd(rt),
		newAdminSchedulerCmd(rt),
		newAdminAuditCmd(rt),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// axme admin users
// ---------------------------------------------------------------------------

func newAdminUsersCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Manage AXME Cloud user organisations",
	}
	cmd.AddCommand(
		newAdminUsersListCmd(rt),
		newAdminUsersSuspendCmd(rt),
		newAdminUsersUnsuspendCmd(rt),
	)
	return cmd
}

func newAdminUsersListCmd(rt *runtime) *cobra.Command {
	var limit int
	var domainFilter string
	var emailFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List alpha user organisations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			query := map[string]string{
				"limit": strconv.Itoa(limit),
			}
			if domainFilter != "" {
				query["primary_domain"] = domainFilter
			}
			if emailFilter != "" {
				query["email"] = emailFilter
			}

			status, body, raw, err := rt.request(ctx, c, "GET", "/v1/organizations", query, nil, true)
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

			items := asSlice(body["organizations"])
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ORG_ID\tNAME\tPRIMARY_DOMAIN\tCREATED_AT")
			for _, item := range items {
				m := asMap(item)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					asString(m["org_id"]),
					asString(m["name"]),
					asString(m["primary_domain"]),
					asString(m["created_at"]),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of organisations to return (1–500)")
	cmd.Flags().StringVar(&domainFilter, "domain", "", "Filter by primary domain (prefix match)")
	cmd.Flags().StringVar(&emailFilter, "email", "", "Filter by registered email")
	return cmd
}

func newAdminUsersSuspendCmd(rt *runtime) *cobra.Command {
	var orgID string
	var reason string

	cmd := &cobra.Command{
		Use:   "suspend",
		Short: "Suspend an organisation (blocks all API access)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(orgID) == "" {
				return fmt.Errorf("--org-id is required")
			}
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			payload := map[string]any{
				"status": "suspended",
			}
			if reason != "" {
				payload["metadata"] = map[string]any{"suspension_reason": reason}
			}

			path := fmt.Sprintf("/v1/organizations/%s", strings.TrimSpace(orgID))
			status, body, raw, err := rt.request(ctx, c, "PATCH", path, nil, payload, true)
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
			org := asMap(body["organization"])
			fmt.Printf("Suspended: %s (%s)\n", asString(org["org_id"]), asString(org["name"]))
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organisation ID to suspend (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "Human-readable suspension reason (stored in metadata)")
	_ = cmd.MarkFlagRequired("org-id")
	return cmd
}

func newAdminUsersUnsuspendCmd(rt *runtime) *cobra.Command {
	var orgID string

	cmd := &cobra.Command{
		Use:   "unsuspend",
		Short: "Restore a suspended organisation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(orgID) == "" {
				return fmt.Errorf("--org-id is required")
			}
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			payload := map[string]any{"status": "active"}
			path := fmt.Sprintf("/v1/organizations/%s", strings.TrimSpace(orgID))
			status, body, raw, err := rt.request(ctx, c, "PATCH", path, nil, payload, true)
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
			org := asMap(body["organization"])
			fmt.Printf("Unsuspended: %s (%s)\n", asString(org["org_id"]), asString(org["name"]))
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organisation ID to restore (required)")
	_ = cmd.MarkFlagRequired("org-id")
	return cmd
}

// ---------------------------------------------------------------------------
// axme admin quota
// ---------------------------------------------------------------------------

func newAdminQuotaCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quota",
		Short: "Manage workspace quota policies",
	}
	cmd.AddCommand(
		newAdminQuotaGetCmd(rt),
		newAdminQuotaSetCmd(rt),
		newAdminQuotaResetCmd(rt),
	)
	return cmd
}

func newAdminQuotaGetCmd(rt *runtime) *cobra.Command {
	var orgID string
	var workspaceID string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the quota policy for a workspace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			status, body, raw, err := rt.request(ctx, c, "GET", "/v1/quotas", map[string]string{
				"org_id":       orgID,
				"workspace_id": workspaceID,
			}, nil, true)
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
			policy := asMap(body["quota_policy"])
			dims := asMap(policy["dimensions"])
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DIMENSION\tLIMIT")
			for k, v := range dims {
				fmt.Fprintf(w, "%s\t%v\n", k, v)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Printf("\nhard_enforcement=%v  overage_mode=%s  soft_threshold=%v%%\n",
				policy["hard_enforcement"],
				asString(policy["overage_mode"]),
				policy["soft_threshold_percent"],
			)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organisation ID (required)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "Workspace ID (required)")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("workspace-id")
	return cmd
}

func newAdminQuotaSetCmd(rt *runtime) *cobra.Command {
	var orgID string
	var workspaceID string
	var intentsPerDay int
	var actorsTotal int
	var serviceAccountsPerWorkspace int
	var overageMode string
	var softThreshold int
	var noHardEnforcement bool

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Override quota dimensions for a workspace",
		Long: `Set or update quota limits for a workspace.
Omit a flag to keep the existing value for that dimension.
At least one dimension flag must be provided.

Tier shortcuts (applied as full tier override):
  --tier unverified        50 intents/day, 5 actors, 2 SAs
  --tier email_verified   500 intents/day, 20 actors, 10 SAs
  --tier corporate       5000 intents/day, 200 actors, 50 SAs`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dims := map[string]any{}
			if cmd.Flags().Changed("intents-per-day") {
				dims["intents_per_day"] = intentsPerDay
			}
			if cmd.Flags().Changed("actors-total") {
				dims["actors_total"] = actorsTotal
			}
			if cmd.Flags().Changed("service-accounts") {
				dims["service_accounts_per_workspace"] = serviceAccountsPerWorkspace
			}
			if len(dims) == 0 {
				return fmt.Errorf("at least one of --intents-per-day, --actors-total, --service-accounts is required")
			}

			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			payload := map[string]any{
				"org_id":                 orgID,
				"workspace_id":           workspaceID,
				"dimensions":             dims,
				"overage_mode":           overageMode,
				"soft_threshold_percent": softThreshold,
				"hard_enforcement":       !noHardEnforcement,
			}
			status, body, raw, err := rt.request(ctx, c, "PATCH", "/v1/quotas", nil, payload, true)
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
			policy := asMap(body["quota_policy"])
			dimsOut, _ := json.MarshalIndent(policy["dimensions"], "", "  ")
			fmt.Printf("Quota updated for workspace %s:\n%s\n", workspaceID, string(dimsOut))
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organisation ID (required)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "Workspace ID (required)")
	cmd.Flags().IntVar(&intentsPerDay, "intents-per-day", 0, "Max intents per day")
	cmd.Flags().IntVar(&actorsTotal, "actors-total", 0, "Max total actors")
	cmd.Flags().IntVar(&serviceAccountsPerWorkspace, "service-accounts", 0, "Max service accounts per workspace")
	cmd.Flags().StringVar(&overageMode, "overage-mode", "block", "Overage mode: block or warn")
	cmd.Flags().IntVar(&softThreshold, "soft-threshold", 80, "Soft warning threshold percent (0–100)")
	cmd.Flags().BoolVar(&noHardEnforcement, "no-hard-enforcement", false, "Disable hard enforcement (warn-only)")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("workspace-id")
	return cmd
}

func newAdminQuotaResetCmd(rt *runtime) *cobra.Command {
	var orgID string
	var workspaceID string
	var tier string

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset quota to a named tier default",
		Long: `Reset the quota policy to one of the built-in alpha tier defaults.

Valid tiers: unverified, email_verified, corporate`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tierDims := map[string]map[string]any{
				"unverified": {
					"intents_per_day":                50,
					"actors_total":                   5,
					"service_accounts_per_workspace": 2,
				},
				"email_verified": {
					"intents_per_day":                500,
					"actors_total":                   20,
					"service_accounts_per_workspace": 10,
				},
				"corporate": {
					"intents_per_day":                5000,
					"actors_total":                   200,
					"service_accounts_per_workspace": 50,
				},
			}
			dims, ok := tierDims[strings.TrimSpace(tier)]
			if !ok {
				return fmt.Errorf("unknown tier %q — valid values: unverified, email_verified, corporate", tier)
			}

			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			payload := map[string]any{
				"org_id":                 orgID,
				"workspace_id":           workspaceID,
				"dimensions":             dims,
				"overage_mode":           "block",
				"soft_threshold_percent": 80,
				"hard_enforcement":       true,
			}
			status, body, raw, err := rt.request(ctx, c, "PATCH", "/v1/quotas", nil, payload, true)
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
			policy := asMap(body["quota_policy"])
			dimsOut, _ := json.MarshalIndent(policy["dimensions"], "", "  ")
			fmt.Printf("Quota reset to %q tier for workspace %s:\n%s\n", tier, workspaceID, string(dimsOut))
			return nil
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organisation ID (required)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "Workspace ID (required)")
	cmd.Flags().StringVar(&tier, "tier", "", "Tier name: unverified, email_verified, corporate (required)")
	_ = cmd.MarkFlagRequired("org-id")
	_ = cmd.MarkFlagRequired("workspace-id")
	_ = cmd.MarkFlagRequired("tier")
	return cmd
}

// ---------------------------------------------------------------------------
// axme admin scheduler
// ---------------------------------------------------------------------------

func newAdminSchedulerCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scheduler",
		Short: "Scheduler and delivery health",
	}
	cmd.AddCommand(newAdminSchedulerHealthCmd(rt))
	return cmd
}

func newAdminSchedulerHealthCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Show scheduler and delivery health summary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			status, body, raw, err := rt.request(ctx, c, "GET", "/health", nil, nil, true)
			if err != nil {
				return err
			}
			if rt.outputJSON {
				fmt.Println(raw)
				return nil
			}
			overall := asString(body["status"])
			if overall == "" {
				overall = fmt.Sprintf("HTTP %d", status)
			}
			fmt.Printf("Overall status: %s\n\n", overall)

			checks := asSlice(body["checks"])
			if len(checks) == 0 {
				checks = asSlice(body["components"])
			}
			if len(checks) > 0 {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "COMPONENT\tSTATUS\tDETAIL")
				for _, item := range checks {
					m := asMap(item)
					name := asString(m["name"])
					if name == "" {
						name = asString(m["component"])
					}
					chkStatus := asString(m["status"])
					detail := asString(m["message"])
					if detail == "" {
						detail = asString(m["detail"])
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", name, chkStatus, detail)
				}
				_ = w.Flush()
			}
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// axme admin audit
// ---------------------------------------------------------------------------

func newAdminAuditCmd(rt *runtime) *cobra.Command {
	var limit int
	var actionPrefix string
	var ownerAgent string

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View platform audit log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := rt.effectiveContext()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			query := map[string]string{
				"limit": strconv.Itoa(limit),
			}
			if actionPrefix != "" {
				query["action_prefix"] = actionPrefix
			}
			if ownerAgent != "" {
				query["owner_agent"] = ownerAgent
			}

			status, body, raw, err := rt.request(ctx, c, "GET", "/v1/audit/events", query, nil, true)
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

			events := asSlice(body["events"])
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TIMESTAMP\tACTION\tSTATUS\tENDPOINT")
			for _, item := range events {
				m := asMap(item)
				ts := asString(m["timestamp"])
				if ts == "" {
					ts = asString(m["created_at"])
				}
				fmt.Fprintf(w, "%s\t%s\t%v\t%s\n",
					ts,
					asString(m["action"]),
					m["status_code"],
					asString(m["endpoint"]),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum number of audit events (1–1000)")
	cmd.Flags().StringVar(&actionPrefix, "action", "", "Filter by action prefix (e.g. alpha_bootstrap)")
	cmd.Flags().StringVar(&ownerAgent, "owner-agent", "", "Filter by owner agent")
	return cmd
}
