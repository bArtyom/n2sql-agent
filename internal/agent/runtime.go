package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var (
	ErrInvalidRunID           = errors.New("invalid agent run ID")
	ErrInvalidRunTransition   = errors.New("invalid agent run transition")
	ErrMissingRunError        = errors.New("agent run error is required")
	ErrInvalidStep            = errors.New("invalid agent step")
	ErrInvalidFailureCategory = errors.New("invalid agent failure category")
)

type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

type StepKind string

const (
	StepModelDecision StepKind = "model_decision"
	StepToolCall      StepKind = "tool_call"
	StepFinalAnswer   StepKind = "final_answer"
)

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
)

type FailureCategory string

const (
	FailureNone       FailureCategory = ""
	FailureModel      FailureCategory = "model_failed"
	FailureTool       FailureCategory = "tool_failed"
	FailureTimeout    FailureCategory = "timeout"
	FailureCanceled   FailureCategory = "canceled"
	FailureStepLimit  FailureCategory = "step_limit_exceeded"
	FailureValidation FailureCategory = "validation_failed"
	FailureInternal   FailureCategory = "internal_failed"
)

// Step records one observable unit in an Agent run.
type Step struct {
	Number   int        `json:"number"`
	Kind     StepKind   `json:"kind"`
	Status   StepStatus `json:"status"`
	ToolName string     `json:"tool_name,omitempty"`
}

// AgentRun tracks one complete execution of an Agent.
type AgentRun struct {
	id               string
	status           RunStatus
	finalAnswer      string
	errorMessage     string
	steps            []Step
	startedAt        time.Time
	finishedAt       time.Time
	modelCalls       int
	toolCalls        int
	successTools     int
	failedTools      int
	promptTokens     int
	completionTokens int
	embeddingTokens  int
	totalTokens      int
	failure          FailureCategory
	queryRewrite     usage.QueryRewriteObservation
	mu               sync.Mutex
}

// RunStats is a safe, bounded summary of one Agent execution. PromptTokens
// and CompletionTokens describe chat calls; EmbeddingTokens is kept separate,
// while TotalTokens is the sum of all reported chat and embedding tokens.
type RunStats struct {
	Status              RunStatus                      `json:"status"`
	StartedAt           time.Time                      `json:"started_at"`
	FinishedAt          time.Time                      `json:"finished_at"`
	DurationMS          int64                          `json:"duration_ms"`
	StepCount           int                            `json:"step_count"`
	ModelCalls          int                            `json:"model_calls"`
	ToolCalls           int                            `json:"tool_calls"`
	SuccessfulToolCalls int                            `json:"successful_tool_calls"`
	FailedToolCalls     int                            `json:"failed_tool_calls"`
	PromptTokens        int                            `json:"prompt_tokens"`
	CompletionTokens    int                            `json:"completion_tokens"`
	EmbeddingTokens     int                            `json:"embedding_tokens"`
	TotalTokens         int                            `json:"total_tokens"`
	FailureCategory     FailureCategory                `json:"failure_category,omitempty"`
	QueryRewrite        *usage.QueryRewriteObservation `json:"query_rewrite,omitempty"`
}

func NewAgentRun(id string) (*AgentRun, error) {
	if id == "" || strings.TrimSpace(id) != id {
		return nil, ErrInvalidRunID
	}
	return &AgentRun{id: id, status: RunPending, steps: make([]Step, 0)}, nil
}

func (r *AgentRun) ID() string {
	return r.id
}

func (r *AgentRun) Status() RunStatus {
	return r.status
}

func (r *AgentRun) FinalAnswer() string {
	return r.finalAnswer
}

func (r *AgentRun) ErrorMessage() string {
	return r.errorMessage
}

func (r *AgentRun) Steps() []Step {
	steps := make([]Step, len(r.steps))
	copy(steps, r.steps)
	return steps
}

func (r *AgentRun) Start() error {
	if r.status != RunPending {
		return fmt.Errorf("%w: start from %s", ErrInvalidRunTransition, r.status)
	}
	r.status = RunRunning
	r.startedAt = time.Now().UTC()
	return nil
}

func (r *AgentRun) Complete(answer string) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: complete from %s", ErrInvalidRunTransition, r.status)
	}
	r.status = RunSucceeded
	r.finalAnswer = answer
	r.finishedAt = time.Now().UTC()
	return nil
}

func (r *AgentRun) Fail(err error) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: fail from %s", ErrInvalidRunTransition, r.status)
	}
	if err == nil {
		return ErrMissingRunError
	}
	r.status = RunFailed
	r.errorMessage = err.Error()
	if r.failure == FailureNone {
		r.failure = FailureInternal
	}
	r.finishedAt = time.Now().UTC()
	return nil
}

