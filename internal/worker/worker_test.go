package worker_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/worker"
)

type taskStoreStub struct {
	task         worker.Task
	claimErr     error
	claims       int
	succeeded    int64
	succeededErr error
	failed       struct {
		id      int64
		message string
	}
	failedErr error
}

type cacheInvalidatorStub struct {
	knowledgeBaseIDs []int64
}

func (s *cacheInvalidatorStub) ClearCache(knowledgeBaseID int64) {
	s.knowledgeBaseIDs = append(s.knowledgeBaseIDs, knowledgeBaseID)
}

func captureLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var output strings.Builder
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	return &output
}

type extractorStub struct{}

func (extractorStub) Extract(context.Context, string, string) (string, error) {
	return "source text", nil
}

type oversizedProtectedBlockExtractor struct{}

func (oversizedProtectedBlockExtractor) Extract(context.Context, string, string) (string, error) {
	return "```go\n" + strings.Repeat("x", 40) + "\n```", nil
}

type longTextExtractor struct{}

func (longTextExtractor) Extract(context.Context, string, string) (string, error) {
	return strings.Repeat("一段用于验证任务级分块配置的正文。", 20), nil
}

type richExtractorStub struct{}

func (richExtractorStub) Extract(context.Context, string, string) (string, error) {
	return "source text", nil
}

func (richExtractorStub) ExtractResult(context.Context, string, string) (documentextractor.ParseResult, error) {
	return documentextractor.ParseResult{
		Markdown: "source text",
		Images: []documentextractor.ImageAsset{{
			Filename: "diagram.png",
			MIMEType: "image/png",
			Data:     []byte("png"),
			Source:   "embedded",
		}},
		Metadata: map[string]string{"parser": "office"},
	}, nil
}

type engineRecordingExtractor struct {
	engine  string
	options map[string]string
}

func (s *engineRecordingExtractor) Extract(context.Context, string, string) (string, error) {
	return "source text", nil
}

func (s *engineRecordingExtractor) ExtractResultWithEngine(_ context.Context, _, _, engine string) (documentextractor.ParseResult, error) {
	s.engine = engine
	return documentextractor.ParseResult{Markdown: "source text"}, nil
}

func (s *engineRecordingExtractor) ExtractResultWithEngineOptions(_ context.Context, _, _, engine string, options map[string]string) (documentextractor.ParseResult, error) {
	s.engine = engine
	s.options = options
	return documentextractor.ParseResult{Markdown: "source text"}, nil
}

type parseResultStoreStub struct {
	documentID int64
	result     documentextractor.ParseResult
}

func (s *parseResultStoreStub) SaveParseResult(_ context.Context, documentID int64, result documentextractor.ParseResult) error {
	s.documentID, s.result = documentID, result
	return nil
}

type splitterStub struct{}

func (splitterStub) Split(string) []string { return []string{"first", "second"} }

type fixedSplitter struct{ chunks []string }

func (s fixedSplitter) Split(string) []string { return s.chunks }

type chunkStoreStub struct {
	chunks     []string
	embeddings [][]float32
}

type hierarchicalChunkStoreStub struct {
	parents      []documentchunk.ParentChunk
	children     []documentchunk.ChildChunk
	embeddings   [][]float32
	replacements int
}

func (s *hierarchicalChunkStoreStub) ReplaceHierarchical(_ context.Context, _ int64, parents []documentchunk.ParentChunk, children []documentchunk.ChildChunk, embeddings [][]float32) error {
	s.replacements++
	s.parents, s.children, s.embeddings = parents, children, embeddings
	return nil
}

func (s *chunkStoreStub) Replace(_ context.Context, _ int64, chunks []string, embeddings [][]float32) error {
	s.chunks, s.embeddings = chunks, embeddings
	return nil
}

type embedderStub struct{}

func (embedderStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{1}}, {Index: 1, Vector: []float32{2}}}}, nil
}

type matchingEmbedderStub struct{}

