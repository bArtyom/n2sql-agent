package agentstream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHubWaitApprovalResolvesDecision(t *testing.T) {
	hub := NewHub()
	if err := hub.Start("run-approval", 7); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() {
		decision, err := hub.WaitApproval(context.Background(), "run-approval", 7, "knowledge_search", []byte(`{"q":"go"}`))
		if err != nil {
			t.Errorf("WaitApproval() error = %v", err)
		}
		result <- decision.Approved
	}()
	time.Sleep(10 * time.Millisecond)
	if err := hub.ResolveApproval("run-approval", 7, true, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case approved := <-result:
		if !approved {
			t.Fatal("approval decision = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not resolve")
	}
}

func TestHubWaitApprovalCancellationClearsPendingState(t *testing.T) {
	hub := NewHub()
	if err := hub.Start("run-cancel", 7); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hub.WaitApproval(ctx, "run-cancel", 7, "knowledge_search", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitApproval() error = %v, want context.Canceled", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	resolved := make(chan error, 1)
	go func() {
		_, err := hub.WaitApproval(ctx2, "run-cancel", 7, "knowledge_search", nil)
		resolved <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := hub.ResolveApproval("run-cancel", 7, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-resolved; err != nil {
		t.Fatalf("second WaitApproval() error = %v", err)
	}
}
