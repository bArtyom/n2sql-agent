package knowledgegraph

import (
	"context"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type ChatClient interface {
	Chat(context.Context, string) (modelclient.ChatResponse, error)
}

type Extractor struct {
	chat ChatClient
}

func NewExtractor(chat ChatClient) *Extractor {
	return &Extractor{chat: chat}
}

func (e *Extractor) ExtractChunk(ctx context.Context, content string) (ChunkGraph, *modelclient.TokenUsage, error) {
	if e == nil || e.chat == nil {
		return ChunkGraph{}, nil, fmt.Errorf("graph extractor is unavailable")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return ChunkGraph{}, nil, nil
	}
	response, err := e.chat.Chat(ctx, graphPrompt(content))
	if err != nil {
		return ChunkGraph{}, response.Usage, err
	}
	graph, err := ParseGraphResponse(response.Message)
	if err != nil {
		return ChunkGraph{}, response.Usage, err
	}
	return graph, response.Usage, nil
}

func (e *Extractor) ExtractQueryEntities(ctx context.Context, query string) ([]string, *modelclient.TokenUsage, error) {
	if e == nil || e.chat == nil {
		return nil, nil, fmt.Errorf("graph extractor is unavailable")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil, nil
	}
	response, err := e.chat.Chat(ctx, queryPrompt(query))
	if err != nil {
		return nil, response.Usage, err
	}
	graph, err := ParseGraphResponse(response.Message)
	if err != nil {
		return nil, response.Usage, err
	}
	entities := make([]string, 0, len(graph.Entities))
	for _, entity := range graph.Entities {
		entities = append(entities, entity.Name)
	}
	return entities, response.Usage, nil
}

func graphPrompt(content string) string {
	return "你是知识图谱抽取器。只根据原文抽取实体和明确存在的关系，不要推测。只返回 JSON，不要 Markdown，不要解释。JSON 格式为 {\"entities\":[{\"name\":\"实体名称\",\"type\":\"实体类型\",\"attributes\":[\"属性\"]}],\"relations\":[{\"source\":\"实体名称\",\"target\":\"实体名称\",\"type\":\"关系类型\"}]}。没有实体或关系时返回空数组。原文：\n\n" + content
}

func queryPrompt(query string) string {
	return "从下面的用户问题中提取用于知识图谱检索的实体名称。不要抽取泛化词，例如‘什么’、‘如何’、‘哪个’。只返回 JSON，格式为 {\"entities\":[{\"name\":\"实体名称\"}],\"relations\":[]}。用户问题：\n\n" + query
}
