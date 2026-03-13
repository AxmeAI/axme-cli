package main

import (
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parseDurationShorthand
// ---------------------------------------------------------------------------

func TestParseDurationShorthand_StandardUnits(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"1h", time.Hour},
		{"30m", 30 * time.Minute},
		{"45s", 45 * time.Second},
		{"2h30m", 2*time.Hour + 30*time.Minute},
	}
	for _, tc := range cases {
		got, err := parseDurationShorthand(tc.input)
		if err != nil {
			t.Errorf("parseDurationShorthand(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.expected {
			t.Errorf("parseDurationShorthand(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestParseDurationShorthand_DaysShorthand(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"1d", 24 * time.Hour},
		{"2d", 48 * time.Hour},
		{"0.5d", 12 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		got, err := parseDurationShorthand(tc.input)
		if err != nil {
			t.Errorf("parseDurationShorthand(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.expected {
			t.Errorf("parseDurationShorthand(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestParseDurationShorthand_InvalidInput(t *testing.T) {
	cases := []string{"none", "abc", "d", "xd", ""}
	for _, tc := range cases {
		_, err := parseDurationShorthand(tc)
		if err == nil {
			t.Errorf("parseDurationShorthand(%q) expected error, got nil", tc)
		}
	}
}

func TestParseDurationShorthand_CaseInsensitive(t *testing.T) {
	d1, err1 := parseDurationShorthand("4H")
	d2, err2 := parseDurationShorthand("4h")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if d1 != d2 {
		t.Errorf("case sensitivity mismatch: %v vs %v", d1, d2)
	}
}

// ---------------------------------------------------------------------------
// scenarioBundle JSON round-trip
// ---------------------------------------------------------------------------

func TestScenarioBundleJSON_RoundTrip(t *testing.T) {
	bundle := scenarioBundle{
		ScenarioID:  "approval.nginx.v1",
		Description: "Test bundle",
		Agents: []scenarioAgentEntry{
			{
				Role:            "validator",
				Address:         "agent://acme/prod/validator",
				DisplayName:     "Validator Agent",
				CreateIfMissing: true,
			},
		},
		Humans: []scenarioHumanEntry{
			{
				Role:        "cab_approver",
				Contact:     "alice@example.com",
				DisplayName: "Alice",
			},
		},
		Workflow: &scenarioWorkflowEntry{
			MacroID: "macro.approval.v1",
			Parameters: map[string]interface{}{
				"step_deadline_seconds": "300",
			},
		},
		Intent: scenarioIntentSettings{
			Type:                  "approval.change.v1",
			DeadlineAt:            "2026-04-01T00:00:00Z",
			RemindAfterSeconds:    1800,
			RemindIntervalSeconds: 1800,
			MaxReminders:          3,
			EscalateTo:            "user://ops-lead",
			MaxDeliveryAttempts:   5,
			Payload: map[string]interface{}{
				"service":     "nginx",
				"environment": "prod",
			},
		},
	}

	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded scenarioBundle
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.ScenarioID != bundle.ScenarioID {
		t.Errorf("ScenarioID mismatch: got %q, want %q", decoded.ScenarioID, bundle.ScenarioID)
	}
	if len(decoded.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(decoded.Agents))
	}
	if decoded.Agents[0].Address != "agent://acme/prod/validator" {
		t.Errorf("agent address mismatch: got %q", decoded.Agents[0].Address)
	}
	if decoded.Agents[0].Role != "validator" {
		t.Errorf("agent role mismatch: got %q", decoded.Agents[0].Role)
	}
	if !decoded.Agents[0].CreateIfMissing {
		t.Error("create_if_missing should be true")
	}
	if len(decoded.Humans) != 1 {
		t.Fatalf("expected 1 human, got %d", len(decoded.Humans))
	}
	if decoded.Humans[0].Contact != "alice@example.com" {
		t.Errorf("human contact mismatch: got %q", decoded.Humans[0].Contact)
	}
	if decoded.Humans[0].Role != "cab_approver" {
		t.Errorf("human role mismatch: got %q", decoded.Humans[0].Role)
	}
	if decoded.Workflow == nil || decoded.Workflow.MacroID != "macro.approval.v1" {
		t.Error("workflow MacroID not preserved")
	}
	if decoded.Intent.RemindAfterSeconds != 1800 {
		t.Errorf("RemindAfterSeconds mismatch: got %d", decoded.Intent.RemindAfterSeconds)
	}
	if decoded.Intent.MaxDeliveryAttempts != 5 {
		t.Errorf("MaxDeliveryAttempts mismatch: got %d", decoded.Intent.MaxDeliveryAttempts)
	}
	if decoded.Intent.Type != "approval.change.v1" {
		t.Errorf("Intent.Type mismatch: got %q", decoded.Intent.Type)
	}
}

func TestScenarioBundleJSON_OmitsEmptyOptionalFields(t *testing.T) {
	bundle := scenarioBundle{
		ScenarioID: "minimal.v1",
		Intent: scenarioIntentSettings{
			Type: "test.v1",
		},
	}

	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Optional empty fields must be omitted
	if _, ok := raw["description"]; ok {
		t.Error("empty description should be omitted from JSON")
	}
	if _, ok := raw["agents"]; ok {
		t.Error("nil agents should be omitted from JSON")
	}
	if _, ok := raw["workflow"]; ok {
		t.Error("nil workflow should be omitted from JSON")
	}
	if _, ok := raw["humans"]; ok {
		t.Error("nil humans should be omitted from JSON")
	}

	// intent.deadline_at should be omitted when empty
	intent := raw["intent"].(map[string]interface{})
	if _, ok := intent["deadline_at"]; ok {
		t.Error("empty deadline_at should be omitted from intent JSON")
	}
}

func TestScenarioBundleJSON_InvalidJSONReturnsError(t *testing.T) {
	var bundle scenarioBundle
	err := json.Unmarshal([]byte(`{not valid json`), &bundle)
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// scenarioIntentSettings zero-value omission
// ---------------------------------------------------------------------------

func TestScenarioIntentSettings_ZeroRemindersOmitted(t *testing.T) {
	settings := scenarioIntentSettings{
		Type: "test.v1",
		// all int fields zero
	}
	data, _ := json.Marshal(settings)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	for _, field := range []string{"remind_after_seconds", "remind_interval_seconds", "max_reminders", "max_delivery_attempts"} {
		if v, ok := raw[field]; ok {
			// Zero values with omitempty should not appear, OR appear as 0
			// Our struct uses omitempty so they must be absent
			t.Errorf("field %s should be omitted when zero, got %v", field, v)
		}
	}
}

func TestScenarioAgentEntry_CreateIfMissingDefaultsFalse(t *testing.T) {
	var entry scenarioAgentEntry
	if entry.CreateIfMissing {
		t.Error("CreateIfMissing should default to false")
	}
}
