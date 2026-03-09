package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const (
	defaultAlphaOnboardingURL = "https://cloud.axme.ai/alpha/cli"
	defaultCloudAPIBaseURL    = "https://api.cloud.axme.ai"
	defaultLocalAPIBaseURL    = "http://127.0.0.1:8100"
)

type cliError struct {
	Code int
	Msg  string
}

func (e *cliError) Error() string { return e.Msg }

type appConfig struct {
	ActiveContext string                   `json:"active_context"`
	Contexts      map[string]*clientConfig `json:"contexts"`
}

type clientConfig struct {
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key,omitempty"`
	ActorToken   string `json:"actor_token,omitempty"`
	BearerToken  string `json:"bearer_token,omitempty"`
	OwnerAgent   string `json:"owner_agent,omitempty"`
	OrgID        string `json:"org_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	Environment  string `json:"environment,omitempty"`
	RefreshToken string `json:"-"` // loaded from secret store, never persisted to config.json
	secretsLoaded bool  `json:"-"`
}

type runtime struct {
	cfgFile      string
	cfg          *appConfig
	secretStore  secretStore
	httpClient   *http.Client
	outputJSON   bool
	contextName  string
	overrideBase string
	overrideKey  string
	overrideJWT  string
	overrideOrg  string
	overrideWs   string
	overrideOwn  string
	overrideEnv  string
}

type intentRow struct {
	ID        string         `json:"id"`
	Status    string         `json:"status"`
	Age       string         `json:"age"`
	LastStep  string         `json:"last_step"`
	Owner     string         `json:"owner"`
	UpdatedAt string         `json:"updated_at"`
	Payload   map[string]any `json:"payload,omitempty"`
}

func main() {
	os.Exit(run())
}

func run() int {
	cfgFile, err := resolveConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, err := loadConfig(cfgFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rt := &runtime{
		cfgFile: cfgFile,
		cfg:     cfg,
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
	secretStore, err := initSecretStore(cfgFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rt.secretStore = secretStore
	if warning := secretStorageFallbackWarning(rt.secretStore); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	root := buildRoot(rt)
	if err := root.Execute(); err != nil {
		var ce *cliError
		if errors.As(err, &ce) {
			fmt.Fprintln(os.Stderr, ce.Msg)
			return ce.Code
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func buildRoot(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "axme",
		Short:        "Axme B2B infra CLI",
		SilenceUsage: true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().BoolVar(&rt.outputJSON, "json", false, "machine-readable JSON output")
	cmd.PersistentFlags().StringVar(&rt.contextName, "context-name", "", "override active context name")
	cmd.PersistentFlags().StringVar(&rt.overrideBase, "base-url", "", "gateway base URL override")
	cmd.PersistentFlags().StringVar(&rt.overrideKey, "api-key", "", "gateway API key override")
	cmd.PersistentFlags().StringVar(&rt.overrideJWT, "actor-token", "", "actor token override")
	cmd.PersistentFlags().StringVar(&rt.overrideJWT, "bearer-token", "", "bearer token override")
	cmd.PersistentFlags().StringVar(&rt.overrideOrg, "org-id", "", "default org id override")
	cmd.PersistentFlags().StringVar(&rt.overrideWs, "workspace-id", "", "default workspace id override")
	cmd.PersistentFlags().StringVar(&rt.overrideOwn, "owner-agent", "", "owner agent override")
	cmd.PersistentFlags().StringVar(&rt.overrideEnv, "environment", "", "environment override")

	cmd.AddCommand(
		newLoginCmd(rt),
		newLogoutCmd(rt),
		newWhoamiCmd(rt),
		newSessionCmd(rt),
		newOrgCmd(rt),
		newWorkspaceCmd(rt),
		newMemberCmd(rt),
		newContextCmd(rt),
		newInitCmd(rt),
		newExamplesCmd(rt),
		newRunCmd(rt),
		newIntentsCmd(rt),
		newLogsCmd(rt),
		newTraceCmd(rt),
		newAgentsCmd(rt),
		newServiceAccountsCmd(rt),
		newKeysCmd(rt),
		newStatusCmd(rt),
		newDoctorCmd(rt),
		newVersionCmd(rt),
		newRawCmd(rt),
		newQuotaCmd(rt),
	)
	return cmd
}

func newLoginCmd(rt *runtime) *cobra.Command {
	var key string
	var token string
	var owner string
	var targetContext string
	var useWebOnboarding bool
	var useBrowserFlow bool
	var useAlphaBootstrap bool
	var noBrowser bool
	var onboardingURL string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to your AXME account",
		Long: `Sign in to AXME using your email address.

A one-time code will be sent to your email. Enter it at the prompt to complete
sign-in. Your credentials are stored securely for future CLI commands.

Use --browser to use the legacy browser-based approval flow instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if onboardingURL == "" {
				onboardingURL = defaultAlphaOnboardingURL
			}

			ctxName := targetContext
			if ctxName == "" {
				ctxName = rt.activeContextName()
			}
			accountCtx := rt.ensureContext(ctxName)
			prepareCloudAlphaContext(accountCtx)
			if key != "" {
				accountCtx.APIKey = strings.TrimSpace(key)
			}
			if token != "" {
				accountCtx.setActorToken(strings.TrimSpace(token))
			}
			if owner != "" {
				accountCtx.OwnerAgent = strings.TrimSpace(owner)
			}
			rt.applyPersistentContextOverrides(accountCtx)

			// Legacy browser/device flow
			if useBrowserFlow {
				return rt.runDeviceLogin(cmd.Context(), ctxName, !noBrowser)
			}

			// Alpha bootstrap flow
			if useAlphaBootstrap {
				return rt.runAlphaBootstrapLogin(cmd.Context(), ctxName)
			}

			// Manual key/token provided — just store them
			if key != "" || token != "" {
				if err := rt.persistConfig(); err != nil {
					return err
				}
				if rt.outputJSON {
					return rt.printJSON(map[string]any{"ok": true, "context": ctxName, "method": "manual"})
				}
				fmt.Fprintf(os.Stderr, "Credentials stored in context %q.\n", ctxName)
				return nil
			}

			// Web onboarding (legacy)
			if useWebOnboarding {
				if !noBrowser {
					if err := openURLInBrowser(onboardingURL); err != nil && !rt.outputJSON {
						fmt.Fprintf(os.Stderr, "warning: could not open browser automatically: %v\n", err)
					}
				}
				enteredKey, err := promptLine("Paste AXME API key (or press Enter to cancel): ")
				if err != nil {
					return err
				}
				if strings.TrimSpace(enteredKey) == "" {
					if rt.outputJSON {
						return rt.printJSON(map[string]any{"ok": false, "error": "no key provided"})
					}
					fmt.Fprintln(os.Stderr, "No key provided. Run `axme login` to use email sign-in.")
					return nil
				}
				accountCtx.APIKey = strings.TrimSpace(enteredKey)
				if err := rt.persistConfig(); err != nil {
					return err
				}
				if rt.outputJSON {
					return rt.printJSON(map[string]any{"ok": true, "context": ctxName})
				}
				fmt.Fprintf(os.Stderr, "API key stored in context %q.\n", ctxName)
				return nil
			}

			// Default: email-first OTP flow
			if interactiveInputAvailable() {
				return rt.runEmailLogin(cmd.Context(), ctxName)
			}

			// Non-interactive with no credentials
			msg := fmt.Sprintf(
				"No credentials were stored. Run `axme login` interactively to sign in, or pass --api-key/--actor-token for manual setup.",
			)
			if rt.outputJSON {
				return rt.printJSON(map[string]any{
					"ok":          false,
					"error":       "no_credentials",
					"message":     msg,
					"recommended": "axme login",
				})
			}
			return fmt.Errorf("%s", msg)
		},
	}
	cmd.Flags().StringVar(&key, "api-key", "", "API key")
	cmd.Flags().StringVar(&token, "actor-token", "", "Actor token (Authorization: Bearer ...)")
	cmd.Flags().StringVar(&token, "bearer-token", "", "Bearer token")
	cmd.Flags().StringVar(&owner, "owner-agent", "", "Owner agent (e.g. agent://alice)")
	cmd.Flags().StringVar(&targetContext, "context", "", "Target context name")
	cmd.Flags().BoolVar(&useWebOnboarding, "web", false, "legacy fallback")
	cmd.Flags().BoolVar(&useBrowserFlow, "browser", false, "use browser-based approval flow instead of email sign-in")
	cmd.Flags().BoolVar(&useAlphaBootstrap, "bootstrap-alpha", false, "legacy alpha bootstrap flow")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't try to open browser automatically (only relevant with --browser)")
	cmd.Flags().StringVar(&onboardingURL, "onboarding-url", defaultAlphaOnboardingURL, "CLI onboarding page URL")
	_ = cmd.Flags().MarkHidden("web")
	_ = cmd.Flags().MarkHidden("bootstrap-alpha")
	_ = cmd.Flags().MarkHidden("onboarding-url")
	return cmd
}

func (rt *runtime) runAlphaBootstrapLogin(ctx context.Context, ctxName string) error {
	c := rt.ensureContext(ctxName)
	prepareCloudAlphaContext(c)

	if !rt.outputJSON {
		fmt.Fprintln(os.Stderr, "Starting cloud alpha onboarding...")
		fmt.Fprintf(os.Stderr, "Base URL: %s\n\n", c.BaseURL)
	}

	email, err := promptRequiredLine("Email: ")
	if err != nil {
		return err
	}
	company, err := promptLine("Company (optional): ")
	if err != nil {
		return err
	}
	useCase, err := promptRequiredLine("Use case (what are you building?): ")
	if err != nil {
		return err
	}

	payload := map[string]any{
		"email":    email,
		"use_case": useCase,
	}
	if strings.TrimSpace(company) != "" {
		payload["company"] = strings.TrimSpace(company)
	}

	status, body, raw, err := rt.request(ctx, c, "POST", "/v1/alpha/bootstrap", nil, payload, true)
	if err != nil {
		return fmt.Errorf("alpha onboarding failed: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("alpha onboarding failed (%d): %s", status, raw)
	}

	org := asMap(body["organization"])
	workspace := asMap(body["workspace"])
	keyBody := asMap(body["key"])
	emailVerification := asMap(body["email_verification"])

	apiKey := asString(keyBody["token"])
	if apiKey == "" {
		return fmt.Errorf("alpha onboarding succeeded but no API key was returned")
	}

	c.APIKey = apiKey
	if orgID := asString(org["org_id"]); orgID != "" {
		c.OrgID = orgID
	}
	if workspaceID := asString(workspace["workspace_id"]); workspaceID != "" {
		c.WorkspaceID = workspaceID
	}

	if err := rt.persistConfig(); err != nil {
		return err
	}

	result := map[string]any{
		"ok":             true,
		"context":        ctxName,
		"base_url":       c.BaseURL,
		"org_id":         c.OrgID,
		"workspace_id":   c.WorkspaceID,
		"organization":   org,
		"workspace":      workspace,
		"email":          asString(emailVerification["email"]),
		"verify_status":  asString(emailVerification["status"]),
		"verify_expires": asString(emailVerification["expires_at"]),
	}
	if rt.outputJSON {
		return rt.printJSON(result)
	}

	fmt.Fprintln(os.Stderr, "Alpha workspace created and saved to your CLI context.")
	fmt.Fprintf(os.Stderr, "Context:      %s\n", ctxName)
	fmt.Fprintf(os.Stderr, "Organization: %s\n", asString(org["name"]))
	fmt.Fprintf(os.Stderr, "org_id:       %s\n", c.OrgID)
	fmt.Fprintf(os.Stderr, "Workspace:    %s\n", asString(workspace["name"]))
	fmt.Fprintf(os.Stderr, "workspace_id: %s\n", c.WorkspaceID)
	if emailAddress := asString(emailVerification["email"]); emailAddress != "" {
		fmt.Fprintf(os.Stderr, "Email verify: pending for %s\n", emailAddress)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Next commands:")
	fmt.Fprintln(os.Stderr, "  axme whoami")
	fmt.Fprintln(os.Stderr, "  axme quota show")
	fmt.Fprintln(os.Stderr)
	return nil
}

func newWhoamiCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show current identity and context",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			out := map[string]any{
				"context":     rt.activeContextName(),
				"base_url":    ctx.BaseURL,
				"org_id":      ctx.OrgID,
				"workspace_id": ctx.WorkspaceID,
				"environment": ctx.Environment,
				"secret_storage": map[string]any{
					"mode":   rt.secretStore.Mode(),
					"detail": rt.secretStore.Detail(),
				},
				"status": map[string]any{
					"has_workspace_access_token": ctx.APIKey != "",
					"has_account_session":        ctx.resolvedActorToken() != "",
					"has_local_workspace_cache":  ctx.OrgID != "" && ctx.WorkspaceID != "",
				},
			}
			if ctx.OwnerAgent != "" {
				out["owner_agent"] = ctx.OwnerAgent
			}

			var serverContextHint string
			if personalContext, err := rt.personalContextFromServer(cmd.Context(), ctx); err == nil {
				out["server_context"] = asMap(personalContext["context"])
				if account := asMap(personalContext["account"]); len(account) > 0 {
					out["account"] = account
				}
				if selectedOrg := asMap(personalContext["selected_organization"]); len(selectedOrg) > 0 {
					out["selected_organization"] = selectedOrg
				}
				if selectedWorkspace := asMap(personalContext["selected_workspace"]); len(selectedWorkspace) > 0 {
					out["selected_workspace"] = selectedWorkspace
				}
				if guidance := asMap(personalContext["guidance"]); len(guidance) > 0 {
					out["server_guidance"] = guidance
				}
				status := asMap(out["status"])
				status["account_signed_in"] = true
				status["workspace_attached"] = len(asMap(personalContext["selected_workspace"])) > 0
				status["membership_count"] = len(asSlice(personalContext["workspaces"]))
				out["status"] = status
			} else {
				// Only include hint in JSON output; human output prints it inline below.
				serverContextHint = "Not signed in — run `axme login` to connect your account."
				out["hint"] = serverContextHint
			}
			if ctx.resolvedActorToken() != "" {
				sessions, err := rt.listAccountSessions(cmd.Context(), ctx, false)
				if err == nil {
					out["sessions"] = sessions
					out["session_summary"] = accountSessionSummary(sessions, false)
				} else {
					out["sessions_error"] = err.Error()
				}
			}

			if rt.outputJSON {
				return rt.printJSON(out)
			}

			// Human-readable output
			ctxName := rt.activeContextName()
			status := asMap(out["status"])
			fmt.Printf("Context:       %s\n", ctxName)
			fmt.Printf("API endpoint:  %s\n", ctx.BaseURL)
			if ctx.OrgID != "" {
				fmt.Printf("Org ID:        %s\n", ctx.OrgID)
			}
			if ctx.WorkspaceID != "" {
				fmt.Printf("Workspace ID:  %s\n", ctx.WorkspaceID)
			}
			if ctx.OwnerAgent != "" {
				fmt.Printf("Owner agent:   %s\n", ctx.OwnerAgent)
			}
			fmt.Printf("Environment:   %s\n", ctx.Environment)
			fmt.Println()

			if asBool(status["has_workspace_access_token"]) {
				fmt.Println("Workspace access: active")
			} else {
				fmt.Println("Workspace access: none (run `axme login`)")
			}
			if asBool(status["has_account_session"]) {
				fmt.Println("Account session:  active")
				if account := asMap(out["account"]); len(account) > 0 {
					if email := asString(account["email"]); email != "" {
						fmt.Printf("Account email:    %s\n", email)
					}
				}
			} else {
				fmt.Println("Account session:  none")
				fmt.Println()
				fmt.Println("  Run `axme login` to sign in with your email address.")
			}
			if serverContextHint == "" {
				if selectedWS := asMap(out["selected_workspace"]); len(selectedWS) > 0 {
					label := asString(selectedWS["name"])
					if label == "" {
						label = asString(selectedWS["workspace_id"])
					}
					fmt.Printf("\nSelected workspace: %s\n", label)
				}
				if cnt, ok := status["membership_count"].(float64); ok && cnt > 0 {
					fmt.Printf("Visible workspaces: %d (run `axme workspace list`)\n", int(cnt))
				}
			}
			return nil
		},
	}
}

func newSessionCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect and revoke account sessions",
	}

	var includeRevoked bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List account sessions for the current human account",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			if ctx.resolvedActorToken() == "" {
				return &cliError{Code: 2, Msg: personalContextRequirementMessage("missing account session")}
			}
			sessions, err := rt.listAccountSessions(cmd.Context(), ctx, includeRevoked)
			if err != nil {
				return err
			}
			summary := accountSessionSummary(sessions, includeRevoked)
			if rt.outputJSON {
				return rt.printJSON(map[string]any{
					"sessions":        sessions,
					"include_revoked": includeRevoked,
					"summary":         summary,
				})
			}
			printTable(
				[]string{"SESSION_ID", "CURRENT", "CLIENT", "DEVICE", "CREATED_AT", "EXPIRES_AT", "REVOKED_AT"},
				sessions,
				[]string{"session_id", "is_current", "client_type", "device_label", "created_at", "expires_at", "revoked_at"},
			)
			if message := asString(summary["guidance_message"]); message != "" {
				fmt.Println()
				fmt.Println(message)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&includeRevoked, "all", false, "include revoked sessions")

	var revokeCurrent bool
	revokeCmd := &cobra.Command{
		Use:   "revoke <session-id>",
		Short: "Revoke an account session",
		Args: func(cmd *cobra.Command, args []string) error {
			if revokeCurrent {
				if len(args) != 0 {
					return &cliError{Code: 2, Msg: "do not pass a session id when using --current"}
				}
				return nil
			}
			if len(args) != 1 {
				return &cliError{Code: 2, Msg: "usage: axme session revoke <session-id> or axme session revoke --current"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			if ctx.resolvedActorToken() == "" {
				return &cliError{Code: 2, Msg: personalContextRequirementMessage("missing account session")}
			}
			if revokeCurrent {
				sessionID, revoked, err := rt.revokeCurrentAccountSession(cmd.Context(), ctx)
				if err != nil {
					return err
				}
				if rt.outputJSON {
					return rt.printJSON(map[string]any{
						"ok":       revoked,
						"session_id": sessionID,
						"revoked":  revoked,
						"mode":     "current_session",
						"guidance_message": sessionRevokeGuidanceMessage("current_session", revoked),
					})
				}
				if revoked {
					fmt.Printf("Session %s revoked.\n", sessionID)
				} else {
					fmt.Printf("Could not revoke session %s.\n", sessionID)
				}
				return nil
			}
			sessionID := strings.TrimSpace(args[0])
			revoked, err := rt.revokeAccountSessionByID(cmd.Context(), ctx, sessionID)
			if err != nil {
				return err
			}
			if rt.outputJSON {
				return rt.printJSON(map[string]any{
					"ok":       revoked,
					"session_id": sessionID,
					"revoked":  revoked,
					"mode":     "explicit_session",
					"guidance_message": sessionRevokeGuidanceMessage("explicit_session", revoked),
				})
			}
			if revoked {
				fmt.Printf("Session %s revoked.\n", sessionID)
			} else {
				fmt.Printf("Could not revoke session %s.\n", sessionID)
			}
			return nil
		},
	}
	revokeCmd.Flags().BoolVar(&revokeCurrent, "current", false, "revoke the current account session")

	cmd.AddCommand(listCmd, revokeCmd)

	return cmd
}

func newOrgCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "org", Short: "List organizations visible to the current account session"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List organizations from personal context",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := rt.effectiveContextWithSecrets()
				if err != nil {
					return err
				}
				body, err := rt.personalContextFromServer(cmd.Context(), ctx)
				if err != nil {
					return err
				}
				organizations := asSlice(body["organizations"])
				summary := personalContextSummary(body)
				if rt.outputJSON {
					out := map[string]any{
						"organizations":         organizations,
						"selected_organization": asMap(body["selected_organization"]),
					}
					for k, v := range summary {
						out[k] = v
					}
					return rt.printJSON(out)
				}
				selectedOrgID := asString(asMap(body["selected_organization"])["org_id"])
				rows := make([]map[string]any, 0, len(organizations))
				for _, item := range organizations {
					org := asMap(item)
					rows = append(rows, map[string]any{
						"selected":   asString(org["org_id"]) == selectedOrgID,
						"org_id":     asString(org["org_id"]),
						"name":       asString(org["name"]),
						"status":     asString(org["status"]),
						"roles":      strings.Join(asStringSlice(org["roles"]), ","),
						"workspaces": len(asSlice(org["workspaces"])),
					})
				}
				printTable(
					[]string{"SELECTED", "ORG_ID", "NAME", "STATUS", "ROLES", "WORKSPACES"},
					rows,
					[]string{"selected", "org_id", "name", "status", "roles", "workspaces"},
				)
				if message := personalContextGuidanceMessage(body); message != "" {
					fmt.Println()
					fmt.Println(message)
				}
				return nil
			},
		},
	)
	return cmd
}

func newMemberCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "member",
		Short: "Manage organization and workspace members",
	}

	var listOrgID string
	var listWorkspaceID string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List members in an organization or workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			orgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(listOrgID))
			if err != nil {
				return err
			}
			workspaceID := strings.TrimSpace(listWorkspaceID)
			members, err := rt.listEnterpriseMembers(cmd.Context(), ctx, orgID, workspaceID)
			if err != nil {
				return err
			}
			if rt.outputJSON {
				return rt.printJSON(map[string]any{
					"org_id":       orgID,
					"workspace_id": workspaceID,
					"scope":        memberScopeSummary(orgID, workspaceID),
					"members":      members,
				})
			}
			printTable(
				[]string{"MEMBER_ID", "ACTOR_ID", "ROLE", "STATUS", "WORKSPACE_ID", "UPDATED_AT"},
				members,
				[]string{"member_id", "actor_id", "role", "status", "workspace_id", "updated_at"},
			)
			if message := memberListGuidance(ctx, orgID, workspaceID); message != "" {
				fmt.Println()
				fmt.Println(message)
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&listOrgID, "org-id", "", "organization id override")
	listCmd.Flags().StringVar(&listWorkspaceID, "workspace-id", "", "workspace id filter")

	var addOrgID string
	var addWorkspaceID string
	var addRole string
	addCmd := &cobra.Command{
		Use:   "add <actor-id>",
		Short: "Add a workspace member",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			orgID, workspaceID, err := rt.resolveEnterpriseWorkspaceContext(
				cmd.Context(),
				ctx,
				strings.TrimSpace(addOrgID),
				strings.TrimSpace(addWorkspaceID),
			)
			if err != nil {
				return err
			}
			if strings.TrimSpace(addRole) == "" {
				return &cliError{Code: 2, Msg: "Role is required. Pass `--role`."}
			}
			member, err := rt.addEnterpriseMember(cmd.Context(), ctx, orgID, workspaceID, strings.TrimSpace(args[0]), strings.TrimSpace(addRole))
			if err != nil {
				return err
			}
			return rt.printGeneric(map[string]any{
				"ok":           true,
				"org_id":       orgID,
				"workspace_id": workspaceID,
				"scope":        memberScopeSummary(orgID, workspaceID),
				"member":       member,
			})
		},
	}
	addCmd.Flags().StringVar(&addOrgID, "org-id", "", "organization id override")
	addCmd.Flags().StringVar(&addWorkspaceID, "workspace-id", "", "workspace id override")
	addCmd.Flags().StringVar(&addRole, "role", "", "member role")

	var updateOrgID string
	var updateRole string
	var updateStatus string
	updateCmd := &cobra.Command{
		Use:   "update <member-id>",
		Short: "Update a member role or status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			orgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(updateOrgID))
			if err != nil {
				return err
			}
			if strings.TrimSpace(updateRole) == "" && strings.TrimSpace(updateStatus) == "" {
				return &cliError{Code: 2, Msg: "Nothing to update. Pass `--role` and/or `--status`."}
			}
			member, err := rt.updateEnterpriseMember(cmd.Context(), ctx, orgID, strings.TrimSpace(args[0]), strings.TrimSpace(updateRole), strings.TrimSpace(updateStatus))
			if err != nil {
				return err
			}
			workspaceID := asString(member["workspace_id"])
			return rt.printGeneric(map[string]any{
				"ok":           true,
				"org_id":       orgID,
				"workspace_id": workspaceID,
				"scope":        memberScopeSummary(orgID, workspaceID),
				"member":       member,
			})
		},
	}
	updateCmd.Flags().StringVar(&updateOrgID, "org-id", "", "organization id override")
	updateCmd.Flags().StringVar(&updateRole, "role", "", "updated member role")
	updateCmd.Flags().StringVar(&updateStatus, "status", "", "updated member status")

	var removeOrgID string
	removeCmd := &cobra.Command{
		Use:   "remove <member-id>",
		Short: "Remove a member from the organization/workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			orgID, err := rt.resolveEnterpriseOrganizationContext(cmd.Context(), ctx, strings.TrimSpace(removeOrgID))
			if err != nil {
				return err
			}
			result, err := rt.removeEnterpriseMember(cmd.Context(), ctx, orgID, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			workspaceID := asString(result["workspace_id"])
			return rt.printGeneric(map[string]any{
				"ok":           true,
				"org_id":       orgID,
				"workspace_id": workspaceID,
				"scope":        memberScopeSummary(orgID, workspaceID),
				"result":       result,
			})
		},
	}
	removeCmd.Flags().StringVar(&removeOrgID, "org-id", "", "organization id override")

	cmd.AddCommand(listCmd, addCmd, updateCmd, removeCmd)
	return cmd
}

func newWorkspaceCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "List or select workspaces from personal context"}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List workspaces from personal context",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := rt.effectiveContextWithSecrets()
				if err != nil {
					return err
				}
				body, err := rt.personalContextFromServer(cmd.Context(), ctx)
				if err != nil {
					return err
				}
				workspaces := asSlice(body["workspaces"])
				summary := personalContextSummary(body)
				if rt.outputJSON {
					out := map[string]any{
						"workspaces":         workspaces,
						"selected_workspace": asMap(body["selected_workspace"]),
						"selected_org":       asMap(body["selected_organization"]),
						"server_context":     asMap(body["context"]),
					}
					for k, v := range summary {
						out[k] = v
					}
					return rt.printJSON(out)
				}
				selectedWorkspaceID := asString(asMap(body["selected_workspace"])["workspace_id"])
				rows := make([]map[string]any, 0, len(workspaces))
				for _, item := range workspaces {
					workspace := asMap(item)
					rows = append(rows, map[string]any{
						"selected":     asString(workspace["workspace_id"]) == selectedWorkspaceID,
						"workspace_id": asString(workspace["workspace_id"]),
						"name":         asString(workspace["name"]),
						"org_id":       asString(workspace["org_id"]),
						"org_name":     asString(workspace["org_name"]),
						"env":          asString(workspace["environment"]),
						"status":       asString(workspace["status"]),
						"roles":        strings.Join(asStringSlice(workspace["roles"]), ","),
					})
				}
				printTable(
					[]string{"SELECTED", "WORKSPACE_ID", "NAME", "ORG_ID", "ORG_NAME", "ENV", "STATUS", "ROLES"},
					rows,
					[]string{"selected", "workspace_id", "name", "org_id", "org_name", "env", "status", "roles"},
				)
				if message := personalContextGuidanceMessage(body); message != "" {
					fmt.Println()
					fmt.Println(message)
				}
				return nil
			},
		},
	)

	cmd.AddCommand(
		&cobra.Command{
			Use:   "use <workspace>",
			Short: "Select active workspace from personal context",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				target := strings.TrimSpace(args[0])
				if target == "" {
					return &cliError{Code: 2, Msg: "workspace identifier is required"}
				}
				activeContextName := rt.activeContextName()
				ctx, err := rt.contextWithSecrets(activeContextName)
				if err != nil {
					return err
				}
				requestContext, err := rt.effectiveContextWithSecrets()
				if err != nil {
					return err
				}
				body, err := rt.personalContextFromServer(cmd.Context(), requestContext)
				if err != nil {
					return err
				}
				workspaces := asSlice(body["workspaces"])
				var exactID map[string]any
				nameMatches := make([]map[string]any, 0)
				for _, item := range workspaces {
					workspace := asMap(item)
					if asString(workspace["workspace_id"]) == target {
						exactID = workspace
						break
					}
					if strings.EqualFold(asString(workspace["name"]), target) {
						nameMatches = append(nameMatches, workspace)
					}
				}
				selectedWorkspace := exactID
				if len(selectedWorkspace) == 0 {
					if len(nameMatches) == 1 {
						selectedWorkspace = nameMatches[0]
					} else if len(nameMatches) > 1 {
						return &cliError{Code: 2, Msg: "workspace name is ambiguous; use workspace_id"}
					}
				}
				if len(selectedWorkspace) == 0 {
					return &cliError{Code: 2, Msg: "workspace not found in personal context"}
				}
				payload := map[string]any{
					"org_id":       asString(selectedWorkspace["org_id"]),
					"workspace_id": asString(selectedWorkspace["workspace_id"]),
				}
				status, responseBody, raw, err := rt.request(
					cmd.Context(),
					requestContext,
					"POST",
					"/v1/portal/personal/workspace-selection",
					nil,
					payload,
					true,
				)
				if err != nil {
					return err
				}
				if status >= 400 {
					return rt.personalWorkspaceSelectionAPIError(status, responseBody, raw)
				}
				serverContext := asMap(responseBody["context"])
				if orgID := asString(serverContext["org_id"]); orgID != "" {
					ctx.OrgID = orgID
				}
				if workspaceID := asString(serverContext["workspace_id"]); workspaceID != "" {
					ctx.WorkspaceID = workspaceID
				}
				if err := rt.persistConfig(); err != nil {
					return err
				}
				if rt.outputJSON {
					return rt.printJSON(map[string]any{
						"ok":                    true,
						"context":               rt.activeContextName(),
						"org_id":                ctx.OrgID,
						"workspace_id":          ctx.WorkspaceID,
						"selected_workspace":    asMap(responseBody["selected_workspace"]),
						"selected_organization": asMap(responseBody["selected_organization"]),
						"server_context":        serverContext,
					})
				}
				ws := asMap(responseBody["selected_workspace"])
				wsName := asString(ws["name"])
				wsID := asString(ws["workspace_id"])
				orgName := asString(ws["org_name"])
				label := wsName
				if label == "" {
					label = wsID
				}
				fmt.Printf("Switched to workspace: %s", label)
				if orgName != "" {
					fmt.Printf(" (%s)", orgName)
				}
				fmt.Println()
				return nil
			},
		},
	)

	return cmd
}

