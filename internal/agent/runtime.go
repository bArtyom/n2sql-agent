package agent

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidRunID         = errors.New("invalid agent run ID")
	ErrInvalidRunTransition = errors.New("invalid agent run transition")
	ErrMissingRunError      = errors.New("agent run error is required")
	ErrInvalidStep          = errors.New("invalid agent step")
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

// Step records one observable unit in an Agent run.
type Step struct {
	Number   int        `json:"number"`
	Kind     StepKind   `json:"kind"`
	Status   StepStatus `json:"status"`
	ToolName string     `json:"tool_name,omitempty"`
}

// AgentRun tracks one complete execution of an Agent.
type AgentRun struct {
	id           string
	status       RunStatus
	finalAnswer  string
	errorMessage string
	steps        []Step
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
	return nil
}

func (r *AgentRun) Complete(answer string) error {
	if r.status != RunRunning {
		return fmt.Errorf("%w: complete from %s", ErrInvalidRunTransition, r.status)
	}
	r.status = RunSucceeded
	r.finalAnswer = answer
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
	return nil
}

func (r *AgentRun) Cancel(reason string) error {
	if r.status != RunPending && r.status != RunRunning {
		return fmt.Errorf("%w: cancel from %s", ErrInvalidRunTransition, r.status)
	}
	r.status = RunCanceled
	r.errorMessage = reason
	return nil
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
