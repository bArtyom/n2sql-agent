package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
)

var (
	ErrInvalidEvaluator = errors.New("invalid agent evaluator")
	ErrEmptyCases       = errors.New("agent evaluation cases are required")
	ErrInvalidCase      = errors.New("invalid agent evaluation case")
	ErrInvalidCaseFile  = errors.New("invalid agent evaluation case file")
)

// Case describes one deterministic evaluation question. The evaluator does
// not decide whether an answer is factually correct; it checks execution
// contracts such as success status, tool usage and step limits.
type Case struct {
	ID              string                        `json:"id"`
	KnowledgeBaseID int64                         `json:"knowledge_base_id"`
	Question        string                        `json:"question"`
	History         []agentservice.HistoryMessage `json:"history,omitempty"`
	ExpectedStatus  agent.RunStatus               `json:"expected_status,omitempty"`
	RequireToolCall bool                          `json:"require_tool_call,omitempty"`
	MaxSteps        int                           `json:"max_steps,omitempty"`
}

// LoadCases decodes and validates a JSON array of evaluation cases.
func LoadCases(reader io.Reader) ([]Case, error) {
	if reader == nil {
		return nil, ErrInvalidCaseFile
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var cases []Case
	if err := decoder.Decode(&cases); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrEmptyCases
		}
		return nil, ErrInvalidCaseFile
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidCaseFile
	}
	if len(cases) == 0 {
		return nil, ErrEmptyCases
	}
	for _, evaluationCase := range cases {
		if err := validateCase(evaluationCase); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

// LimitCases returns at most maxCases cases while preserving file order.
func LimitCases(cases []Case, maxCases int) ([]Case, error) {
	if maxCases <= 0 {
		return nil, ErrInvalidCase
	}
	if len(cases) <= maxCases {
		return append([]Case(nil), cases...), nil
	}
	return append([]Case(nil), cases[:maxCases]...), nil
}

type CaseResult struct {
	ID                  string          `json:"id"`
	Passed              bool            `json:"passed"`
	Status              agent.RunStatus `json:"status"`
	ErrorCategory       string          `json:"error_category,omitempty"`
	DurationMS          int64           `json:"duration_ms"`
	StepCount           int             `json:"step_count"`
	ToolCalls           int             `json:"tool_calls"`
	SuccessfulToolCalls int             `json:"successful_tool_calls"`
	FailedToolCalls     int             `json:"failed_tool_calls"`
	PromptTokens        int             `json:"prompt_tokens"`
	CompletionTokens    int             `json:"completion_tokens"`
	EmbeddingTokens     int             `json:"embedding_tokens"`
	TotalTokens         int             `json:"total_tokens"`
}

type Report struct {
	Total               int            `json:"total"`
	Passed              int            `json:"passed"`
	PassRate            float64        `json:"pass_rate"`
	AverageDurationMS   float64        `json:"average_duration_ms"`
	AverageSteps        float64        `json:"average_steps"`
	ToolCalls           int            `json:"tool_calls"`
	SuccessfulToolCalls int            `json:"successful_tool_calls"`
	FailedToolCalls     int            `json:"failed_tool_calls"`
	ToolSuccessRate     float64        `json:"tool_success_rate"`
	PromptTokens        int            `json:"prompt_tokens"`
	CompletionTokens    int            `json:"completion_tokens"`
	EmbeddingTokens     int            `json:"embedding_tokens"`
	TotalTokens         int            `json:"total_tokens"`
	FailureCategories   map[string]int `json:"failure_categories"`
	Cases               []CaseResult   `json:"cases"`
}

func Evaluate(ctx context.Context, answerer agentservice.Answerer, cases []Case) (Report, error) {
	if ctx == nil || answerer == nil {
		return Report{}, ErrInvalidEvaluator
	}
	if len(cases) == 0 {
		return Report{}, ErrEmptyCases
	}
	for _, evaluationCase := range cases {
		if err := validateCase(evaluationCase); err != nil {
			return Report{}, err
		}
	}

	report := Report{
		Total:             len(cases),
		FailureCategories: make(map[string]int),
		Cases:             make([]CaseResult, 0, len(cases)),
	}
	var totalDurationMS int64
	var totalSteps int
	for _, evaluationCase := range cases {
		result := evaluateCase(ctx, answerer, evaluationCase)
		report.Cases = append(report.Cases, result)
		if result.Passed {
			report.Passed++
		} else if result.ErrorCategory != "" {
			report.FailureCategories[result.ErrorCategory]++
		}
		totalDurationMS += result.DurationMS
		totalSteps += result.StepCount
		report.ToolCalls += result.ToolCalls
		report.SuccessfulToolCalls += result.SuccessfulToolCalls
		report.FailedToolCalls += result.FailedToolCalls
		report.PromptTokens += result.PromptTokens
		report.CompletionTokens += result.CompletionTokens
		report.EmbeddingTokens += result.EmbeddingTokens
		report.TotalTokens += result.TotalTokens
	}
	report.PassRate = float64(report.Passed) / float64(report.Total)
	report.AverageDurationMS = float64(totalDurationMS) / float64(report.Total)
	report.AverageSteps = float64(totalSteps) / float64(report.Total)
	if report.ToolCalls > 0 {
		report.ToolSuccessRate = float64(report.SuccessfulToolCalls) / float64(report.ToolCalls)
	}
	return report, nil
}

func validateCase(evaluationCase Case) error {
	if evaluationCase.ID == "" || strings.TrimSpace(evaluationCase.ID) != evaluationCase.ID {
		return ErrInvalidCase
	}
	if evaluationCase.KnowledgeBaseID <= 0 || strings.TrimSpace(evaluationCase.Question) == "" {
		return ErrInvalidCase
	}
	if evaluationCase.ExpectedStatus != "" && !validRunStatus(evaluationCase.ExpectedStatus) {
		return ErrInvalidCase
	}
	if evaluationCase.MaxSteps < 0 {
		return ErrInvalidCase
	}
	return nil
}

func evaluateCase(ctx context.Context, answerer agentservice.Answerer, evaluationCase Case) CaseResult {
	startedAt := time.Now()
	response, err := answerer.Answer(ctx, evaluationCase.KnowledgeBaseID, agentservice.ChatRequest{
		Message: evaluationCase.Question,
		History: evaluationCase.History,
	})
	result := metricsFromResponse(response, time.Since(startedAt))
	result.ID = evaluationCase.ID
	expectedStatus := evaluationCase.ExpectedStatus
	if expectedStatus == "" {
		expectedStatus = agent.RunSucceeded
	}
	result.Passed = result.Status == expectedStatus && (expectedStatus != agent.RunSucceeded || err == nil)
	if evaluationCase.RequireToolCall && result.SuccessfulToolCalls == 0 {
		result.Passed = false
		if result.ErrorCategory == "" {
			result.ErrorCategory = "missing_tool_call"
		}
	}
	if evaluationCase.MaxSteps > 0 && result.StepCount > evaluationCase.MaxSteps {
		result.Passed = false
		if result.ErrorCategory == "" {
			result.ErrorCategory = "step_limit_exceeded"
		}
	}
	if !result.Passed && result.ErrorCategory == "" {
		result.ErrorCategory = failureCategory(err, result.Status)
	}
	return result
}

func metricsFromResponse(response agentservice.Response, elapsed time.Duration) CaseResult {
	result := CaseResult{Status: response.Status, DurationMS: elapsed.Milliseconds(), StepCount: len(response.Steps)}
	if response.Stats != nil {
		stats := response.Stats
		if stats.Status != "" {
			result.Status = stats.Status
		}
		result.DurationMS = stats.DurationMS
		result.StepCount = stats.StepCount
		result.ToolCalls = stats.ToolCalls
		result.SuccessfulToolCalls = stats.SuccessfulToolCalls
		result.FailedToolCalls = stats.FailedToolCalls
		result.PromptTokens = stats.PromptTokens
		result.CompletionTokens = stats.CompletionTokens
		result.EmbeddingTokens = stats.EmbeddingTokens
		result.TotalTokens = stats.TotalTokens
		result.ErrorCategory = string(stats.FailureCategory)
		return result
	}
	for _, step := range response.Steps {
		if step.Kind != agent.StepToolCall {
			continue
		}
		result.ToolCalls++
		if step.Status == agent.StepSucceeded {
			result.SuccessfulToolCalls++
		} else if step.Status == agent.StepFailed {
			result.FailedToolCalls++
		}
	}
	return result
}

func failureCategory(err error, status agent.RunStatus) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	switch status {
	case agent.RunFailed:
		return "run_failed"
	case agent.RunCanceled:
		return "run_canceled"
	default:
		return "expectation_failed"
	}
}

func validRunStatus(status agent.RunStatus) bool {
	switch status {
	case agent.RunPending, agent.RunRunning, agent.RunSucceeded, agent.RunFailed, agent.RunCanceled:
		return true
	default:
		return false
	}
}
