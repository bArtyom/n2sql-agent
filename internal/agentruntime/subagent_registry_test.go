package agentruntime_test

import (
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

func TestSubagentRegistryResolvesNamedBoundedConfig(t *testing.T) {
	registry, err := agentruntime.NewSubagentRegistry([]agentruntime.SubagentConfig{
		{Name: "research", SystemPrompt: "只读研究", Tools: []string{"knowledge_search"}, MaxSteps: 6, Timeout: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := registry.Get("research")
	if err != nil || config.MaxSteps != 6 || config.Timeout != time.Minute {
		t.Fatalf("Get() = %#v, %v", config, err)
	}
	if config.AllowsTool("delegate_research") {
		t.Fatal("subagent must not inherit the parent delegation tool")
	}
}

func TestSubagentRegistryRejectsUnknownOrUnsafeConfig(t *testing.T) {
	if _, err := agentruntime.NewSubagentRegistry([]agentruntime.SubagentConfig{{Name: "", MaxSteps: 1}}); err == nil {
		t.Fatal("empty subagent name unexpectedly accepted")
	}
	registry, err := agentruntime.NewSubagentRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("missing"); err == nil {
		t.Fatal("unknown subagent unexpectedly resolved")
	}
}
