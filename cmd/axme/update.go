package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	updateCheckRepo        = "AxmeAI/axme-cli"
	updateCheckCacheFile   = "update_check.json"
	updateCheckInterval    = 24 * time.Hour
	updateCheckTimeout     = 3 * time.Second
	updateCheckDisabledEnv = "AXME_NO_UPDATE_CHECK"
)

type updateCheckCache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
	LatestURL string    `json:"latest_url"`
}

// startBackgroundUpdateCheck launches a goroutine that checks for a newer CLI
// release on GitHub and returns a channel that will receive a non-empty hint
// string when a newer version is found. The channel is closed when done.
// Returns nil if the check is disabled or the current version is "dev".
func startBackgroundUpdateCheck() <-chan string {
	if version == "dev" {
		return nil
	}
	if os.Getenv(updateCheckDisabledEnv) != "" {
		return nil
	}

	ch := make(chan string, 1)
	go func() {
		defer close(ch)
		hint := checkForUpdate()
		if hint != "" {
			ch <- hint
		}
	}()
	return ch
}

// checkForUpdate consults a local cache and, if stale, queries GitHub releases.
// Returns a human-readable hint string if a newer version is available.
func checkForUpdate() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cacheDir := filepath.Join(home, ".config", "axme")
	cachePath := filepath.Join(cacheDir, updateCheckCacheFile)

	var cache updateCheckCache
	if data, readErr := os.ReadFile(cachePath); readErr == nil {
		_ = json.Unmarshal(data, &cache)
	}

	latestTag := cache.LatestTag
	latestURL := cache.LatestURL

	if latestTag == "" || time.Since(cache.CheckedAt) > updateCheckInterval {
		tag, url, fetchErr := fetchLatestRelease()
		if fetchErr != nil {
			return ""
		}
		latestTag = tag
		latestURL = url
		newCache := updateCheckCache{
			CheckedAt: time.Now(),
			LatestTag: latestTag,
			LatestURL: latestURL,
		}
		if b, marshalErr := json.MarshalIndent(newCache, "", "  "); marshalErr == nil {
			_ = os.WriteFile(cachePath, b, 0o600)
		}
	}

	if latestTag == "" {
		return ""
	}

	currentNorm := strings.TrimPrefix(version, "v")
	latestNorm := strings.TrimPrefix(latestTag, "v")
	if currentNorm == latestNorm {
		return ""
	}
	if semverGreater(latestNorm, currentNorm) {
		return fmt.Sprintf(
			"A newer version of axme is available: %s → %s\nRun to upgrade: curl -fsSL https://raw.githubusercontent.com/AxmeAI/axme-cli/main/install.sh | sh\nRelease notes: %s",
			version, latestTag, latestURL,
		)
	}
	return ""
}

func fetchLatestRelease() (tag string, htmlURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()

	apiURL := "https://api.github.com/repos/" + updateCheckRepo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "axme-cli/"+version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github api: %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&payload); decodeErr != nil {
		return "", "", decodeErr
	}
	return payload.TagName, payload.HTMLURL, nil
}

// semverGreater returns true if a > b (simple three-part numeric comparison).
func semverGreater(a, b string) bool {
	ap := parseSemver(a)
	bp := parseSemver(b)
	for i := range ap {
		if i >= len(bp) {
			return true
		}
		if ap[i] > bp[i] {
			return true
		}
		if ap[i] < bp[i] {
			return false
		}
	}
	return false
}

func parseSemver(s string) []int {
	parts := strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3)
	nums := make([]int, len(parts))
	for i, p := range parts {
		p = strings.SplitN(p, "-", 2)[0]
		for _, c := range p {
			if c >= '0' && c <= '9' {
				nums[i] = nums[i]*10 + int(c-'0')
			} else {
				break
			}
		}
	}
	return nums
}

// printUpdateHint drains the channel from startBackgroundUpdateCheck and prints
// a hint to stderr if a newer version was found. Should be called right before
// os.Exit so output appears after command output.
func printUpdateHint(ch <-chan string) {
	if ch == nil {
		return
	}
	// Give the background goroutine a short window to finish. For fast commands
	// (e.g. `axme version`) the HTTP check may still be in flight; waiting up to
	// 500ms avoids silently dropping the hint while keeping startup latency low.
	select {
	case hint, ok := <-ch:
		if ok && hint != "" {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "╭─────────────────────────────────────────────────╮")
			for _, line := range strings.Split(hint, "\n") {
				fmt.Fprintf(os.Stderr, "│ %s\n", line)
			}
			fmt.Fprintln(os.Stderr, "╰─────────────────────────────────────────────────╯")
		}
	case <-time.After(500 * time.Millisecond):
		// Background check still running — skip hint to avoid blocking the user
	}
}

// newUpdateCmd returns a command that upgrades axme to the latest release via
// the official install script.
func newUpdateCmd(_ *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "update",
		Short:         "Upgrade axme CLI to the latest release",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Download and install the latest axme CLI release using the official install script.

Equivalent to:
  curl -fsSL https://raw.githubusercontent.com/AxmeAI/axme-cli/main/install.sh | sh

Set AXME_NO_UPDATE_CHECK=1 to suppress automatic update notifications.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(os.Stderr, "Checking latest release...")
			tag, releaseURL, err := fetchLatestRelease()
			if err != nil {
				return fmt.Errorf("could not check latest release: %w", err)
			}

			currentNorm := strings.TrimPrefix(version, "v")
			latestNorm := strings.TrimPrefix(tag, "v")
			if version != "dev" && currentNorm == latestNorm {
				fmt.Printf("axme is already up to date (%s).\n", version)
				return nil
			}
			if version != "dev" && !semverGreater(latestNorm, currentNorm) {
				fmt.Printf("axme %s is at or ahead of latest release (%s).\n", version, tag)
				return nil
			}

			fmt.Printf("Upgrading axme %s → %s\n", version, tag)
			fmt.Printf("Release notes: %s\n\n", releaseURL)

			// Fetch and pipe install script through sh
			scriptURL := "https://raw.githubusercontent.com/" + updateCheckRepo + "/main/install.sh"
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scriptURL, nil)
			resp, httpErr := http.DefaultClient.Do(req)
			if httpErr != nil {
				return fmt.Errorf("could not download install script: %w", httpErr)
			}
			defer resp.Body.Close()

			shCmd := exec.CommandContext(ctx, "sh")
			shCmd.Stdin = resp.Body
			shCmd.Stdout = os.Stdout
			shCmd.Stderr = os.Stderr
			if runErr := shCmd.Run(); runErr != nil {
				return fmt.Errorf("install script failed: %w", runErr)
			}
			return nil
		},
	}
}
