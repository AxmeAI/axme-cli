package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// scenarioAgentCreds stores the credentials for a provisioned scenario agent.
type scenarioAgentCreds struct {
	Address          string `json:"address"`
	ServiceAccountID string `json:"service_account_id"`
	KeyID            string `json:"key_id"`
	APIKey           string `json:"api_key"`
	CreatedAt        string `json:"created_at"`
}

// scenarioAgentsStore is the on-disk format of ~/.config/axme/scenario-agents.json.
type scenarioAgentsStore struct {
	Agents []scenarioAgentCreds `json:"agents"`
}

func scenarioAgentsStorePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "axme", "scenario-agents.json")
}

func loadScenarioAgentsStore() scenarioAgentsStore {
	data, err := os.ReadFile(scenarioAgentsStorePath())
	if err != nil {
		return scenarioAgentsStore{}
	}
	var store scenarioAgentsStore
	_ = json.Unmarshal(data, &store)
	return store
}

func saveScenarioAgentsStore(store scenarioAgentsStore) error {
	path := scenarioAgentsStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func upsertScenarioAgent(store *scenarioAgentsStore, creds scenarioAgentCreds) {
	for i, a := range store.Agents {
		if a.Address == creds.Address {
			store.Agents[i] = creds
			return
		}
	}
	store.Agents = append(store.Agents, creds)
}

// ---------------------------------------------------------------------------
// Scenario bundle types (mirrors gateway ScenarioBundleRequest)
// ---------------------------------------------------------------------------

type scenarioAgentEntry struct {
	Role            string `json:"role"`
	Address         string `json:"address"`
	DisplayName     string `json:"display_name,omitempty"`
	DeliveryMode    string `json:"delivery_mode,omitempty"`
	CreateIfMissing bool   `json:"create_if_missing,omitempty"`
}

type scenarioHumanEntry struct {
	Role        string `json:"role"`
	Contact     string `json:"contact"`
	DisplayName string `json:"display_name,omitempty"`
}

type scenarioWorkflowStep struct {
	StepID               string                 `json:"step_id"`
	ToolID               string                 `json:"tool_id"`
	AssignedTo           string                 `json:"assigned_to,omitempty"`
	StepDeadlineSeconds  int                    `json:"step_deadline_seconds,omitempty"`
	Input                map[string]interface{} `json:"input,omitempty"`
	OnSuccess            string                 `json:"on_success,omitempty"`
	OnFailure            string                 `json:"on_failure,omitempty"`
	RequiresApproval     *bool                  `json:"requires_approval,omitempty"`
	HumanTask            map[string]interface{} `json:"human_task,omitempty"`
	RemindAfterSeconds   int                    `json:"remind_after_seconds,omitempty"`
	MaxReminders         int                    `json:"max_reminders,omitempty"`
	EscalateTo           string                 `json:"escalate_to,omitempty"`
}

type scenarioWorkflowEntry struct {
	MacroID    string                   `json:"macro_id,omitempty"`
	Parameters map[string]interface{}   `json:"parameters,omitempty"`
	Steps      []scenarioWorkflowStep   `json:"steps,omitempty"`
	EntryStep  string                   `json:"entry_step,omitempty"`
}

type scenarioIntentSettings struct {
	Type                  string                 `json:"type"`
	DeadlineAt            string                 `json:"deadline_at,omitempty"`
	RemindAfterSeconds    int                    `json:"remind_after_seconds,omitempty"`
	RemindIntervalSeconds int                    `json:"remind_interval_seconds,omitempty"`
	MaxReminders          int                    `json:"max_reminders,omitempty"`
	EscalateTo            string                 `json:"escalate_to,omitempty"`
	MaxDeliveryAttempts   int                    `json:"max_delivery_attempts,omitempty"`
	Payload               map[string]interface{} `json:"payload,omitempty"`
}

type scenarioBundle struct {
	ScenarioID  string                 `json:"scenario_id"`
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	Agents      []scenarioAgentEntry   `json:"agents,omitempty"`
	Humans      []scenarioHumanEntry   `json:"humans,omitempty"`
	Workflow    *scenarioWorkflowEntry `json:"workflow,omitempty"`
	Intent      scenarioIntentSettings `json:"intent"`
}

// ---------------------------------------------------------------------------
// Command group
// ---------------------------------------------------------------------------

func newScenariosCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scenarios",
		Aliases: []string{"scenario"},
		Short:   "Scenario bundle management",
		Long:    "Create, validate and apply scenario bundles for durable intent orchestration.",
	}
	cmd.AddCommand(
		newScenariosCreateCmd(rt),
		newScenariosApplyCmd(rt),
		newScenariosValidateCmd(rt),
		newScenariosListTemplatesCmd(rt),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// axme scenarios list-templates
// ---------------------------------------------------------------------------

func newScenariosListTemplatesCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-templates",
		Short: "List available macro workflow templates from the Tool Registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/tool-registry/macros", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to list templates (HTTP %d)", status)}
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(body)
				return nil
			}

			macros := asSlice(body["macros"])
			if len(macros) == 0 {
				fmt.Println("No macro templates registered.")
				return nil
			}
			fmt.Printf("%-42s  %s\n", "MACRO ID", "DESCRIPTION")
			fmt.Println(strings.Repeat("─", 78))
			for _, raw := range macros {
				m := asMap(raw)
				fmt.Printf("%-42s  %s\n", asString(m["macro_id"]), asString(m["description"]))
			}
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// axme scenarios validate <file.json>
// ---------------------------------------------------------------------------

func newScenariosValidateCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <file.json>",
		Short: "Validate a scenario bundle file without applying it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("cannot read file: %v", err)}
			}

			var bundle map[string]interface{}
			if err := json.Unmarshal(data, &bundle); err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("invalid JSON: %v", err)}
			}

			ctx := rt.effectiveContext()
			status, respBody, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/scenarios/validate", nil, bundle, true)
			if err != nil {
				return err
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(respBody)
				return nil
			}

			if status >= 400 {
				detail := asString(respBody["detail"])
				if detail == "" {
					detail = fmt.Sprintf("HTTP %d", status)
				}
				return &cliError{Code: 1, Msg: fmt.Sprintf("validation failed: %s", detail)}
			}

			warnings := asSlice(respBody["warnings"])
			errors := asSlice(respBody["errors"])

			if len(errors) > 0 {
				fmt.Println("✗ Validation errors:")
				for _, e := range errors {
					fmt.Printf("  • %s\n", asString(e))
				}
				return &cliError{Code: 1, Msg: "scenario bundle has validation errors"}
			}

			if len(warnings) > 0 {
				fmt.Println("⚠ Warnings:")
				for _, w := range warnings {
					fmt.Printf("  • %s\n", asString(w))
				}
			}

			fmt.Println("✓ Scenario bundle is valid.")
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// axme scenarios apply <file.json> [--watch]
// ---------------------------------------------------------------------------

func newScenariosApplyCmd(rt *runtime) *cobra.Command {
	var watch bool
	var serverSide bool

	cmd := &cobra.Command{
		Use:   "apply <file.json>",
		Short: "Provision agents, compile workflow and submit intent from a scenario bundle",
		Long: `Provision agents (SA creation), compile workflow and submit intent.

With --watch: streams the intent lifecycle in real time until terminal status.

Example:
  axme scenarios apply scenario.json
  axme scenarios apply scenario.json --watch`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("cannot read file: %v", err)}
			}

			var bundle scenarioBundle
			if err := json.Unmarshal(data, &bundle); err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("invalid JSON: %v", err)}
			}

			if serverSide {
				return scenariosServerSideApply(rt, cmd, bundle, watch)
			}
			return scenariosClientSideApply(rt, cmd, bundle, watch)
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Stream intent lifecycle events after submission")
	cmd.Flags().BoolVar(&serverSide, "server-side", false, "Use server-side apply via POST /v1/scenarios/apply (deprecated)")
	_ = cmd.Flags().MarkHidden("server-side")
	return cmd
}

func scenariosServerSideApply(rt *runtime, cmd *cobra.Command, bundle scenarioBundle, watch bool) error {
	ctx := rt.effectiveContext()
	var payload map[string]interface{}
	data, _ := json.Marshal(bundle)
	_ = json.Unmarshal(data, &payload)

	status, body, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/scenarios/apply", nil, payload, true)
	if err != nil {
		return err
	}
	if status >= 400 {
		detail := asString(body["detail"])
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", status)
		}
		return &cliError{Code: 1, Msg: fmt.Sprintf("apply failed: %s", detail)}
	}

	intentID := asString(body["intent_id"])
	watchTag("intent:create", fmt.Sprintf("intent_id=%s", intentID))
	if compileID := asString(body["compile_id"]); compileID != "" {
		watchTag("intent:create", fmt.Sprintf("workflow compile_id=%s", compileID))
	}

	if watch && intentID != "" {
		return watchIntentLive(rt, cmd, intentID, &bundle)
	}

	fmt.Println()
	fmt.Println("  Track with:")
	fmt.Printf("    axme intents get %s\n", intentID)
	fmt.Printf("    axme scenarios apply --watch  (re-run with --watch to stream events)\n")
	return nil
}

