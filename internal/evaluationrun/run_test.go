package evaluationrun_test

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
)

func TestIsTerminalOnlyMarksFinalStates(t *testing.T) {
	for _, status := range []evaluationrun.Status{evaluationrun.StatusPending, evaluationrun.StatusRunning} {
		if evaluationrun.IsTerminal(status) {
			t.Fatalf("%q should not be terminal", status)
		}
	}
	for _, status := range []evaluationrun.Status{evaluationrun.StatusSucceeded, evaluationrun.StatusFailed, evaluationrun.StatusCanceled} {
		if !evaluationrun.IsTerminal(status) {
			t.Fatalf("%q should be terminal", status)
		}
	}
}
