package worker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/worker"
)

type taskStoreStub struct {
	task      worker.Task
	claimErr  error
	succeeded int64
	failed    struct {
		id      int64
		message string
	}
}

type extractorStub struct{}

func (extractorStub) Extract(context.Context, string, string) (string, error) {
	return "source text", nil
}

type splitterStub struct{}

func (splitterStub) Split(string) []string { return []string{"first", "second"} }

type chunkStoreStub struct {
	chunks     []string
	embeddings [][]float32
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

func (s *taskStoreStub) ClaimNext(context.Context) (worker.Task, error) { return s.task, s.claimErr }
func (s *taskStoreStub) MarkSucceeded(_ context.Context, id int64) error {
	s.succeeded = id
	return nil
}
func (s *taskStoreStub) MarkFailed(_ context.Context, id int64, message string) error {
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

func TestRunnerMarksFailedTask(t *testing.T) {
	store := &taskStoreStub{task: worker.Task{ID: 9}}
	runner := worker.NewRunner(store, func(context.Context, worker.Task) error { return errors.New("invalid PDF") })

	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed || store.failed.id != 9 || store.failed.message != "invalid PDF" {
		t.Fatalf("processed=%v err=%v failed=%#v", processed, err, store.failed)
	}
}

func TestRunnerDoesNothingWhenQueueIsEmpty(t *testing.T) {
	runner := worker.NewRunner(&taskStoreStub{claimErr: worker.ErrNoTask}, func(context.Context, worker.Task) error { t.Fatal("processor should not run"); return nil })
	processed, err := runner.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
}

func workerPDF(text string) []byte {
	stream := "BT /F1 18 Tf 72 720 Td (" + text + ") Tj ET\n"
	return []byte(fmt.Sprintf("%%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n2 0 obj << /Length %d >> stream\n%sendstream\n%%%%EOF\n", len(stream), stream))
}