func scenariosClientSideApply(rt *runtime, cmd *cobra.Command, bundle scenarioBundle, watch bool) error {
	ctx := rt.effectiveContext()

	// Print scenario header
	watchDivider()
	title := bundle.Title
	if title == "" {
		title = bundle.ScenarioID
	}
	fmt.Printf("  axme scenarios apply  ·  %s\n", bundle.ScenarioID)
	watchDivider()
	if title != bundle.ScenarioID {
		fmt.Printf("  %s\n", title)
	}
	if bundle.Description != "" {
		fmt.Printf("  %s\n", bundle.Description)
	}
	fmt.Println()

	// Step 1 — Provision agents (client-side: needs Bearer token for SA creation)
	agentsStore := loadScenarioAgentsStore()
	storeChanged := false

	for _, agent := range bundle.Agents {
		if !agent.CreateIfMissing {
			continue
		}
		if ctx.OrgID == "" || ctx.WorkspaceID == "" {
			watchTag("system", fmt.Sprintf("skipping SA provision for %s — run 'axme login' first", agent.Address))
			continue
		}

		// Check if SA already exists by name
		statusCode, body, _, err := rt.request(
			cmd.Context(), ctx, "GET",
			"/v1/service-accounts?org_id="+ctx.OrgID+"&workspace_id="+ctx.WorkspaceID,
			nil, nil, true,
		)
		existingSAID := ""
		existingAddress := ""
		if err == nil && statusCode == 200 {
			for _, raw := range asSlice(body["service_accounts"]) {
				sa := asMap(raw)
				if asString(sa["name"]) == agent.Address {
					existingSAID = asString(sa["service_account_id"])
					existingAddress = asString(sa["address"])
					break
				}
			}
		}

		if existingSAID != "" {
			// Ensure we have a key stored locally
			hasLocalKey := false
			for _, a := range agentsStore.Agents {
				if a.Address == agent.Address && a.APIKey != "" {
					hasLocalKey = true
					break
				}
			}
			if hasLocalKey {
				watchTag("agent:ready", fmt.Sprintf("%s  (sa_id=%s, key cached)", agent.Address, existingSAID))
				continue
			}
			// Create a new key for existing SA
			kStatus, kBody, _, kErr := rt.request(
				cmd.Context(), ctx,
				"POST", "/v1/service-accounts/"+existingSAID+"/keys",
				nil, map[string]interface{}{}, true,
			)
			if kErr != nil || kStatus >= 400 {
				watchTag("system", fmt.Sprintf("warning: could not create key for %s: %s", agent.Address, asString(kBody["detail"])))
			} else {
				keyObj := asMap(kBody["key"])
				creds := scenarioAgentCreds{
					Address:          agent.Address,
					ServiceAccountID: existingSAID,
					KeyID:            asString(keyObj["key_id"]),
					APIKey:           asString(keyObj["token"]),
					CreatedAt:        asString(keyObj["created_at"]),
				}
				if existingAddress != "" {
					creds.Address = existingAddress
				}
				upsertScenarioAgent(&agentsStore, creds)
				storeChanged = true
				watchTag("agent:ready", fmt.Sprintf("%s  (sa_id=%s, new key created)", agent.Address, existingSAID))
			}
			continue
		}

		// SA does not exist — create it
		saPayload := map[string]interface{}{
			"name":         agent.Address,
			"display_name": agent.DisplayName,
			"org_id":       ctx.OrgID,
			"workspace_id": ctx.WorkspaceID,
		}
		cStatus, cBody, _, cErr := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts", nil, saPayload, true)
		if cErr != nil || (cStatus >= 400 && cStatus != 409) {
			watchTag("system", fmt.Sprintf("warning: could not create SA %s: %s", agent.Address, asString(cBody["detail"])))
			continue
		}
		saObj := asMap(cBody["service_account"])
		saID := asString(saObj["service_account_id"])
		if saID == "" {
			saID = asString(saObj["id"])
		}
		saAddr := asString(saObj["address"])

		if saID == "" {
			watchTag("system", fmt.Sprintf("warning: no sa_id returned for %s — skipping key creation", agent.Address))
			continue
		}

		// Create API key
		kStatus, kBody, _, kErr := rt.request(
			cmd.Context(), ctx,
			"POST", "/v1/service-accounts/"+saID+"/keys",
			nil, map[string]interface{}{}, true,
		)
		if kErr != nil || kStatus >= 400 {
			watchTag("system", fmt.Sprintf("warning: key creation failed for %s: %s", agent.Address, asString(kBody["detail"])))
		} else {
			keyObj := asMap(kBody["key"])
			addrToStore := saAddr
			if addrToStore == "" {
				addrToStore = agent.Address
			}
			creds := scenarioAgentCreds{
				Address:          addrToStore,
				ServiceAccountID: saID,
				KeyID:            asString(keyObj["key_id"]),
				APIKey:           asString(keyObj["token"]),
				CreatedAt:        asString(keyObj["created_at"]),
			}
			upsertScenarioAgent(&agentsStore, creds)
			storeChanged = true
			watchTag("agent:create", fmt.Sprintf("%s  (sa_id=%s)", addrToStore, saID))
		}
	}

	if storeChanged {
		if err := saveScenarioAgentsStore(agentsStore); err != nil {
			watchTag("system", fmt.Sprintf("warning: could not save agent credentials: %v", err))
		}
	}

	// Print agent/human assignments
	for _, a := range bundle.Agents {
		label := a.DisplayName
		if label == "" {
			label = a.Address
		}
		if a.DeliveryMode != "" {
			watchTag("agent:assign", fmt.Sprintf("%s  [%s]", label, watchFmtBinding(a.DeliveryMode)))
		} else {
			watchTag("agent:assign", label)
		}
	}
	for _, h := range bundle.Humans {
		label := h.DisplayName
		if label == "" {
			label = h.Role
		}
		suffix := ""
		if h.Contact != "" {
			suffix = fmt.Sprintf("  <%s>", h.Contact)
		}
		watchTag("human:assign", label+suffix)
	}

	// Submit intent via POST /v1/scenarios/apply (gateway handles compile + intent create)
	fmt.Println()
	watchTag("system", "compiling workflow and submitting intent…")

	var rawBundle map[string]interface{}
	{
		d, _ := json.Marshal(bundle)
		_ = json.Unmarshal(d, &rawBundle)
	}

	applyStatus, applyBody, _, applyErr := rt.request(cmd.Context(), ctx, "POST", "/v1/scenarios/apply", nil, rawBundle, true)
	if applyErr != nil {
		return &cliError{Code: 1, Msg: fmt.Sprintf("apply failed: %v", applyErr)}
	}
	if applyStatus >= 400 {
		detail := asString(applyBody["detail"])
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", applyStatus)
		}
		return &cliError{Code: 1, Msg: fmt.Sprintf("apply failed: %s", detail)}
	}

	intentID := asString(applyBody["intent_id"])
	compileID := asString(applyBody["compile_id"])

	fmt.Println()
	watchTag("intent:create", fmt.Sprintf("intent_id=%s", intentID))
	if compileID != "" {
		watchTag("intent:create", fmt.Sprintf("workflow compile_id=%s", compileID))
	}
	watchTag("status:change", "—  →  SUBMITTED")

	if watch && intentID != "" {
		return watchIntentLive(rt, cmd, intentID, &bundle)
	}

	fmt.Println()
	watchDivider()
	fmt.Println()
	fmt.Println("  Track with:")
	fmt.Printf("    axme intents get %s\n", intentID)
	fmt.Printf("    axme scenarios apply %s --watch\n", os.Args[len(os.Args)-1])
	fmt.Println()
	return nil
}

