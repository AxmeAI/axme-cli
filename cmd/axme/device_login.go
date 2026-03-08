package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const deviceLoginApprovalBase = "https://cloud.axme.ai/app/login/cli"

// runDeviceLogin implements the browser/device login flow:
//  1. Create a CLI grant on the server → get grant_id + user_code
//  2. Open browser to approval URL
//  3. Poll server until approved or expired
//  4. Store retrieved api_key + context in config
func (rt *runtime) runDeviceLogin(ctx context.Context, ctxName string) error {
	c := rt.ensureContext(ctxName)

	if !rt.outputJSON {
		fmt.Fprintln(os.Stderr, "Starting browser login flow...")
	}

	// Step 1: Create grant
	status, body, raw, err := rt.request(ctx, c, "POST", "/v1/auth/cli-grants", nil, nil, false)
	if err != nil {
		return fmt.Errorf("device login: could not create grant: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("device login: grant creation failed (%d): %s", status, raw)
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
		fmt.Fprintf(os.Stderr, "\n  Opening browser...\n")
		fmt.Fprintf(os.Stderr, "  Approval URL: %s\n\n", approvalURL)
		fmt.Fprintf(os.Stderr, "  Confirm this code matches what you see in the browser:\n")
		fmt.Fprintf(os.Stderr, "\n      %s\n\n", userCode)
		fmt.Fprintf(os.Stderr, "  Waiting for approval (max %ds)...\n", expiresIn)
	}

	// Open browser
	if !rt.outputJSON {
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
				return fmt.Errorf("device login: timed out waiting for browser approval")
			}

			pollStatus, pollBody, pollRaw, pollErr := rt.request(ctx, c, "GET", "/v1/auth/cli-grants/"+grantID, nil, nil, false)
			if pollErr != nil {
				if !rt.outputJSON {
					fmt.Fprint(os.Stderr, ".")
					dotPrinted++
				}
				continue
			}
			if pollStatus == 410 {
				return fmt.Errorf("device login: grant expired before approval")
			}
			if pollStatus >= 400 {
				return fmt.Errorf("device login: poll error (%d): %s", pollStatus, pollRaw)
			}

			state := asString(pollBody["state"])
			switch state {
			case "approved":
				if !rt.outputJSON && dotPrinted > 0 {
					fmt.Fprintln(os.Stderr)
				}
				retrievedKey := asString(pollBody["api_key"])
				if retrievedKey == "" {
					return fmt.Errorf("device login: grant approved but no api_key returned")
				}
				c.APIKey = retrievedKey
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
				if err := saveConfig(rt.cfgFile, rt.cfg); err != nil {
					return err
				}
				if rt.outputJSON {
					return rt.printJSON(map[string]any{
						"ok":           true,
						"context":      ctxName,
						"hydrated":     hydrated,
						"org_id":       c.OrgID,
						"workspace_id": c.WorkspaceID,
					})
				}
				fmt.Fprintf(os.Stderr, "\n  Login approved. Credentials saved to context %q.\n", ctxName)
				if c.OrgID != "" {
					fmt.Fprintf(os.Stderr, "  org_id:       %s\n", c.OrgID)
				}
				if c.WorkspaceID != "" {
					fmt.Fprintf(os.Stderr, "  workspace_id: %s\n", c.WorkspaceID)
				}
				fmt.Fprintln(os.Stderr)
				return nil
			case "expired":
				return fmt.Errorf("device login: grant expired")
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
