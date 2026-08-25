package knowledgegraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidGraph = errors.New("invalid knowledge graph extraction")
)

const (
	maxEntityNameBytes = 256
	maxEntityTypeBytes = 128
	maxRelationTypeBytes = 128
	maxAttributes = 16
	maxEntities = 64
	maxRelations = 128
)

// Entity is a normalized entity extracted from one document chunk.
type Entity struct {
	Name       string   `json:"name"`
	Type       string   `json:"type,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
}

// Relation is a directed relation grounded in the source chunk.
type Relation struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// ChunkGraph is the graph fragment produced for one text chunk.
type ChunkGraph struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
}

// ChunkRef points back to the original searchable chunk. The graph stores
// references, not a second copy of the document body.
type ChunkRef struct {
	DocumentID int64
	Position   int
}

type SearchResult struct {
	Nodes     []Entity
	Relations []Relation
	Chunks    []ChunkRef
}

type Store interface {
	UpsertChunk(context.Context, int64, ChunkRef, ChunkGraph) error
	Search(context.Context, int64, []string, []int64) (SearchResult, error)
	DeleteDocument(context.Context, int64, int64) error
}

func (r ChunkRef) String() string {
	return fmt.Sprintf("%d:%d", r.DocumentID, r.Position)
}

func ParseGraphResponse(raw string) (ChunkGraph, error) {
	raw = strings.TrimSpace(raw)
	raw = stripJSONFence(raw)
	if raw == "" {
		return ChunkGraph{}, ErrInvalidGraph
	}

	var payload struct {
		Entities  []Entity   `json:"entities"`
		Nodes     []Entity   `json:"nodes"`
		Relations []Relation `json:"relations"`
		Relation  []Relation `json:"relation"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ChunkGraph{}, fmt.Errorf("parse graph JSON: %w", err)
	}
	if len(payload.Entities) == 0 {
		payload.Entities = payload.Nodes
	}
	if len(payload.Relations) == 0 {
		payload.Relations = payload.Relation
	}
	return normalizeGraph(ChunkGraph{Entities: payload.Entities, Relations: payload.Relations})
}

func normalizeGraph(graph ChunkGraph) (ChunkGraph, error) {
	if len(graph.Entities) > maxEntities || len(graph.Relations) > maxRelations {
		return ChunkGraph{}, ErrInvalidGraph
	}
	entities := make([]Entity, 0, len(graph.Entities))
	seen := make(map[string]struct{}, len(graph.Entities))
	for _, entity := range graph.Entities {
		entity.Name = strings.TrimSpace(entity.Name)
		entity.Type = strings.TrimSpace(entity.Type)
		if entity.Name == "" || len([]byte(entity.Name)) > maxEntityNameBytes || len([]byte(entity.Type)) > maxEntityTypeBytes {
			continue
		}
		key := strings.ToLower(entity.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		attributes := make([]string, 0, min(len(entity.Attributes), maxAttributes))
		for _, attribute := range entity.Attributes {
			attribute = strings.TrimSpace(attribute)
			if attribute != "" && len(attributes) < maxAttributes {
				attributes = append(attributes, attribute)
			}
		}
		entity.Attributes = attributes
		entities = append(entities, entity)
	}
	if len(entities) == 0 && len(graph.Relations) > 0 {
		return ChunkGraph{}, ErrInvalidGraph
	}

	known := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		known[strings.ToLower(entity.Name)] = struct{}{}
	}
	relations := make([]Relation, 0, len(graph.Relations))
	seenRelations := make(map[string]struct{}, len(graph.Relations))
	for _, relation := range graph.Relations {
		relation.Source = strings.TrimSpace(relation.Source)
		relation.Target = strings.TrimSpace(relation.Target)
		relation.Type = strings.TrimSpace(relation.Type)
		if relation.Source == "" || relation.Target == "" || relation.Type == "" ||
			len([]byte(relation.Type)) > maxRelationTypeBytes ||
			relation.Source == relation.Target {
			continue
		}
		if _, ok := known[strings.ToLower(relation.Source)]; !ok {
			continue
		}
		if _, ok := known[strings.ToLower(relation.Target)]; !ok {
			continue
		}
		key := strings.ToLower(relation.Source + "\x00" + relation.Type + "\x00" + relation.Target)
		if _, exists := seenRelations[key]; exists {
			continue
		}
		seenRelations[key] = struct{}{}
		relations = append(relations, relation)
	}
	return ChunkGraph{Entities: entities, Relations: relations}, nil
}

func stripJSONFence(raw string) string {
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 {
		return raw
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