// ---------------------------------------------------------------------------
// --watch: live intent lifecycle stream
// ---------------------------------------------------------------------------

// watchIntentLive streams GET /v1/intents/{id}/events/stream and prints events
// in the agreed terminal format until a terminal status is reached.
func watchIntentLive(rt *runtime, cmd *cobra.Command, intentID string, bundle *scenarioBundle) error {
	ctx := rt.effectiveContext()
	baseURL := strings.TrimRight(ctx.BaseURL, "/")
	apiKey := ctx.APIKey
	if rt.overrideKey != "" {
		apiKey = rt.overrideKey
	}

	fmt.Println()
	watchDivider()
	fmt.Printf("  Watching intent  %s\n", intentID)
	watchDivider()
	fmt.Println()

	curStatus := "SUBMITTED"
	lastHolder := ""
	nextSeq := 0
	terminalStatuses := map[string]bool{
		"COMPLETED": true, "FAILED": true, "CANCELED": true, "TIMED_OUT": true,
	}

	for {
		url := fmt.Sprintf("%s/v1/intents/%s/events/stream?since=%d&wait_seconds=30", baseURL, intentID, nextSeq)
		req, err := http.NewRequestWithContext(cmd.Context(), "GET", url, nil)
		if err != nil {
			return fmt.Errorf("watch: %w", err)
		}
		req.Header.Set("X-Api-Key", apiKey)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := rt.streamClient.Do(req)
		if err != nil {
			// SSE connection failed — retry after short delay
			time.Sleep(2 * time.Second)
			continue
		}

		done, newSeq, newStatus, newHolder := consumeEventStream(resp.Body, curStatus, lastHolder, nextSeq, terminalStatuses, bundle)
		_ = resp.Body.Close()
		curStatus = newStatus
		lastHolder = newHolder
		nextSeq = newSeq

		if done {
			break
		}

		// SSE stream ended (timeout or disconnect) — check intent status
		// directly to catch completions missed during reconnect gap.
		if fallbackStatus := rt.checkIntentStatus(cmd.Context(), apiKey, baseURL, intentID); terminalStatuses[fallbackStatus] {
			curStatus = fallbackStatus
			break
		}
		// Reconnect
	}

	// Final summary
	fmt.Println()
	watchDivider()
	fmt.Println()
	fmt.Printf("  Final status:  %s\n", watchFmtStatus(curStatus, ""))
	fmt.Printf("  Intent ID:     %s\n", intentID)
	fmt.Println()
	fmt.Println("  Audit log:")
	fmt.Printf("    axme intents get %s\n", intentID)
	fmt.Printf("    axme intents log %s\n", intentID)
	fmt.Println()
	return nil
}

