package agent

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidEvent = errors.New("invalid agent event")

const EventSchemaVersion = 1

// StreamMode follows the three useful layers in DeerFlow's stream contract:
// state snapshots, model message deltas, and application-defined progress.
// The mode is transport metadata; the event Type remains the stable UI/API
// contract used by existing consumers.
type StreamMode string

const (
	StreamModeValues   StreamMode = "values"
	StreamModeMessages StreamMode = "messages"
	StreamModeCustom   StreamMode = "custom"
)

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
	EventChildEvent       EventType = "child_event"
)

func (eventType EventType) StreamMode() StreamMode {
	switch eventType {
	case EventReasoningDelta, EventMessageDelta:
		return StreamModeMessages
	case EventToolCalled, EventToolFinished, EventApprovalRequired, EventApprovalResolved,
		EventApprovalExpired, EventChildEvent:
		return StreamModeCustom
	default:
		return StreamModeValues
	}
}

// Event is an observable occurrence in one Agent run.
type Event struct {
	Version    int        `json:"version"`
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Type       EventType  `json:"type"`
	Mode       StreamMode `json:"mode,omitempty"`
	StepNumber int        `json:"step_number,omitempty"`
	Data       any        `json:"data,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
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
		Mode:      eventType.StreamMode(),
		Data:      data,
		CreatedAt: time.Now().UTC(),
	}, nil
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
		EventChildEvent:
		return true
	default:
		return false
	}
}
