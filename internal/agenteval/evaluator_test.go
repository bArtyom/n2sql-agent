package agenteval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agenteval"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
)

type answererStub struct {
	responses []agentservice.Response
	errors    []error
	index     int
}

func (s *answererStub) Answer(context.Context, int64, agentservice.ChatRequest) (agentservice.Response, error) {
	index := s.index
	s.index++
	return s.responses[index], s.errors[index]
}

func TestEvaluateReportsPassRateAndRuntimeMetrics(t *testing.T) {
	succeededStats := agent.RunStats{
		Status:              agent.RunSucceeded,
		DurationMS:          40,
		StepCount:           4,
		ToolCalls:           1,
		SuccessfulToolCalls: 1,
	}
	failedStats := agent.RunStats{
		Status:          agent.RunFailed,
		DurationMS:      20,
		StepCount:       2,
		ToolCalls:       1,
		FailedToolCalls: 1,
		FailureCategory: agent.FailureTool,
	}
	answerer := &answererStub{
		responses: []agentservice.Response{
			{Answer: "年假答案", Status: agent.RunSucceeded, Steps: []agent.Step{{Kind: agent.StepToolCall, Status: agent.StepSucceeded}}, Stats: &succeededStats},
			{Status: agent.RunFailed, Stats: &failedStats},
		},
		errors: []error{nil, errors.New("tool failed")},
	}

	report, err := agenteval.Evaluate(context.Background(), answerer, []agenteval.Case{
		{ID: "success", KnowledgeBaseID: 7, Question: "年假怎么计算？", RequireToolCall: true},
		{ID: "failure", KnowledgeBaseID: 7, Question: "查询失败案例"},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Total != 2 || report.Passed != 1 || report.PassRate != 0.5 {
		t.Fatalf("report = %#v, want one of two cases passed", report)
	}
	if report.AverageSteps != 3 || report.AverageDurationMS != 30 {
		t.Fatalf("report averages = %#v, want steps=3 duration=30ms", report)
	}
	if report.ToolCalls != 2 || report.SuccessfulToolCalls != 1 || report.FailedToolCalls != 1 {
		t.Fatalf("report tool metrics = %#v", report)
	}
	if report.ToolSuccessRate != 0.5 {
		t.Fatalf("tool success rate = %v, want 0.5", report.ToolSuccessRate)
	}
	if report.FailureCategories[string(agent.FailureTool)] != 1 {
		t.Fatalf("failure categories = %#v, want one tool failure", report.FailureCategories)
	}
}

func TestEvaluateRejectsInvalidInput(t *testing.T) {
	if _, err := agenteval.Evaluate(context.Background(), nil, []agenteval.Case{{ID: "case", KnowledgeBaseID: 7, Question: "问题"}}); !errors.Is(err, agenteval.ErrInvalidEvaluator) {
		t.Fatalf("nil answerer error = %v, want %v", err, agenteval.ErrInvalidEvaluator)
	}
	if _, err := agenteval.Evaluate(context.Background(), &answererStub{}, nil); !errors.Is(err, agenteval.ErrEmptyCases) {
		t.Fatalf("empty cases error = %v, want %v", err, agenteval.ErrEmptyCases)
	}
	if _, err := agenteval.Evaluate(context.Background(), &answererStub{}, []agenteval.Case{{ID: "", KnowledgeBaseID: 7, Question: "问题"}}); !errors.Is(err, agenteval.ErrInvalidCase) {
		t.Fatalf("invalid case error = %v, want %v", err, agenteval.ErrInvalidCase)
	}
}

func TestEvaluateAcceptsExpectedFailureStatus(t *testing.T) {
	answerer := &answererStub{
		responses: []agentservice.Response{{Status: agent.RunFailed}},
		errors:    []error{errors.New("expected failure")},
	}

	report, err := agenteval.Evaluate(context.Background(), answerer, []agenteval.Case{{
		ID:              "expected-failure",
		KnowledgeBaseID: 7,
		Question:        "验证失败路径",
		ExpectedStatus:  agent.RunFailed,
	}})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Passed != 1 || !report.Cases[0].Passed {
		t.Fatalf("report = %#v, want expected failure to pass", report)
	}
}