func (matchingEmbedderStub) Embed(_ context.Context, texts []string) (modelclient.EmbeddingResponse, error) {
	data := make([]modelclient.Embedding, len(texts))
	for index := range texts {
		data[index] = modelclient.Embedding{Index: index, Vector: []float32{float32(index + 1)}}
	}
	return modelclient.EmbeddingResponse{Data: data}, nil
}

type recordingEmbedderStub struct {
	batches [][]string
}

func (s *recordingEmbedderStub) Embed(_ context.Context, texts []string) (modelclient.EmbeddingResponse, error) {
	batchIndex := len(s.batches)
	s.batches = append(s.batches, append([]string(nil), texts...))
	data := make([]modelclient.Embedding, len(texts))
	for index := range texts {
		data[index] = modelclient.Embedding{Index: index, Vector: []float32{float32(batchIndex*10 + index)}}
	}
	return modelclient.EmbeddingResponse{Data: data}, nil
}

type failingBatchEmbedderStub struct {
	failAt int
	calls  int
}

func (s *failingBatchEmbedderStub) Embed(_ context.Context, texts []string) (modelclient.EmbeddingResponse, error) {
	if s.calls == s.failAt {
		return modelclient.EmbeddingResponse{}, errors.New("embedding service unavailable")
	}
	s.calls++
	data := make([]modelclient.Embedding, len(texts))
	for index := range texts {
		data[index] = modelclient.Embedding{Index: index, Vector: []float32{1}}
	}
	return modelclient.EmbeddingResponse{Data: data}, nil
}

