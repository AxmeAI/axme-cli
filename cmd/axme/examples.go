package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Example catalog — each entry is a self-contained demo scenario.
// Agent logic is implemented in Go so `axme examples run` needs zero
// external dependencies (no Python, no subprocess).
// ---------------------------------------------------------------------------

type agentDef struct {
	NameFragment string // to find API key in scenario-agents.json
	Addr         string // AXME_AGENT_ADDRESS bare name
	Handler      func(payload map[string]interface{}) map[string]interface{}
}

type exampleEntry struct {
	ID          string     // e.g. "delivery/stream"
	Title       string
	Description string
	Agents      []agentDef // zero = no agent (pure human / timeout)
}

// Shared agent handlers
func complianceHandler(p map[string]interface{}) map[string]interface{} {
	env, _ := p["environment"].(string)
	risk, _ := p["risk_level"].(string)
	passed := env == "staging" && risk != "critical"
	return map[string]interface{}{
		"action": boolStr(passed, "complete", "fail"),
		"passed": passed,
		"reason": fmt.Sprintf("compliance %s (env=%s, risk=%s)", boolStr(passed, "passed", "failed"), env, risk),
	}
}

func riskAssessmentHandler(p map[string]interface{}) map[string]interface{} {
	riskLevel, _ := p["risk_level"].(string)
	threshold, _ := p["risk_threshold"].(float64)
	scores := map[string]float64{"low": 10, "medium": 25, "high": 45, "critical": 80}
	score := scores[riskLevel]
	if score == 0 {
		score = 20
	}
	return map[string]interface{}{
		"action":     boolStr(score <= threshold || threshold == 0, "complete", "fail"),
		"risk_score": score,
		"risk_level": strings.ToUpper(riskLevel),
	}
}

var exampleCatalog = []exampleEntry{
	{
		ID:          "delivery/stream",
		Title:       "Compliance Checker — SSE stream delivery",
		Description: "Agent validates change compliance via SSE stream binding.",
		Agents: []agentDef{
			{NameFragment: "compliance-checker", Addr: "compliance-checker-agent", Handler: complianceHandler},
		},
	},
	{
		ID:          "human/cli",
		Title:       "Human approval via CLI — deployment readiness",
		Description: "Agent checks readiness, then workflow pauses for human approval via `axme tasks approve`.",
		Agents: []agentDef{
			{NameFragment: "deploy-readiness", Addr: "deploy-readiness-checker", Handler: func(p map[string]interface{}) map[string]interface{} {
				return map[string]interface{}{"action": "complete", "ready": true, "passed_checks": []string{"version", "environment"}}
			}},
		},
	},
	{
		ID:          "human/email",
		Title:       "Human approval via email — budget request",
		Description: "Agent validates budget envelope, then workflow sends approval email with magic link.",
		Agents: []agentDef{
			{NameFragment: "budget", Addr: "budget-envelope-validator", Handler: func(p map[string]interface{}) map[string]interface{} {
				amount, _ := p["amount"].(float64)
				category, _ := p["category"].(string)
				envelopes := map[string]float64{"cloud_infrastructure": 50000, "software_licenses": 20000}
				envelope := envelopes[category]
				return map[string]interface{}{"action": boolStr(amount <= envelope && envelope > 0, "complete", "fail"), "within_envelope": amount <= envelope}
			}},
		},
	},
	{
		ID:          "internal/timeout",
		Title:       "Step timeout — deadline enforcement",
		Description: "Step with 15s deadline, no agent responds. Scheduler enforces TIMED_OUT → FAILED.",
	},
	{
		ID:          "full/multi-agent",
		Title:       "Full multi-agent — compliance → risk → CAB approval",
		Description: "Three-step workflow: compliance agent, risk assessment agent, then human CAB sign-off via email.",
		Agents: []agentDef{
			{NameFragment: "compliance-checker", Addr: "compliance-checker-agent", Handler: complianceHandler},
			{NameFragment: "risk-assessment", Addr: "risk-assessment-agent", Handler: riskAssessmentHandler},
		},
	},
}

func boolStr(cond bool, t, f string) string {
	if cond {
		return t
	}
	return f
}

