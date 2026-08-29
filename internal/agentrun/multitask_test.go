package agentrun

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeMultitaskStrategy(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  MultitaskStrategy
	}{
		{name: "empty defaults to reject", want: MultitaskReject},
		{name: "trims and lowercases", input: "  INTERRUPT ", want: MultitaskInterrupt},
		{name: "rollback", input: "rollback", want: MultitaskRollback},
		{name: "reject", input: "reject", want: MultitaskReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeMultitaskStrategy(tt.input)
			if err != nil {
				t.Fatalf("NormalizeMultitaskStrategy(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeMultitaskStrategy(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}

	if _, err := NormalizeMultitaskStrategy("parallel"); !errors.Is(err, ErrInvalidMultitaskStrategy) {
		t.Fatalf("NormalizeMultitaskStrategy(\"parallel\") error = %v, want %v", err, ErrInvalidMultitaskStrategy)
	}
}

func TestActiveRunConflictCarriesRunMetadata(t *testing.T) {
	active := Run{RunID: "run-101", ConversationID: 42, Status: StatusRunning}
	err := &ActiveRunConflict{ActiveRun: active, Requested: MultitaskReject}

	var conflict *ActiveRunConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("errors.As() did not recover ActiveRunConflict from %T", err)
	}
	if conflict.ActiveRun.RunID != "run-101" || conflict.ActiveRun.Status != StatusRunning {
		t.Fatalf("conflict metadata = %#v", conflict.ActiveRun)
	}
	if conflict.Error() == "" {
		t.Fatal("ActiveRunConflict.Error() is empty")
	}
}

func TestMultitaskAdmitterContractIsUsable(t *testing.T) {
	var _ MultitaskAdmitter = multitaskAdmitterStub{}
	result, err := (multitaskAdmitterStub{}).Admit(context.Background(), AdmissionInput{
		Create:   CreateInput{RunID: "run-1", KnowledgeBaseID: 7},
		Strategy: MultitaskReject,
	})
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if result.Run.RunID != "run-1" {
		t.Fatalf("admission result = %#v", result)
	}
}

type multitaskAdmitterStub struct{}

func (multitaskAdmitterStub) Admit(_ context.Context, input AdmissionInput) (AdmissionResult, error) {
	return AdmissionResult{Run: Run{RunID: input.Create.RunID}}, nil
}
