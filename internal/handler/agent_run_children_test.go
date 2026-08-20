package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
)

type agentRunChildrenReaderStub struct {
	parent   agentrun.Run
	children []agentrun.Run
}

func (s agentRunChildrenReaderStub) Get(context.Context, string, int64) (agentrun.Run, error) {
	return s.parent, nil
}

func (s agentRunChildrenReaderStub) ListChildren(context.Context, int64, int64) ([]agentrun.Run, error) {
	return s.children, nil
}

func TestAgentRunChildrenReturnsExecutionTree(t *testing.T) {
	parent := agentrun.Run{ID: 1, RunID: "parent-1", KnowledgeBaseID: 7, RunKind: agentrun.KindRoot, Status: agentrun.StatusRunning}
	child := agentrun.Run{ID: 2, RunID: "child-1", ParentRunID: 1, KnowledgeBaseID: 7, RunKind: agentrun.KindChild, Status: agentrun.StatusSucceeded}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/parent-1/children", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "parent-1")
	response := httptest.NewRecorder()
	NewAgentRunChildren(agentRunChildrenReaderStub{parent: parent, children: []agentrun.Run{child}}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RunID    string `json:"run_id"`
		Children []struct {
			RunID       string `json:"run_id"`
			ParentRunID string `json:"parent_run_id"`
		} `json:"children"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.RunID != "parent-1" || len(body.Children) != 1 || body.Children[0].RunID != "child-1" || body.Children[0].ParentRunID != "parent-1" {
		t.Fatalf("tree = %#v", body)
	}
}