func newExamplesCmd(rt *runtime) *cobra.Command {
	examplesCmd := &cobra.Command{
		Use:   "examples",
		Short: "Run, list, or download AXME example scenarios",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all available examples",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println("  Available examples:")
			fmt.Println()
			for _, ex := range exampleCatalog {
				agentInfo := "no agent (server-side)"
				if len(ex.Agents) == 1 {
					agentInfo = "built-in Go agent"
				} else if len(ex.Agents) > 1 {
					agentInfo = fmt.Sprintf("%d built-in Go agents", len(ex.Agents))
				}
				fmt.Printf("  %-28s %s\n", ex.ID, ex.Title)
				fmt.Printf("  %-28s %s  [%s]\n", "", ex.Description, agentInfo)
				fmt.Println()
			}
			fmt.Println("  Run an example:")
			fmt.Println("    axme examples run delivery/stream")
			fmt.Println()
			return nil
		},
	}

	runCmd := &cobra.Command{
		Use:   "run <example-id>",
		Short: "Run an example end-to-end (provision + agent + scenario)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rt.runExample(cmd, args[0])
		},
	}

	examplesCmd.AddCommand(listCmd, runCmd)
	return examplesCmd
}

func (rt *runtime) runExample(cmd *cobra.Command, exampleID string) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel() // kills agent goroutines when example finishes

	var example *exampleEntry
	for i := range exampleCatalog {
		if exampleCatalog[i].ID == exampleID {
			example = &exampleCatalog[i]
			break
		}
	}
	if example == nil {
		return fmt.Errorf("unknown example: %q — run `axme examples list`", exampleID)
	}

	c := rt.effectiveContext()
	if c.APIKey == "" {
		return fmt.Errorf("not logged in — run `axme login` first")
	}

	fmt.Println()
	fmt.Println("  " + strings.Repeat("─", 70))
	fmt.Printf("  axme examples run  ·  %s\n", example.ID)
	fmt.Println("  " + strings.Repeat("─", 70))
	fmt.Printf("  %s\n", example.Title)
	fmt.Printf("  %s\n", example.Description)
	fmt.Println()

	// Step 1: Load embedded scenario
	fmt.Println("  [step 1]  Loading scenario...")
	scenarioJSON, ok := embeddedScenarios[example.ID]
	if !ok {
		return fmt.Errorf("no embedded scenario for %s", example.ID)
	}
	var scenario map[string]interface{}
	if err := json.Unmarshal([]byte(scenarioJSON), &scenario); err != nil {
		return fmt.Errorf("invalid embedded scenario: %w", err)
	}
	fmt.Println("            ✓ Scenario loaded")

	// Step 2: Apply scenario (provision agents + create intent)
	fmt.Println()
	fmt.Println("  [step 2]  Provisioning agents...")
	applyStatus, applyBody, _, applyErr := rt.request(ctx, c, "POST", "/v1/scenarios/apply",
		nil, scenario, true)
	if applyErr != nil {
		return fmt.Errorf("scenarios apply failed: %w", applyErr)
	}
	if applyStatus >= 400 {
		return fmt.Errorf("scenarios apply returned %d", applyStatus)
	}
	intentID := asString(applyBody["intent_id"])
	if intentID == "" {
		return fmt.Errorf("scenarios apply did not return intent_id")
	}
	fmt.Printf("            ✓ Intent created: %s\n", intentID)

	// Step 3: Start built-in Go agents
	if len(example.Agents) > 0 {
		fmt.Println()
		for i, agent := range example.Agents {
			fmt.Printf("  [step 3]  Starting agent %d/%d (%s)...\n", i+1, len(example.Agents), agent.Addr)
			go rt.runBuiltinAgent(ctx, c, &agent)
		}
		fmt.Printf("            ✓ %d agent(s) listening\n", len(example.Agents))
	}

	// Step 4: Watch intent lifecycle
	return watchIntentLive(rt, cmd, intentID, nil)
}

