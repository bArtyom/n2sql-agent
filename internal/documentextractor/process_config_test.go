package documentextractor_test

import (
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

func TestValidateProcessConfigAllowsParserOverrides(t *testing.T) {
	config := &documentextractor.ProcessConfig{
		ParserEngineRules: []documentextractor.ParserEngineRule{
			{FileTypes: []string{"pdf", "application/pdf"}, Engine: "mineru"},
		},
		ChunkingConfig: &documentextractor.ChunkingConfig{
			ChunkSize:       600,
			ChunkOverlap:    80,
			ParentChunkSize: 1800,
			ChildChunkSize:  600,
		},
		ParserEngineOverrides: map[string]string{"pdf_force_scanned": "true"},
	}
	if err := documentextractor.ValidateProcessConfig(config); err != nil {
		t.Fatalf("ValidateProcessConfig() error = %v", err)
	}
}

func TestValidateProcessConfigRejectsInvalidParserOverride(t *testing.T) {
	config := &documentextractor.ProcessConfig{ParserEngineOverrides: map[string]string{
		"pdf_force_scanned": "not-a-bool",
	}}
	if err := documentextractor.ValidateProcessConfig(config); err == nil {
		t.Fatal("ValidateProcessConfig() error = nil, want invalid parser override")
	}
}

func TestValidateProcessConfigRejectsInvalidChunkingConfig(t *testing.T) {
	cases := []*documentextractor.ProcessConfig{
		{ChunkingConfig: &documentextractor.ChunkingConfig{ChunkSize: 31}},
		{ChunkingConfig: &documentextractor.ChunkingConfig{ChunkSize: 100, ChunkOverlap: 100}},
		{ChunkingConfig: &documentextractor.ChunkingConfig{ParentChunkSize: 400, ChildChunkSize: 800}},
		{ChunkingConfig: &documentextractor.ChunkingConfig{Strategy: "unknown"}},
	}
	for index, config := range cases {
		if err := documentextractor.ValidateProcessConfig(config); err == nil {
			t.Fatalf("case %d: ValidateProcessConfig() error = nil", index)
		}
	}
}

func TestValidateProcessConfigAllowsNilAsKnowledgeBaseDefault(t *testing.T) {
	if err := documentextractor.ValidateProcessConfig(nil); err != nil {
		t.Fatalf("ValidateProcessConfig(nil) error = %v", err)
	}
}

func TestValidateProcessConfigRejectsUnsafeShape(t *testing.T) {
	cases := []*documentextractor.ProcessConfig{
		{ParserEngineRules: []documentextractor.ParserEngineRule{{Engine: "mineru", FileTypes: nil}}},
		{ParserEngineRules: []documentextractor.ParserEngineRule{{Engine: "", FileTypes: []string{"pdf"}}}},
		{ParserEngineRules: []documentextractor.ParserEngineRule{{Engine: "mineru", FileTypes: []string{strings.Repeat("x", 101)}}}},
	}
	for index, config := range cases {
		if err := documentextractor.ValidateProcessConfig(config); err == nil {
			t.Fatalf("case %d: ValidateProcessConfig() error = nil", index)
		}
	}
}
