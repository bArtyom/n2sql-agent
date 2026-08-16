// Package agentstream keeps a short-lived copy of Agent SSE events so a
// browser can reconnect after a temporary network failure.
package agentstream

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

var (
	ErrRunNotFound            = errors.New("agent run not found")
	ErrRunAlreadyStarted      = errors.New("agent run already started")
	ErrApprovalNotFound       = errors.New("agent approval not found")
	ErrApprovalAlreadyPending = errors.New("agent approval already pending")
)

const (
	defaultMaxRuns         = 128
	defaultMaxEventsPerRun = 512
	defaultRunTTL          = 10 * time.Minute
)

// Event is the small transport-neutral event envelope used by the HTTP
// handler. Agent events and handler-only events share the same replay path.
type Event struct {
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	Type       string    `json:"type"`
	StepNumber int       `json:"step_number,omitempty"`
	Data       any       `json:"data,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type run struct {
	knowledgeBaseID int64
	// cancel stops the underlying Agent execution context. It is registered by
	// the HTTP handler after Start and invoked exactly once by Cancel; a nil
	// value means the run is finished or no cancel was registered.
	cancel      func()
	events      []Event
	subscribers map[chan Event]struct{}
	done        bool
	expiresAt   time.Time
	nextEventID uint64
	approval    *pendingApproval
}

type pendingApproval struct {
	toolName  string
	arguments string
	decision  chan bool
}

// Hub is an in-process, bounded event replay store. It is deliberately not a
// durable queue: a process restart loses active runs, while a later stage can
// replace this implementation with PostgreSQL/Redis without changing the
// Agent Engine or frontend protocol.
type Hub struct {
	mu              sync.Mutex
	runs            map[string]*run
	maxRuns         int
	maxEventsPerRun int
	ttl             time.Duration
}

func NewHub() *Hub {
	return &Hub{
		runs:            make(map[string]*run),
		maxRuns:         defaultMaxRuns,
		maxEventsPerRun: defaultMaxEventsPerRun,
		ttl:             defaultRunTTL,
	}
}

// NewRunID creates an opaque identifier that can safely be exposed to the
// reconnecting browser. The random suffix avoids collisions across handlers.
func NewRunID() string {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("agent-stream-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("agent-stream-%x", suffix[:])
}

// Start registers a run before the Agent goroutine begins emitting events.
func (h *Hub) Start(runID string, knowledgeBaseID int64) error {
	if h == nil || runID == "" || knowledgeBaseID <= 0 {
		return ErrRunNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(time.Now())
	if _, ok := h.runs[runID]; ok {
		return ErrRunAlreadyStarted
	}
	h.runs[runID] = &run{
		knowledgeBaseID: knowledgeBaseID,
		subscribers:     make(map[chan Event]struct{}),
		expiresAt:       time.Now().Add(h.ttl),
	}
	return nil
}

// RegisterCancel attaches the caller-provided cancel function to an active
// run. The handler owns the underlying context; the Hub only remembers how to
// stop the run so the stop endpoint can reach it without sharing context
// across handlers.
func (h *Hub) RegisterCancel(runID string, cancel func()) error {
	if h == nil || runID == "" || cancel == nil {
		return ErrRunNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	run.cancel = cancel
	return nil
}

// Cancel stops an active run by invoking its registered cancel function once.
// The run is scoped by knowledge base ID like Subscribe, so a stop request
// cannot cancel another knowledge base's run. Canceling a finished or
// unknown run is a no-op for the former and ErrRunNotFound for the latter.
func (h *Hub) Cancel(runID string, knowledgeBaseID int64) error {
	if h == nil || runID == "" || knowledgeBaseID <= 0 {
		return ErrRunNotFound
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok || run.knowledgeBaseID != knowledgeBaseID {
		h.mu.Unlock()
		return ErrRunNotFound
	}
	cancel := run.cancel
	run.cancel = nil
	h.mu.Unlock()
	// The cancel function is called outside the lock: it may synchronously
	// wake the Agent loop, which must not contend with hub mutations.
	if cancel != nil {
		cancel()
	}
	return nil
}

func (h *Hub) PublishAgent(event agent.Event) error {
	return h.Publish(Event{
		ID:         event.ID,
		RunID:      event.RunID,
		Type:       string(event.Type),
		StepNumber: event.StepNumber,
		Data:       event.Data,
		CreatedAt:  event.CreatedAt,
	})
}

func (h *Hub) Publish(event Event) error {
	if h == nil || event.RunID == "" || event.Type == "" {
		return ErrRunNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[event.RunID]
	if !ok {
		return ErrRunNotFound
	}
	if event.ID == "" {
		run.nextEventID++
		event.ID = fmt.Sprintf("%s-event-%d", event.RunID, run.nextEventID)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	run.events = append(run.events, event)
	if len(run.events) > h.maxEventsPerRun {
		run.events = run.events[len(run.events)-h.maxEventsPerRun:]
	}
	run.expiresAt = time.Now().Add(h.ttl)
	for subscriber := range run.subscribers {
		// The full event history remains available for a reconnect. A slow live
		// subscriber may miss this notification, but it can replay from the
		// last event it received instead of blocking the Agent run.
		select {
		case subscriber <- event:
		default:
		}
	}
	return nil
}

// Finish closes current subscribers after the final event has been published.
func (h *Hub) Finish(runID string) error {
	if h == nil || runID == "" {
		return ErrRunNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[runID]
	if !ok {
		return ErrRunNotFound
	}
	if run.done {
		return nil
	}
	run.done = true
	if run.approval != nil {
		run.approval.decision <- false
		run.approval = nil
	}
	run.expiresAt = time.Now().Add(h.ttl)
	for subscriber := range run.subscribers {
		close(subscriber)
		delete(run.subscribers, subscriber)
	}
	return nil
}

// WaitApproval blocks the Agent run until ResolveApproval is called or the
// run context is canceled. Only one tool approval may be pending per run.
func (h *Hub) WaitApproval(ctx context.Context, runID string, knowledgeBaseID int64, toolName string, arguments []byte) (bool, error) {
	if h == nil || ctx == nil || runID == "" || knowledgeBaseID <= 0 || toolName == "" {
		return false, ErrApprovalNotFound
	}
	approval := &pendingApproval{toolName: toolName, arguments: string(arguments), decision: make(chan bool, 1)}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok || run.knowledgeBaseID != knowledgeBaseID || run.done {
		h.mu.Unlock()
		return false, ErrApprovalNotFound
	}
	if run.approval != nil {
		h.mu.Unlock()
		return false, ErrApprovalAlreadyPending
	}
	run.approval = approval
	h.mu.Unlock()

	select {
	case approved := <-approval.decision:
		return approved, nil
	case <-ctx.Done():
		h.mu.Lock()
		if run.approval == approval {
			run.approval = nil
		}
		h.mu.Unlock()
		return false, ctx.Err()
	}
}

// ResolveApproval applies a user's decision to the current pending tool.
func (h *Hub) ResolveApproval(runID string, knowledgeBaseID int64, approved bool) error {
	if h == nil || runID == "" || knowledgeBaseID <= 0 {
		return ErrApprovalNotFound
	}
	h.mu.Lock()
	run, ok := h.runs[runID]
	if !ok || run.knowledgeBaseID != knowledgeBaseID || run.approval == nil {
		h.mu.Unlock()
		return ErrApprovalNotFound
	}
	approval := run.approval
	run.approval = nil
	h.mu.Unlock()
	approval.decision <- approved
	return nil
}

// Subscribe returns a snapshot followed by live events. The caller must call
// cancel when it stops consuming; the context only controls this subscription,
// not the underlying Agent run.
func (h *Hub) Subscribe(runID string, knowledgeBaseID int64, ctxDone <-chan struct{}) ([]Event, <-chan Event, func(), bool, error) {
	if h == nil || runID == "" || knowledgeBaseID <= 0 {
		return nil, nil, nil, false, ErrRunNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(time.Now())
	run, ok := h.runs[runID]
	if !ok || run.knowledgeBaseID != knowledgeBaseID {
		return nil, nil, nil, false, ErrRunNotFound
	}
	snapshot := append([]Event(nil), run.events...)
	if run.done {
		return snapshot, closedEvents(), func() {}, true, nil
	}
	live := make(chan Event, 256)
	run.subscribers[live] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := run.subscribers[live]; ok {
				delete(run.subscribers, live)
				close(live)
			}
			h.mu.Unlock()
		})
	}
	if ctxDone != nil {
		go func() {
			<-ctxDone
			cancel()
		}()
	}
	return snapshot, live, cancel, false, nil
}

func closedEvents() <-chan Event {
	channel := make(chan Event)
	close(channel)
	return channel
}

func (h *Hub) pruneLocked(now time.Time) {
	for runID, run := range h.runs {
		if now.Before(run.expiresAt) {
			continue
		}
		for subscriber := range run.subscribers {
			close(subscriber)
		}
		delete(h.runs, runID)
	}
	for len(h.runs) > h.maxRuns {
		oldestID := ""
		var oldest time.Time
		for runID, run := range h.runs {
			if oldestID == "" || run.expiresAt.Before(oldest) {
				oldestID, oldest = runID, run.expiresAt
			}
		}
		if oldestID == "" {
			break
		}
		for subscriber := range h.runs[oldestID].subscribers {
			close(subscriber)
		}
		delete(h.runs, oldestID)
	}
}

func (h *Hub) String() string {
	if h == nil {
		return "agent stream hub <nil>"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("agent stream hub runs=%d", len(h.runs))
}
