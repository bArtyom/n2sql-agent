package agent

import (
	"errors"
	"strings"
	"time"
)

var ErrInvalidEvent = errors.New("invalid agent event")

type EventType string

const (
	EventRunStarted     EventType = "run_started"
	EventStepStarted    EventType = "step_started"
	EventToolCalled     EventType = "tool_called"
	EventToolFinished   EventType = "tool_finished"
	EventReasoningDelta EventType = "reasoning_delta"
	EventMessageDelta   EventType = "message_delta"
	EventRunFinished    EventType = "run_finished"
	EventRunFailed      EventType = "run_failed"
	EventRunCanceled    EventType = "run_canceled"
)

// Event is an observable occurrence in one Agent run.
type Event struct {
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	Type       EventType `json:"type"`
	StepNumber int       `json:"step_number,omitempty"`
	Data       any       `json:"data,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
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
		ID:        id,
		RunID:     runID,
		Type:      eventType,
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
		EventReasoningDelta,
		EventMessageDelta,
		EventRunFinished,
		EventRunFailed,
		EventRunCanceled:
		return true
	default:
		return false
	}
}
