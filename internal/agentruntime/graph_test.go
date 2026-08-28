package agentruntime_test

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

type graphNode struct {
	name string
	next string
}

func (n graphNode) Name() string { return n.name }
func (n graphNode) Run(_ context.Context, state *agentruntime.AgentState) (agentruntime.Transition, error) {
	state.Values[n.name] = true
	return agentruntime.Transition{NextNode: n.next}, nil
}

func TestGraphRunsModelToolModelFinish(t *testing.T) {
	graph, err := agentruntime.NewGraph("model", 8,
		graphNode{name: "model", next: "tool"},
		graphNode{name: "tool", next: "model-2"},
		graphNode{name: "model-2", next: "finish"},
		graphNode{name: "finish"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := graph.Run(context.Background(), &agentruntime.AgentState{Values: map[string]any{}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, node := range []string{"model", "tool", "model-2", "finish"} {
		if state.Values[node] != true {
			t.Fatalf("node %q was not executed: %#v", node, state.Values)
		}
	}
}

func TestGraphRejectsUnknownNode(t *testing.T) {
	graph, err := agentruntime.NewGraph("start", 2, graphNode{name: "start", next: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Run(context.Background(), &agentruntime.AgentState{Values: map[string]any{}}); err == nil {
		t.Fatal("Run() error = nil, want unknown node error")
	}
}

func TestGraphResumesFromPersistedCurrentNode(t *testing.T) {
	graph, err := agentruntime.NewGraph("model", 3,
		graphNode{name: "model", next: "tool"},
		graphNode{name: "tool", next: "finish"},
		graphNode{name: "finish"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := graph.Run(context.Background(), &agentruntime.AgentState{CurrentNode: "tool", Values: map[string]any{}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Values["model"] == true || state.Values["tool"] != true || state.CurrentNode != "" {
		t.Fatalf("resumed state = %#v, want tool then finish", state)
	}
}