func newLogoutCmd(rt *runtime) *cobra.Command {
	var all bool
	var allSessions bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Clear stored credentials for active context",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.contextWithSecrets(rt.activeContextName())
			if err != nil {
				return err
			}
			serverResult := map[string]any{
				"attempted": false,
				"revoked":   false,
			}
			if actorToken := ctx.resolvedActorToken(); actorToken != "" {
				serverResult["attempted"] = true
				if allSessions {
					serverResult["mode"] = "all_sessions"
					status, err := rt.logoutAllAccountSessions(cmd.Context(), ctx)
					if err != nil {
						serverResult["error"] = err.Error()
					} else {
						serverResult["revoked"] = true
						serverResult["status"] = status
					}
				} else {
					serverResult["mode"] = "current_session"
					sessionID, revoked, err := rt.revokeCurrentAccountSession(cmd.Context(), ctx)
					if sessionID != "" {
						serverResult["session_id"] = sessionID
					}
					serverResult["revoked"] = revoked
					if err != nil {
						serverResult["error"] = err.Error()
					}
				}
			}
			ctx.setActorToken("")
			if all {
				ctx.APIKey = ""
			}
			if err := rt.persistConfig(); err != nil {
				return err
			}
			serverResult["local_account_session_present"] = false
			serverResult["local_workspace_api_key_present"] = !all && ctx.APIKey != ""
			body := map[string]any{
				"ok":                              true,
				"context":                         rt.activeContextName(),
				"api_key_cleared":                 all,
				"workspace_api_key_remaining":     !all && ctx.APIKey != "",
				"account_session_cleared":         true,
				"local_account_session_present":   false,
				"local_workspace_api_key_present": !all && ctx.APIKey != "",
				"server_logout":                   serverResult,
				"guidance_message":                logoutGuidanceMessage(all, serverResult),
			}
			if rt.outputJSON {
				return rt.printJSON(body)
			}
			guidanceMsg := logoutGuidanceMessage(all, serverResult)
			if all {
				fmt.Printf("Signed out of context %q. Workspace API key and account session cleared.\n", rt.activeContextName())
			} else {
				fmt.Printf("Signed out of context %q. Account session cleared.\n", rt.activeContextName())
				if !all && ctx.APIKey != "" {
					fmt.Println("Workspace API key retained. Use --all to also clear the API key.")
				}
			}
			if serverResult["revoked"] == true {
				fmt.Println("Server-side session revoked.")
			}
			if guidanceMsg != "" && serverResult["attempted"] == true {
				fmt.Println(guidanceMsg)
			}
			return nil
		},
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&all, "all", false, "clear API key as well")
	cmd.Flags().BoolVar(&allSessions, "all-sessions", false, "revoke all server-side account sessions before clearing local credentials")
	return cmd
}

func newContextCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Manage local contexts"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List contexts",
			RunE: func(cmd *cobra.Command, args []string) error {
				rows := make([]map[string]any, 0, len(rt.cfg.Contexts))
				for name, c := range rt.cfg.Contexts {
					c.normalizeActorToken()
					if err := rt.loadSecretsIntoContext(name, c); err != nil {
						rows = append(rows, map[string]any{
							"name":         name,
							"active":       name == rt.activeContextName(),
							"base_url":     c.BaseURL,
							"org_id":       c.OrgID,
							"workspace_id": c.WorkspaceID,
							"owner_agent":  c.OwnerAgent,
							"environment":  c.Environment,
							"has_api_key":  false,
							"has_actor":    false,
							"has_bearer":   false,
							"secret_error": err.Error(),
						})
						continue
					}
					rows = append(rows, map[string]any{
						"name":         name,
						"active":       name == rt.activeContextName(),
						"base_url":     c.BaseURL,
						"org_id":       c.OrgID,
						"workspace_id": c.WorkspaceID,
						"owner_agent":  c.OwnerAgent,
						"environment":  c.Environment,
						"has_api_key":  c.APIKey != "",
						"has_actor":    c.resolvedActorToken() != "",
						"has_bearer":   c.BearerToken != "",
						"secret_error": "",
					})
				}
				if rt.outputJSON {
					return rt.printJSON(rows)
				}
				printTable([]string{"NAME", "ACTIVE", "BASE_URL", "ORG", "WORKSPACE", "OWNER", "ENV", "API_KEY", "ACTOR", "BEARER", "SECRET_ERROR"}, rows, []string{"name", "active", "base_url", "org_id", "workspace_id", "owner_agent", "environment", "has_api_key", "has_actor", "has_bearer", "secret_error"})
				return nil
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Show active context",
			RunE: func(cmd *cobra.Command, args []string) error {
				name := rt.activeContextName()
				c, err := rt.effectiveContextWithSecrets()
				if err != nil {
					return err
				}
				out := map[string]any{
					"name":         name,
					"base_url":     c.BaseURL,
					"org_id":       c.OrgID,
					"workspace_id": c.WorkspaceID,
					"owner_agent":  c.OwnerAgent,
					"environment":  c.Environment,
					"has_api_key":  c.APIKey != "",
					"has_actor":    c.resolvedActorToken() != "",
					"has_bearer":   c.BearerToken != "",
				}
				if personalContext, err := rt.personalContextFromServer(cmd.Context(), c); err == nil {
					if serverContext := asMap(personalContext["context"]); len(serverContext) > 0 {
						out["server_context"] = serverContext
					}
					if selectedOrg := asMap(personalContext["selected_organization"]); len(selectedOrg) > 0 {
						out["selected_organization"] = selectedOrg
					}
					if selectedWorkspace := asMap(personalContext["selected_workspace"]); len(selectedWorkspace) > 0 {
						out["selected_workspace"] = selectedWorkspace
					}
					if guidance := asMap(personalContext["guidance"]); len(guidance) > 0 {
						out["server_guidance"] = guidance
					}
				} else {
					out["server_context_error"] = err.Error()
				}
				return rt.printGeneric(out)
			},
		},
		newContextUseCmd(rt),
		newContextSetCmd(rt),
	)
	return cmd
}

func newContextUseCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch active context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if _, ok := rt.cfg.Contexts[name]; !ok {
				return &cliError{Code: 2, Msg: fmt.Sprintf("context not found: %s", name)}
			}
			targetContext, err := rt.contextWithSecrets(name)
			if err != nil {
				return err
			}
			rt.cfg.ActiveContext = name
			hydrated := false
			warning := ""
			if targetContext.resolvedActorToken() != "" {
				if resolvedContext, err := rt.hydrateContextFromServer(cmd.Context(), targetContext); err == nil {
					hydrated = true
					if resolvedOrgID := asString(resolvedContext["org_id"]); resolvedOrgID != "" {
						targetContext.OrgID = resolvedOrgID
					}
					if resolvedWorkspaceID := asString(resolvedContext["workspace_id"]); resolvedWorkspaceID != "" {
						targetContext.WorkspaceID = resolvedWorkspaceID
					}
				} else {
					warning = err.Error()
				}
			}
			if err := rt.persistConfig(); err != nil {
				return err
			}
			body := map[string]any{
				"ok":             true,
				"active_context": name,
				"hydrated":       hydrated,
				"org_id":         targetContext.OrgID,
				"workspace_id":   targetContext.WorkspaceID,
			}
			if warning != "" {
				body["warning"] = warning
			}
			return rt.printResult(200, body)
		},
	}
}

func newContextSetCmd(rt *runtime) *cobra.Command {
	var base, key, token, org, ws, owner, env string
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Create or update context fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c := rt.ensureContext(name)
			if base != "" {
				c.BaseURL = strings.TrimRight(base, "/")
			}
			if key != "" {
				c.APIKey = key
			}
			if token != "" {
				c.setActorToken(token)
			}
			if org != "" {
				c.OrgID = org
			}
			if ws != "" {
				c.WorkspaceID = ws
			}
			if owner != "" {
				c.OwnerAgent = owner
			}
			if env != "" {
				c.Environment = env
			}
			if err := rt.persistConfig(); err != nil {
				return err
			}
			return rt.printResult(200, map[string]any{"ok": true, "context": name})
		},
	}
	cmd.Flags().StringVar(&base, "base-url", "", "base URL")
	cmd.Flags().StringVar(&key, "api-key", "", "API key")
	cmd.Flags().StringVar(&token, "actor-token", "", "actor token")
	cmd.Flags().StringVar(&token, "bearer-token", "", "bearer token")
	cmd.Flags().StringVar(&org, "org-id", "", "default org id")
	cmd.Flags().StringVar(&ws, "workspace-id", "", "default workspace id")
	cmd.Flags().StringVar(&owner, "owner-agent", "", "owner agent")
	cmd.Flags().StringVar(&env, "environment", "", "environment")
	return cmd
}

func newInitCmd(rt *runtime) *cobra.Command {
	var force bool
	var example string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize local CLI sample files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if example == "" {
				example = "approval-resume"
			}
			sample, ok := builtInExamples()[example]
			if !ok {
				return &cliError{Code: 2, Msg: "unknown example"}
			}
			root := "."
			cfgFile := filepath.Join(root, "axme.yaml")
			exDir := filepath.Join(root, "examples")
			if err := os.MkdirAll(exDir, 0o755); err != nil {
				return err
			}
			if force || !fileExists(cfgFile) {
				content := "base_url: " + rt.effectiveContext().BaseURL + "\n"
				content += "org_id: " + rt.effectiveContext().OrgID + "\n"
				content += "workspace_id: " + rt.effectiveContext().WorkspaceID + "\n"
				if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
					return err
				}
			}
			exPath := filepath.Join(exDir, example+".json")
			if force || !fileExists(exPath) {
				raw, _ := json.MarshalIndent(sample, "", "  ")
				if err := os.WriteFile(exPath, raw, 0o644); err != nil {
					return err
				}
			}
			return rt.printGeneric(map[string]any{"ok": true, "config_file": cfgFile, "example_file": exPath})
		},
	}
	cmd.Flags().StringVar(&example, "example", "approval-resume", "example name")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

func newExamplesCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "examples",
		Short: "List runnable built-in examples",
		RunE: func(cmd *cobra.Command, args []string) error {
			items := []map[string]any{
				{"name": "approval-resume", "description": "Durable approval flow with waiting and resume-ready payload"},
				{"name": "tool-waiting", "description": "Intent payload simulating WAITING_FOR_TOOL stage"},
			}
			if rt.outputJSON {
				return rt.printJSON(items)
			}
			printTable([]string{"NAME", "DESCRIPTION"}, items, []string{"name", "description"})
			return nil
		},
	}
}

func newRunCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "run <example|file.json>",
		Short: "Run example or JSON intent request file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			payload, err := loadRunPayload(target)
			if err != nil {
				return err
			}
			if payload["correlation_id"] == nil {
				payload["correlation_id"] = uuid.NewString()
			}
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/intents", nil, payload, true)
			if err != nil {
				return err
			}
			intentID := asNestedString(body, "intent_id")
			runLink := strings.TrimRight(ctx.BaseURL, "/") + "/v1/intents/" + intentID
			return rt.printResult(status, map[string]any{
				"ok":        status < 400,
				"intent_id": intentID,
				"run_link":  runLink,
				"body":      body,
			})
		},
	}
}

func newIntentsCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "intents", Short: "Durable execution intents"}
	cmd.AddCommand(newIntentsListCmd(rt), newIntentsGetCmd(rt), newIntentsWatchCmd(rt), newIntentsCancelCmd(rt), newIntentsRetryCmd(rt), newIntentsResumeCmd(rt))
	return cmd
}

