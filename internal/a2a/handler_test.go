package a2a_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/a2a"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

type answererStub struct {
	response multiagent.Response
	err      error
}

func (s answererStub) Answer(context.Context, int64, string, int) (multiagent.Response, error) {
	return s.response, s.err
}

func TestHandlerServesAgentCard(t *testing.T) {
	server := httptest.NewServer(a2a.NewHandler(answererStub{}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatalf("GET agent card: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var card map[string]any
	if err := json.NewDecoder(response.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card["name"] != "n2sql-agent" {
		t.Fatalf("card name = %#v, want n2sql-agent", card["name"])
	}
	if _, ok := card["skills"]; !ok {
		t.Fatalf("card = %#v, want skills", card)
	}
}

func TestHandlerCreatesTaskAndReturnsResult(t *testing.T) {
	server := httptest.NewServer(a2a.NewHandler(answererStub{response: multiagent.Response{
		Answer: "服务通过 go run 启动",
	}}))
	defer server.Close()

	response, err := server.Client().Post(
		server.URL+"/api/a2a/tasks",
		"application/json",
		strings.NewReader(`{"knowledge_base_id":7,"message":"如何启动服务？","top_k":3}`),
	)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	var task struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if task.ID == "" || task.Status != "submitted" {
		t.Fatalf("task = %#v, want submitted task with id", task)
	}

	var result struct {
		Answer string `json:"answer"`
	}
	var lastStatus string
	for attempt := 0; attempt < 50; attempt++ {
		resultResponse, err := server.Client().Get(server.URL + "/api/a2a/tasks/" + task.ID + "/result")
		if err != nil {
			t.Fatalf("get result: %v", err)
		}
		if resultResponse.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resultResponse.Body).Decode(&result); err != nil {
				resultResponse.Body.Close()
				t.Fatalf("decode result: %v", err)
			}
			resultResponse.Body.Close()
			if result.Answer != "服务通过 go run 启动" {
				t.Fatalf("answer = %q, want completed answer", result.Answer)
			}
			return
		}
		var state struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resultResponse.Body).Decode(&state)
		resultResponse.Body.Close()
		lastStatus = state.Status
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task did not complete, last status = %q", lastStatus)
}

func TestHandlerValidatesTaskInputAndNotFound(t *testing.T) {
	server := httptest.NewServer(a2a.NewHandler(answererStub{}))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/a2a/tasks", "application/json", strings.NewReader(`{"knowledge_base_id":0,"message":" "}`))
	if err != nil {
		t.Fatalf("create invalid task: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}

	response, err = server.Client().Get(server.URL + "/api/a2a/tasks/missing")
	if err != nil {
		t.Fatalf("get missing task: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestHandlerPublishesFailedTaskWithoutInternalError(t *testing.T) {
	server := httptest.NewServer(a2a.NewHandler(answererStub{err: errors.New("provider secret detail")}))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/a2a/tasks", "application/json", strings.NewReader(`{"knowledge_base_id":7,"message":"问题"}`))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	defer response.Body.Close()
	var task struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	for attempt := 0; attempt < 50; attempt++ {
		stateResponse, err := server.Client().Get(server.URL + "/api/a2a/tasks/" + task.ID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		var state struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.NewDecoder(stateResponse.Body).Decode(&state); err != nil {
			stateResponse.Body.Close()
			t.Fatalf("decode state: %v", err)
		}
		stateResponse.Body.Close()
		if state.Status == "failed" {
			if strings.Contains(state.Error, "provider secret") {
				t.Fatalf("task exposed internal error: %q", state.Error)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task did not reach failed state")
}

func TestHandlerRecordsTaskMetrics(t *testing.T) {
	registry := metrics.New()
	server := httptest.NewServer(a2a.NewHandlerWithTimeoutAndMetrics(answererStub{
		response: multiagent.Response{Answer: "完成"},
	}, time.Second, registry))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/a2a/tasks", "application/json", strings.NewReader(`{"knowledge_base_id":7,"message":"问题"}`))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		response.Body.Close()
		t.Fatalf("decode task: %v", err)
	}
	response.Body.Close()

	waitForTask(t, server.Client(), server.URL, task.ID, "completed")
	metricsResponse := httptest.NewRecorder()
	registry.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsResponse.Body.String()
	for _, want := range []string{
		"a2a_tasks_submitted_total 1",
		"a2a_tasks_started_total 1",
		"a2a_tasks_completed_total 1",
		"a2a_tasks_failed_total 0",
		"a2a_task_duration_ms_total ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body = %q, want %q", body, want)
		}
	}
}

func waitForTask(t *testing.T, client *http.Client, baseURL, id, wantStatus string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		response, err := client.Get(baseURL + "/api/a2a/tasks/" + id)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		var state struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
			response.Body.Close()
			t.Fatalf("decode state: %v", err)
		}
		response.Body.Close()
		if state.Status == wantStatus {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s did not reach %q", id, wantStatus)
}