// checkIntentStatus does a direct GET to check if intent reached terminal state.
// Used as fallback when SSE stream disconnects.
func (rt *runtime) checkIntentStatus(ctx context.Context, apiKey, baseURL, intentID string) string {
	url := baseURL + "/v1/intents/" + intentID
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-Api-Key", apiKey)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return ""
	}
	intent, _ := body["intent"].(map[string]interface{})
	if intent == nil {
		intent = body
	}
	return strings.ToUpper(asString(intent["lifecycle_status"]))
}

// consumeEventStream reads SSE events and prints them. Returns (terminal, nextSeq, lastStatus, lastHolder).
func consumeEventStream(body io.Reader, curStatus string, curHolder string, startSeq int, terminalStatuses map[string]bool, bundle *scenarioBundle) (bool, int, string, string) {
	scanner := bufio.NewScanner(body)
	nextSeq := startSeq
	lastStatus := curStatus
	lastHolder := curHolder

	var eventType, eventData string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// dispatch accumulated event
			if eventType != "" && eventData != "" {
				if eventType == "stream.timeout" {
					return false, nextSeq, lastStatus, lastHolder
				}
				var ev map[string]interface{}
				if err := json.Unmarshal([]byte(eventData), &ev); err == nil {
					terminal, newStatus, newHolder := renderWatchEvent(ev, lastStatus, lastHolder, bundle)
					lastStatus = newStatus
					if newHolder != "" {
						lastHolder = newHolder
					}
					if terminal {
						return true, nextSeq, lastStatus, lastHolder
					}
					if seq, ok := ev["seq"].(float64); ok {
						nextSeq = int(seq) + 1
					}
				}
			}
			eventType = ""
			eventData = ""
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			eventData = strings.TrimPrefix(line, "data: ")
		} else if strings.HasPrefix(line, "id: ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(line, "id: ")); err == nil {
				nextSeq = n + 1
			}
		}
	}
	return false, nextSeq, lastStatus, lastHolder
}

// renderWatchEvent processes a single SSE event and prints it.
// Returns (isTerminal, newStatus, currentHolder).
func renderWatchEvent(ev map[string]interface{}, curStatus string, prevHolder string, bundle *scenarioBundle) (bool, string, string) {
	evType := asString(ev["event_type"])
	evStatus := asString(ev["status"])
	evReason := asString(ev["waiting_reason"])
	if evReason == "" {
		evReason = asString(ev["lifecycle_waiting_reason"])
	}
	if evReason == "" {
		evReason = asString(ev["reason"])
	}

	terminalStatuses := map[string]bool{
		"COMPLETED": true, "FAILED": true, "CANCELED": true, "TIMED_OUT": true,
	}

	switch evType {
	case "intent.human_task_assigned":
		ht := asMap(ev["human_task"])
		title := asString(ht["title"])
		if title == "" {
			title = "Human approval required"
		}
		watchTag("system", fmt.Sprintf("human task: %s", title))
		watchTag("status:change", fmt.Sprintf("%s  →  WAITING (for human)", watchFmtStatus(curStatus, "")))
		return false, "WAITING", prevHolder

	case "intent.reminder":
		pw := asMap(ev["pending_with"])
		holder := watchFmtHolder(pw)
		watchTag("reminder", fmt.Sprintf("REMINDER sent to %s", holder))
		return false, curStatus, prevHolder

	case "intent.escalated":
		details := asMap(ev["details"])
		escalatedTo := asString(details["escalated_to"])
		watchTag("escalation", fmt.Sprintf("escalated to %s", escalatedTo))
		return false, curStatus, prevHolder

	case "intent.timed_out":
		watchTag("timeout", "TIMED_OUT — deadline exceeded")
		return true, "TIMED_OUT", prevHolder

	case "intent.delivery_failed":
		details := asMap(ev["details"])
		attempt := asString(details["delivery_attempt"])
		watchTag("delivery:failed", fmt.Sprintf("max delivery attempts reached  (attempt=%s)", attempt))
		return false, "FAILED", prevHolder
	}

	// Status change event
	if evStatus == "" {
		return false, curStatus, prevHolder
	}

	pw := asMap(ev["pending_with"])
	holder := watchFmtHolder(pw)
	newFmt := watchFmtStatus(evStatus, evReason)
	prevFmt := watchFmtStatus(curStatus, "")

	if newFmt != prevFmt {
		// Status changed (e.g. DELIVERED → WAITING, WAITING → COMPLETED)
		if prevHolder != "" && holder != "" && holder != prevHolder && prevHolder != "agent_core" {
			watchTag("step:done", fmt.Sprintf("%s completed", prevHolder))
		}
		watchTag("status:change", fmt.Sprintf("%s  →  %s", prevFmt, newFmt))
		if holder != "" {
			watchTag("cur_holder", holder)
		}
	} else if holder != "" && holder != prevHolder {
		// Same status but different holder (e.g. WAITING(agent1) → WAITING(agent2)).
		if prevHolder != "" && prevHolder != "agent_core" {
			watchTag("step:done", fmt.Sprintf("%s completed", prevHolder))
		}
		watchTag("status:change", fmt.Sprintf("%s  →  %s (%s)", prevFmt, newFmt, holder))
		watchTag("cur_holder", holder)
	}

	if terminalStatuses[evStatus] {
		return true, evStatus, holder
	}
	return false, evStatus, holder
}