func newIntentsListCmd(rt *runtime) *cobra.Command {
	var statusFilter, since, service, tag string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List intents",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/inbox", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				detail := asString(body["detail"])
				if detail == "" {
					errorBody := asMap(body["error"])
					detail = asString(errorBody["message"])
				}
				if strings.TrimSpace(detail) == "" {
					return &cliError{Code: 1, Msg: "failed to list inbox threads"}
				}
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to list inbox threads: %s", detail)}
			}
			threads := asSlice(body["threads"])
			rows := make([]intentRow, 0, len(threads))
			sinceAt, hasSince := parseSince(since)
			for _, raw := range threads {
				t := asMap(raw)
				intentID := asString(t["intent_id"])
				if intentID == "" {
					continue
				}
				_, ibody, _, ierr := rt.request(cmd.Context(), ctx, "GET", "/v1/intents/"+intentID, nil, nil, true)
				if ierr != nil {
					continue
				}
				intent := asMap(ibody["intent"])
				intentStatus := strings.ToUpper(asString(intent["status"]))
				if statusFilter != "" && strings.ToUpper(statusFilter) != intentStatus {
					continue
				}
				updated := asString(intent["updated_at"])
				if hasSince && updated != "" {
					if ts, err := time.Parse(time.RFC3339, updated); err == nil && ts.Before(sinceAt) {
						continue
					}
				}
				payload := asMap(intent["payload"])
				if service != "" && asString(payload["service"]) != service {
					continue
				}
				if tag != "" {
					tags := asStringSlice(payload["tags"])
					if !slices.Contains(tags, tag) {
						continue
					}
				}
				row := intentRow{
					ID:        intentID,
					Status:    intentStatus,
					Age:       ageFromTime(asString(intent["updated_at"]), asString(intent["created_at"])),
					LastStep:  asString(intent["lifecycle_status"]),
					Owner:     asString(intent["to_agent"]),
					UpdatedAt: asString(intent["updated_at"]),
					Payload:   payload,
				}
				if row.LastStep == "" {
					row.LastStep = row.Status
				}
				rows = append(rows, row)
			}
			if limit > 0 && len(rows) > limit {
				rows = rows[:limit]
			}
			if rt.outputJSON {
				return rt.printJSON(rows)
			}
			listRows := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				listRows = append(listRows, map[string]any{
					"id": row.ID, "status": row.Status, "age": row.Age, "last_step": row.LastStep, "owner": row.Owner,
				})
			}
			printTable([]string{"ID", "STATUS", "AGE", "LAST_STEP", "OWNER"}, listRows, []string{"id", "status", "age", "last_step", "owner"})
			return nil
		},
	}
	cmd.Flags().StringVar(&statusFilter, "status", "", "filter by lifecycle status")
	cmd.Flags().StringVar(&since, "since", "", "RFC3339 timestamp or duration (e.g. 2h)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max items")
	cmd.Flags().StringVar(&service, "service", "", "filter by payload.service")
	cmd.Flags().StringVar(&tag, "tag", "", "filter by payload tag")
	return cmd
}

func newIntentsGetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "get <intent_id>",
		Short: "Get intent details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/intents/"+args[0], nil, nil, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
}

func newIntentsWatchCmd(rt *runtime) *cobra.Command {
	var follow bool
	var since int
	cmd := &cobra.Command{
		Use:   "watch <intent_id>",
		Short: "Watch intent lifecycle events (SSE)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentID := args[0]
			ctx := rt.effectiveContext()
			next := since + 1
			for {
				n, err := rt.streamEvents(cmd.Context(), ctx, intentID, next)
				if err != nil {
					return err
				}
				next = n
				if !follow {
					return nil
				}
			}
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", true, "keep following stream timeouts")
	cmd.Flags().IntVar(&since, "since", 0, "start from sequence id")
	return cmd
}

func newIntentsCancelCmd(rt *runtime) *cobra.Command {
	var reason, actor string
	cmd := &cobra.Command{
		Use:   "cancel <intent_id>",
		Short: "Cancel intent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if actor == "" {
				actor = rt.effectiveContext().OwnerAgent
			}
			payload := map[string]any{"status": "CANCELED", "reason": reason}
			if actor != "" {
				payload["actor"] = actor
			}
			status, body, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "POST", "/v1/intents/"+args[0]+"/resolve", nil, payload, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "canceled via axme cli", "cancel reason")
	cmd.Flags().StringVar(&actor, "actor", "", "actor id")
	return cmd
}

func newIntentsRetryCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <intent_id>",
		Short: "Retry intent by resubmitting original payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rt.recreateIntent(cmd.Context(), args[0], "retry")
		},
	}
}

func newIntentsResumeCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <intent_id>",
		Short: "Resume waiting intent by resubmitting original payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rt.recreateIntent(cmd.Context(), args[0], "resume")
		},
	}
}

func newLogsCmd(rt *runtime) *cobra.Command {
	var tail int
	var level, step, since string
	cmd := &cobra.Command{
		Use:   "logs <intent_id>",
		Short: "Show intent lifecycle log-like events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, body, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "GET", "/v1/intents/"+args[0]+"/events", nil, nil, true)
			if err != nil {
				return err
			}
			events := asSlice(body["events"])
			sinceAt, hasSince := parseSince(since)
			rows := make([]map[string]any, 0, len(events))
			for _, raw := range events {
				ev := asMap(raw)
				status := strings.ToUpper(asString(ev["status"]))
				evLevel := "info"
				if status == "FAILED" {
					evLevel = "error"
				} else if strings.HasPrefix(status, "WAITING") || status == "CANCELED" {
					evLevel = "warn"
				}
				if level != "" && strings.ToLower(level) != evLevel {
					continue
				}
				evType := asString(ev["event_type"])
				if step != "" && !strings.Contains(evType, step) {
					continue
				}
				if hasSince {
					if ts, err := time.Parse(time.RFC3339, asString(ev["at"])); err == nil && ts.Before(sinceAt) {
						continue
					}
				}
				rows = append(rows, map[string]any{
					"seq": ev["seq"], "at": ev["at"], "level": evLevel, "status": status, "event_type": evType, "waiting_reason": ev["waiting_reason"],
				})
			}
			if tail > 0 && len(rows) > tail {
				rows = rows[len(rows)-tail:]
			}
			if rt.outputJSON {
				return rt.printJSON(rows)
			}
			printTable([]string{"SEQ", "AT", "LEVEL", "STATUS", "EVENT", "WAITING_REASON"}, rows, []string{"seq", "at", "level", "status", "event_type", "waiting_reason"})
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 50, "tail count")
	cmd.Flags().StringVar(&since, "since", "", "RFC3339 timestamp or duration")
	cmd.Flags().StringVar(&level, "level", "", "filter by level: info|warn|error")
	cmd.Flags().StringVar(&step, "step", "", "filter by event_type substring")
	return cmd
}

func newTraceCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "trace <intent_id>",
		Short: "Show concise timeline and next action",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, ibody, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "GET", "/v1/intents/"+args[0], nil, nil, true)
			if err != nil {
				return err
			}
			_, ebody, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "GET", "/v1/intents/"+args[0]+"/events", nil, nil, true)
			if err != nil {
				return err
			}
			intent := asMap(ibody["intent"])
			events := asSlice(ebody["events"])
			last := map[string]any{}
			if len(events) > 0 {
				last = asMap(events[len(events)-1])
			}
			waitingReason := asString(intent["lifecycle_waiting_reason"])
			if waitingReason == "" {
				waitingReason = asString(last["waiting_reason"])
			}
			status := strings.ToUpper(asString(intent["status"]))
			nextAction := "none"
			switch {
			case strings.Contains(waitingReason, "HUMAN"):
				nextAction = "await human decision, then run `axme intents resume <id>` or resolve upstream"
			case strings.Contains(waitingReason, "TOOL"):
				nextAction = "inspect downstream tool and run `axme intents retry <id>` if fixed"
			case strings.Contains(waitingReason, "AGENT"):
				nextAction = "verify receiving agent availability and routing"
			case strings.Contains(waitingReason, "TIME"):
				nextAction = "time-gated wait; keep watching events stream"
			case status == "FAILED":
				nextAction = "run `axme intents retry <id>`"
			case status == "CANCELED":
				nextAction = "run `axme intents retry <id>` to create new execution"
			case status == "COMPLETED":
				nextAction = "no action required"
			default:
				nextAction = "monitor with `axme intents watch <id>`"
			}
			out := map[string]any{
				"intent_id":      asString(intent["intent_id"]),
				"status":         status,
				"waiting_reason": waitingReason,
				"last_event":     last,
				"next_action":    nextAction,
				"events_count":   len(events),
			}
			return rt.printGeneric(out)
		},
	}
}

func newAgentsCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "agents", Short: "Registry/agent operations"}
	cmd.AddCommand(newAgentsListCmd(rt), newAgentsRegisterCmd(rt), newAgentsResolveCmd(rt))
	return cmd
}

func newAgentsListCmd(rt *runtime) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agents (alias-backed)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			if ctx.OrgID == "" || ctx.WorkspaceID == "" {
				return &cliError{Code: 2, Msg: "org_id and workspace_id are required in context for agents list"}
			}
			query := map[string]string{"org_id": ctx.OrgID, "workspace_id": ctx.WorkspaceID, "limit": strconv.Itoa(max(1, limit))}
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/aliases", query, nil, true)
			if err != nil {
				return err
			}
			if rt.outputJSON {
				return rt.printResult(status, body)
			}
			aliases := asSlice(body["aliases"])
			rows := make([]map[string]any, 0, len(aliases))
			for _, raw := range aliases {
				a := asMap(raw)
				rows = append(rows, map[string]any{"alias": a["alias"], "principal_id": a["principal_id"], "status": a["status"], "type": a["alias_type"]})
			}
			printTable([]string{"ALIAS", "PRINCIPAL_ID", "STATUS", "TYPE"}, rows, []string{"alias", "principal_id", "status", "type"})
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "max aliases")
	return cmd
}

func newAgentsRegisterCmd(rt *runtime) *cobra.Command {
	var name, capability, publicKey, transport, endpointURL, authMode, region, clusterID, failoverPolicy string
	var priority int
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register agent principal + alias (+ optional route)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			if ctx.OrgID == "" || ctx.WorkspaceID == "" {
				return &cliError{Code: 2, Msg: "org_id and workspace_id are required in context"}
			}
			if name == "" {
				return &cliError{Code: 2, Msg: "--name is required"}
			}
			prPayload := map[string]any{
				"org_id":         ctx.OrgID,
				"workspace_id":   ctx.WorkspaceID,
				"principal_type": "service_agent",
				"display_name":   name,
				"metadata": map[string]any{
					"capability": capability,
					"public_key": publicKey,
				},
			}
			_, prBody, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/principals", nil, prPayload, true)
			if err != nil {
				return err
			}
			principal := asMap(prBody["principal"])
			principalID := asString(principal["principal_id"])
			aliasPayload := map[string]any{
				"principal_id": principalID,
				"alias":        name,
				"alias_type":   "service",
				"metadata":     map[string]any{"capability": capability},
			}
			_, aliasBody, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/aliases", nil, aliasPayload, true)
			if err != nil {
				return err
			}
			result := map[string]any{"ok": true, "principal": prBody["principal"], "alias": aliasBody["alias"]}
			if endpointURL != "" {
				routePayload := map[string]any{
					"principal_id":    principalID,
					"transport_type":  transport,
					"endpoint_url":    endpointURL,
					"auth_mode":       authMode,
					"region":          region,
					"cluster_id":      clusterID,
					"failover_policy": failoverPolicy,
					"priority":        priority,
				}
				_, routeBody, _, rerr := rt.request(cmd.Context(), ctx, "POST", "/v1/routing/endpoints", nil, routePayload, true)
				if rerr != nil {
					return rerr
				}
				result["route"] = routeBody["route"]
			}
			return rt.printGeneric(result)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "agent alias name")
	cmd.Flags().StringVar(&capability, "capability", "", "agent capability label")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "agent public key")
	cmd.Flags().StringVar(&transport, "transport", "http", "transport type")
	cmd.Flags().StringVar(&endpointURL, "endpoint-url", "", "routing endpoint url (optional)")
	cmd.Flags().StringVar(&authMode, "auth-mode", "jwt", "auth mode")
	cmd.Flags().StringVar(&region, "region", "", "region")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster id")
	cmd.Flags().StringVar(&failoverPolicy, "failover-policy", "none", "failover policy")
	cmd.Flags().IntVar(&priority, "priority", 100, "route priority")
	return cmd
}

func newAgentsResolveCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <name_or_alias>",
		Short: "Resolve agent alias and route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			if ctx.OrgID == "" || ctx.WorkspaceID == "" {
				return &cliError{Code: 2, Msg: "org_id and workspace_id are required in context"}
			}
			alias := args[0]
			query := map[string]string{"org_id": ctx.OrgID, "workspace_id": ctx.WorkspaceID, "alias": alias}
			_, aliasBody, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/aliases/resolve", query, nil, true)
			if err != nil {
				return err
			}
			routePayload := map[string]any{"org_id": ctx.OrgID, "workspace_id": ctx.WorkspaceID, "alias": alias}
			_, routeBody, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/routing/resolve", nil, routePayload, true)
			if err != nil {
				return err
			}
			return rt.printGeneric(map[string]any{"ok": true, "alias_resolution": aliasBody["resolution"], "route_resolution": routeBody["resolution"]})
		},
	}
}

func newKeysCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Legacy alias for service-account key operations",
		Long:  "Legacy alias for `axme service-accounts ...`. Prefer `axme service-accounts list` and `axme service-accounts keys ...` for the guided account-level flow.",
	}
	cmd.AddCommand(newKeysListCmd(rt), newKeysCreateCmd(rt), newKeysRevokeCmd(rt))
	return cmd
}

func newServiceAccountsCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "service-accounts",
		Aliases: []string{"serviceaccounts"},
		Short:   "Manage service accounts and their keys",
	}
	cmd.AddCommand(
		newServiceAccountsListCmd(rt),
		newServiceAccountsCreateCmd(rt),
		newServiceAccountKeysCmd(rt),
	)
	return cmd
}

func newServiceAccountsListCmd(rt *runtime) *cobra.Command {
	var serviceAccountID string
	var orgID string
	var workspaceID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List service accounts or fetch one",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			if serviceAccountID != "" {
				status, body, raw, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts/"+serviceAccountID, nil, nil, true)
				if err != nil {
					return err
				}
				if status >= 400 {
					return rt.serviceAccountsAPIError(status, body, raw)
				}
				serviceAccount := asMap(body["service_account"])
				return rt.printGeneric(map[string]any{
					"ok":              true,
					"service_account": serviceAccount,
					"scope": serviceAccountScopeSummary(
						asString(serviceAccount["org_id"]),
						asString(serviceAccount["workspace_id"]),
					),
				})
			}

			resolvedOrgID, resolvedWorkspaceID, err := rt.resolveServiceAccountListContext(
				cmd.Context(),
				ctx,
				strings.TrimSpace(orgID),
				strings.TrimSpace(workspaceID),
			)
			if err != nil {
				return err
			}
			query := map[string]string{"org_id": resolvedOrgID}
			if resolvedWorkspaceID != "" {
				query["workspace_id"] = resolvedWorkspaceID
			}
			status, body, raw, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts", query, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return rt.serviceAccountsAPIError(status, body, raw)
			}
			serviceAccounts := asSlice(body["service_accounts"])
			scope := serviceAccountScopeSummary(resolvedOrgID, resolvedWorkspaceID)
			if rt.outputJSON {
				return rt.printJSON(map[string]any{
					"ok":               true,
					"org_id":           resolvedOrgID,
					"workspace_id":     resolvedWorkspaceID,
					"scope":            scope,
					"service_accounts": serviceAccounts,
				})
			}
			rows := make([]map[string]any, 0, len(serviceAccounts))
			for _, item := range serviceAccounts {
				account := asMap(item)
				rows = append(rows, map[string]any{
					"service_account_id":  asString(account["service_account_id"]),
					"name":                asString(account["name"]),
					"workspace_id":        asString(account["workspace_id"]),
					"status":              asString(account["status"]),
					"created_by_actor_id": asString(account["created_by_actor_id"]),
					"created_at":          asString(account["created_at"]),
				})
			}
			printTable(
				[]string{"SERVICE_ACCOUNT_ID", "NAME", "WORKSPACE_ID", "STATUS", "CREATED_BY", "CREATED_AT"},
				rows,
				[]string{"service_account_id", "name", "workspace_id", "status", "created_by_actor_id", "created_at"},
			)
			if message := serviceAccountListGuidance(ctx, resolvedOrgID, resolvedWorkspaceID); message != "" {
				fmt.Println()
				fmt.Println(message)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "specific service account id")
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization id (defaults to context org_id)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "workspace id (defaults to context workspace_id)")
	return cmd
}

