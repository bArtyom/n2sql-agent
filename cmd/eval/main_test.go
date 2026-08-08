package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agenteval"
)

func TestRunDryRunProducesReportWithoutExternalCalls(t *testing.T) {
	casePath := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(casePath, []byte(`[
		{"id":"first","knowledge_base_id":7,"question":"第一个问题"},
		{"id":"second","knowledge_base_id":7,"question":"第二个问题"}
	]`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--cases", casePath, "--dry-run", "--max-cases", "1"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	var report agenteval.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Total != 1 || report.Passed != 1 || report.Cases[0].ID != "first" {
		t.Fatalf("report = %#v, want one first case passed", report)
	}
	if !strings.Contains(stderr.String(), "no model or database calls") {
		t.Fatalf("stderr = %q, want dry-run notice", stderr.String())
	}
}

func TestRunRejectsConflictingModes(t *testing.T) {
	err := run(context.Background(), []string{"--live", "--dry-run"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("run() error = %v, want conflicting mode error", err)
	}
}

func TestRunHelpIsNotAnError(t *testing.T) {
	if err := run(context.Background(), []string{"--help"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(--help) error = %v, want nil", err)
	}
}