// ---------------------------------------------------------------------------
// Watch render helpers — matches render.py format
// ---------------------------------------------------------------------------

const _tagWidth = 20
const _lineWidth = 74

func watchDivider() {
	fmt.Println("  " + strings.Repeat("─", _lineWidth))
}

func watchTag(kind, msg string) {
	pad := _tagWidth - len(kind)
	if pad < 0 {
		pad = 0
	}
	fmt.Printf("  [%s]%s:  %s\n", kind, strings.Repeat(" ", pad), msg)
}

func watchFmtStatus(raw, reason string) string {
	isHuman := strings.Contains(strings.ToUpper(reason), "HUMAN")
	waiting := "WAITING (for agent)"
	if isHuman {
		waiting = "WAITING (for human)"
	}
	m := map[string]string{
		"CREATED":      "CREATED",
		"SUBMITTED":    "SUBMITTED",
		"DELIVERED":    "DELIVERED",
		"ACKNOWLEDGED": "ACKNOWLEDGED",
		"IN_PROGRESS":  "IN_PROGRESS",
		"WAITING":      waiting,
		"COMPLETED":    "COMPLETED ✓",
		"FAILED":       "FAILED ✗",
		"CANCELED":     "CANCELED",
		"TIMED_OUT":    "TIMED_OUT ✗",
	}
	if v, ok := m[raw]; ok {
		return v
	}
	return raw
}

