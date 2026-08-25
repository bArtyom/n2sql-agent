package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/knowledgegraph"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/postprocess"
)

type GraphChunkExtractor interface {
	ExtractChunk(context.Context, string) (knowledgegraph.ChunkGraph, *modelclient.TokenUsage, error)
}

type GraphPostprocessHandler struct {
	chunks    documentchunk.Reader
	extractor GraphChunkExtractor
	store     knowledgegraph.Store
}

func NewGraphPostprocessHandler(chunks documentchunk.Reader, extractor GraphChunkExtractor, store knowledgegraph.Store) *GraphPostprocessHandler {
	return &GraphPostprocessHandler{chunks: chunks, extractor: extractor, store: store}
}

func (h *GraphPostprocessHandler) Handle(ctx context.Context, task postprocess.Task) (postprocess.Result, error) {
	if h == nil || h.chunks == nil || h.extractor == nil || h.store == nil {
		return postprocess.Result{}, errors.New("graph postprocess dependencies are unavailable")
	}
	if task.KnowledgeBaseID <= 0 || task.DocumentID <= 0 || task.ChunkPosition < 0 {
		return postprocess.Result{}, errors.New("invalid graph postprocess task")
	}
	chunk, err := h.chunks.Read(ctx, task.KnowledgeBaseID, task.DocumentID, task.ChunkPosition)
	if err != nil {
		return postprocess.Result{}, fmt.Errorf("read graph source chunk: %w", err)
	}
	if strings.TrimSpace(chunk.Content) == "" {
		return postprocess.Result{Text: `{"entities":[],"relations":[]}`}, nil
	}
	graph, usage, err := h.extractor.ExtractChunk(ctx, chunk.Content)
	if err != nil {
		return postprocess.Result{}, fmt.Errorf("extract graph from chunk: %w", err)
	}
	if len(graph.Entities) > 0 {
		if err := h.store.UpsertChunk(ctx, task.KnowledgeBaseID, knowledgegraph.ChunkRef{DocumentID: task.DocumentID, Position: task.ChunkPosition}, graph); err != nil {
			return postprocess.Result{}, fmt.Errorf("persist graph chunk: %w", err)
		}
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		return postprocess.Result{}, fmt.Errorf("encode graph result: %w", err)
	}
	result := postprocess.Result{Text: string(encoded)}
	if usage != nil {
		result.InputTokens = usage.PromptTokens
		result.OutputTokens = usage.CompletionTokens
	}
	return result, nil
}
