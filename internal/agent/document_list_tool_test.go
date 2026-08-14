package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/document"
)

type documentReaderStub struct {
	documents       []document.Document
	err             error
	knowledgeBaseID int64
}

func (s *documentReaderStub) List(_ context.Context, knowledgeBaseID int64) ([]document.Document, error) {
	s.knowledgeBaseID = knowledgeBaseID
	return s.documents, s.err
}

func TestDocumentListToolReturnsScopedMetadata(t *testing.T) {
	reader := &documentReaderStub{documents: []document.Document{
		{ID: 8, OriginalFilename: "guide.md", ContentType: "text/markdown", SizeBytes: 128, ProcessingStatus: "succeeded"},
	}}
	tool, err := agent.NewDocumentListToolForKnowledgeBase(reader, 7, 4096, 20)
	if err != nil {
		t.Fatalf("NewDocumentListToolForKnowledgeBase() error = %v", err)
	}

	result, err := tool.Call(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if reader.knowledgeBaseID != 7 {
		t.Fatalf("knowledge base ID = %d, want 7", reader.knowledgeBaseID)
	}
	var payload struct {
		Documents []struct {
			Filename string `json:"original_filename"`
			Status   string `json:"processing_status"`
		} `json:"documents"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("result JSON = %q: %v", result.Content, err)
	}
	if len(payload.Documents) != 1 || payload.Documents[0].Filename != "guide.md" || payload.Documents[0].Status != "succeeded" {
		t.Fatalf("documents = %#v", payload.Documents)
	}
}

func TestDocumentListToolRejectsModelKnowledgeBaseArgument(t *testing.T) {
	tool, err := agent.NewDocumentListToolForKnowledgeBase(&documentReaderStub{}, 7, 4096, 20)
	if err != nil {
		t.Fatalf("NewDocumentListToolForKnowledgeBase() error = %v", err)
	}
	_, err = tool.Call(context.Background(), []byte(`{"knowledge_base_id":99}`))
	if !errors.Is(err, agent.ErrInvalidDocumentListInput) {
		t.Fatalf("Call() error = %v, want ErrInvalidDocumentListInput", err)
	}
}

func TestDocumentListToolReturnsReaderFailure(t *testing.T) {
	expected := errors.New("reader failed")
	tool, err := agent.NewDocumentListToolForKnowledgeBase(&documentReaderStub{err: expected}, 7, 4096, 20)
	if err != nil {
		t.Fatalf("NewDocumentListToolForKnowledgeBase() error = %v", err)
	}
	_, err = tool.Call(context.Background(), []byte(`{}`))
	if !errors.Is(err, expected) {
		t.Fatalf("Call() error = %v, want wrapped reader failure", err)
	}
}

func TestDocumentInfoToolReturnsOneDocument(t *testing.T) {
	reader := &documentReaderStub{documents: []document.Document{
		{ID: 8, OriginalFilename: "guide.md", ContentType: "text/markdown", SizeBytes: 128, ProcessingStatus: "succeeded"},
	}}
	tool, err := agent.NewDocumentInfoToolForKnowledgeBase(reader, 7, 4096)
	if err != nil {
		t.Fatalf("NewDocumentInfoToolForKnowledgeBase() error = %v", err)
	}
	result, err := tool.Call(context.Background(), []byte(`{"document_id":8}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(result.Content, `"original_filename":"guide.md"`) {
		t.Fatalf("result = %s", result.Content)
	}
}

func TestDocumentInfoToolRejectsDocumentOutsideScope(t *testing.T) {
	reader := &documentReaderStub{documents: []document.Document{{ID: 8}}}
	tool, err := agent.NewDocumentInfoToolForKnowledgeBase(reader, 7, 4096)
	if err != nil {
		t.Fatalf("NewDocumentInfoToolForKnowledgeBase() error = %v", err)
	}
	_, err = tool.Call(context.Background(), []byte(`{"document_id":99}`))
	if !errors.Is(err, document.ErrDocumentNotFound) {
		t.Fatalf("Call() error = %v, want ErrDocumentNotFound", err)
	}
}
