package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
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

type documentChunkReaderStub struct {
	chunks          map[int]documentchunk.SearchResult
	err             error
	knowledgeBaseID int64
}

func (s *documentChunkReaderStub) Read(_ context.Context, knowledgeBaseID, _ int64, position int) (documentchunk.SearchResult, error) {
	s.knowledgeBaseID = knowledgeBaseID
	if s.err != nil {
		return documentchunk.SearchResult{}, s.err
	}
	chunk, ok := s.chunks[position]
	if !ok {
		return documentchunk.SearchResult{}, documentchunk.ErrChunkNotFound
	}
	return chunk, nil
}

func TestDocumentReadToolReturnsBoundedChunks(t *testing.T) {
	reader := &documentChunkReaderStub{chunks: map[int]documentchunk.SearchResult{
		0: {DocumentID: 8, OriginalFilename: "guide.md", Position: 0, Content: "第一段"},
		1: {DocumentID: 8, OriginalFilename: "guide.md", Position: 1, Content: "第二段"},
	}}
	tool, err := agent.NewDocumentReadToolForKnowledgeBase(reader, 7, 4096, 4)
	if err != nil {
		t.Fatalf("NewDocumentReadToolForKnowledgeBase() error = %v", err)
	}
	result, err := tool.Call(context.Background(), []byte(`{"document_id":8,"limit":2}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if reader.knowledgeBaseID != 7 || !strings.Contains(result.Content, "第一段") || !strings.Contains(result.Content, "第二段") {
		t.Fatalf("scope/content = %d/%q", reader.knowledgeBaseID, result.Content)
	}
}

func TestDocumentReadToolRejectsInvalidInputAndReaderFailure(t *testing.T) {
	tool, err := agent.NewDocumentReadToolForKnowledgeBase(&documentChunkReaderStub{}, 7, 4096, 4)
	if err != nil {
		t.Fatalf("NewDocumentReadToolForKnowledgeBase() error = %v", err)
	}
	if _, err := tool.Call(context.Background(), []byte(`{"knowledge_base_id":99}`)); !errors.Is(err, agent.ErrInvalidDocumentReadInput) {
		t.Fatalf("invalid input error = %v, want ErrInvalidDocumentReadInput", err)
	}
	expected := errors.New("chunk reader failed")
	failingTool, err := agent.NewDocumentReadToolForKnowledgeBase(&documentChunkReaderStub{err: expected}, 7, 4096, 4)
	if err != nil {
		t.Fatalf("NewDocumentReadToolForKnowledgeBase() error = %v", err)
	}
	if _, err := failingTool.Call(context.Background(), []byte(`{"document_id":8}`)); !errors.Is(err, expected) {
		t.Fatalf("reader failure = %v, want wrapped error", err)
	}
}
