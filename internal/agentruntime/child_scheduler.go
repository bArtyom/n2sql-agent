package agentruntime

import (
	"context"
	"errors"
)

const DefaultChildAgentConcurrency = 3

var ErrInvalidChildScheduler = errors.New("child agent scheduler concurrency must be positive")

// ChildScheduler bounds child Agent executions across all parent Runs in one
// process. The parent Engine may invoke several read-only tools concurrently;
// this scheduler prevents those calls from creating an unbounded number of
// nested model executions.
type ChildScheduler interface {
	Run(context.Context, func(context.Context) error) error
}

type BoundedChildScheduler struct {
	slots chan struct{}
}

func NewBoundedChildScheduler(concurrency int) (*BoundedChildScheduler, error) {
	if concurrency <= 0 {
		return nil, ErrInvalidChildScheduler
	}
	return &BoundedChildScheduler{slots: make(chan struct{}, concurrency)}, nil
}

func (s *BoundedChildScheduler) Run(ctx context.Context, task func(context.Context) error) error {
	if s == nil || cap(s.slots) == 0 {
		return ErrInvalidChildScheduler
	}
	if ctx == nil {
		return context.Canceled
	}
	if task == nil {
		return errors.New("child agent task is required")
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	defer func() {
		<-s.slots
	}()
	return task(ctx)
}
