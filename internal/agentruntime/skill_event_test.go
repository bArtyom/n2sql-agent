package agentruntime_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type loadedSkillTool struct{}

func (loadedSkillTool) Name() string { return "skill_read" }

func (loadedSkillTool) Description() string { return "load a skill" }

func (loadedSkillTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (loadedSkillTool) Call(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "skill body", Metadata: map[string]any{
		"skill_name":        "pdf-processing",
		"skill_body_loaded": true,
	}}, nil
}

func TestEngineRecordsSkillLoadedAsRunStateAndEvent(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{ID: "call-skill", Type: "function", Function: modelclient.ToolCallFunction{Name: "skill_read", Arguments: `{"name":"pdf-processing"}`}}}}, nil
		}
		return modelclient.ChatResponse{Message: "已加载 Skill"}, nil
	}}
	registry := agent.NewToolRegistry()
	if err := registry.Register(loadedSkillTool{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	engine, err := agentruntime.NewEngine(chat, registry, 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event
	result, err := engine.RunWithEvents(context.Background(), "run-skill", []modelclient.ChatMessage{{Role: "user", Content: "读取 Skill"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	stats := result.Run.Stats()
	if len(stats.Skills) != 1 || stats.Skills[0] != "pdf-processing" {
		t.Fatalf("skills = %#v, want loaded skill reference", stats.Skills)
	}
	for _, event := range events {
		if event.Type == agent.EventSkillLoaded {
			return
		}
	}
	t.Fatal("events do not contain skill_loaded")
}