func newServiceAccountsCreateCmd(rt *runtime) *cobra.Command {
	var orgID string
	var workspaceID string
	var name string
	var description string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create service account",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			resolvedOrgID, resolvedWorkspaceID, err := rt.resolveEnterpriseWorkspaceContext(
				cmd.Context(),
				ctx,
				strings.TrimSpace(orgID),
				strings.TrimSpace(workspaceID),
			)
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				return &cliError{Code: 2, Msg: "--name is required"}
			}
			payload := map[string]any{
				"org_id":       resolvedOrgID,
				"workspace_id": resolvedWorkspaceID,
				"name":         strings.TrimSpace(name),
			}
			if strings.TrimSpace(description) != "" {
				payload["description"] = strings.TrimSpace(description)
			}
			status, body, raw, err := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts", nil, payload, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return rt.serviceAccountsAPIError(status, body, raw)
			}
			serviceAccount := asMap(body["service_account"])
			return rt.printGeneric(map[string]any{
				"ok":              true,
				"org_id":          resolvedOrgID,
				"workspace_id":    resolvedWorkspaceID,
				"scope":           serviceAccountScopeSummary(resolvedOrgID, resolvedWorkspaceID),
				"service_account": serviceAccount,
				"guidance_message": fmt.Sprintf(
					"Service account created for workspace %s. Run `axme service-accounts keys create --service-account-id %s` to mint a key.",
					resolvedWorkspaceID,
					asString(serviceAccount["service_account_id"]),
				),
			})
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization id (defaults to context org_id)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "workspace id (defaults to context workspace_id)")
	cmd.Flags().StringVar(&name, "name", "", "service account name")
	cmd.Flags().StringVar(&description, "description", "", "service account description")
	return cmd
}

func newServiceAccountKeysCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage service-account keys",
	}
	cmd.AddCommand(newServiceAccountKeysCreateCmd(rt), newServiceAccountKeysRevokeCmd(rt))
	return cmd
}

func newServiceAccountKeysCreateCmd(rt *runtime) *cobra.Command {
	var serviceAccountID, expiresAt string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create service-account key",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serviceAccountID == "" {
				return &cliError{Code: 2, Msg: "--service-account-id is required"}
			}
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			payload := map[string]any{}
			if expiresAt != "" {
				payload["expires_at"] = expiresAt
			}
			status, body, raw, err := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts/"+serviceAccountID+"/keys", nil, payload, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return rt.serviceAccountsAPIError(status, body, raw)
			}
			return rt.printGeneric(map[string]any{
				"ok":                 true,
				"service_account_id": serviceAccountID,
				"key":                asMap(body["key"]),
				"guidance_message":   "Store this service-account token now. The raw token is only returned at creation time.",
			})
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "service account id")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "optional ISO8601 expiration")
	return cmd
}

func newServiceAccountKeysRevokeCmd(rt *runtime) *cobra.Command {
	var serviceAccountID, keyID string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke service-account key",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serviceAccountID == "" || keyID == "" {
				return &cliError{Code: 2, Msg: "--service-account-id and --key-id are required"}
			}
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			status, body, raw, err := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts/"+serviceAccountID+"/keys/"+keyID+"/revoke", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return rt.serviceAccountsAPIError(status, body, raw)
			}
			return rt.printGeneric(map[string]any{
				"ok":                 true,
				"service_account_id": serviceAccountID,
				"key":                asMap(body["key"]),
			})
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "service account id")
	cmd.Flags().StringVar(&keyID, "key-id", "", "key id")
	return cmd
}

func newKeysListCmd(rt *runtime) *cobra.Command {
	cmd := newServiceAccountsListCmd(rt)
	cmd.Use = "list"
	cmd.Short = "Legacy alias for `axme service-accounts list`"
	cmd.Long = "Legacy alias for `axme service-accounts list`. This keeps compatibility while using the same guided account-level service-account flow."
	return cmd
}

func newKeysCreateCmd(rt *runtime) *cobra.Command {
	cmd := newServiceAccountKeysCreateCmd(rt)
	cmd.Short = "Legacy alias for `axme service-accounts keys create`"
	return cmd
}

func newKeysRevokeCmd(rt *runtime) *cobra.Command {
	cmd := newServiceAccountKeysRevokeCmd(rt)
	cmd.Short = "Legacy alias for `axme service-accounts keys revoke`"
	return cmd
}

func newStatusCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Health and connectivity status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, raw, err := rt.request(cmd.Context(), ctx, "GET", "/health", nil, nil, true)
			if err != nil {
				return fmt.Errorf("could not reach gateway at %s: %w", ctx.BaseURL, err)
			}
			if rt.outputJSON {
				return rt.printJSON(map[string]any{"status_code": status, "ok": status < 400, "body": body})
			}
			if status >= 400 {
				return fmt.Errorf("gateway returned %d: %s", status, raw)
			}
			svc := asString(body["service"])
			if svc == "" {
				svc = "gateway"
			}
			profile := asString(body["deployment_profile"])
			storage := asString(body["storage"])
			fmt.Printf("Gateway:    %s (ok)\n", ctx.BaseURL)
			fmt.Printf("Service:    %s\n", svc)
			if profile != "" {
				fmt.Printf("Profile:    %s\n", profile)
			}
			if storage != "" {
				fmt.Printf("Storage:    %s\n", storage)
			}
			if registered, ok := body["registered_users"].(float64); ok {
				fmt.Printf("Users:      %d registered\n", int(registered))
			}
			return nil
		},
	}
}

func newDoctorCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Configuration and API diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := rt.effectiveContextWithSecrets()
			if err != nil {
				return err
			}
			checks := rt.doctorChecks(cmd.Context(), ctx)
			if rt.outputJSON {
				return rt.printJSON(checks)
			}
			printTable([]string{"CHECK", "OK", "DETAIL"}, checks, []string{"check", "ok", "detail"})
			return nil
		},
	}
}

func (rt *runtime) doctorChecks(ctx context.Context, c *clientConfig) []map[string]any {
	secretStorageDetail := "not configured"
	if rt.secretStore != nil {
		secretStorageDetail = fmt.Sprintf("%s (%s)", rt.secretStore.Mode(), rt.secretStore.Detail())
	}
	checks := []map[string]any{
		{"check": "config_file", "ok": fileExists(rt.cfgFile), "detail": rt.cfgFile},
		{"check": "base_url", "ok": c.BaseURL != "", "detail": c.BaseURL},
		{"check": "api_key_or_actor", "ok": c.APIKey != "" || c.resolvedActorToken() != "", "detail": "credentials present"},
		{"check": "secret_storage", "ok": rt.secretStore != nil, "detail": secretStorageDetail},
	}
	if warning := secretStorageFallbackWarning(rt.secretStore); warning != "" {
		isAutoFallback := false
		if fs, ok := rt.secretStore.(*fileSecretStore); ok && fs.autoFallback {
			isAutoFallback = true
		}
		modeDetail := "file (auto-fallback; keyring unavailable)"
		if !isAutoFallback {
			modeDetail = "file (explicit)"
		}
		checks = append(checks, map[string]any{
			"check":  "secret_storage_mode",
			"ok":     isAutoFallback,
			"detail": modeDetail,
		})
	}
	_, body, _, err := rt.request(ctx, c, "GET", "/health", nil, nil, true)
	healthDetail := "ok"
	if err != nil {
		healthDetail = err.Error()
	} else if svc := asString(body["service"]); svc != "" {
		healthDetail = fmt.Sprintf("service=%s ok", svc)
	}
	checks = append(checks, map[string]any{"check": "health_endpoint", "ok": err == nil, "detail": healthDetail})

	if c.resolvedActorToken() == "" {
		msg := personalContextRequirementMessage("")
		checks = append(checks,
			map[string]any{"check": "account_session", "ok": false, "detail": msg},
			map[string]any{"check": "personal_context", "ok": false, "detail": msg},
		)
		return checks
	}

	checks = append(checks, map[string]any{
		"check":  "account_session",
		"ok":     true,
		"detail": "account session token loaded",
	})
	sessions, err := rt.listAccountSessions(ctx, c, false)
	if err != nil {
		checks = append(checks, map[string]any{
			"check":  "account_session_inventory",
			"ok":     false,
			"detail": err.Error(),
		})
	} else {
		summary := accountSessionSummary(sessions, false)
		check := map[string]any{
			"check":         "account_session_inventory",
			"ok":            asBool(summary["has_current_session"]),
			"detail":        asString(summary["guidance_message"]),
			"active_count":  summary["active_count"],
			"revoked_count": summary["revoked_count"],
		}
		if currentSessionID := asString(summary["current_session_id"]); currentSessionID != "" {
			check["current_session_id"] = currentSessionID
		}
		if asString(check["detail"]) == "" {
			check["detail"] = fmt.Sprintf("%v active account sessions visible", summary["active_count"])
		}
		checks = append(checks, check)
	}
	personalContext, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		checks = append(checks, map[string]any{
			"check":  "personal_context",
			"ok":     false,
			"detail": err.Error(),
		})
		return checks
	}

	serverContext := asMap(personalContext["context"])
	selectedWorkspace := asMap(personalContext["selected_workspace"])
	guidance := asMap(personalContext["guidance"])
	membershipCount := len(asSlice(personalContext["workspaces"]))
	personalContextDetail := fmt.Sprintf("server returned %d visible workspaces", membershipCount)
	if count, ok := guidance["workspace_count"]; ok {
		personalContextDetail = fmt.Sprintf("server returned %v visible workspaces", count)
	}
	checks = append(checks, map[string]any{
		"check":  "personal_context",
		"ok":     true,
		"detail": personalContextDetail,
	})
	selectedWorkspaceID := asString(selectedWorkspace["workspace_id"])
	checks = append(checks, map[string]any{
		"check":  "server_selected_workspace",
		"ok":     selectedWorkspaceID != "",
		"detail": selectedWorkspaceID,
	})

	serverOrgID := asString(serverContext["org_id"])
	serverWorkspaceID := asString(serverContext["workspace_id"])
	cacheAligned := c.OrgID == serverOrgID && c.WorkspaceID == serverWorkspaceID
	cacheDetail := "local cache matches server selection"
	if !cacheAligned {
		cacheDetail = fmt.Sprintf(
			"local cache org=%q workspace=%q differs from server org=%q workspace=%q",
			c.OrgID,
			c.WorkspaceID,
			serverOrgID,
			serverWorkspaceID,
		)
	}
	checks = append(checks, map[string]any{
		"check":  "workspace_cache_alignment",
		"ok":     cacheAligned,
		"detail": cacheDetail,
	})
	return checks
}

func secretStorageFallbackWarning(store secretStore) string {
	if store == nil || store.Mode() != "file" {
		return ""
	}
	if fs, ok := store.(*fileSecretStore); ok && fs.autoFallback {
		// Quiet fallback: keyring unavailable, silently using file. Print only once.
		return fmt.Sprintf(
			"Note: OS keyring unavailable; credentials are stored in %s. Set %s=keyring if running a desktop environment.",
			store.Detail(),
			axmeCLISecretStorageEnv,
		)
	}
	return fmt.Sprintf(
		"Warning: %s=file enabled; credentials stored in plaintext at %s. Use only in headless or CI environments.",
		axmeCLISecretStorageEnv,
		store.Detail(),
	)
}

func newVersionCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.outputJSON {
				return rt.printJSON(map[string]any{"version": version, "commit": commit, "date": date})
			}
			fmt.Printf("axme %s (%s, %s)\n", version, commit, date)
			return nil
		},
	}
}

