package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const defaultMeshDashboardURL = "https://mesh.axme.ai"

// resolveDashboardURL picks the dashboard URL using this precedence:
//  1. explicit --dashboard-url flag (if non-empty and not equal to default)
//  2. AXME_MESH_DASHBOARD_URL env var
//  3. context-aware default: if gateway base_url looks like staging, the
//     hardcoded prod URL will produce token mismatches — return empty string
//     so the caller fails fast with a clear message
//  4. defaultMeshDashboardURL (prod)
func resolveDashboardURL(flagValue, gatewayBaseURL string) (string, error) {
	if flagValue != "" && flagValue != defaultMeshDashboardURL {
		return flagValue, nil
	}
	if envURL := strings.TrimSpace(os.Getenv("AXME_MESH_DASHBOARD_URL")); envURL != "" {
		return envURL, nil
	}
	// Detect non-prod gateway: if the gateway is not api.cloud.axme.ai, the
	// hardcoded prod dashboard URL will fail token exchange (token lives in a
	// different environment's database). Refuse to open the wrong dashboard.
	if gatewayBaseURL != "" && !strings.Contains(gatewayBaseURL, "api.cloud.axme.ai") {
		return "", fmt.Errorf(
			"current gateway is %q (non-prod). The default mesh dashboard at %s "+
				"only works with the prod gateway. Set AXME_MESH_DASHBOARD_URL to a dashboard "+
				"deployment connected to your gateway, or pass --dashboard-url",
			gatewayBaseURL, defaultMeshDashboardURL,
		)
	}
	return defaultMeshDashboardURL, nil
}

func newMeshCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mesh",
		Short: "Agent Mesh - monitor and control your AI agents",
	}
	cmd.AddCommand(newMeshDashboardCmd(rt))
	return cmd
}

func newMeshDashboardCmd(rt *runtime) *cobra.Command {
	var noBrowser bool
	var dashboardURL string

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the Agent Mesh dashboard in your browser",
		Long: `Opens the Agent Mesh dashboard and signs you in automatically.

The command creates a one-time exchange token using your API key,
then opens the dashboard in your default browser. The token is
valid for 5 minutes and can only be used once.

Dashboard URL precedence:
  1. --dashboard-url flag
  2. AXME_MESH_DASHBOARD_URL environment variable
  3. https://mesh.axme.ai (default, prod-only)

Non-prod gateways (e.g. staging) will refuse to open the default URL because
the token would be created on the staging backend but the prod dashboard
would try to exchange it against the prod backend, producing
"Invalid exchange token". Use --dashboard-url or AXME_MESH_DASHBOARD_URL.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := rt.effectiveContext()
			if c.APIKey == "" {
				return &cliError{Code: 2, Msg: "no API key configured. Run 'axme login' first."}
			}

			// Resolve dashboard URL BEFORE creating the token, so we fail fast
			// when the gateway is non-prod and no override is provided.
			resolvedDashboardURL, urlErr := resolveDashboardURL(dashboardURL, c.BaseURL)
			if urlErr != nil {
				return &cliError{Code: 2, Msg: urlErr.Error()}
			}

			// Create exchange token
			fmt.Fprintf(os.Stderr, "Creating dashboard token...\n")
			status, body, _, err := rt.doRequest(
				context.Background(), c,
				"POST", "/v1/auth/dashboard-token",
				nil, nil, true,
			)
			if err != nil {
				return fmt.Errorf("failed to create dashboard token: %w", err)
			}
			if status != 200 {
				detail := ""
				if d, ok := body["detail"]; ok {
					detail = fmt.Sprintf(": %v", d)
				}
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to create dashboard token (HTTP %d)%s", status, detail)}
			}

			token, ok := body["token"].(string)
			if !ok || token == "" {
				return &cliError{Code: 1, Msg: "server returned empty token"}
			}

			exchangeURL := fmt.Sprintf("%s/auth/exchange?token=%s", resolvedDashboardURL, token)

			if rt.outputJSON {
				rt.printJSON(map[string]any{
					"ok":           true,
					"exchange_url": exchangeURL,
					"expires_in":   body["expires_in"],
				})
				return nil
			}

			if noBrowser {
				fmt.Fprintf(os.Stderr, "\nOpen this URL in your browser:\n\n  %s\n\n", exchangeURL)
				fmt.Fprintf(os.Stderr, "Token expires in 5 minutes.\n")
				return nil
			}

			fmt.Fprintf(os.Stderr, "Opening dashboard in browser...\n")
			if err := openURLInBrowser(exchangeURL); err != nil {
				fmt.Fprintf(os.Stderr, "Could not open browser automatically.\n\nOpen this URL manually:\n\n  %s\n\n", exchangeURL)
				fmt.Fprintf(os.Stderr, "Token expires in 5 minutes.\n")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print URL instead of opening browser")
	cmd.Flags().StringVar(&dashboardURL, "dashboard-url", defaultMeshDashboardURL, "dashboard base URL")
	_ = cmd.Flags().MarkHidden("dashboard-url")

	return cmd
}