func (r *AgentRun) Cancel(reason string) error {
	if r.status != RunPending && r.status != RunRunning {
		return fmt.Errorf("%w: cancel from %s", ErrInvalidRunTransition, r.status)
	}
	r.status = RunCanceled
	r.errorMessage = reason
	if r.failure == FailureNone {
		r.failure = FailureCanceled
	}
	r.finishedAt = time.Now().UTC()
	return nil
}

func (r *AgentRun) SetFailureCategory(category FailureCategory) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: set failure category from %s", ErrInvalidRunTransition, r.status)
	}
	if !validFailureCategory(category) {
		return ErrInvalidFailureCategory
	}
	r.failure = category
	return nil
}

func (r *AgentRun) RecordModelCall() error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: record model call from %s", ErrInvalidRunTransition, r.status)
	}
	r.modelCalls++
	return nil
}

func (r *AgentRun) RecordToolCall(success bool) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: record tool call from %s", ErrInvalidRunTransition, r.status)
	}
	r.toolCalls++
	if success {
		r.successTools++
	} else {
		r.failedTools++
	}
	return nil
}

// ObserveChatTokens adds provider-reported chat usage to this running Agent.
func (r *AgentRun) ObserveChatTokens(tokenUsage usage.TokenUsage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != RunRunning {
		return
	}
	r.promptTokens += nonNegative(tokenUsage.PromptTokens)
	r.completionTokens += nonNegative(tokenUsage.CompletionTokens)
	r.totalTokens += tokenUsage.EffectiveTotal()
}

// ObserveEmbeddingTokens adds provider-reported embedding usage to this run.
func (r *AgentRun) ObserveEmbeddingTokens(tokenUsage usage.TokenUsage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != RunRunning {
		return
	}
	total := tokenUsage.EffectiveTotal()
	r.embeddingTokens += total
	r.totalTokens += total
}

// ObserveQueryRewrite records bounded retrieval strategy information for the
// run. It is intentionally separate from token usage and contains no query.
func (r *AgentRun) ObserveQueryRewrite(observation usage.QueryRewriteObservation) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != RunRunning {
		return
	}
	r.queryRewrite.Enabled = r.queryRewrite.Enabled || observation.Enabled
	r.queryRewrite.Applied = r.queryRewrite.Applied || observation.Applied
	r.queryRewrite.Fallback = r.queryRewrite.Fallback || observation.Fallback
	r.queryRewrite.VariantCount += nonNegative(observation.VariantCount)
}

func (r *AgentRun) QueryRewriteSnapshot() usage.QueryRewriteObservation {
	if r == nil {
		return usage.QueryRewriteObservation{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queryRewrite
}

func (r *AgentRun) Stats() RunStats {
	if r == nil {
		return RunStats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	finishedAt := r.finishedAt
	if r.status == RunRunning {
		finishedAt = time.Now().UTC()
	}
	var durationMS int64
	if !r.startedAt.IsZero() && !finishedAt.IsZero() && finishedAt.After(r.startedAt) {
		durationMS = finishedAt.Sub(r.startedAt).Milliseconds()
	}
	stats := RunStats{
		Status:              r.status,
		StartedAt:           r.startedAt,
		FinishedAt:          finishedAt,
		DurationMS:          durationMS,
		StepCount:           len(r.steps),
		ModelCalls:          r.modelCalls,
		ToolCalls:           r.toolCalls,
		SuccessfulToolCalls: r.successTools,
		FailedToolCalls:     r.failedTools,
		PromptTokens:        r.promptTokens,
		CompletionTokens:    r.completionTokens,
		EmbeddingTokens:     r.embeddingTokens,
		TotalTokens:         r.totalTokens,
		FailureCategory:     r.failure,
	}
	if r.queryRewrite.Enabled {
		queryRewrite := r.queryRewrite
		stats.QueryRewrite = &queryRewrite
	}
	return stats
}

func (r *AgentRun) AddStep(step Step) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: add step from %s", ErrInvalidRunTransition, r.status)
	}
	if !validStepKind(step.Kind) {
		return ErrInvalidStep
	}
	if step.Status == "" {
		step.Status = StepPending
	}
	if !validStepStatus(step.Status) {
		return ErrInvalidStep
	}
	step.Number = len(r.steps) + 1
	r.steps = append(r.steps, step)
	return nil
}

func validStepKind(kind StepKind) bool {
	switch kind {
	case StepModelDecision, StepToolCall, StepFinalAnswer:
		return true
	default:
		return false
	}
}

func validStepStatus(status StepStatus) bool {
	switch status {
	case StepPending, StepRunning, StepSucceeded, StepFailed:
		return true
	default:
		return false
	}
}

func validFailureCategory(category FailureCategory) bool {
	switch category {
	case FailureNone, FailureModel, FailureTool, FailureTimeout, FailureCanceled, FailureStepLimit, FailureValidation, FailureInternal:
		return true
	default:
		return false
	}
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