func TestEmbeddingChunkingProcessorStoresMatchingVectors(t *testing.T) {
	store := &chunkStoreStub{}
	processor := worker.NewEmbeddingChunkingProcessor(extractorStub{}, splitterStub{}, store, embedderStub{})
	if err := processor(context.Background(), worker.Task{DocumentID: 4}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if len(store.chunks) != 2 || store.chunks[1] != "second" || store.embeddings[1][0] != 2 {
		t.Fatalf("chunks=%#v embeddings=%#v", store.chunks, store.embeddings)
	}
}

func TestEmbeddingChunkingProcessorUsesTaskParserEngineSnapshot(t *testing.T) {
	extractor := &engineRecordingExtractor{}
	processor := worker.NewEmbeddingChunkingProcessor(
		extractor,
		fixedSplitter{chunks: []string{"chunk"}},
		&chunkStoreStub{},
		matchingEmbedderStub{},
	)

	if err := processor(context.Background(), worker.Task{DocumentID: 4, ParserEngine: "mineru"}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if extractor.engine != "mineru" {
		t.Fatalf("parser engine = %q, want snapshot engine mineru", extractor.engine)
	}
}

func TestEmbeddingChunkingProcessorPassesParserOverridesToExtractor(t *testing.T) {
	extractor := &engineRecordingExtractor{}
	processor := worker.NewEmbeddingChunkingProcessor(
		extractor,
		fixedSplitter{chunks: []string{"chunk"}},
		&chunkStoreStub{},
		matchingEmbedderStub{},
	)
	if err := processor(context.Background(), worker.Task{
		DocumentID: 4,
		ProcessConfig: documentextractor.ProcessConfig{ParserEngineOverrides: map[string]string{
			"pdf_force_scanned": "true",
		}},
	}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if extractor.options["pdf_force_scanned"] != "true" {
		t.Fatalf("parser options = %#v", extractor.options)
	}
}

func TestEmbeddingChunkingProcessorBatchesEmbeddingRequests(t *testing.T) {
	parts := make([]string, 11)
	for index := range parts {
		parts[index] = fmt.Sprintf("chunk-%d", index)
	}
	store := &chunkStoreStub{}
	embedder := &recordingEmbedderStub{}
	processor := worker.NewEmbeddingChunkingProcessor(extractorStub{}, fixedSplitter{chunks: parts}, store, embedder)

	if err := processor(context.Background(), worker.Task{DocumentID: 4}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if len(embedder.batches) != 2 || len(embedder.batches[0]) != 10 || len(embedder.batches[1]) != 1 {
		t.Fatalf("embedding batches = %#v, want sizes [10 1]", embedder.batches)
	}
	if len(store.chunks) != len(parts) || store.chunks[10] != "chunk-10" {
		t.Fatalf("chunks = %#v", store.chunks)
	}
	if len(store.embeddings) != len(parts) || store.embeddings[0][0] != 0 || store.embeddings[10][0] != 10 {
		t.Fatalf("embeddings = %#v", store.embeddings)
	}
}

func TestEmbeddingHierarchicalProcessorEmbedsOnlyChildren(t *testing.T) {
	store := &hierarchicalChunkStoreStub{}
	embedder := &recordingEmbedderStub{}
	processor := worker.NewEmbeddingHierarchicalChunkingProcessor(
		extractorStub{},
		fixedSplitter{chunks: []string{"parent one", "parent two"}},
		fixedSplitter{chunks: []string{"child"}},
		store,
		embedder,
	)

	if err := processor(context.Background(), worker.Task{DocumentID: 4}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if len(store.parents) != 2 || len(store.children) != 2 || len(store.embeddings) != 2 {
		t.Fatalf("parents=%#v children=%#v embeddings=%#v", store.parents, store.children, store.embeddings)
	}
	if store.children[0].ParentPosition != 0 || store.children[1].ParentPosition != 1 {
		t.Fatalf("child parent positions = %#v", store.children)
	}
	if store.children[0].Content != "child" || store.children[1].Content != "child" {
		t.Fatalf("child contents = %#v", store.children)
	}
	if len(embedder.batches) != 1 || !reflect.DeepEqual(embedder.batches[0], []string{"child", "child"}) {
		t.Fatalf("embedding input = %#v", embedder.batches)
	}
}

func TestEmbeddingHierarchicalProcessorUsesTaskChunkingConfig(t *testing.T) {
	store := &hierarchicalChunkStoreStub{}
	processor := worker.NewEmbeddingHierarchicalChunkingProcessor(
		longTextExtractor{},
		documentchunk.NewAdaptiveSplitter(200, 0),
		documentchunk.NewAdaptiveSplitter(100, 0),
		store,
		matchingEmbedderStub{},
	)
	task := worker.Task{
		DocumentID: 4,
		ProcessConfig: documentextractor.ProcessConfig{ChunkingConfig: &documentextractor.ChunkingConfig{
			ParentChunkSize: 60,
			ChildChunkSize:  20,
			Strategy:        "recursive",
		}},
	}

	if err := processor(context.Background(), task); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if len(store.parents) < 2 || len(store.children) <= len(store.parents) {
		t.Fatalf("parents=%d children=%d, want task config to create nested chunks", len(store.parents), len(store.children))
	}
	for _, parent := range store.parents {
		if len([]rune(parent.Content)) > 60 {
			t.Fatalf("parent chunk length=%d, want <= 60: %q", len([]rune(parent.Content)), parent.Content)
		}
	}
}

func TestEmbeddingHierarchicalProcessorPersistsParseResult(t *testing.T) {
	chunks := &hierarchicalChunkStoreStub{}
	assets := &parseResultStoreStub{}
	processor := worker.NewEmbeddingHierarchicalChunkingProcessorWithParseResultStore(
		richExtractorStub{},
		fixedSplitter{chunks: []string{"parent"}},
		fixedSplitter{chunks: []string{"child"}},
		chunks,
		matchingEmbedderStub{},
		assets,
	)

	if err := processor(context.Background(), worker.Task{DocumentID: 9}); err != nil {
		t.Fatalf("processor error = %v", err)
	}
	if assets.documentID != 9 || len(assets.result.Images) != 1 || assets.result.Metadata["parser"] != "office" {
		t.Fatalf("saved parse result = %#v", assets)
	}
}

func TestEmbeddingChunkingProcessorDoesNotStoreWhenBatchFails(t *testing.T) {
	parts := make([]string, 11)
	for index := range parts {
		parts[index] = fmt.Sprintf("chunk-%d", index)
	}
	store := &chunkStoreStub{}
	embedder := &failingBatchEmbedderStub{failAt: 1}
	processor := worker.NewEmbeddingChunkingProcessor(extractorStub{}, fixedSplitter{chunks: parts}, store, embedder)

	err := processor(context.Background(), worker.Task{DocumentID: 4})
	if err == nil || !strings.Contains(err.Error(), "embed batch 10-10") {
		t.Fatalf("processor error = %v, want second batch context", err)
	}
	if store.chunks != nil || store.embeddings != nil {
		t.Fatalf("store received partial data: chunks=%#v embeddings=%#v", store.chunks, store.embeddings)
	}
}

func TestEmbeddingHierarchicalProcessorIndexesWithChunkQualityWarning(t *testing.T) {
	store := &hierarchicalChunkStoreStub{}
	embedder := &recordingEmbedderStub{}
	processor := worker.NewEmbeddingHierarchicalChunkingProcessor(
		oversizedProtectedBlockExtractor{},
		documentchunk.NewAdaptiveSplitter(1, 0),
		fixedSplitter{chunks: []string{"child"}},
		store,
		embedder,
	)

	err := processor(context.Background(), worker.Task{DocumentID: 4})
	if err != nil {
		t.Fatalf("processor error = %v, want warning-only success", err)
	}
	if len(embedder.batches) == 0 || store.parents == nil || store.children == nil {
		t.Fatalf("quality warning stopped indexing: batches=%#v parents=%#v children=%#v", embedder.batches, store.parents, store.children)
	}
}

func TestEmbeddingHierarchicalProcessorReplacesIndexOnReprocess(t *testing.T) {
	store := &hierarchicalChunkStoreStub{}
	processor := worker.NewEmbeddingHierarchicalChunkingProcessor(
		extractorStub{},
		fixedSplitter{chunks: []string{"parent"}},
		fixedSplitter{chunks: []string{"child"}},
		store,
		matchingEmbedderStub{},
	)

	for attempt := 0; attempt < 2; attempt++ {
		if err := processor(context.Background(), worker.Task{DocumentID: 4}); err != nil {
			t.Fatalf("reprocess attempt %d error = %v", attempt+1, err)
		}
	}
	if store.replacements != 2 || len(store.parents) != 1 || len(store.children) != 1 {
		t.Fatalf("replacements=%d parents=%#v children=%#v, want two complete replacements", store.replacements, store.parents, store.children)
	}
}

func TestRunnerProcessesPDFIntoChunks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "documents"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "guide.pdf"), workerPDF("PDF worker text"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &taskStoreStub{task: worker.Task{
		ID:          12,
		DocumentID:  8,
		StoragePath: "documents/guide.pdf",
		ContentType: "application/pdf",
	}}
	chunks := &chunkStoreStub{}
	processor := worker.NewEmbeddingChunkingProcessor(
		documentextractor.New(root),
		documentchunk.NewSplitter(200, 0),
		chunks,
		matchingEmbedderStub{},
	)

	processed, err := worker.NewRunner(store, processor).RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if store.succeeded != 12 || store.failed.id != 0 {
		t.Fatalf("task result succeeded=%d failed=%#v", store.succeeded, store.failed)
	}
	if len(chunks.chunks) != 1 || chunks.chunks[0] != "PDF worker text" {
		t.Fatalf("chunks=%#v", chunks.chunks)
	}
	if len(chunks.embeddings) != 1 || chunks.embeddings[0][0] != 1 {
		t.Fatalf("embeddings=%#v", chunks.embeddings)
	}
}

func (s *taskStoreStub) ClaimNext(context.Context) (worker.Task, error) {
	s.claims++
	return s.task, s.claimErr
}
func (s *taskStoreStub) MarkSucceeded(_ context.Context, id int64) error {
	if s.succeededErr != nil {
		return s.succeededErr
	}
	s.succeeded = id
	return nil
}
func (s *taskStoreStub) MarkFailed(_ context.Context, id int64, message string) error {
	if s.failedErr != nil {
		return s.failedErr
	}
	s.failed.id, s.failed.message = id, message
	return nil
}

func TestRunnerMarksSuccessfulTask(t *testing.T) {
	store := &taskStoreStub{task: worker.Task{ID: 9, DocumentID: 4}}
	runner := worker.NewRunner(store, func(_ context.Context, task worker.Task) error {
		if task.ID != 9 {
			t.Fatalf("task ID = %d", task.ID)
		}
		return nil
	})

	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed || store.succeeded != 9 {
		t.Fatalf("processed=%v err=%v succeeded=%d", processed, err, store.succeeded)
	}
}

func TestRunnerClearsRetrievalCacheAfterSuccessfulProcessing(t *testing.T) {
	store := &taskStoreStub{task: worker.Task{ID: 9, DocumentID: 4, KnowledgeBaseID: 7}}
	invalidator := &cacheInvalidatorStub{}
	runner := worker.NewRunnerWithMetricsAndInvalidator(store, func(context.Context, worker.Task) error { return nil }, nil, invalidator)

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(invalidator.knowledgeBaseIDs) != 1 || invalidator.knowledgeBaseIDs[0] != 7 {
		t.Fatalf("invalidated knowledge bases = %#v, want [7]", invalidator.knowledgeBaseIDs)
	}
}

func TestRunnerInvokesSuccessHookAfterTaskCommit(t *testing.T) {
	store := &taskStoreStub{task: worker.Task{ID: 9, DocumentID: 4, KnowledgeBaseID: 7}}
	var hooked worker.Task
	runner := worker.NewRunner(store, func(context.Context, worker.Task) error { return nil })
	runner.SetSuccessHook(func(_ context.Context, task worker.Task) { hooked = task })

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if store.succeeded != 9 || hooked.DocumentID != 4 || hooked.KnowledgeBaseID != 7 {
		t.Fatalf("succeeded=%d hooked=%#v, want committed task hook", store.succeeded, hooked)
	}
}

func TestRunnerLogsSuccessfulTask(t *testing.T) {
	output := captureLogs(t)
	store := &taskStoreStub{task: worker.Task{ID: 9, DocumentID: 4}}

	if _, err := worker.NewRunner(store, func(context.Context, worker.Task) error { return nil }).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	for _, field := range []string{"msg=document_task_started", "msg=document_task_succeeded", "status=succeeded", "task_id=9", "document_id=4", "duration_ms="} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
}

func TestRunnerRecordsMetrics(t *testing.T) {
	registry := metrics.New()
	store := &taskStoreStub{task: worker.Task{ID: 13, DocumentID: 8}}
	runner := worker.NewRunnerWithMetrics(store, func(context.Context, worker.Task) error { return nil }, registry)

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "worker_tasks_started_total 1\n") || !strings.Contains(response.Body.String(), "worker_tasks_succeeded_total 1\n") {
		t.Fatalf("metrics body = %q, want started and succeeded counters", response.Body.String())
	}
}

func TestRunnerMarksFailedTask(t *testing.T) {
	store := &taskStoreStub{task: worker.Task{ID: 9}}
	runner := worker.NewRunner(store, func(context.Context, worker.Task) error { return errors.New("invalid PDF") })

	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed || store.failed.id != 9 || store.failed.message != "invalid PDF" {
		t.Fatalf("processed=%v err=%v failed=%#v", processed, err, store.failed)
	}
}

func TestRunnerLogsFailedTask(t *testing.T) {
	output := captureLogs(t)

	store := &taskStoreStub{task: worker.Task{ID: 9, DocumentID: 4}}
	runner := worker.NewRunner(store, func(context.Context, worker.Task) error {
		return errors.New("embedding service unavailable")
	})

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	for _, field := range []string{
		"msg=document_task_failed",
		"task_id=9",
		"document_id=4",
		"status=failed",
		"error=\"embedding service unavailable\"",
		"duration_ms=",
	} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
	if strings.Contains(output.String(), "document processing failed:") {
		t.Fatalf("log output = %q", output.String())
	}
}

func TestRunnerLogsCanceledTask(t *testing.T) {
	output := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &taskStoreStub{task: worker.Task{ID: 10, DocumentID: 5}}

	processed, err := worker.NewRunner(store, func(context.Context, worker.Task) error { return context.Canceled }).RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v, want handled cancellation", processed, err)
	}
	for _, field := range []string{"msg=document_task_canceled", "status=canceled", "task_id=10", "document_id=5", "duration_ms="} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
}

func TestRunnerLogsClaimFailure(t *testing.T) {
	output := captureLogs(t)
	store := &taskStoreStub{claimErr: errors.New("database unavailable")}

	if _, err := worker.NewRunner(store, func(context.Context, worker.Task) error { return nil }).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want claim error")
	}
	for _, field := range []string{"msg=document_task_claim_failed", "status=claim_failed", "error=\"database unavailable\"", "duration_ms="} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
}

func TestRunnerLogsStatusUpdateFailure(t *testing.T) {
	output := captureLogs(t)
	store := &taskStoreStub{
		task:         worker.Task{ID: 11, DocumentID: 6},
		succeededErr: errors.New("database write failed"),
	}

	if _, err := worker.NewRunner(store, func(context.Context, worker.Task) error { return nil }).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want status update error")
	}
	for _, field := range []string{"msg=document_task_status_update_failed", "status=status_update_failed", "target_status=succeeded", "task_id=11", "document_id=6", "error=\"database write failed\""} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
}

