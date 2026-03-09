package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const deviceLoginApprovalBase = "https://cloud.axme.ai/app/login/cli"

// runDeviceLogin implements the default browser-based account sign-in flow:
//  1. Create a CLI grant on the server → get grant_id + user_code
//  2. Open browser to approval URL
//  3. Poll server until approved or expired
//  4. Store retrieved api_key + context in config
func (rt *runtime) runDeviceLogin(ctx context.Context, ctxName string, openBrowser bool) error {
	c := rt.ensureContext(ctxName)

	if !rt.outputJSON {
		fmt.Fprintln(os.Stderr, "Starting account sign-in flow...")
	}

	// Step 1: Create grant
	status, body, raw, err := rt.request(ctx, c, "POST", "/v1/auth/cli-grants", nil, nil, true)
	if err != nil {
		return fmt.Errorf("account login: could not create grant: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("account login: grant creation failed (%d): %s", status, raw)
	}

	grantID := asString(body["grant_id"])
	userCode := asString(body["user_code"])
	approvalURL := asString(body["approval_url"])
	pollInterval := int(asFloat(body["poll_interval"]))
	expiresIn := int(asFloat(body["expires_in"]))
	if pollInterval <= 0 {
		pollInterval = 3
	}
	if expiresIn <= 0 {
		expiresIn = 600
	}
	if approvalURL == "" {
		approvalURL = fmt.Sprintf("%s?code=%s&grant=%s", deviceLoginApprovalBase, userCode, grantID)
	} else {
		// Append grant_id so approval page can use it
		approvalURL = fmt.Sprintf("%s&grant=%s", approvalURL, grantID)
	}

	if rt.outputJSON {
		_ = rt.printJSON(map[string]any{
			"ok":           true,
			"grant_id":     grantID,
			"user_code":    userCode,
			"approval_url": approvalURL,
			"expires_in":   expiresIn,
			"message":      "open approval_url in browser and complete login there",
		})
	} else {
		if openBrowser {
			fmt.Fprintf(os.Stderr, "\n  Opening browser...\n")
		} else {
			fmt.Fprintf(os.Stderr, "\n  Browser auto-open disabled.\n")
		}
		fmt.Fprintf(os.Stderr, "  Approval URL: %s\n\n", approvalURL)
		fmt.Fprintf(os.Stderr, "  Confirm this code matches what you see in the browser:\n")
		fmt.Fprintf(os.Stderr, "\n      %s\n\n", userCode)
		fmt.Fprintf(os.Stderr, "  Waiting for approval (max %ds)...\n", expiresIn)
	}

	// Open browser
	if !rt.outputJSON && openBrowser {
		if err := openURLInBrowser(approvalURL); err != nil {
			fmt.Fprintf(os.Stderr, "  (could not open browser automatically: %v)\n", err)
			fmt.Fprintf(os.Stderr, "  Please open the URL manually.\n")
		}
	}

	// Step 3: Poll until approved or expired
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	ticker := time.NewTicker(time.Duration(pollInterval) * time.Second)
	defer ticker.Stop()
	dotPrinted := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("account login: timed out waiting for browser approval")
			}

			pollStatus, pollBody, pollRaw, pollErr := rt.request(ctx, c, "GET", "/v1/auth/cli-grants/"+grantID, nil, nil, true)
			if pollErr != nil {
				if !rt.outputJSON {
					fmt.Fprint(os.Stderr, ".")
					dotPrinted++
				}
				continue
			}
			if pollStatus == 410 {
				return fmt.Errorf("account login: grant expired before approval")
			}
			if pollStatus >= 400 {
				return fmt.Errorf("account login: poll error (%d): %s", pollStatus, pollRaw)
			}

			state := asString(pollBody["state"])
			switch state {
			case "approved":
				if !rt.outputJSON && dotPrinted > 0 {
					fmt.Fprintln(os.Stderr)
				}
				retrievedKey := asString(pollBody["api_key"])
				if retrievedKey == "" {
					return fmt.Errorf("account login: grant approved but no api_key returned")
				}
				c.APIKey = retrievedKey
				if accountSessionToken := asString(pollBody["account_session_token"]); accountSessionToken != "" {
					c.setActorToken(accountSessionToken)
				}
				if orgID := asString(pollBody["org_id"]); orgID != "" {
					c.OrgID = orgID
				}
				if wsID := asString(pollBody["workspace_id"]); wsID != "" {
					c.WorkspaceID = wsID
				}
				// Hydrate org/workspace if not already set
				var hydrated bool
				if c.OrgID == "" || c.WorkspaceID == "" {
					if resolved, err := rt.hydrateContextFromServer(ctx, c); err == nil {
						hydrated = true
						if v := asString(resolved["org_id"]); v != "" {
							c.OrgID = v
						}
						if v := asString(resolved["workspace_id"]); v != "" {
							c.WorkspaceID = v
						}
					}
				} else {
					hydrated = true
				}
				if err := rt.persistConfig(); err != nil {
					return err
				}
				summary := rt.deviceLoginSummary(ctx, c, ctxName, hydrated)
				if rt.outputJSON {
					return rt.printJSON(summary)
				}
				fmt.Fprintf(os.Stderr, "\n  Login approved. Credentials saved to context %q.\n", ctxName)
				if c.OrgID != "" {
					fmt.Fprintf(os.Stderr, "  org_id:       %s\n", c.OrgID)
				}
				if c.WorkspaceID != "" {
					fmt.Fprintf(os.Stderr, "  workspace_id: %s\n", c.WorkspaceID)
				}
				if c.resolvedActorToken() != "" {
					fmt.Fprintln(os.Stderr, "  account session: available")
				}
				if membershipCount, ok := summary["membership_count"].(int); ok && membershipCount > 0 {
					fmt.Fprintf(os.Stderr, "  visible workspaces: %d\n", membershipCount)
				}
				if organizationsCount, ok := summary["organization_count"].(int); ok && organizationsCount > 0 {
					fmt.Fprintf(os.Stderr, "  visible organizations: %d\n", organizationsCount)
				}
				if selectedOrg := asMap(summary["selected_organization"]); len(selectedOrg) > 0 {
					selectedOrgLabel := asString(selectedOrg["name"])
					if selectedOrgLabel == "" {
						selectedOrgLabel = asString(selectedOrg["org_id"])
					}
					if selectedOrgLabel != "" {
						fmt.Fprintf(os.Stderr, "  selected organization: %s\n", selectedOrgLabel)
					}
				}
				if selectedWorkspace := asMap(summary["selected_workspace"]); len(selectedWorkspace) > 0 {
					selectedWorkspaceLabel := asString(selectedWorkspace["name"])
					if selectedWorkspaceLabel == "" {
						selectedWorkspaceLabel = asString(selectedWorkspace["workspace_id"])
					}
					if selectedWorkspaceLabel != "" {
						fmt.Fprintf(os.Stderr, "  selected workspace: %s\n", selectedWorkspaceLabel)
					}
				}
				if warning := asString(summary["server_context_warning"]); warning != "" {
					fmt.Fprintf(os.Stderr, "  server context warning: %s\n", warning)
				}
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, "  Next commands:")
				fmt.Fprintln(os.Stderr, "    axme whoami")
				fmt.Fprintln(os.Stderr, "    axme workspace list")
				fmt.Fprintln(os.Stderr)
				return nil
			case "expired":
				return fmt.Errorf("account login: grant expired")
			default:
				// still pending
				if !rt.outputJSON {
					fmt.Fprint(os.Stderr, ".")
					dotPrinted++
				}
			}
		}
	}
}

func (rt *runtime) deviceLoginSummary(ctx context.Context, c *clientConfig, ctxName string, hydrated bool) map[string]any {
	summary := map[string]any{
		"ok":                  true,
		"context":             ctxName,
		"hydrated":            hydrated,
		"org_id":              c.OrgID,
		"workspace_id":        c.WorkspaceID,
		"has_account_session": c.resolvedActorToken() != "",
	}
	if c.resolvedActorToken() == "" {
		return summary
	}
	personalContext, err := rt.personalContextFromServer(ctx, c)
	if err != nil {
		summary["server_context_warning"] = err.Error()
		return summary
	}
	for k, v := range personalContextSummary(personalContext) {
		summary[k] = v
	}
	return summary
}

func asFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case json.Number:
		f, _ := val.Float64()
		return f
	case int:
		return float64(val)
	case int64:
		return float64(val)
	}
	return 0
}
