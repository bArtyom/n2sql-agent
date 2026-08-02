package worker_test

import (
	"context"
	"errors"
	"testing"

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