// runBuiltinAgent listens for intents via SSE stream and processes them
// using the agent's built-in Go handler. Runs in a goroutine.
func (rt *runtime) runBuiltinAgent(ctx context.Context, c *clientConfig, agent *agentDef) {
	agentKey := rt.loadAgentKey(agent.NameFragment)
	if agentKey == "" {
		fmt.Fprintf(os.Stderr, "\n  [agent] warning: no API key found for %s\n", agent.NameFragment)
		return
	}

	agentCtx := &clientConfig{
		BaseURL: c.BaseURL,
		APIKey:  agentKey,
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")

	// Skip past all old intents: quick SSE request with short timeout to find max cursor.
	// Uses a 3-second context deadline so it doesn't block on large backlogs.
	since := 0
	initCtx, initCancel := context.WithTimeout(ctx, 3*time.Second)
	initReq, _ := http.NewRequestWithContext(initCtx, "GET",
		fmt.Sprintf("%s/v1/agents/%s/intents/stream?since=0&wait_seconds=1", baseURL, agent.Addr), nil)
	if initReq != nil {
		initReq.Header.Set("X-Api-Key", agentKey)
		initReq.Header.Set("Accept", "text/event-stream")
		if initResp, initErr := rt.streamClient.Do(initReq); initErr == nil {
			initScanner := bufio.NewScanner(initResp.Body)
			for initScanner.Scan() {
				line := initScanner.Text()
				if strings.HasPrefix(line, "id: ") {
					if n, e := fmt.Sscanf(line, "id: %d", &since); n == 1 && e == nil {
						since++
					}
				}
			}
			initResp.Body.Close()
		}
	}
	initCancel()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Connect to SSE stream
		streamURL := fmt.Sprintf("%s/v1/agents/%s/intents/stream?since=%d&wait_seconds=5",
			baseURL, agent.Addr, since)
		req, err := http.NewRequestWithContext(ctx, "GET", streamURL, nil)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		req.Header.Set("X-Api-Key", agentKey)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := rt.streamClient.Do(req)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		// Parse SSE events — only process DELIVERED intents (skip old IN_PROGRESS/COMPLETED)
		scanner := bufio.NewScanner(resp.Body)
		var eventData string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				eventData = strings.TrimPrefix(line, "data: ")
			} else if strings.HasPrefix(line, "id: ") {
				if n, parseErr := fmt.Sscanf(line, "id: %d", &since); n == 1 && parseErr == nil {
					since++
				}
			} else if line == "" && eventData != "" {
				var delivery map[string]interface{}
				if json.Unmarshal([]byte(eventData), &delivery) == nil {
					iID := asString(delivery["intent_id"])
					status := strings.ToUpper(asString(delivery["status"]))
					// Only process fresh DELIVERED/CREATED intents, skip already-handled
					if iID != "" && (status == "DELIVERED" || status == "CREATED") {
						rt.processAgentIntent(ctx, agentCtx, agent, iID)
					}
				}
				eventData = ""
			}
		}
		resp.Body.Close()
	}
}

func (rt *runtime) processAgentIntent(ctx context.Context, agentCtx *clientConfig, agent *agentDef, intentID string) {
	_, intentBody, _, err := rt.request(ctx, agentCtx, "GET", "/v1/intents/"+intentID, nil, nil, true)
	if err != nil {
		return
	}
	intent, _ := intentBody["intent"].(map[string]interface{})
	if intent == nil {
		intent = intentBody
	}

	intentStatus := strings.ToUpper(asString(intent["lifecycle_status"]))
	if intentStatus != "" && intentStatus != "CREATED" && intentStatus != "DELIVERED" &&
		intentStatus != "ACKNOWLEDGED" && intentStatus != "IN_PROGRESS" && intentStatus != "WAITING" {
		return
	}

	rawPayload, _ := intent["payload"].(map[string]interface{})
	effectivePayload := rawPayload
	if pp, ok := rawPayload["parent_payload"].(map[string]interface{}); ok {
		effectivePayload = pp
	}

	result := agent.Handler(effectivePayload)
	rt.request(ctx, agentCtx, "POST",
		"/v1/intents/"+intentID+"/resume?owner_agent="+agent.Addr,
		nil, result, true)
}

func (rt *runtime) loadAgentKey(nameFragment string) string {
	home, _ := os.UserHomeDir()
	path := home + "/.config/axme/scenario-agents.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	agents, ok := data["agents"].([]interface{})
	if !ok {
		return ""
	}
	for _, a := range agents {
		agent, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		addr := asString(agent["address"])
		if strings.Contains(addr, nameFragment) {
			return asString(agent["api_key"])
		}
	}
	return ""
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := os.ReadFile(resp.Request.URL.Path)
	if err != nil {
		return nil, err
	}
	return body, nil
}