func newRawCmd(rt *runtime) *cobra.Command {
	var query []string
	var dataJSON string
	cmd := &cobra.Command{
		Use:   "raw <method> <path>",
		Short: "Send raw request to gateway",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			q := map[string]string{}
			for _, item := range query {
				parts := strings.SplitN(item, "=", 2)
				if len(parts) != 2 {
					return &cliError{Code: 2, Msg: "query must be key=value"}
				}
				q[parts[0]] = parts[1]
			}
			var payload map[string]any
			if dataJSON != "" {
				if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
					return &cliError{Code: 2, Msg: "--data-json must be JSON object"}
				}
			}
			status, body, _, err := rt.request(cmd.Context(), rt.effectiveContext(), method, path, q, payload, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringArrayVar(&query, "query", nil, "repeatable query key=value")
	cmd.Flags().StringVar(&dataJSON, "data-json", "", "request JSON object")
	return cmd
}

func (rt *runtime) recreateIntent(ctx context.Context, intentID string, mode string) error {
	_, body, _, err := rt.request(ctx, rt.effectiveContext(), "GET", "/v1/intents/"+intentID, nil, nil, true)
	if err != nil {
		return err
	}
	intent := asMap(body["intent"])
	status := strings.ToUpper(asString(intent["status"]))
	if mode == "resume" && status != "WAITING" && status != "FAILED" {
		return &cliError{Code: 2, Msg: "resume expected WAITING or FAILED intent"}
	}
	if mode == "retry" && status != "FAILED" && status != "CANCELED" {
		return &cliError{Code: 2, Msg: "retry expected FAILED or CANCELED intent"}
	}
	payload := map[string]any{
		"intent_type":    asString(intent["intent_type"]),
		"correlation_id": uuid.NewString(),
		"from_agent":     asString(intent["from_agent"]),
		"to_agent":       asString(intent["to_agent"]),
		"payload":        intent["payload"],
	}
	if v := asString(intent["reply_to"]); v != "" {
		payload["reply_to"] = v
	}
	s, b, _, err := rt.request(ctx, rt.effectiveContext(), "POST", "/v1/intents", nil, payload, true)
	if err != nil {
		return err
	}
	return rt.printResult(s, map[string]any{"ok": s < 400, "mode": mode, "new_intent": b})
}

func (rt *runtime) streamEvents(ctx context.Context, c *clientConfig, intentID string, since int) (int, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + "/v1/intents/" + intentID + "/events/stream")
	if err != nil {
		return since, err
	}
	q := u.Query()
	q.Set("since", strconv.Itoa(since))
	q.Set("wait_seconds", "15")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return since, err
	}
	rt.applyAuthHeaders(req, c)
	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return since, &cliError{Code: 1, Msg: fmt.Sprintf("stream failed: %s", string(raw))}
	}
	sc := bufio.NewScanner(resp.Body)
	lineEvent := ""
	lineData := ""
	lineID := since
	nextSeq := since
	flush := func() {
		if lineData == "" {
			return
		}
		payload := map[string]any{}
		_ = json.Unmarshal([]byte(lineData), &payload)
		if lineEvent == "stream.timeout" {
			if n, ok := payload["next_seq"].(float64); ok {
				nextSeq = int(n)
			}
		} else {
			if id, ok := payload["seq"].(float64); ok {
				nextSeq = int(id)
			} else if lineID > 0 {
				nextSeq = lineID
			}
			if rt.outputJSON {
				_ = rt.printJSON(payload)
			} else {
				fmt.Printf("[%v] %v status=%v waiting=%v\n", payload["at"], payload["event_type"], payload["status"], payload["waiting_reason"])
			}
		}
		lineEvent, lineData, lineID = "", "", 0
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "id:") {
			lineID, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "id:")))
		} else if strings.HasPrefix(line, "event:") {
			lineEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			lineData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	flush()
	return nextSeq, sc.Err()
}

func (rt *runtime) request(ctx context.Context, c *clientConfig, method, path string, query map[string]string, payload any, expectJSON bool) (int, map[string]any, string, error) {
	status, body, raw, err := rt.doRequest(ctx, c, method, path, query, payload, expectJSON)
	if err != nil {
		return status, body, raw, err
	}
	// Auto-refresh: if we get 401 with an expired actor token and have a refresh token, try once.
	if status == 401 && c.RefreshToken != "" && c.resolvedActorToken() != "" {
		code := asString(asMap(body["error"])["code"])
		if code == "token_expired" || code == "invalid_actor_token" || strings.Contains(raw, "token_expired") || strings.Contains(raw, "expired") {
			if newToken, refreshErr := rt.tryRefreshActorToken(ctx, c); refreshErr == nil && newToken != "" {
				// Retry with the fresh token
				return rt.doRequest(ctx, c, method, path, query, payload, expectJSON)
			}
		}
	}
	return status, body, raw, err
}

// tryRefreshActorToken exchanges the stored refresh_token for a new access_token.
// On success it updates c.ActorToken and persists the new secrets.
func (rt *runtime) tryRefreshActorToken(ctx context.Context, c *clientConfig) (string, error) {
	// Use a minimal config without actor token to avoid sending the expired JWT
	refreshCtx := &clientConfig{
		BaseURL: c.BaseURL,
		APIKey:  c.APIKey,
	}
	_, body, _, err := rt.doRequest(ctx, refreshCtx, "POST", "/v1/auth/refresh", nil, map[string]any{
		"refresh_token": c.RefreshToken,
	}, true)
	if err != nil {
		return "", err
	}
	newToken := asString(body["access_token"])
	if newToken == "" {
		return "", fmt.Errorf("refresh: no access_token in response")
	}
	c.setActorToken(newToken)
	if newRefresh := asString(body["refresh_token"]); newRefresh != "" {
		c.RefreshToken = newRefresh
	}
	// Best-effort persist; don't fail the original request if save fails.
	_ = rt.persistConfig()
	return newToken, nil
}

func (rt *runtime) doRequest(ctx context.Context, c *clientConfig, method, path string, query map[string]string, payload any, expectJSON bool) (int, map[string]any, string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		return 0, nil, "", &cliError{Code: 2, Msg: "base_url is empty (set context or --base-url)"}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u, err := url.Parse(base + path)
	if err != nil {
		return 0, nil, "", err
	}
	if query != nil {
		q := u.Query()
		for k, v := range query {
			if strings.TrimSpace(v) != "" {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, "", err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("accept", "application/json")
	if payload != nil {
		req.Header.Set("content-type", "application/json")
	}
	rt.applyAuthHeaders(req, c)
	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(resp.Body)
	rawStr := string(rawBody)
	out := map[string]any{}
	if expectJSON && len(rawBody) > 0 {
		_ = json.Unmarshal(rawBody, &out)
	}
	return resp.StatusCode, out, rawStr, nil
}

func personalContextRequirementMessage(detail string) string {
	return "This command requires an account session. Run `axme login` to sign in, then try again."
}

// httpErrorMessage converts a non-2xx HTTP status and raw response body into a
// human-readable error message, promoting well-known error codes to actionable
// guidance.
func httpErrorMessage(status int, raw string) string {
	// Try to extract a machine-readable error code from the response body.
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal([]byte(raw), &parsed)

	code := parsed.Error.Code
	switch {
	case status == 401 && (code == "missing_actor_token" || code == "missing_api_key" || code == "unauthorized"):
		return "Not authenticated. Run `axme login` to sign in."
	case status == 401:
		return "Authentication failed. Run `axme login` to sign in."
	case status == 403:
		return "Permission denied. You may not have the required role for this action."
	case status == 404:
		msg := parsed.Error.Message
		if msg == "" {
			msg = parsed.Detail
		}
		if msg != "" {
			return fmt.Sprintf("Not found: %s", msg)
		}
		return "Not found."
	case status == 429:
		return "Rate limit exceeded. Please wait before retrying."
	case status >= 500:
		return fmt.Sprintf("Server error (%d). Please try again later.", status)
	default:
		if parsed.Error.Message != "" {
			return fmt.Sprintf("Request failed (%d): %s", status, parsed.Error.Message)
		}
		if parsed.Detail != "" {
			return fmt.Sprintf("Request failed (%d): %s", status, parsed.Detail)
		}
		return fmt.Sprintf("Request failed (%d).", status)
	}
}

func (rt *runtime) personalContextFromServer(ctx context.Context, c *clientConfig) (map[string]any, error) {
	status, body, raw, err := rt.request(ctx, c, "GET", "/v1/portal/personal/context", nil, nil, true)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		errorBody := asMap(body["error"])
		errorCode := asString(errorBody["code"])
		errorMessage := asString(errorBody["message"])
		detail := asString(body["detail"])
		if detail == "" {
			detail = errorMessage
		}
		switch {
		case status == 401 && errorCode == "missing_actor_token":
			return nil, &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
		case status == 401 && errorCode == "invalid_actor_token":
			return nil, &cliError{Code: 2, Msg: "Your account session token is invalid or expired. Run `axme login` to refresh it."}
		case status == 403 && (errorCode == "invalid_actor_scope" || strings.Contains(strings.ToLower(detail), "actor identity")):
			return nil, &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
		case status == 404 && strings.Contains(strings.ToLower(detail), "not bound to an organization/workspace context"):
			return nil, &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
		}
		return nil, fmt.Errorf("personal context returned %d: %s", status, raw)
	}
	return body, nil
}

func personalContextSummary(body map[string]any) map[string]any {
	summary := map[string]any{
		"server_context":        asMap(body["context"]),
		"selected_organization": asMap(body["selected_organization"]),
		"selected_workspace":    asMap(body["selected_workspace"]),
		"server_guidance":       asMap(body["guidance"]),
		"membership_count":      len(asSlice(body["workspaces"])),
		"organization_count":    len(asSlice(body["organizations"])),
	}
	if message := personalContextGuidanceMessage(body); message != "" {
		summary["guidance_message"] = message
	}
	return summary
}

func personalContextGuidanceMessage(body map[string]any) string {
	selectedWorkspace := asMap(body["selected_workspace"])
	selectedWorkspaceID := asString(selectedWorkspace["workspace_id"])
	selectedWorkspaceName := asString(selectedWorkspace["name"])
	if selectedWorkspaceName == "" {
		selectedWorkspaceName = selectedWorkspaceID
	}
	selectedOrg := asMap(body["selected_organization"])
	selectedOrgID := asString(selectedOrg["org_id"])
	selectedOrgName := asString(selectedOrg["name"])
	if selectedOrgName == "" {
		selectedOrgName = selectedOrgID
	}
	workspaceCount := len(asSlice(body["workspaces"]))
	switch {
	case workspaceCount > 1 && selectedWorkspaceID != "":
		return fmt.Sprintf(
			"Selected workspace: %s. You have %d visible workspaces. Run `axme workspace use <workspace-id>` to switch when needed.",
			selectedWorkspaceName,
			workspaceCount,
		)
	case workspaceCount > 1:
		return fmt.Sprintf(
			"You have %d visible workspaces and no server-selected workspace yet. Run `axme workspace use <workspace-id>` to choose one.",
			workspaceCount,
		)
	case workspaceCount == 1 && selectedWorkspaceID != "":
		return fmt.Sprintf("Selected workspace: %s.", selectedWorkspaceName)
	case workspaceCount == 1:
		return "One workspace is visible in your account membership inventory. Run `axme workspace use <workspace-id>` if you want to pin it as your selected workspace."
	case selectedOrgID != "":
		return fmt.Sprintf("Selected organization: %s.", selectedOrgName)
	default:
		return ""
	}
}

func accountSessionSummary(sessions []map[string]any, includeRevoked bool) map[string]any {
	summary := map[string]any{
		"session_count":       len(sessions),
		"active_count":        0,
		"revoked_count":       0,
		"include_revoked":     includeRevoked,
		"has_current_session": false,
	}
	currentSession := map[string]any{}
	activeCount := 0
	revokedCount := 0
	for _, session := range sessions {
		revoked := asBool(session["revoked"]) || strings.TrimSpace(asString(session["revoked_at"])) != ""
		if revoked {
			revokedCount++
		} else {
			activeCount++
		}
		if len(currentSession) == 0 && asBool(session["is_current"]) {
			currentSession = session
		}
	}
	summary["active_count"] = activeCount
	summary["revoked_count"] = revokedCount
	if len(currentSession) > 0 {
		summary["has_current_session"] = true
		summary["current_session_id"] = asString(currentSession["session_id"])
		summary["current_session"] = currentSession
	}
	if message := accountSessionGuidanceMessage(sessions, includeRevoked, activeCount, revokedCount, currentSession); message != "" {
		summary["guidance_message"] = message
	}
	return summary
}

func accountSessionGuidanceMessage(sessions []map[string]any, includeRevoked bool, activeCount, revokedCount int, currentSession map[string]any) string {
	currentSessionID := asString(currentSession["session_id"])
	switch {
	case len(sessions) == 0:
		return "No account sessions were returned by the server. Run `axme login` to create a new account session."
	case currentSessionID == "" && activeCount > 0:
		return "The server returned active account sessions, but none is marked as current for this token. Run `axme login` if this context no longer matches an active session."
	case currentSessionID != "" && activeCount > 1:
		return fmt.Sprintf(
			"Current account session: %s. %d active sessions are still available. Run `axme session revoke <session-id>` to clean up older sessions when needed.",
			currentSessionID,
			activeCount,
		)
	case currentSessionID != "" && activeCount == 1 && includeRevoked && revokedCount > 0:
		return fmt.Sprintf(
			"Current account session: %s. One active session remains and %d revoked sessions are also visible.",
			currentSessionID,
			revokedCount,
		)
	case currentSessionID != "":
		return fmt.Sprintf("Current account session: %s.", currentSessionID)
	case includeRevoked && revokedCount > 0:
		return fmt.Sprintf("%d revoked account sessions are visible.", revokedCount)
	default:
		return ""
	}
}

func sessionRevokeGuidanceMessage(mode string, revoked bool) string {
	if !revoked {
		return "The server did not confirm session revocation. Run `axme session list` and retry."
	}
	if mode == "current_session" {
		return "Current account session revoked on the server. Run `axme logout` to clear the stale local token, or `axme login` to create a fresh account session."
	}
	return "Server-side session revoked. Run `axme session list` to confirm the remaining active sessions."
}

func logoutGuidanceMessage(apiKeyCleared bool, serverResult map[string]any) string {
	attempted := asBool(serverResult["attempted"])
	revoked := asBool(serverResult["revoked"])
	mode := asString(serverResult["mode"])
	if strings.TrimSpace(asString(serverResult["error"])) != "" {
		if apiKeyCleared {
			return "Cleared local account and workspace credentials, but server-side session revocation did not complete. If another environment is still signed in, use `axme session list` or `axme logout --all-sessions` there."
		}
		return "Cleared the local account session token, but server-side session revocation did not complete. Your workspace API key remains available for workspace-scoped commands. If another environment is still signed in, use `axme session list` or `axme logout --all-sessions` there."
	}
	if attempted && revoked && mode == "all_sessions" {
		if apiKeyCleared {
			return "All server-side account sessions were revoked, and both local credentials were cleared. Run `axme login` before the next account-level command."
		}
		return "All server-side account sessions were revoked and the local account session was cleared. The workspace API key remains available for workspace-scoped commands."
	}
	if attempted && revoked {
		if apiKeyCleared {
			return "Current server-side account session was revoked, and both local credentials were cleared. Run `axme login` before the next account-level command."
		}
		return "Current server-side account session was revoked and the local account session was cleared. The workspace API key remains available for workspace-scoped commands."
	}
	if !attempted {
		if apiKeyCleared {
			return "No local account session token was loaded. Cleared the local workspace API key too."
		}
		return "No local account session token was loaded. Cleared local account-session state only; the workspace API key remains available."
	}
	if apiKeyCleared {
		return "Cleared both local credentials. Run `axme login` before the next account-level command."
	}
	return "Cleared the local account session token. The workspace API key remains available for workspace-scoped commands."
}

func organizationContextRequirementMessage(detail string) string {
	base := "Organization context is required. Use `axme workspace use`, `axme org list`, pass `--org-id`, or set an active org in your context."
	if strings.TrimSpace(detail) == "" {
		return base
	}
	return fmt.Sprintf("%s %s", base, detail)
}

func workspaceContextRequirementMessage(detail string) string {
	base := "Workspace context is required. Use `axme workspace use`, `axme workspace list`, pass `--workspace-id`, or set an active workspace in your context."
	if strings.TrimSpace(detail) == "" {
		return base
	}
	return fmt.Sprintf("%s %s", base, detail)
}

func memberScopeSummary(orgID string, workspaceID string) map[string]any {
	scope := "organization"
	description := "organization-wide member operation"
	if strings.TrimSpace(workspaceID) != "" {
		scope = "workspace"
		description = "workspace-scoped member operation"
	}
	return map[string]any{
		"scope":        scope,
		"description":  description,
		"org_id":       orgID,
		"workspace_id": workspaceID,
	}
}

func memberListGuidance(c *clientConfig, orgID string, workspaceID string) string {
	if strings.TrimSpace(workspaceID) != "" {
		return fmt.Sprintf("Listing workspace-scoped members for workspace %s in org %s.", workspaceID, orgID)
	}
	if c != nil && strings.TrimSpace(c.WorkspaceID) != "" {
		return fmt.Sprintf(
			"Listing organization-wide members for org %s. Current selected workspace is %s. Pass `--workspace-id %s` to narrow the view.",
			orgID,
			c.WorkspaceID,
			c.WorkspaceID,
		)
	}
	return fmt.Sprintf("Listing organization-wide members for org %s. Pass `--workspace-id <workspace-id>` to narrow the view.", orgID)
}

func serviceAccountScopeSummary(orgID string, workspaceID string) map[string]any {
	scope := "organization"
	description := "organization-wide service-account operation"
	if strings.TrimSpace(workspaceID) != "" {
		scope = "workspace"
		description = "workspace-scoped service-account operation"
	}
	return map[string]any{
		"scope":        scope,
		"description":  description,
		"org_id":       orgID,
		"workspace_id": workspaceID,
	}
}

func serviceAccountListGuidance(c *clientConfig, orgID string, workspaceID string) string {
	if strings.TrimSpace(workspaceID) != "" {
		return fmt.Sprintf("Listing service accounts for workspace %s in org %s.", workspaceID, orgID)
	}
	if c != nil && strings.TrimSpace(c.WorkspaceID) != "" {
		return fmt.Sprintf(
			"Listing organization-wide service accounts for org %s. Current selected workspace is %s. Pass `--workspace-id %s` to narrow the view.",
			orgID,
			c.WorkspaceID,
			c.WorkspaceID,
		)
	}
	return fmt.Sprintf("Listing organization-wide service accounts for org %s. Pass `--workspace-id <workspace-id>` to narrow the view.", orgID)
}

func serviceAccountAdminRequirementMessage(_ string) string {
	return "This command requires an account session with organization or workspace admin access, plus a valid workspace or platform API key. Run `axme login`, select the right workspace, and try again."
}

func serviceAccountPlatformAPIKeyRequirementMessage(_ string) string {
	return "This command requires a workspace or platform API key in the active context. Set the right API key or select the right workspace context, then try again."
}

func (rt *runtime) resolveServiceAccountListContext(ctx context.Context, c *clientConfig, overrideOrgID, overrideWorkspaceID string) (string, string, error) {
	orgID := strings.TrimSpace(overrideOrgID)
	if orgID == "" {
		orgID = strings.TrimSpace(c.OrgID)
	}
	workspaceID := strings.TrimSpace(overrideWorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(c.WorkspaceID)
	}
	if orgID != "" && workspaceID != "" {
		return orgID, workspaceID, nil
	}
	if strings.TrimSpace(c.resolvedActorToken()) == "" {
		if orgID == "" {
			return "", "", &cliError{Code: 2, Msg: organizationContextRequirementMessage("")}
		}
		return orgID, workspaceID, nil
	}
	body, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		return "", "", err
	}
	serverContext := asMap(body["context"])
	if orgID == "" {
		orgID = strings.TrimSpace(asString(serverContext["org_id"]))
	}
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(asString(serverContext["workspace_id"]))
	}
	if orgID == "" {
		return "", "", &cliError{Code: 2, Msg: organizationContextRequirementMessage(personalContextGuidanceMessage(body))}
	}
	return orgID, workspaceID, nil
}

