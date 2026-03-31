package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const defaultMeshDashboardURL = "https://mesh.axme.ai"

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
valid for 5 minutes and can only be used once.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := rt.effectiveContext()
			if c.APIKey == "" {
				return &cliError{Code: 2, Msg: "no API key configured. Run 'axme login' first."}
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

			exchangeURL := fmt.Sprintf("%s/auth/exchange?token=%s", dashboardURL, token)

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
