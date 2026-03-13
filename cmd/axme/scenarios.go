package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Scenario bundle types (mirrors gateway ScenarioBundleRequest)
// ---------------------------------------------------------------------------

type scenarioAgentEntry struct {
	Role            string `json:"role"`
	Address         string `json:"address"`
	DisplayName     string `json:"display_name,omitempty"`
	CreateIfMissing bool   `json:"create_if_missing,omitempty"`
}

type scenarioHumanEntry struct {
	Role        string `json:"role"`
	Contact     string `json:"contact"`
	DisplayName string `json:"display_name,omitempty"`
}

type scenarioWorkflowEntry struct {
	MacroID    string                 `json:"macro_id,omitempty"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
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
// axme scenarios apply <file.json>
// ---------------------------------------------------------------------------

func newScenariosApplyCmd(rt *runtime) *cobra.Command {
	var serverSide bool
	cmd := &cobra.Command{
		Use:   "apply <file.json>",
		Short: "Provision agents, compile workflow and submit intent from a scenario bundle",
		Args:  cobra.ExactArgs(1),
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
				return scenariosServerSideApply(rt, cmd, bundle)
			}
			return scenariosClientSideApply(rt, cmd, bundle)
		},
	}
	cmd.Flags().BoolVar(&serverSide, "server-side", false, "Use atomic server-side apply via POST /v1/scenarios/apply")
	return cmd
}

func scenariosServerSideApply(rt *runtime, cmd *cobra.Command, bundle scenarioBundle) error {
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

	fmt.Println("✓ Scenario applied.")
	intentID := asString(body["intent_id"])
	if intentID != "" {
		fmt.Printf("  intent_id = %s\n", intentID)
		fmt.Printf("\nTrack with:\n  axme intents get %s\n  axme intents watch %s\n", intentID, intentID)
	}
	return nil
}

func scenariosClientSideApply(rt *runtime, cmd *cobra.Command, bundle scenarioBundle) error {
	ctx := rt.effectiveContext()

	// Step 1/4 — Check / create agents
	fmt.Println("[1/4] Checking agents...")
	for _, agent := range bundle.Agents {
		statusCode, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/service-accounts/"+agent.Address, nil, nil, true)
		if err != nil {
			fmt.Printf("  ?  %-36s  (error: %v)\n", agent.Address, err)
			continue
		}
		if statusCode == 200 {
			saID := asString(asMap(body["service_account"])["sa_id"])
			fmt.Printf("  ✓  %-36s  (exists, sa_id=%s)\n", agent.Address, saID)
		} else if agent.CreateIfMissing {
			fmt.Printf("  +  %-36s  (creating...)", agent.Address)
			saPayload := map[string]interface{}{
				"agent_address": agent.Address,
				"display_name":  agent.DisplayName,
			}
			cStatus, cBody, _, cErr := rt.request(cmd.Context(), ctx, "POST", "/v1/service-accounts", nil, saPayload, true)
			if cErr != nil || cStatus >= 400 {
				detail := asString(cBody["detail"])
				fmt.Printf("  ✗  (failed: %s)\n", detail)
			} else {
				saID := asString(asMap(cBody["service_account"])["sa_id"])
				fmt.Printf("  ✓  (created, sa_id=%s)\n", saID)
			}
		} else {
			fmt.Printf("  ✗  %-36s  (not found)\n", agent.Address)
		}
	}

	// Step 2/4 — Skip tool registration (tools are registered separately)
	fmt.Println("[2/4] Registering tools with Tool Registry...")
	fmt.Println("  (skipped — tools are registered via 'axme tool-registry register')")

	// Step 3/4 — Compile workflow
	fmt.Println("[3/4] Compiling workflow...")
	var compileID string
	if bundle.Workflow != nil && bundle.Workflow.MacroID != "" {
		compilePayload := map[string]interface{}{
			"macro_id":   bundle.Workflow.MacroID,
			"parameters": bundle.Workflow.Parameters,
		}
		cStatus, cBody, _, cErr := rt.request(cmd.Context(), ctx, "POST", "/v1/tool-registry/macros/compile", nil, compilePayload, true)
		if cErr != nil {
			return &cliError{Code: 1, Msg: fmt.Sprintf("workflow compile failed: %v", cErr)}
		}
		if cStatus >= 400 {
			return &cliError{Code: 1, Msg: fmt.Sprintf("workflow compile failed (HTTP %d): %s", cStatus, asString(cBody["detail"]))}
		}
		compileID = asString(cBody["compile_id"])
		if compileID == "" {
			compileID = asString(cBody["workflow_compile_id"])
		}
		fmt.Printf("  ✓  compile_id = %s\n", compileID)
	} else {
		fmt.Println("  (skipped — no macro workflow specified)")
	}

	// Step 4/4 — Submit intent
	fmt.Println("[4/4] Submitting intent...")
	settings := bundle.Intent

	roleMap := map[string]string{}
	for _, ag := range bundle.Agents {
		roleMap[ag.Role] = ag.Address
	}
	for _, hu := range bundle.Humans {
		roleMap[hu.Role] = hu.Contact
	}

	toAgent := roleMap["initiator"]
	if toAgent == "" {
		for _, v := range roleMap {
			toAgent = v
			break
		}
	}
	if toAgent == "" {
		toAgent = "unknown"
	}

	intentPayload := map[string]interface{}{
		"intent_type": settings.Type,
		"to_agent":    toAgent,
		"payload":     settings.Payload,
	}
	if compileID != "" {
		intentPayload["workflow_compile_id"] = compileID
	}
	if settings.DeadlineAt != "" {
		intentPayload["deadline_at"] = settings.DeadlineAt
	}
	if settings.RemindAfterSeconds > 0 {
		intentPayload["remind_after_seconds"] = settings.RemindAfterSeconds
	}
	if settings.RemindIntervalSeconds > 0 {
		intentPayload["remind_interval_seconds"] = settings.RemindIntervalSeconds
	}
	if settings.MaxReminders > 0 {
		intentPayload["max_reminders"] = settings.MaxReminders
	}
	if settings.EscalateTo != "" {
		intentPayload["escalate_to"] = settings.EscalateTo
	}
	if settings.MaxDeliveryAttempts > 0 {
		intentPayload["max_delivery_attempts"] = settings.MaxDeliveryAttempts
	}

	iStatus, iBody, _, iErr := rt.request(cmd.Context(), ctx, "POST", "/v1/intents", nil, intentPayload, true)
	if iErr != nil {
		return &cliError{Code: 1, Msg: fmt.Sprintf("intent submission failed: %v", iErr)}
	}
	if iStatus >= 400 {
		detail := asString(iBody["detail"])
		return &cliError{Code: 1, Msg: fmt.Sprintf("intent submission failed (HTTP %d): %s", iStatus, detail)}
	}

	intentID := asString(iBody["intent_id"])
	fmt.Printf("  ✓  intent_id = %s\n", intentID)

	fmt.Println("\n" + strings.Repeat("─", 40))
	fmt.Println("Scenario started. Track with:")
	fmt.Printf("  axme intents get %s\n", intentID)
	fmt.Printf("  axme intents watch %s\n", intentID)
	return nil
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
					// fetch templates
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
					// fetch macro definition for parameter hints
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
						// fallback: common approval parameters
						for _, paramName := range []string{"step_deadline_seconds", "remind_after_seconds", "max_reminders", "escalate_to"} {
							defaults := map[string]string{
								"step_deadline_seconds": "300",
								"remind_after_seconds":  "1800",
								"max_reminders":        "3",
								"escalate_to":          "skip",
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

			// ── Payload fields ──────────────────────────────────────────────
			fmt.Println("\n  Payload fields (press Enter to skip):")
			payload := map[string]interface{}{}
			for _, fieldName := range []string{"service", "environment", "reason"} {
				val := wizardPromptIndented(scanner, fieldName, "")
				if val != "" {
					payload[fieldName] = val
				}
			}

			// ── Summary ─────────────────────────────────────────────────────
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

			// ── Save ────────────────────────────────────────────────────────
			if outputFile == "" {
				outputFile = wizardPrompt(scanner, fmt.Sprintf("Save to file [%s.json]", scenarioID), scenarioID+".json")
			}

			bundle := scenarioBundle{
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
				bundle.Humans = humans
			}
			if workflow != nil {
				bundle.Workflow = workflow
			}

			data, err := json.MarshalIndent(bundle, "", "  ")
			if err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to marshal bundle: %v", err)}
			}
			if err := os.WriteFile(outputFile, data, 0o644); err != nil {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to write file: %v", err)}
			}
			fmt.Printf("✓ Saved to %s\n", outputFile)

			applyNow := wizardPrompt(scanner, "Apply now? [y/N]", "N")
			if strings.EqualFold(strings.TrimSpace(applyNow), "y") {
				fmt.Printf("→ running axme scenarios apply %s\n", outputFile)
				return scenariosClientSideApply(rt, cmd, bundle)
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
	// Handle days shorthand since Go's time.ParseDuration doesn't
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