func (rt *runtime) resolveEnterpriseOrganizationContext(ctx context.Context, c *clientConfig, overrideOrgID string) (string, error) {
	if orgID := strings.TrimSpace(overrideOrgID); orgID != "" {
		return orgID, nil
	}
	if orgID := strings.TrimSpace(c.OrgID); orgID != "" {
		return orgID, nil
	}
	if strings.TrimSpace(c.resolvedActorToken()) == "" {
		return "", &cliError{Code: 2, Msg: organizationContextRequirementMessage("")}
	}
	body, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		return "", err
	}
	serverContext := asMap(body["context"])
	if orgID := strings.TrimSpace(asString(serverContext["org_id"])); orgID != "" {
		return orgID, nil
	}
	return "", &cliError{Code: 2, Msg: organizationContextRequirementMessage(personalContextGuidanceMessage(body))}
}

func (rt *runtime) resolveEnterpriseWorkspaceContext(ctx context.Context, c *clientConfig, overrideOrgID, overrideWorkspaceID string) (string, string, error) {
	orgID := strings.TrimSpace(overrideOrgID)
	if orgID == "" {
		orgID = strings.TrimSpace(c.OrgID)
	}
	workspaceID := strings.TrimSpace(overrideWorkspaceID)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(c.WorkspaceID)
	}
	if orgID != "" && workspaceID != "" {
		return orgID, workspaceID, nil
	}
	if strings.TrimSpace(c.resolvedActorToken()) == "" {
		if orgID == "" {
			return "", "", &cliError{Code: 2, Msg: organizationContextRequirementMessage("")}
		}
		return "", "", &cliError{Code: 2, Msg: workspaceContextRequirementMessage("")}
	}
	body, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		return "", "", err
	}
	serverContext := asMap(body["context"])
	if orgID == "" {
		orgID = strings.TrimSpace(asString(serverContext["org_id"]))
	}
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(asString(serverContext["workspace_id"]))
	}
	if orgID == "" {
		return "", "", &cliError{Code: 2, Msg: organizationContextRequirementMessage(personalContextGuidanceMessage(body))}
	}
	if workspaceID == "" {
		return "", "", &cliError{Code: 2, Msg: workspaceContextRequirementMessage(personalContextGuidanceMessage(body))}
	}
	return orgID, workspaceID, nil
}

func (rt *runtime) personalWorkspaceSelectionAPIError(status int, body map[string]any, raw string) error {
	errorBody := asMap(body["error"])
	errorCode := asString(errorBody["code"])
	errorMessage := asString(errorBody["message"])
	detail := asString(body["detail"])
	if detail == "" {
		detail = errorMessage
	}
	switch {
	case status == 401 && errorCode == "missing_actor_token":
		return &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
	case status == 401 && errorCode == "invalid_actor_token":
		return &cliError{Code: 2, Msg: "Your account session token is invalid or expired. Run `axme login` to refresh it."}
	case status == 403 && strings.Contains(strings.ToLower(detail), "outside actor membership scope"):
		return &cliError{Code: 2, Msg: "That workspace is not part of your account membership inventory. Run `axme workspace list` to see available workspaces, then try again."}
	case status == 403 && (errorCode == "invalid_actor_scope" || strings.Contains(strings.ToLower(detail), "actor identity")):
		return &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
	}
	return fmt.Errorf("workspace selection returned %d: %s", status, raw)
}

func enterpriseMemberRequirementMessage(_ string) string {
	return "This command requires an account session with organization or workspace admin access. Run `axme login`, select the right workspace, and try again."
}

func enterpriseMemberScopeMessage(detail string) string {
	base := "That organization or workspace is outside your account membership inventory. Run `axme workspace list` to confirm the selected workspace, then retry with the right `--org-id` or `--workspace-id`."
	if strings.TrimSpace(detail) == "" {
		return base
	}
	return fmt.Sprintf("%s Server detail: %s", base, detail)
}

func (rt *runtime) enterpriseMembersAPIError(status int, body map[string]any, raw string) error {
	errorBody := asMap(body["error"])
	errorCode := asString(errorBody["code"])
	errorMessage := asString(errorBody["message"])
	detail := asString(body["detail"])
	if detail == "" {
		detail = errorMessage
	}
	switch {
	case status == 401 && errorCode == "missing_actor_token":
		return &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
	case status == 401 && errorCode == "invalid_actor_token":
		return &cliError{Code: 2, Msg: "Your account session token is invalid or expired. Run `axme login` to refresh it."}
	case status == 403 && (strings.Contains(strings.ToLower(detail), "outside actor membership scope") ||
		strings.Contains(strings.ToLower(detail), "workspace_id does not match target workspace_id") ||
		strings.Contains(strings.ToLower(detail), "membership scope")):
		return &cliError{Code: 2, Msg: enterpriseMemberScopeMessage(detail)}
	case status == 403:
		return &cliError{Code: 2, Msg: enterpriseMemberRequirementMessage(detail)}
	case status == 404 && strings.TrimSpace(detail) != "":
		return &cliError{Code: 2, Msg: detail}
	case status == 422 && strings.TrimSpace(detail) != "":
		return &cliError{Code: 2, Msg: detail}
	default:
		return fmt.Errorf("enterprise member request returned %d: %s", status, raw)
	}
}

func (rt *runtime) serviceAccountsAPIError(status int, body map[string]any, raw string) error {
	errorBody := asMap(body["error"])
	errorCode := asString(errorBody["code"])
	errorMessage := asString(errorBody["message"])
	detail := asString(body["detail"])
	if detail == "" {
		detail = errorMessage
	}
	switch {
	case status == 401 && errorCode == "missing_actor_token":
		return &cliError{Code: 2, Msg: personalContextRequirementMessage(detail)}
	case status == 401 && errorCode == "invalid_actor_token":
		return &cliError{Code: 2, Msg: "Your account session token is invalid or expired. Run `axme login` to refresh it."}
	case status == 401 && errorCode == "missing_platform_api_key":
		return &cliError{Code: 2, Msg: serviceAccountPlatformAPIKeyRequirementMessage(detail)}
	case status == 403 && (strings.Contains(strings.ToLower(detail), "outside actor membership scope") ||
		strings.Contains(strings.ToLower(detail), "workspace_id does not match target workspace_id") ||
		strings.Contains(strings.ToLower(detail), "membership scope")):
		return &cliError{Code: 2, Msg: enterpriseMemberScopeMessage(detail)}
	case status == 403:
		return &cliError{Code: 2, Msg: serviceAccountAdminRequirementMessage(detail)}
	case status == 404 && strings.TrimSpace(detail) != "":
		return &cliError{Code: 2, Msg: detail}
	case status == 422 && strings.TrimSpace(detail) != "":
		return &cliError{Code: 2, Msg: detail}
	default:
		return fmt.Errorf("service account request returned %d: %s", status, raw)
	}
}

func (rt *runtime) listEnterpriseMembers(ctx context.Context, c *clientConfig, orgID string, workspaceID string) ([]map[string]any, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"GET",
		"/v1/organizations/"+orgID+"/members",
		map[string]string{"workspace_id": workspaceID},
		nil,
		true,
	)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, rt.enterpriseMembersAPIError(status, body, raw)
	}
	items := asSlice(body["members"])
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (rt *runtime) addEnterpriseMember(ctx context.Context, c *clientConfig, orgID string, workspaceID string, actorID string, role string) (map[string]any, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"POST",
		"/v1/organizations/"+orgID+"/members",
		nil,
		map[string]any{
			"actor_id":     actorID,
			"role":         role,
			"workspace_id": workspaceID,
		},
		true,
	)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, rt.enterpriseMembersAPIError(status, body, raw)
	}
	return asMap(body["member"]), nil
}

func (rt *runtime) updateEnterpriseMember(ctx context.Context, c *clientConfig, orgID string, memberID string, role string, statusValue string) (map[string]any, error) {
	payload := map[string]any{}
	if strings.TrimSpace(role) != "" {
		payload["role"] = role
	}
	if strings.TrimSpace(statusValue) != "" {
		payload["status"] = statusValue
	}
	status, body, raw, err := rt.request(
		ctx,
		c,
		"PATCH",
		"/v1/organizations/"+orgID+"/members/"+memberID,
		nil,
		payload,
		true,
	)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, rt.enterpriseMembersAPIError(status, body, raw)
	}
	return asMap(body["member"]), nil
}

func (rt *runtime) removeEnterpriseMember(ctx context.Context, c *clientConfig, orgID string, memberID string) (map[string]any, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"DELETE",
		"/v1/organizations/"+orgID+"/members/"+memberID,
		nil,
		nil,
		true,
	)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, rt.enterpriseMembersAPIError(status, body, raw)
	}
	return asMap(body["result"]), nil
}

func (rt *runtime) listAccountSessions(ctx context.Context, c *clientConfig, includeRevoked bool) ([]map[string]any, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"GET",
		"/v1/auth/sessions",
		map[string]string{"include_revoked": strconv.FormatBool(includeRevoked)},
		nil,
		true,
	)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("list sessions returned %d: %s", status, raw)
	}
	items := asSlice(body["sessions"])
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, asMap(item))
	}
	return out, nil
}

func (rt *runtime) revokeAccountSessionByID(ctx context.Context, c *clientConfig, sessionID string) (bool, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"POST",
		"/v1/auth/sessions/revoke",
		nil,
		map[string]any{"session_id": sessionID},
		true,
	)
	if err != nil {
		return false, err
	}
	if status >= 400 {
		return false, fmt.Errorf("revoke session returned %d: %s", status, raw)
	}
	return asBool(body["revoked"]) || asBool(body["ok"]), nil
}

func (rt *runtime) revokeCurrentAccountSession(ctx context.Context, c *clientConfig) (string, bool, error) {
	sessions, err := rt.listAccountSessions(ctx, c, false)
	if err != nil {
		return "", false, err
	}
	for _, session := range sessions {
		if !asBool(session["is_current"]) {
			continue
		}
		sessionID := asString(session["session_id"])
		if sessionID == "" {
			break
		}
		revoked, err := rt.revokeAccountSessionByID(ctx, c, sessionID)
		if err != nil {
			return sessionID, false, err
		}
		return sessionID, revoked, nil
	}
	return "", false, fmt.Errorf("current account session was not found on the server")
}

