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
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key,omitempty"`
	ActorToken  string `json:"actor_token,omitempty"`
	BearerToken string `json:"bearer_token,omitempty"`
	OwnerAgent  string `json:"owner_agent,omitempty"`
	OrgID       string `json:"org_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type runtime struct {
	cfgFile      string
	cfg          *appConfig
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
		Use:   "axme",
		Short: "Axme B2B infra CLI",
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
		newAdminCmd(rt),
	)
	return cmd
}

func newLoginCmd(rt *runtime) *cobra.Command {
	var key string
	var token string
	var owner string
	var targetContext string
	var useWebOnboarding bool
	var useDeviceFlow bool
	var noBrowser bool
	var onboardingURL string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in, bootstrap alpha workspace, or store credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			if onboardingURL == "" {
				onboardingURL = defaultAlphaOnboardingURL
			}
			// Device/browser flow: no API key required upfront
			if useDeviceFlow {
				ctxName := targetContext
				if ctxName == "" {
					ctxName = rt.activeContextName()
				}
				prepareCloudAlphaContext(rt.ensureContext(ctxName))
				return rt.runDeviceLogin(cmd.Context(), ctxName)
			}
			if key == "" && token == "" {
				if interactiveInputAvailable() {
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
						key = enteredKey
					} else {
						enteredKey, err := promptLine("AXME API key (press Enter to start cloud alpha onboarding in CLI): ")
						if err != nil {
							return err
						}
						if enteredKey == "" {
							ctxName := targetContext
							if ctxName == "" {
								ctxName = rt.activeContextName()
							}
							return rt.runAlphaBootstrapLogin(cmd.Context(), ctxName)
						} else {
							key = enteredKey
						}
					}
				}
			}
			if key == "" && token == "" {
				msg := fmt.Sprintf(
					"No credentials were stored. Run `axme login` interactively to create or attach a cloud alpha workspace, use --api-key/--actor-token directly, or open %s for CLI onboarding instructions.",
					onboardingURL,
				)
				if rt.outputJSON {
					return rt.printJSON(map[string]any{
						"ok":             true,
						"deferred":       true,
						"onboarding_url": onboardingURL,
						"message":        msg,
					})
				}
				fmt.Println(msg)
				return nil
			}
			ctxName := targetContext
			if ctxName == "" {
				ctxName = rt.activeContextName()
			}
			ctx := rt.ensureContext(ctxName)
			if key != "" {
				ctx.APIKey = strings.TrimSpace(key)
			}
			if token != "" {
				ctx.setActorToken(strings.TrimSpace(token))
			}
			if owner != "" {
				ctx.OwnerAgent = strings.TrimSpace(owner)
			}
			var hydrated bool
			var hydrateWarning string
			if resolvedContext, err := rt.hydrateContextFromServer(cmd.Context(), ctx); err == nil {
				hydrated = true
				if resolvedOrgID := asString(resolvedContext["org_id"]); resolvedOrgID != "" {
					ctx.OrgID = resolvedOrgID
				}
				if resolvedWorkspaceID := asString(resolvedContext["workspace_id"]); resolvedWorkspaceID != "" {
					ctx.WorkspaceID = resolvedWorkspaceID
				}
			} else {
				hydrateWarning = err.Error()
				if !rt.outputJSON {
					fmt.Fprintf(
						os.Stderr,
						"warning: saved credentials, but could not resolve default org/workspace automatically: %v\n",
						err,
					)
				}
			}
			if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
				return err
			}
			body := map[string]any{
				"ok":           true,
				"context":      ctxName,
				"hydrated":     hydrated,
				"org_id":       ctx.OrgID,
				"workspace_id": ctx.WorkspaceID,
			}
			if hydrateWarning != "" {
				body["warning"] = hydrateWarning
			}
			return rt.printResult(200, body)
		},
	}
	cmd.Flags().StringVar(&key, "api-key", "", "API key")
	cmd.Flags().StringVar(&token, "actor-token", "", "Actor token (Authorization: Bearer ...)")
	cmd.Flags().StringVar(&token, "bearer-token", "", "Bearer token")
	cmd.Flags().StringVar(&owner, "owner-agent", "", "Owner agent (e.g. agent://alice)")
	cmd.Flags().StringVar(&targetContext, "context", "", "Target context name")
	cmd.Flags().BoolVar(&useWebOnboarding, "web", false, "legacy fallback")
	cmd.Flags().BoolVar(&useDeviceFlow, "device", false, "browser device flow: open approval page and wait for confirmation (no copy-paste)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't try to open browser automatically during login")
	cmd.Flags().StringVar(&onboardingURL, "onboarding-url", defaultAlphaOnboardingURL, "CLI onboarding page URL")
	_ = cmd.Flags().MarkHidden("web")
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

	if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
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
			ctx := rt.effectiveContext()
			out := map[string]any{
				"context":      rt.activeContextName(),
				"base_url":     ctx.BaseURL,
				"org_id":       ctx.OrgID,
				"workspace_id": ctx.WorkspaceID,
				"owner_agent":  ctx.OwnerAgent,
				"environment":  ctx.Environment,
			}
			if ctx.resolvedActorToken() != "" {
				_, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/auth/sessions", map[string]string{"include_revoked": "false"}, nil, false)
				if err == nil {
					out["sessions"] = body["sessions"]
				} else {
					out["sessions_error"] = err.Error()
				}
			}
			return rt.printGeneric(out)
		},
	}
}

func newLogoutCmd(rt *runtime) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Clear stored credentials for active context",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.ensureContext(rt.activeContextName())
			ctx.setActorToken("")
			if all {
				ctx.APIKey = ""
			}
			if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
				return err
			}
			return rt.printResult(200, map[string]any{"ok": true, "context": rt.activeContextName(), "api_key_cleared": all})
		},
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&all, "all", false, "clear API key as well")
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
					})
				}
				if rt.outputJSON {
					return rt.printJSON(rows)
				}
				printTable([]string{"NAME", "ACTIVE", "BASE_URL", "ORG", "WORKSPACE", "OWNER", "ENV", "API_KEY", "ACTOR", "BEARER"}, rows, []string{"name", "active", "base_url", "org_id", "workspace_id", "owner_agent", "environment", "has_api_key", "has_actor", "has_bearer"})
				return nil
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Show active context",
			RunE: func(cmd *cobra.Command, args []string) error {
				name := rt.activeContextName()
				c := rt.effectiveContext()
				return rt.printGeneric(map[string]any{
					"name":         name,
					"base_url":     c.BaseURL,
					"org_id":       c.OrgID,
					"workspace_id": c.WorkspaceID,
					"owner_agent":  c.OwnerAgent,
					"environment":  c.Environment,
					"has_api_key":  c.APIKey != "",
					"has_actor":    c.resolvedActorToken() != "",
					"has_bearer":   c.BearerToken != "",
				})
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
			rt.cfg.ActiveContext = name
			if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
				return err
			}
			return rt.printResult(200, map[string]any{"ok": true, "active_context": name})
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
			if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
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
				return &cliError{Code: 1, Msg: "failed to list inbox threads"}
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
	cmd := &cobra.Command{Use: "keys", Short: "Service-account keys operations"}
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
			ctx := rt.effectiveContext()
			if serviceAccountID != "" {
				status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts/"+serviceAccountID, nil, nil, true)
				if err != nil {
					return err
				}
				return rt.printResult(status, body)
			}

			resolvedOrgID := strings.TrimSpace(orgID)
			if resolvedOrgID == "" {
				resolvedOrgID = strings.TrimSpace(ctx.OrgID)
			}
			if resolvedOrgID == "" {
				return &cliError{Code: 2, Msg: "org_id is required to list service accounts"}
			}
			resolvedWorkspaceID := strings.TrimSpace(workspaceID)
			if resolvedWorkspaceID == "" {
				resolvedWorkspaceID = strings.TrimSpace(ctx.WorkspaceID)
			}
			query := map[string]string{"org_id": resolvedOrgID}
			if resolvedWorkspaceID != "" {
				query["workspace_id"] = resolvedWorkspaceID
			}
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts", query, nil, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
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
	var createdBy string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create service account",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			resolvedOrgID := strings.TrimSpace(orgID)
			if resolvedOrgID == "" {
				resolvedOrgID = strings.TrimSpace(ctx.OrgID)
			}
			resolvedWorkspaceID := strings.TrimSpace(workspaceID)
			if resolvedWorkspaceID == "" {
				resolvedWorkspaceID = strings.TrimSpace(ctx.WorkspaceID)
			}
			if resolvedOrgID == "" || resolvedWorkspaceID == "" {
				return &cliError{Code: 2, Msg: "org_id and workspace_id are required (flags or active context)"}
			}
			if strings.TrimSpace(name) == "" || strings.TrimSpace(createdBy) == "" {
				return &cliError{Code: 2, Msg: "--name and --created-by-actor-id are required"}
			}
			payload := map[string]any{
				"org_id":              resolvedOrgID,
				"workspace_id":        resolvedWorkspaceID,
				"name":                strings.TrimSpace(name),
				"created_by_actor_id": strings.TrimSpace(createdBy),
			}
			if strings.TrimSpace(description) != "" {
				payload["description"] = strings.TrimSpace(description)
			}
			status, body, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts", nil, payload, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringVar(&orgID, "org-id", "", "organization id (defaults to context org_id)")
	cmd.Flags().StringVar(&workspaceID, "workspace-id", "", "workspace id (defaults to context workspace_id)")
	cmd.Flags().StringVar(&name, "name", "", "service account name")
	cmd.Flags().StringVar(&description, "description", "", "service account description")
	cmd.Flags().StringVar(&createdBy, "created-by-actor-id", "", "actor id creating service account")
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
	var serviceAccountID, createdBy, expiresAt string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create service-account key",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serviceAccountID == "" || createdBy == "" {
				return &cliError{Code: 2, Msg: "--service-account-id and --created-by-actor-id are required"}
			}
			payload := map[string]any{"created_by_actor_id": createdBy}
			if expiresAt != "" {
				payload["expires_at"] = expiresAt
			}
			status, body, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "POST", "/v1/service-accounts/"+serviceAccountID+"/keys", nil, payload, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "service account id")
	cmd.Flags().StringVar(&createdBy, "created-by-actor-id", "", "actor id creating key")
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
			status, body, _, err := rt.request(cmd.Context(), rt.effectiveContext(), "POST", "/v1/service-accounts/"+serviceAccountID+"/keys/"+keyID+"/revoke", nil, nil, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "service account id")
	cmd.Flags().StringVar(&keyID, "key-id", "", "key id")
	return cmd
}

func newKeysListCmd(rt *runtime) *cobra.Command {
	var serviceAccountID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List service-account keys (or service accounts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			if serviceAccountID != "" {
				status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts/"+serviceAccountID, nil, nil, true)
				if err != nil {
					return err
				}
				return rt.printResult(status, body)
			}
			if ctx.OrgID == "" {
				return &cliError{Code: 2, Msg: "org_id is required to list service accounts (or pass --service-account-id)"}
			}
			query := map[string]string{"org_id": ctx.OrgID}
			if ctx.WorkspaceID != "" {
				query["workspace_id"] = ctx.WorkspaceID
			}
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts", query, nil, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
	cmd.Flags().StringVar(&serviceAccountID, "service-account-id", "", "specific service account id")
	return cmd
}

func newKeysCreateCmd(rt *runtime) *cobra.Command {
	return newServiceAccountKeysCreateCmd(rt)
}

func newKeysRevokeCmd(rt *runtime) *cobra.Command {
	return newServiceAccountKeysRevokeCmd(rt)
}

func newStatusCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Health and connectivity status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/health", nil, nil, true)
			if err != nil {
				return err
			}
			return rt.printResult(status, body)
		},
	}
}

func newDoctorCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Configuration and API diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			checks := []map[string]any{
				{"check": "config_file", "ok": fileExists(rt.cfgFile), "detail": rt.cfgFile},
				{"check": "base_url", "ok": ctx.BaseURL != "", "detail": ctx.BaseURL},
				{"check": "api_key_or_actor", "ok": ctx.APIKey != "" || ctx.resolvedActorToken() != "", "detail": "credentials present"},
			}
			_, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/health", nil, nil, true)
			checks = append(checks, map[string]any{"check": "health_endpoint", "ok": err == nil, "detail": body})
			if rt.outputJSON {
				return rt.printJSON(checks)
			}
			printTable([]string{"CHECK", "OK", "DETAIL"}, checks, []string{"check", "ok", "detail"})
			return nil
		},
	}
}

func newVersionCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			return rt.printGeneric(map[string]any{"version": version, "commit": commit, "date": date})
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

func (rt *runtime) hydrateContextFromServer(ctx context.Context, c *clientConfig) (map[string]any, error) {
	status, body, raw, err := rt.request(ctx, c, "GET", "/v1/portal/personal/context", nil, nil, true)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("context resolution returned %d: %s", status, raw)
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

func (rt *runtime) effectiveContext() *clientConfig {
	active := rt.ensureContext(rt.activeContextName())
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
	return &merged
}

func (rt *runtime) ensureContext(name string) *clientConfig {
	c, ok := rt.cfg.Contexts[name]
	if ok {
		c.normalizeActorToken()
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
	raw, err := json.MarshalIndent(cfg, "", "  ")
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
