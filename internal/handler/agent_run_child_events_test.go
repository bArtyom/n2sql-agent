package handler

import (
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
)

func TestWrapChildAgentEventKeepsChildIdentityAndSafeSummary(t *testing.T) {
	createdAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	event := wrapChildAgentEvent(
		agentrun.Run{RunID: "child-1"},
		agentservice.ChatRequest{ParentRunPublicID: "parent-1"},
		agent.Event{ID: "event-7", Type: agent.EventToolFinished, StepNumber: 2, CreatedAt: createdAt, Data: map[string]any{
			"tool_name": "knowledge_search", "result_summary": "5 条资料", "raw_content": "不应透传",
		}},
	)
	if event.Type != agent.EventChildEvent || event.RunID != "parent-1" || event.ID != "parent-1-child-event-7" {
		t.Fatalf("wrapped event = %#v", event)
	}
	data, ok := event.Data.(map[string]any)
	if !ok || data["child_run_id"] != "child-1" || data["child_event_type"] != string(agent.EventToolFinished) {
		t.Fatalf("child metadata = %#v", event.Data)
	}
	if _, leaked := data["raw_content"]; leaked {
		t.Fatalf("raw child payload leaked: %#v", data)
	}
}