func watchFmtHolder(pw map[string]interface{}) string {
	if pw == nil {
		return ""
	}
	name := asString(pw["name"])
	if name == "" {
		name = asString(pw["ref"])
	}
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

func watchFmtBinding(mode string) string {
	labels := map[string]string{
		"stream":   "stream ← SSE listen()",
		"poll":     "poll ← periodic pull",
		"http":     "http ← AXME pushes to callback_url",
		"inbox":    "inbox ← reply_to mechanism",
		"internal": "internal ← built-in runtime",
	}
	if l, ok := labels[mode]; ok {
		return l
	}
	return mode
}

// ---------------------------------------------------------------------------
// axme scenarios create  (interactive wizard)
// ---------------------------------------------------------------------------

func newScenariosCreateCmd(rt *runtime) *cobra.Command {
	var fromTemplate string
	var outputFile string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Interactive wizard to create a scenario bundle JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)

			fmt.Println()
			fmt.Println("Scenario Wizard")
			fmt.Println(strings.Repeat("─", 43))

			scenarioID := wizardPrompt(scanner, "Scenario ID (e.g. approval.nginx_rollout.v1)", "my_scenario.v1")
			description := wizardPrompt(scanner, "Description", "")

			// ── Agents ────────────────────────────────────────────────────
			fmt.Println("\n── Agents ──────────────────────────────────────────────")
			numAgentsStr := wizardPrompt(scanner, "Number of service account agents [1-10]", "1")
			numAgents, _ := strconv.Atoi(numAgentsStr)
			if numAgents < 0 {
				numAgents = 0
			}
			if numAgents > 10 {
				numAgents = 10
			}

			var agents []scenarioAgentEntry
			for i := 0; i < numAgents; i++ {
				fmt.Printf("\n  Agent %d:\n", i+1)
				roleAlias := wizardPromptIndented(scanner, "Role alias", fmt.Sprintf("agent%d", i+1))
				address := wizardPromptIndented(scanner, "Agent address", roleAlias)
				createStr := wizardPromptIndented(scanner, "Create if missing? [Y/n]", "Y")
				createIfMissing := !strings.EqualFold(strings.TrimSpace(createStr), "n")
				displayName := wizardPromptIndented(scanner, fmt.Sprintf("Display name [%s]", address), address)
				agents = append(agents, scenarioAgentEntry{
					Role:            roleAlias,
					Address:         address,
					DisplayName:     displayName,
					CreateIfMissing: createIfMissing,
				})
			}

			// ── Human Participants ──────────────────────────────────────────
			fmt.Println("\n── Human Participants ───────────────────────────────────")
			numHumansStr := wizardPrompt(scanner, "Number of human participants [0-5]", "0")
			numHumans, _ := strconv.Atoi(numHumansStr)
			if numHumans < 0 {
				numHumans = 0
			}
			if numHumans > 5 {
				numHumans = 5
			}

			var humans []scenarioHumanEntry
			for i := 0; i < numHumans; i++ {
				fmt.Printf("\n  Human %d:\n", i+1)
				roleAlias := wizardPromptIndented(scanner, "Role alias", fmt.Sprintf("human%d", i+1))
				contact := wizardPromptIndented(scanner, "Contact (email)", "")
				displayName := wizardPromptIndented(scanner, "Display name", roleAlias)
				humans = append(humans, scenarioHumanEntry{
					Role:        roleAlias,
					Contact:     contact,
					DisplayName: displayName,
				})
			}

			// ── Workflow ───────────────────────────────────────────────────
			var workflow *scenarioWorkflowEntry
			useTemplateStr := fromTemplate
			if useTemplateStr == "" {
				useTemplateStr = wizardPrompt(scanner, "Start from macro template? [y/N]", "N")
			}

			useMacro := strings.EqualFold(strings.TrimSpace(useTemplateStr), "y") || fromTemplate != ""

			if useMacro {
				var chosenMacroID string
				if fromTemplate != "" {
					chosenMacroID = fromTemplate
				} else {
					fmt.Println("? Fetching templates from Tool Registry...")
					ctx := rt.effectiveContext()
					tStatus, tBody, _, tErr := rt.request(cmd.Context(), ctx, "GET", "/v1/tool-registry/macros", nil, nil, true)
					macroIDs := []string{}
					if tErr == nil && tStatus < 400 {
						macroList := asSlice(tBody["macros"])
						if len(macroList) > 0 {
							fmt.Println("\n  Available templates:")
							for idx, raw := range macroList {
								m := asMap(raw)
								mid := asString(m["macro_id"])
								desc := asString(m["description"])
								fmt.Printf("    %d. %-40s  %s\n", idx+1, mid, desc)
								macroIDs = append(macroIDs, mid)
							}
							selStr := wizardPrompt(scanner, "  Select template number", "1")
							selIdx, _ := strconv.Atoi(selStr)
							selIdx--
							if selIdx >= 0 && selIdx < len(macroIDs) {
								chosenMacroID = macroIDs[selIdx]
							}
						} else {
							fmt.Println("  No templates available.")
						}
					} else {
						fmt.Println("  (could not fetch templates — enter macro ID manually)")
						chosenMacroID = wizardPrompt(scanner, "  Macro ID", "")
					}
				}

				if chosenMacroID != "" {
					fmt.Printf("\n  Template: %s\n", chosenMacroID)
					fmt.Println("  Parameters:")
					params := map[string]interface{}{}
					ctx := rt.effectiveContext()
					mStatus, mBody, _, mErr := rt.request(cmd.Context(), ctx, "GET", "/v1/tool-registry/macros/"+chosenMacroID, nil, nil, true)
					if mErr == nil && mStatus < 400 {
						macro := asMap(mBody["macro"])
						paramDefs := asSlice(macro["parameters"])
						for _, p := range paramDefs {
							pd := asMap(p)
							paramName := asString(pd["name"])
							paramDefault := asString(pd["default"])
							val := wizardPromptIndented(scanner, fmt.Sprintf("%s (role alias or address)", paramName), paramDefault)
							if val != "" {
								params[paramName] = val
							}
						}
					} else {
						for _, paramName := range []string{"step_deadline_seconds", "remind_after_seconds", "max_reminders", "escalate_to"} {
							defaults := map[string]string{
								"step_deadline_seconds": "300",
								"remind_after_seconds":  "1800",
								"max_reminders":         "3",
								"escalate_to":           "skip",
							}
							val := wizardPromptIndented(scanner, paramName, defaults[paramName])
							if val != "" && val != "skip" {
								params[paramName] = val
							}
						}
					}
					workflow = &scenarioWorkflowEntry{
						MacroID:    chosenMacroID,
						Parameters: params,
					}
				}
			}

			// ── Intent Settings ─────────────────────────────────────────────
			fmt.Println("\n── Intent Settings ──────────────────────────────────────")
			intentType := wizardPrompt(scanner, "Intent type", "approval.change.v1")
			deadlineStr := wizardPrompt(scanner, "Deadline (e.g. 4h, 24h, none) [none]", "none")
			maxDeliveryStr := wizardPrompt(scanner, "Max delivery attempts", "5")
			maxDelivery, _ := strconv.Atoi(maxDeliveryStr)

			var deadlineAt string
			if deadlineStr != "" && deadlineStr != "none" {
				dur, err := parseDurationShorthand(deadlineStr)
				if err == nil {
					deadlineAt = time.Now().Add(dur).UTC().Format(time.RFC3339)
				} else {
					fmt.Printf("  (warning: cannot parse deadline '%s': %v — skipping)\n", deadlineStr, err)
				}
			}

			var remindAfter, remindInterval, maxReminders int
			var escalateTo string
			if numHumans > 0 {
				fmt.Println("\n  Reminder settings for human steps:")
				remindAfterStr := wizardPromptIndented(scanner, "Remind after seconds [1800]", "1800")
				remindAfter, _ = strconv.Atoi(remindAfterStr)
				remindIntervalStr := wizardPromptIndented(scanner, "Remind interval seconds [1800]", "1800")
				remindInterval, _ = strconv.Atoi(remindIntervalStr)
				maxRemindersStr := wizardPromptIndented(scanner, "Max reminders [3]", "3")
				maxReminders, _ = strconv.Atoi(maxRemindersStr)
				escalateTo = wizardPromptIndented(scanner, "Escalate to (role alias or skip) [skip]", "skip")
				if escalateTo == "skip" {
					escalateTo = ""
				}
			}

			fmt.Println("\n  Payload fields (press Enter to skip):")
			payload := map[string]interface{}{}
			for _, fieldName := range []string{"service", "environment", "reason"} {
				val := wizardPromptIndented(scanner, fieldName, "")
				if val != "" {
					payload[fieldName] = val
				}
			}

			fmt.Println("\n── Summary ──────────────────────────────────────────────")
			fmt.Printf("  Scenario:    %s\n", scenarioID)
			agentNames := make([]string, 0, len(agents))
			for _, a := range agents {
				agentNames = append(agentNames, a.Role)
			}
			fmt.Printf("  Agents:      %s (%d SA agents)\n", strings.Join(agentNames, ", "), len(agents))
			if len(humans) > 0 {
				humanNames := make([]string, 0, len(humans))
				for _, h := range humans {
					humanNames = append(humanNames, h.Role)
				}
				fmt.Printf("  Humans:      %s\n", strings.Join(humanNames, ", "))
			}
			if workflow != nil {
				fmt.Printf("  Workflow:    %s\n", workflow.MacroID)
			}
			if deadlineAt != "" {
				fmt.Printf("  Deadline:    %s from now\n", deadlineStr)
			}

			if outputFile == "" {
				outputFile = wizardPrompt(scanner, fmt.Sprintf("Save to file [%s.json]", scenarioID), scenarioID+".json")
			}

			newBundle := scenarioBundle{
				ScenarioID:  scenarioID,
				Description: description,
				Agents:      agents,
				Intent: scenarioIntentSettings{
					Type:                  intentType,
					DeadlineAt:            deadlineAt,
					RemindAfterSeconds:    remindAfter,
					RemindIntervalSeconds: remindInterval,
					MaxReminders:          maxReminders,
					EscalateTo:            escalateTo,
					MaxDeliveryAttempts:   maxDelivery,
					Payload:               payload,
				},
			}
			if len(humans) > 0 {
				newBundle.Humans = humans
			}
			if workflow != nil {
				newBundle.Workflow = workflow
			}

			data, err := json.MarshalIndent(newBundle, "", "  ")
			if err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to marshal bundle: %v", err)}
			}
			if err := os.WriteFile(outputFile, data, 0o644); err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to write file: %v", err)}
			}
			fmt.Printf("✓ Saved to %s\n", outputFile)

			applyNow := wizardPrompt(scanner, "Apply now? [y/N]", "N")
			if strings.EqualFold(strings.TrimSpace(applyNow), "y") {
				watchNow := wizardPrompt(scanner, "Watch intent lifecycle? [y/N]", "N")
				doWatch := strings.EqualFold(strings.TrimSpace(watchNow), "y")
				fmt.Printf("→ running axme scenarios apply %s\n", outputFile)
				return scenariosClientSideApply(rt, cmd, newBundle, doWatch)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&fromTemplate, "from-template", "", "Start wizard with a specific macro template ID")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file path (skips the save prompt)")
	return cmd
}

// ---------------------------------------------------------------------------
// Wizard prompt helpers
// ---------------------------------------------------------------------------

func wizardPrompt(scanner *bufio.Scanner, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("? %s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("? %s: ", label)
	}
	scanner.Scan()
	val := strings.TrimSpace(scanner.Text())
	if val == "" {
		return defaultVal
	}
	return val
}

func wizardPromptIndented(scanner *bufio.Scanner, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  ? %s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("  ? %s: ", label)
	}
	scanner.Scan()
	val := strings.TrimSpace(scanner.Text())
	if val == "" {
		return defaultVal
	}
	return val
}

// parseDurationShorthand parses durations like "4h", "30m", "2d" etc.
func parseDurationShorthand(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		num, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		return time.Duration(num * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}
