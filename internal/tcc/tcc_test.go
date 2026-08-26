package tcc

import (
	"context"
	"errors"
	"testing"
)

type participantStub struct {
	tryCalls     int
	confirmCalls int
	cancelCalls  int
	tryErr       error
	confirmErr   error
}

func (p *participantStub) Try(context.Context, string) (Result, error) {
	p.tryCalls++
	if p.tryErr != nil {
		return Result{}, p.tryErr
	}
	return Result{Content: "reserved"}, nil
}

func (p *participantStub) Confirm(context.Context, string) error {
	p.confirmCalls++
	return p.confirmErr
}

func (p *participantStub) Cancel(context.Context, string) error {
	p.cancelCalls++
	return nil
}

func transactionRequest() TransactionRequest {
	return TransactionRequest{
		TransactionID: "txn-1",
		AgentRunID:    42,
		ToolName:      "write_tool",
		Arguments:     []byte(`{"value":"hello"}`),
	}
}

func participantSpecs(p *participantStub) []ParticipantSpec {
	return []ParticipantSpec{{OperationID: "op-1", Name: "write-branch", Participant: p}}
}

func TestCoordinatorExecuteConfirmsAllBranches(t *testing.T) {
	store := NewMemoryStore()
	coordinator, err := NewCoordinator(store)
	if err != nil {
		t.Fatal(err)
	}
	participant := &participantStub{}

	result, err := coordinator.Execute(context.Background(), transactionRequest(), participantSpecs(participant))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "reserved" {
		t.Fatalf("result content = %q, want reserved", result.Content)
	}
	if participant.tryCalls != 1 || participant.confirmCalls != 1 || participant.cancelCalls != 0 {
		t.Fatalf("calls = try:%d confirm:%d cancel:%d", participant.tryCalls, participant.confirmCalls, participant.cancelCalls)
	}
	transaction, err := store.GetTransaction(context.Background(), "txn-1")
	if err != nil {
		t.Fatal(err)
	}
	if transaction.State != StateConfirmed {
		t.Fatalf("transaction state = %q, want %q", transaction.State, StateConfirmed)
	}
}

func TestCoordinatorExecuteIsIdempotentAfterConfirmation(t *testing.T) {
	store := NewMemoryStore()
	coordinator, err := NewCoordinator(store)
	if err != nil {
		t.Fatal(err)
	}
	participant := &participantStub{}
	request := transactionRequest()
	specs := participantSpecs(participant)

	if _, err := coordinator.Execute(context.Background(), request, specs); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Execute(context.Background(), request, specs); err != nil {
		t.Fatal(err)
	}
	if participant.tryCalls != 1 || participant.confirmCalls != 1 {
		t.Fatalf("replayed calls = try:%d confirm:%d, want 1/1", participant.tryCalls, participant.confirmCalls)
	}
}

func TestCoordinatorTryFailureCancelsBranches(t *testing.T) {
	store := NewMemoryStore()
	coordinator, err := NewCoordinator(store)
	if err != nil {
		t.Fatal(err)
	}
	participant := &participantStub{tryErr: errors.New("downstream unavailable")}

	_, err = coordinator.Execute(context.Background(), transactionRequest(), participantSpecs(participant))
	if err == nil {
		t.Fatal("expected Try error")
	}
	if participant.cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want 1", participant.cancelCalls)
	}
	transaction, getErr := store.GetTransaction(context.Background(), "txn-1")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if transaction.State != StateCanceled {
		t.Fatalf("transaction state = %q, want %q", transaction.State, StateCanceled)
	}
}

func TestCoordinatorConfirmFailureRequiresRecovery(t *testing.T) {
	store := NewMemoryStore()
	coordinator, err := NewCoordinator(store)
	if err != nil {
		t.Fatal(err)
	}
	participant := &participantStub{confirmErr: errors.New("temporary failure")}

	_, err = coordinator.Execute(context.Background(), transactionRequest(), participantSpecs(participant))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	transaction, getErr := store.GetTransaction(context.Background(), "txn-1")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if transaction.State != StateConfirming {
		t.Fatalf("transaction state = %q, want %q", transaction.State, StateConfirming)
	}
}

func TestCoordinatorRecoverConfirmsDurableTryWithoutRepeatingTry(t *testing.T) {
	store := NewMemoryStore()
	coordinator, err := NewCoordinator(store)
	if err != nil {
		t.Fatal(err)
	}
	participant := &participantStub{}
	request := transactionRequest()
	if err := store.CreateTransaction(context.Background(), Transaction{
		ID: request.TransactionID, AgentRunID: request.AgentRunID, ToolName: request.ToolName,
		Arguments: request.Arguments, State: StateConfirming,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBranch(context.Background(), Branch{
		TransactionID: request.TransactionID, OperationID: "op-1", Participant: "write-branch",
		Arguments: request.Arguments, State: StateTried,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Recover(context.Background(), request.TransactionID, participantSpecs(participant)); err != nil {
		t.Fatal(err)
	}
	if participant.tryCalls != 0 || participant.confirmCalls != 1 {
		t.Fatalf("recovery calls = try:%d confirm:%d, want 0/1", participant.tryCalls, participant.confirmCalls)
	}
}
