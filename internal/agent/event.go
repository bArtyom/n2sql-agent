package agent

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidEvent = errors.New("invalid agent event")

const EventSchemaVersion = 1

type EventType string

const (
	EventRunStarted       EventType = "run_started"
	EventStepStarted      EventType = "step_started"
	EventToolCalled       EventType = "tool_called"
	EventToolFinished     EventType = "tool_finished"
	EventApprovalRequired EventType = "approval_required"
	EventApprovalResolved EventType = "approval_resolved"
	EventApprovalExpired  EventType = "approval_expired"
	EventReasoningDelta   EventType = "reasoning_delta"
	EventMessageDelta     EventType = "message_delta"
	EventRunFinished      EventType = "run_finished"
	EventRunFailed        EventType = "run_failed"
	EventRunCanceled      EventType = "run_canceled"
	EventRunInterrupted   EventType = "run_interrupted"
	EventChildEvent       EventType = "child_event"
	EventChildResult      EventType = "child_result"
	EventLoopDetected     EventType = "loop_detected"
	EventSkillLoaded      EventType = "skill_loaded"
)

// Event is an observable occurrence in one Agent run.
type Event struct {
	Version     int       `json:"version"`
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	Type        EventType `json:"type"`
	Category    string    `json:"category,omitempty"`
	TaskID      string    `json:"task_id,omitempty"`
	Seq         int64     `json:"seq,omitempty"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	StepNumber  int       `json:"step_number,omitempty"`
	Data        any       `json:"data,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func NewEvent(id, runID string, eventType EventType, data any) (Event, error) {
	if strings.TrimSpace(id) != id || id == "" {
		return Event{}, ErrInvalidEvent
	}
	if strings.TrimSpace(runID) != runID || runID == "" {
		return Event{}, ErrInvalidEvent
	}
	if !validEventType(eventType) {
		return Event{}, ErrInvalidEvent
	}
	return Event{
		Version:   EventSchemaVersion,
		ID:        id,
		RunID:     runID,
		Type:      eventType,
		Category:  EventCategory(eventType),
		Data:      data,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// EventCategory gives consumers a stable coarse-grained grouping without
// making them parse the event payload or infer meaning from event names.
func EventCategory(eventType EventType) string {
	switch eventType {
	case EventToolCalled, EventToolFinished:
		return "tool"
	case EventReasoningDelta, EventMessageDelta:
		return "output"
	case EventApprovalRequired, EventApprovalResolved, EventApprovalExpired,
		EventLoopDetected, EventSkillLoaded, EventChildEvent, EventChildResult:
		return "control"
	case EventRunStarted, EventStepStarted, EventRunFinished, EventRunFailed, EventRunCanceled, EventRunInterrupted:
		return "lifecycle"
	default:
		return "unknown"
	}
}

// IsTerminalEvent reports whether an event closes the current Agent run.
// Keeping this in the event contract prevents each transport from drifting
// when a new terminal lifecycle event is added.
func IsTerminalEvent(eventType EventType) bool {
	switch eventType {
	case EventRunFinished, EventRunFailed, EventRunCanceled, EventRunInterrupted:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventRunStarted,
		EventStepStarted,
		EventToolCalled,
		EventToolFinished,
		EventApprovalRequired,
		EventApprovalResolved,
		EventApprovalExpired,
		EventReasoningDelta,
		EventMessageDelta,
		EventRunFinished,
		EventRunFailed,
		EventRunCanceled,
		EventRunInterrupted,
		EventChildEvent,
		EventChildResult,
		EventLoopDetected:
		return true
	case EventSkillLoaded:
		return true
	default:
		return false
	}
}
