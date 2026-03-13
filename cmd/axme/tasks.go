package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// axme tasks  — human task inbox (GET /v1/me/tasks)
// ---------------------------------------------------------------------------

func newTasksCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tasks",
		Aliases: []string{"task"},
		Short:   "Human task inbox — view and act on tasks assigned to you",
		Long: `List and manage human tasks assigned to the authenticated actor.

These are intents in WAITING/WAITING_FOR_HUMAN state where you are the assignee.
Authenticate with 'axme login' first; the actor bearer token is sent automatically.`,
	}
	cmd.AddCommand(
		newTasksListCmd(rt),
		newTasksGetCmd(rt),
		newTasksApproveCmd(rt),
		newTasksRejectCmd(rt),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// axme tasks list
// ---------------------------------------------------------------------------

func newTasksListCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List pending tasks assigned to you",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := rt.effectiveContext()
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/me/tasks", nil, nil, true)
			if err != nil {
				return err
			}
			if status >= 400 {
				detail := asString(body["detail"])
				if detail == "" {
					detail = fmt.Sprintf("HTTP %d", status)
				}
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to list tasks: %s", detail)}
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(body)
				return nil
			}

			tasks := asSlice(body["tasks"])
			if len(tasks) == 0 {
				fmt.Println("No pending tasks.")
				return nil
			}

			fmt.Printf("%-38s  %-32s  %s\n", "INTENT ID", "TYPE", "TITLE")
			fmt.Println(strings.Repeat("─", 90))
			for _, raw := range tasks {
				t := asMap(raw)
				ht := asMap(t["human_task"])
				title := asString(ht["title"])
				if title == "" {
					title = "(no title)"
				}
				fmt.Printf("%-38s  %-32s  %s\n",
					asString(t["intent_id"]),
					asString(t["intent_type"]),
					title,
				)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// axme tasks get <intent_id>
// ---------------------------------------------------------------------------

func newTasksGetCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "get <intent_id>",
		Short: "Show details of a specific task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentID := args[0]
			ctx := rt.effectiveContext()
			// Fetch via the standard intent GET (no actor token required)
			status, body, _, err := rt.request(cmd.Context(), ctx, "GET", "/v1/intents/"+intentID, nil, nil, true)
			if err != nil {
				return err
			}
			if status == 404 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("task not found: %s", intentID)}
			}
			if status >= 400 {
				return &cliError{Code: 1, Msg: fmt.Sprintf("failed to get task (HTTP %d)", status)}
			}

			if rt.outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(body)
				return nil
			}

			intent := asMap(body["intent"])
			fmt.Printf("Intent ID:  %s\n", asString(intent["intent_id"]))
			fmt.Printf("Type:       %s\n", asString(intent["intent_type"]))
			fmt.Printf("Status:     %s\n", asString(intent["status"]))
			fmt.Printf("From:       %s\n", asString(intent["from_agent"]))

			ht := asMap(intent["human_task"])
			if len(ht) > 0 {
				fmt.Println()
				fmt.Printf("Task Title: %s\n", asString(ht["title"]))
				if desc := asString(ht["description"]); desc != "" {
					fmt.Printf("Description:\n  %s\n", desc)
				}
				if taskType := asString(ht["task_type"]); taskType != "" {
					fmt.Printf("Task Type:  %s\n", taskType)
				}
				if outcomes := asSlice(ht["allowed_outcomes"]); len(outcomes) > 0 {
					outs := make([]string, 0, len(outcomes))
					for _, o := range outcomes {
						outs = append(outs, asString(o))
					}
					fmt.Printf("Outcomes:   %s\n", strings.Join(outs, ", "))
				}
			}

			if dueAt := asString(intent["due_at"]); dueAt != "" {
				fmt.Printf("Due at:     %s\n", dueAt)
			}

			fmt.Printf("\nTo approve: axme tasks approve %s\n", intentID)
			fmt.Printf("To reject:  axme tasks reject %s\n", intentID)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// axme tasks approve <intent_id> [--comment <text>]
// ---------------------------------------------------------------------------

func newTasksApproveCmd(rt *runtime) *cobra.Command {
	var comment string
	var data map[string]string

	cmd := &cobra.Command{
		Use:   "approve <intent_id>",
		Short: "Approve a pending task (submits task_result with outcome=approved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentID := args[0]
			return submitTaskResult(rt, cmd, intentID, "approved", comment, data)
		},
	}
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Optional comment for the approver decision")
	cmd.Flags().StringToStringVar(&data, "data", nil, "Additional key=value data fields in task_result")
	return cmd
}

// ---------------------------------------------------------------------------
// axme tasks reject <intent_id> [--comment <text>]
// ---------------------------------------------------------------------------

func newTasksRejectCmd(rt *runtime) *cobra.Command {
	var comment string
	var data map[string]string

	cmd := &cobra.Command{
		Use:   "reject <intent_id>",
		Short: "Reject a pending task (submits task_result with outcome=rejected)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intentID := args[0]
			return submitTaskResult(rt, cmd, intentID, "rejected", comment, data)
		},
	}
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Reason for rejecting the task")
	cmd.Flags().StringToStringVar(&data, "data", nil, "Additional key=value data fields in task_result")
	return cmd
}

// ---------------------------------------------------------------------------
// Shared: submit task_result via POST /v1/intents/{id}/resume
// ---------------------------------------------------------------------------

func submitTaskResult(rt *runtime, cmd *cobra.Command, intentID, outcome, comment string, extraData map[string]string) error {
	ctx := rt.effectiveContext()

	taskResult := map[string]interface{}{
		"outcome": outcome,
	}
	if comment != "" {
		taskResult["comment"] = comment
	}
	if len(extraData) > 0 {
		data := map[string]interface{}{}
		for k, v := range extraData {
			data[k] = v
		}
		taskResult["data"] = data
	}

	payload := map[string]interface{}{
		"task_result": taskResult,
	}

	status, body, _, err := rt.request(cmd.Context(), ctx, "POST", "/v1/intents/"+intentID+"/resume", nil, payload, true)
	if err != nil {
		return err
	}
	if status >= 400 {
		detail := asString(body["detail"])
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", status)
		}
		return &cliError{Code: 1, Msg: fmt.Sprintf("failed to submit task result: %s", detail)}
	}

	if rt.outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(body)
		return nil
	}

	verb := "approved"
	if outcome == "rejected" {
		verb = "rejected"
	}
	fmt.Printf("✓ Task %s: %s\n", verb, intentID)
	fmt.Printf("\nTrack with: axme intents get %s\n", intentID)
	return nil
}
