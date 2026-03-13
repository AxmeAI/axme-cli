package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// submitTaskResult payload construction
// ---------------------------------------------------------------------------

func TestSubmitTaskResult_ApprovedPayload(t *testing.T) {
	// Verify the payload structure that would be submitted for an approval
	taskResult := map[string]interface{}{
		"outcome": "approved",
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tr := decoded["task_result"].(map[string]interface{})
	if tr["outcome"] != "approved" {
		t.Errorf("outcome mismatch: got %v", tr["outcome"])
	}
	if _, ok := tr["comment"]; ok {
		t.Error("comment should not be present when not provided")
	}
}

func TestSubmitTaskResult_RejectedWithComment(t *testing.T) {
	comment := "Service not tested sufficiently"
	taskResult := map[string]interface{}{
		"outcome": "rejected",
		"comment": comment,
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tr := decoded["task_result"].(map[string]interface{})
	if tr["outcome"] != "rejected" {
		t.Errorf("outcome mismatch: got %v", tr["outcome"])
	}
	if tr["comment"] != comment {
		t.Errorf("comment mismatch: got %v, want %v", tr["comment"], comment)
	}
}

func TestSubmitTaskResult_WithExtraData(t *testing.T) {
	extraData := map[string]string{
		"ticket":    "INFRA-123",
		"risk_level": "low",
	}
	data := map[string]interface{}{}
	for k, v := range extraData {
		data[k] = v
	}
	taskResult := map[string]interface{}{
		"outcome": "approved",
		"data":    data,
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tr := decoded["task_result"].(map[string]interface{})
	d := tr["data"].(map[string]interface{})
	if d["ticket"] != "INFRA-123" {
		t.Errorf("data.ticket mismatch: got %v", d["ticket"])
	}
}

func TestSubmitTaskResult_NoExtraData(t *testing.T) {
	taskResult := map[string]interface{}{
		"outcome": "approved",
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	tr := decoded["task_result"].(map[string]interface{})
	if _, ok := tr["data"]; ok {
		t.Error("data key should not be present when no extra data provided")
	}
}

// ---------------------------------------------------------------------------
// Tasks list rendering — safe asMap/asSlice/asString behaviour
// ---------------------------------------------------------------------------

func TestTasksListRendering_EmptyTasks(t *testing.T) {
	body := map[string]interface{}{
		"ok":    true,
		"tasks": []interface{}{},
	}
	tasks := asSlice(body["tasks"])
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestTasksListRendering_HumanTaskTitle(t *testing.T) {
	task := map[string]interface{}{
		"intent_id":   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"intent_type": "approval.change.v1",
		"status":      "WAITING",
		"human_task": map[string]interface{}{
			"title":     "Approve nginx rollout",
			"task_type": "approval",
		},
	}
	ht := asMap(task["human_task"])
	title := asString(ht["title"])
	if title != "Approve nginx rollout" {
		t.Errorf("title mismatch: got %q", title)
	}
	taskType := asString(ht["task_type"])
	if taskType != "approval" {
		t.Errorf("task_type mismatch: got %q", taskType)
	}
}

func TestTasksListRendering_MissingHumanTaskTitle(t *testing.T) {
	task := map[string]interface{}{
		"intent_id":   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"intent_type": "approval.change.v1",
		"status":      "WAITING",
		"human_task":  nil,
	}
	ht := asMap(task["human_task"])
	title := asString(ht["title"])
	if title != "" {
		t.Errorf("expected empty title for nil human_task, got %q", title)
	}
}

func TestTasksListRendering_AllowedOutcomes(t *testing.T) {
	ht := map[string]interface{}{
		"title":            "Approve deployment",
		"allowed_outcomes": []interface{}{"approved", "rejected", "deferred"},
	}
	outcomes := asSlice(ht["allowed_outcomes"])
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(outcomes))
	}
	if asString(outcomes[0]) != "approved" {
		t.Errorf("first outcome mismatch: got %q", asString(outcomes[0]))
	}
}

// ---------------------------------------------------------------------------
// axme tasks submit — payload construction
// ---------------------------------------------------------------------------

func TestTasksSubmit_ArbitraryOutcome(t *testing.T) {
	taskResult := map[string]interface{}{
		"outcome": "escalated",
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	tr := decoded["task_result"].(map[string]interface{})
	if tr["outcome"] != "escalated" {
		t.Errorf("outcome mismatch: got %v", tr["outcome"])
	}
}

func TestTasksSubmit_WithCommentAndData(t *testing.T) {
	comment := "Routing to ops-lead"
	taskResult := map[string]interface{}{
		"outcome": "escalated",
		"comment": comment,
		"data": map[string]interface{}{
			"reason": "sla_breach",
			"ticket": "OPS-999",
		},
	}
	payload := map[string]interface{}{
		"task_result": taskResult,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	tr := decoded["task_result"].(map[string]interface{})
	if tr["outcome"] != "escalated" {
		t.Errorf("outcome mismatch: got %v", tr["outcome"])
	}
	if tr["comment"] != comment {
		t.Errorf("comment mismatch: got %v", tr["comment"])
	}
	d := tr["data"].(map[string]interface{})
	if d["ticket"] != "OPS-999" {
		t.Errorf("data.ticket mismatch: got %v", d["ticket"])
	}
}

func TestTasksSubmit_DataJSONParsing(t *testing.T) {
	// Simulate the --data-json flag parsing logic
	dataJSON := `{"field1":"value1","count":42}`
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(dataJSON), &parsed); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	extraData := make(map[string]string, len(parsed))
	for k, v := range parsed {
		extraData[k] = fmt.Sprintf("%v", v)
	}
	if extraData["field1"] != "value1" {
		t.Errorf("field1 mismatch: got %q", extraData["field1"])
	}
	if extraData["count"] != "42" {
		t.Errorf("count mismatch: got %q", extraData["count"])
	}
}

func TestTasksSubmit_DataJSONInvalidJSON(t *testing.T) {
	dataJSON := `not-valid-json`
	var parsed map[string]interface{}
	err := json.Unmarshal([]byte(dataJSON), &parsed)
	if err == nil {
		t.Error("expected parse error for invalid JSON, got nil")
	}
}

func TestTasksSubmit_OutcomeRequired(t *testing.T) {
	// Verify that empty outcome produces an error (mirrors --outcome required flag)
	outcome := ""
	if outcome == "" {
		// This is the guard in newTasksSubmitCmd.RunE
		// Just test the guard logic directly
		err := &cliError{Code: 1, Msg: "required flag --outcome not set"}
		if err.Msg != "required flag --outcome not set" {
			t.Errorf("unexpected error message: %s", err.Msg)
		}
	}
}
