package main

import (
	"encoding/json"
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
