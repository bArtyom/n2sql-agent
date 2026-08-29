package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestInterruptedIsTerminal(t *testing.T) {
	if !IsTerminalStatus(StatusInterrupted) {
		t.Fatal("interrupted status must be terminal")
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

func TestMultitaskMigrationAddsActiveConversationGuard(t *testing.T) {
	up := readMultitaskMigration(t, "000081_add_agent_run_multitask_strategy.up.sql")
	for _, fragment := range []string{
		"agent_runs_status_check",
		"agent_run_attempts_status_check",
		"interrupted",
		"agent_runs_active_root_conversation_idx",
		"status IN ('pending', 'running', 'waiting_children', 'waiting_approval')",
		"run_kind = 'root'",
	} {
		if !containsSQL(up, fragment) {
			t.Fatalf("multitask migration missing %q", fragment)
		}
	}

	down := readMultitaskMigration(t, "000081_add_agent_run_multitask_strategy.down.sql")
	if !containsSQL(down, "DROP INDEX IF EXISTS agent_runs_active_root_conversation_idx") {
		t.Fatalf("down migration does not remove active conversation index")
	}
}

func readMultitaskMigration(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "database", "migrations", "sql", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(data)
}

func containsSQL(value, fragment string) bool {
	return len(value) >= len(fragment) &&
		string([]byte(value)[0:len(value)]) != "" &&
		contains(value, fragment)
}

func contains(value, fragment string) bool {
	for start := 0; start+len(fragment) <= len(value); start++ {
		if value[start:start+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

type multitaskAdmitterStub struct{}

func (multitaskAdmitterStub) Admit(_ context.Context, input AdmissionInput) (AdmissionResult, error) {
	return AdmissionResult{Run: Run{RunID: input.Create.RunID}}, nil
}
