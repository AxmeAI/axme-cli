package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// runEmailLogin implements the email-first passwordless login flow:
//
//  1. Prompt user for email address
//  2. POST /v1/auth/login-intent  → intent_id
//  3. Prompt user to enter the 6-digit OTP from their inbox
//  4. POST /v1/auth/login-intent/{id}/verify { code }
//  5. Store api_key + account_session_token + org/workspace
func (rt *runtime) runEmailLogin(ctx context.Context, ctxName string) error {
	c := rt.ensureContext(ctxName)

	// Prompt for email
	email, err := rt.promptEmail()
	if err != nil {
		return err
	}

	if !rt.outputJSON {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  Sending sign-in code to "+email+"...")
		fmt.Fprintln(os.Stderr)
	}

	// Create login intent
	status, body, raw, err := rt.request(ctx, c, "POST", "/v1/auth/login-intent",
		nil, map[string]any{"email": email}, true)
	if err != nil {
		return fmt.Errorf("login: could not send sign-in code: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("login: server rejected request (%d): %s", status, raw)
	}

	intentID := asString(body["intent_id"])
	expiresIn := int(asFloat(body["expires_in"]))
	if expiresIn <= 0 {
		expiresIn = 300
	}

	if rt.outputJSON {
		_ = rt.printJSON(map[string]any{
			"ok":        true,
			"intent_id": intentID,
			"message":   "check your email for a 6-digit code and enter it at the prompt",
		})
	} else {
		fmt.Fprintln(os.Stderr, "  Code sent! Check your inbox.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "  The code expires in %ds. Enter it below.\n", expiresIn)
		fmt.Fprintln(os.Stderr)
	}

	// Prompt for OTP code
	otp, err := rt.promptOTP()
	if err != nil {
		return err
	}

	if !rt.outputJSON {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  Verifying code...")
	}

	// Verify OTP
	verifyStatus, verifyBody, verifyRaw, verifyErr := rt.request(
		ctx, c,
		"POST", "/v1/auth/login-intent/"+intentID+"/verify",
		nil, map[string]any{"code": otp}, true,
	)
	if verifyErr != nil {
		return fmt.Errorf("login: could not verify code: %w", verifyErr)
	}
	switch verifyStatus {
	case 410:
		return fmt.Errorf("login: the sign-in code has expired — run `axme login` again")
	case 422:
		detail := asString(verifyBody["detail"])
		if detail == "" {
			detail = string(verifyRaw)
		}
		return fmt.Errorf("login: invalid code — %s", detail)
	case 409:
		return fmt.Errorf("login: code already used — run `axme login` again")
	}
	if verifyStatus >= 400 {
		return fmt.Errorf("login: verification failed (%d): %s", verifyStatus, verifyRaw)
	}

	retrievedKey := asString(verifyBody["api_key"])
	if retrievedKey == "" {
		return fmt.Errorf("login: sign-in succeeded but no api_key returned")
	}
	c.APIKey = retrievedKey
	if accountSessionToken := asString(verifyBody["account_session_token"]); accountSessionToken != "" {
		c.setActorToken(accountSessionToken)
	}
	if refreshToken := asString(verifyBody["refresh_token"]); refreshToken != "" {
		c.RefreshToken = refreshToken
	}
	if orgID := asString(verifyBody["org_id"]); orgID != "" {
		c.OrgID = orgID
	}
	if wsID := asString(verifyBody["workspace_id"]); wsID != "" {
		c.WorkspaceID = wsID
	}

	// Hydrate context from server if needed
	var hydrated bool
	if c.OrgID == "" || c.WorkspaceID == "" {
		if resolved, hErr := rt.hydrateContextFromServer(ctx, c); hErr == nil {
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

	// Interactive workspace selection when the user has multiple visible workspaces
	// and no workspace was auto-selected by the server.
	if !rt.outputJSON && c.resolvedActorToken() != "" && (c.OrgID == "" || c.WorkspaceID == "") {
		if personalCtx, pErr := rt.personalContextFromServer(ctx, c); pErr == nil {
			workspaces := asSlice(personalCtx["workspaces"])
			selectedWS := asMap(personalCtx["selected_workspace"])
			switch {
			case len(workspaces) == 0:
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, "  No workspaces found in your account. Contact support or run `axme quota upgrade-request`.")
			case len(workspaces) == 1 && len(selectedWS) == 0:
				// Auto-select the only workspace
				ws := asMap(workspaces[0])
				wsID := asString(ws["workspace_id"])
				wsOrgID := asString(ws["org_id"])
				wsName := asString(ws["name"])
				if wsName == "" {
					wsName = wsID
				}
				payload := map[string]any{"org_id": wsOrgID, "workspace_id": wsID}
				if selStatus, selBody, _, selErr := rt.request(ctx, c, "POST", "/v1/portal/personal/workspace-selection", nil, payload, true); selErr == nil && selStatus < 400 {
					selCtx := asMap(selBody["context"])
					if v := asString(selCtx["org_id"]); v != "" {
						c.OrgID = v
					}
					if v := asString(selCtx["workspace_id"]); v != "" {
						c.WorkspaceID = v
					}
					hydrated = true
					fmt.Fprintf(os.Stderr, "  Auto-selected workspace: %s\n", wsName)
				}
			case len(workspaces) > 1 && len(selectedWS) == 0:
				// Prompt user to choose
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, "  Multiple workspaces available. Choose one to set as active:")
				for i, item := range workspaces {
					ws := asMap(item)
					name := asString(ws["name"])
					if name == "" {
						name = asString(ws["workspace_id"])
					}
					fmt.Fprintf(os.Stderr, "    [%d] %s  (%s)\n", i+1, name, asString(ws["workspace_id"]))
				}
				fmt.Fprintln(os.Stderr)
				fmt.Fprint(os.Stderr, "  Enter number (or press Enter to skip): ")
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				choice := strings.TrimSpace(line)
				if choice != "" {
					idx := 0
					fmt.Sscanf(choice, "%d", &idx)
					if idx >= 1 && idx <= len(workspaces) {
						ws := asMap(workspaces[idx-1])
						wsID := asString(ws["workspace_id"])
						wsOrgID := asString(ws["org_id"])
						wsName := asString(ws["name"])
						if wsName == "" {
							wsName = wsID
						}
						payload := map[string]any{"org_id": wsOrgID, "workspace_id": wsID}
						if selStatus, selBody, _, selErr := rt.request(ctx, c, "POST", "/v1/portal/personal/workspace-selection", nil, payload, true); selErr == nil && selStatus < 400 {
							selCtx := asMap(selBody["context"])
							if v := asString(selCtx["org_id"]); v != "" {
								c.OrgID = v
							}
							if v := asString(selCtx["workspace_id"]); v != "" {
								c.WorkspaceID = v
							}
							hydrated = true
							fmt.Fprintf(os.Stderr, "  Active workspace set to: %s\n", wsName)
						}
					} else {
						fmt.Fprintln(os.Stderr, "  Invalid choice — skipped. Run `axme workspace use <id>` to set it later.")
					}
				} else {
					fmt.Fprintln(os.Stderr, "  Skipped. Run `axme workspace list` then `axme workspace use <id>` to set it.")
				}
			}
		}
	}

	rt.cfg.LastLoginEmail = email
	if err := rt.persistConfig(); err != nil {
		return err
	}

	summary := rt.deviceLoginSummary(ctx, c, ctxName, hydrated)
	if rt.outputJSON {
		return rt.printJSON(summary)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  Signed in. Credentials saved to context %q.\n", ctxName)
	if c.OrgID != "" {
		fmt.Fprintf(os.Stderr, "  org_id:       %s\n", c.OrgID)
	}
	if c.WorkspaceID != "" {
		fmt.Fprintf(os.Stderr, "  workspace_id: %s\n", c.WorkspaceID)
	}
	if c.resolvedActorToken() != "" {
		fmt.Fprintln(os.Stderr, "  account session: active")
	}
	if membershipCount, ok := summary["membership_count"].(int); ok && membershipCount > 0 {
		fmt.Fprintf(os.Stderr, "  visible workspaces: %d\n", membershipCount)
	}
	if organizationsCount, ok := summary["organization_count"].(int); ok && organizationsCount > 0 {
		fmt.Fprintf(os.Stderr, "  visible organizations: %d\n", organizationsCount)
	}
	if selectedOrg := asMap(summary["selected_organization"]); len(selectedOrg) > 0 {
		label := asString(selectedOrg["name"])
		if label == "" {
			label = asString(selectedOrg["org_id"])
		}
		if label != "" {
			fmt.Fprintf(os.Stderr, "  selected organization: %s\n", label)
		}
	}
	if selectedWS := asMap(summary["selected_workspace"]); len(selectedWS) > 0 {
		label := asString(selectedWS["name"])
		if label == "" {
			label = asString(selectedWS["workspace_id"])
		}
		if label != "" {
			fmt.Fprintf(os.Stderr, "  selected workspace: %s\n", label)
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
}

func (rt *runtime) promptEmail() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	if !rt.outputJSON && rt.cfg.LastLoginEmail != "" {
		fmt.Fprintf(os.Stderr, "  Use %s? [Y/n]: ", rt.cfg.LastLoginEmail)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("login: could not read input: %w", err)
		}
		answer := strings.TrimSpace(line)
		if answer == "" || strings.EqualFold(answer, "y") {
			return rt.cfg.LastLoginEmail, nil
		}
		// User typed something else — treat as a new email if it looks like one
		if strings.Contains(answer, "@") {
			return answer, nil
		}
		// Otherwise fall through to a fresh prompt
	}
	if !rt.outputJSON {
		fmt.Fprint(os.Stderr, "  Enter your email address: ")
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("login: could not read email: %w", err)
	}
	email := strings.TrimSpace(line)
	if email == "" {
		return "", fmt.Errorf("login: email is required")
	}
	if !strings.Contains(email, "@") {
		return "", fmt.Errorf("login: %q does not look like a valid email address", email)
	}
	return email, nil
}

func (rt *runtime) promptOTP() (string, error) {
	if !rt.outputJSON {
		fmt.Fprint(os.Stderr, "  Enter the 6-digit code from your email: ")
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("login: could not read verification code: %w", err)
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return "", fmt.Errorf("login: verification code is required")
	}
	return code, nil
}
