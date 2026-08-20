package agentruntime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedChildSchedulerLimitsConcurrentChildren(t *testing.T) {
	scheduler, err := NewBoundedChildScheduler(2)
	if err != nil {
		t.Fatalf("NewBoundedChildScheduler() error = %v", err)
	}

	var active atomic.Int32
	var maximum atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 6; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := scheduler.Run(context.Background(), func(context.Context) error {
				current := active.Add(1)
				for {
					old := maximum.Load()
					if current <= old || maximum.CompareAndSwap(old, current) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
				return nil
			}); err != nil {
				t.Errorf("scheduler.Run() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if got := maximum.Load(); got > 2 {
		t.Fatalf("maximum concurrent children = %d, want <= 2", got)
	}
}

func TestBoundedChildSchedulerCancellationWhileWaiting(t *testing.T) {
	scheduler, err := NewBoundedChildScheduler(1)
	if err != nil {
		t.Fatalf("NewBoundedChildScheduler() error = %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = scheduler.Run(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Run(ctx, func(context.Context) error { t.Fatal("canceled task started"); return nil }); err != context.Canceled {
		t.Fatalf("scheduler.Run() error = %v, want context canceled", err)
	}
	close(release)
}