func (rt *runtime) logoutAllAccountSessions(ctx context.Context, c *clientConfig) (int, error) {
	status, body, raw, err := rt.request(
		ctx,
		c,
		"POST",
		"/v1/auth/logout-all",
		nil,
		map[string]any{},
		true,
	)
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return status, fmt.Errorf("logout-all returned %d: %s", status, raw)
	}
	if !asBool(body["ok"]) {
		return status, fmt.Errorf("logout-all did not confirm success")
	}
	return status, nil
}

func (rt *runtime) hydrateContextFromServer(ctx context.Context, c *clientConfig) (map[string]any, error) {
	body, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		return nil, err
	}
	resolvedContext := asMap(body["context"])
	if len(resolvedContext) == 0 {
		return nil, fmt.Errorf("context resolution response is missing context")
	}
	if asString(resolvedContext["org_id"]) == "" || asString(resolvedContext["workspace_id"]) == "" {
		return nil, fmt.Errorf("context resolution response is missing org_id/workspace_id")
	}
	return resolvedContext, nil
}

func (rt *runtime) applyAuthHeaders(req *http.Request, c *clientConfig) {
	if c.APIKey != "" {
		req.Header.Set("x-api-key", c.APIKey)
	}
	if actorToken := c.resolvedActorToken(); actorToken != "" {
		req.Header.Set("authorization", "Bearer "+actorToken)
	}
	if c.OwnerAgent != "" {
		req.Header.Set("x-owner-agent", c.OwnerAgent)
	}
}

func (rt *runtime) loadSecretsIntoContext(name string, c *clientConfig) error {
	if rt == nil || rt.secretStore == nil || c == nil {
		return nil
	}
	if c.secretsLoaded {
		return nil
	}
	secrets, err := rt.secretStore.Load(name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.APIKey) == "" {
		c.APIKey = strings.TrimSpace(secrets.APIKey)
	}
	if strings.TrimSpace(c.resolvedActorToken()) == "" {
		c.setActorToken(strings.TrimSpace(secrets.ActorToken))
	}
	if strings.TrimSpace(c.RefreshToken) == "" {
		c.RefreshToken = strings.TrimSpace(secrets.RefreshToken)
	}
	c.secretsLoaded = true
	return nil
}

func (rt *runtime) contextWithSecrets(name string) (*clientConfig, error) {
	c := rt.ensureContext(name)
	if err := rt.loadSecretsIntoContext(name, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (rt *runtime) persistConfig() error {
	if rt == nil || rt.cfg == nil {
		return nil
	}
	if rt.secretStore != nil {
		for name, c := range rt.cfg.Contexts {
			if c == nil {
				continue
			}
			secrets := storedContextSecrets{
				APIKey:       c.APIKey,
				ActorToken:   c.resolvedActorToken(),
				RefreshToken: c.RefreshToken,
			}
			if !c.secretsLoaded {
				existing, err := rt.secretStore.Load(name)
				if err != nil {
					return err
				}
				if strings.TrimSpace(secrets.APIKey) == "" {
					secrets.APIKey = existing.APIKey
				}
				if strings.TrimSpace(secrets.ActorToken) == "" {
					secrets.ActorToken = existing.ActorToken
				}
				if strings.TrimSpace(secrets.RefreshToken) == "" {
					secrets.RefreshToken = existing.RefreshToken
				}
			}
			if err := rt.secretStore.Save(name, secrets); err != nil {
				return err
			}
		}
	}
	return saveConfig(rt.cfgFile, rt.cfg)
}

func (rt *runtime) migratePlaintextSecrets() error {
	if rt == nil || rt.cfg == nil || rt.secretStore == nil {
		return nil
	}
	var foundPlaintext bool
	for name, c := range rt.cfg.Contexts {
		if c == nil {
			continue
		}
		if strings.TrimSpace(c.APIKey) == "" && strings.TrimSpace(c.resolvedActorToken()) == "" {
			continue
		}
		if err := rt.secretStore.Save(name, storedContextSecrets{
			APIKey:     c.APIKey,
			ActorToken: c.resolvedActorToken(),
		}); err != nil {
			return err
		}
		c.APIKey = ""
		c.ActorToken = ""
		c.BearerToken = ""
		c.secretsLoaded = true
		foundPlaintext = true
	}
	if foundPlaintext {
		return saveConfig(rt.cfgFile, rt.cfg)
	}
	return nil
}

func (rt *runtime) effectiveContextWithSecrets() (*clientConfig, error) {
	active, err := rt.contextWithSecrets(rt.activeContextName())
	if err != nil {
		return nil, err
	}
	merged := *active
	merged.normalizeActorToken()
	if merged.BaseURL == "" {
		if v := strings.TrimSpace(os.Getenv("AXME_PORTAL_BASE_URL")); v != "" {
			merged.BaseURL = strings.TrimRight(v, "/")
		} else if v := strings.TrimSpace(os.Getenv("AXME_GATEWAY_BASE_URL")); v != "" {
			merged.BaseURL = strings.TrimRight(v, "/")
		}
	}
	if merged.APIKey == "" {
		merged.APIKey = strings.TrimSpace(os.Getenv("AXME_GATEWAY_API_KEY"))
	}
	if merged.ActorToken == "" {
		merged.ActorToken = strings.TrimSpace(os.Getenv("AXME_ACTOR_TOKEN"))
	}
	if merged.BearerToken == "" {
		merged.BearerToken = strings.TrimSpace(os.Getenv("AXME_PORTAL_SCOPED_BEARER_TOKEN"))
	}
	merged.normalizeActorToken()
	if merged.OrgID == "" {
		merged.OrgID = strings.TrimSpace(os.Getenv("AXME_ORG_ID"))
	}
	if merged.WorkspaceID == "" {
		merged.WorkspaceID = strings.TrimSpace(os.Getenv("AXME_WORKSPACE_ID"))
	}
	if merged.OwnerAgent == "" {
		merged.OwnerAgent = strings.TrimSpace(os.Getenv("AXME_OWNER_AGENT"))
	}
	if merged.Environment == "" {
		merged.Environment = strings.TrimSpace(os.Getenv("AXME_ENVIRONMENT"))
	}
	if rt.overrideBase != "" {
		merged.BaseURL = strings.TrimRight(rt.overrideBase, "/")
	}
	if rt.overrideKey != "" {
		merged.APIKey = rt.overrideKey
	}
	if rt.overrideJWT != "" {
		merged.setActorToken(rt.overrideJWT)
	}
	if rt.overrideOrg != "" {
		merged.OrgID = rt.overrideOrg
	}
	if rt.overrideWs != "" {
		merged.WorkspaceID = rt.overrideWs
	}
	if rt.overrideOwn != "" {
		merged.OwnerAgent = rt.overrideOwn
	}
	if rt.overrideEnv != "" {
		merged.Environment = rt.overrideEnv
	}
	return &merged, nil
}

func (rt *runtime) effectiveContext() *clientConfig {
	merged, err := rt.effectiveContextWithSecrets()
	if err == nil {
		return merged
	}
	active := rt.ensureContext(rt.activeContextName())
	fallback := *active
	fallback.normalizeActorToken()
	return &fallback
}

func (rt *runtime) applyPersistentContextOverrides(c *clientConfig) {
	if rt == nil || c == nil {
		return
	}
	if rt.overrideBase != "" {
		c.BaseURL = strings.TrimRight(rt.overrideBase, "/")
	}
	if rt.overrideOrg != "" {
		c.OrgID = rt.overrideOrg
	}
	if rt.overrideWs != "" {
		c.WorkspaceID = rt.overrideWs
	}
	if rt.overrideOwn != "" {
		c.OwnerAgent = rt.overrideOwn
	}
	if rt.overrideEnv != "" {
		c.Environment = rt.overrideEnv
	}
}

func (rt *runtime) ensureContext(name string) *clientConfig {
	c, ok := rt.cfg.Contexts[name]
	if ok {
		c.normalizeActorToken()
		if err := rt.loadSecretsIntoContext(name, c); err != nil {
			// Defer the hard failure until a command needs persisted secrets.
		}
		return c
	}
	c = &clientConfig{BaseURL: defaultLocalAPIBaseURL, Environment: "staging"}
	rt.cfg.Contexts[name] = c
	if rt.cfg.ActiveContext == "" {
		rt.cfg.ActiveContext = name
	}
	return c
}

func (rt *runtime) activeContextName() string {
	if rt.contextName != "" {
		return rt.contextName
	}
	if rt.cfg.ActiveContext == "" {
		rt.cfg.ActiveContext = "default"
	}
	return rt.cfg.ActiveContext
}

func (rt *runtime) printResult(status int, body any) error {
	result := map[string]any{"status_code": status, "ok": status < 400, "body": body}
	if rt.outputJSON {
		return rt.printJSON(result)
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(raw))
	if status >= 400 {
		return &cliError{Code: 1, Msg: "request failed"}
	}
	return nil
}

func (rt *runtime) printGeneric(v any) error {
	if rt.outputJSON {
		return rt.printJSON(v)
	}
	raw, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(raw))
	return nil
}

func (rt *runtime) printJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func printTable(headers []string, rows []map[string]any, keys []string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprint(row[key]))
		}
		fmt.Fprintln(w, strings.Join(parts, "\t"))
	}
	_ = w.Flush()
}

func interactiveInputAvailable() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptRequiredLine(prompt string) (string, error) {
	for {
		value, err := promptLine(prompt)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
		fmt.Fprintln(os.Stderr, "This field is required.")
	}
}

func prepareCloudAlphaContext(c *clientConfig) {
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" || baseURL == defaultLocalAPIBaseURL {
		c.BaseURL = defaultCloudAPIBaseURL
	}
	if strings.TrimSpace(c.Environment) == "" || strings.EqualFold(strings.TrimSpace(c.Environment), "staging") {
		c.Environment = "cloud-alpha"
	}
}

func openURLInBrowser(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("empty onboarding URL")
	}
	var cmd *exec.Cmd
	switch stdruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func resolveConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "axme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func loadConfig(path string) (*appConfig, error) {
	if !fileExists(path) {
		cfg := &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: "http://127.0.0.1:8100", Environment: "staging"},
			},
		}
		if err := saveConfig(path, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &appConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*clientConfig{}
	}
	for _, c := range cfg.Contexts {
		if c != nil {
			c.normalizeActorToken()
		}
	}
	if cfg.ActiveContext == "" {
		cfg.ActiveContext = "default"
	}
	if _, ok := cfg.Contexts[cfg.ActiveContext]; !ok {
		cfg.Contexts[cfg.ActiveContext] = &clientConfig{BaseURL: "http://127.0.0.1:8100"}
	}
	return cfg, nil
}

func saveConfig(path string, cfg *appConfig) error {
	for _, c := range cfg.Contexts {
		if c != nil {
			c.normalizeActorToken()
		}
	}
	sanitized := &appConfig{
		ActiveContext: cfg.ActiveContext,
		Contexts:      map[string]*clientConfig{},
	}
	for name, c := range cfg.Contexts {
		if c == nil {
			continue
		}
		sanitized.Contexts[name] = &clientConfig{
			BaseURL:     c.BaseURL,
			OwnerAgent:  c.OwnerAgent,
			OrgID:       c.OrgID,
			WorkspaceID: c.WorkspaceID,
			Environment: c.Environment,
		}
	}
	raw, err := json.MarshalIndent(sanitized, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (c *clientConfig) resolvedActorToken() string {
	if c == nil {
		return ""
	}
	if strings.TrimSpace(c.ActorToken) != "" {
		return strings.TrimSpace(c.ActorToken)
	}
	return strings.TrimSpace(c.BearerToken)
}

func (c *clientConfig) setActorToken(token string) {
	if c == nil {
		return
	}
	normalized := strings.TrimSpace(token)
	c.ActorToken = normalized
	c.BearerToken = normalized
}

func (c *clientConfig) normalizeActorToken() {
	if c == nil {
		return
	}
	if c.ActorToken == "" && c.BearerToken != "" {
		c.ActorToken = c.BearerToken
	}
	if c.BearerToken == "" && c.ActorToken != "" {
		c.BearerToken = c.ActorToken
	}
}

func builtInExamples() map[string]map[string]any {
	return map[string]map[string]any{
		"approval-resume": {
			"intent_type":    "intent.ask.v1",
			"correlation_id": uuid.NewString(),
			"from_agent":     "agent://alpha/requester",
			"to_agent":       "agent://alpha/reviewer",
			"payload": map[string]any{
				"service":  "demo.approval",
				"tags":     []string{"alpha", "approval"},
				"question": "Please approve onboarding action",
			},
		},
		"tool-waiting": {
			"intent_type":    "intent.ask.v1",
			"correlation_id": uuid.NewString(),
			"from_agent":     "agent://alpha/requester",
			"to_agent":       "agent://alpha/tool-runner",
			"payload": map[string]any{
				"service":  "demo.tool",
				"tags":     []string{"alpha", "tool"},
				"question": "Run external tool and resume",
			},
		},
	}
}

func loadRunPayload(target string) (map[string]any, error) {
	if example, ok := builtInExamples()[target]; ok {
		return cloneMap(example), nil
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, &cliError{Code: 2, Msg: "example not found and file cannot be read"}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &cliError{Code: 2, Msg: "run file must be JSON object"}
	}
	return out, nil
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func parseSince(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().UTC().Add(-d), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func ageFromTime(updated, created string) string {
	candidates := []string{updated, created}
	for _, item := range candidates {
		if item == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, item); err == nil {
			d := time.Since(t)
			if d < time.Minute {
				return fmt.Sprintf("%ds", int(d.Seconds()))
			}
			if d < time.Hour {
				return fmt.Sprintf("%dm", int(d.Minutes()))
			}
			return fmt.Sprintf("%dh", int(d.Hours()))
		}
	}
	return ""
}

func asMap(v any) map[string]any {
	if out, ok := v.(map[string]any); ok {
		return out
	}
	return map[string]any{}
}

func asSlice(v any) []any {
	if out, ok := v.([]any); ok {
		return out
	}
	return []any{}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func asStringSlice(v any) []string {
	items := asSlice(v)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asNestedString(m map[string]any, key string) string {
	if s := asString(m[key]); s != "" {
		return s
	}
	for _, nested := range []string{"intent", "result", "data"} {
		if v := asMap(m[nested]); len(v) > 0 {
			if s := asString(v[key]); s != "" {
				return s
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