func TestRunnerLogsFailedStatusUpdateFailure(t *testing.T) {
	output := captureLogs(t)
	store := &taskStoreStub{
		task:      worker.Task{ID: 12, DocumentID: 7},
		failedErr: errors.New("failed state write"),
	}

	if _, err := worker.NewRunner(store, func(context.Context, worker.Task) error {
		return errors.New("invalid PDF")
	}).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() error = nil, want failed status update error")
	}
	for _, field := range []string{"msg=document_task_status_update_failed", "status=status_update_failed", "target_status=failed", "task_id=12", "document_id=7", "error=\"failed state write\""} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("log output = %q, want field %q", output.String(), field)
		}
	}
}

func TestRunnerDoesNothingWhenQueueIsEmpty(t *testing.T) {
	runner := worker.NewRunner(&taskStoreStub{claimErr: worker.ErrNoTask}, func(context.Context, worker.Task) error { t.Fatal("processor should not run"); return nil })
	processed, err := runner.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
}

func TestRunnerDoesNotClaimAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &taskStoreStub{task: worker.Task{ID: 1}}

	worker.NewRunner(store, func(context.Context, worker.Task) error {
		t.Fatal("processor should not run")
		return nil
	}).Run(ctx, time.Millisecond, nil)

	if store.claims != 0 {
		t.Fatalf("claim count = %d, want 0", store.claims)
	}
}

func TestRunnerLeavesCanceledTaskProcessingForRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &taskStoreStub{task: worker.Task{ID: 10, DocumentID: 5}}

	processed, err := worker.NewRunner(store, func(context.Context, worker.Task) error {
		return context.Canceled
	}).RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v, want handled cancellation", processed, err)
	}
	if store.failed.id != 0 || store.succeeded != 0 {
		t.Fatalf("canceled task state = failed=%#v succeeded=%d, want unchanged", store.failed, store.succeeded)
	}
}

func workerPDF(text string) []byte {
	stream := "BT /F1 18 Tf 72 720 Td (" + text + ") Tj ET\n"
	return []byte(fmt.Sprintf("%%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n2 0 obj << /Length %d >> stream\n%sendstream\n%%%%EOF\n", len(stream), stream))
}
