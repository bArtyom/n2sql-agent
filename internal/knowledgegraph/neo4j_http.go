package knowledgegraph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Neo4jHTTPStore uses Neo4j's transactional HTTP endpoint. It keeps the
// graph backend behind Store so the retrieval pipeline does not depend on a
// particular Neo4j driver or protocol.
type Neo4jHTTPStore struct {
	endpoint string
	username string
	password string
	database string
	client   *http.Client
}

func NewNeo4jHTTPStore(endpoint, username, password, database string, client *http.Client) (*Neo4jHTTPStore, error) {
	endpoint = normalizeNeo4jEndpoint(endpoint, database)
	if endpoint == "" || strings.TrimSpace(username) == "" || strings.TrimSpace(database) == "" {
		return nil, fmt.Errorf("neo4j endpoint, username and database are required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Neo4jHTTPStore{endpoint: endpoint, username: username, password: password, database: database, client: client}, nil
}

func normalizeNeo4jEndpoint(raw, database string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "bolt://") {
		raw = "http://" + strings.TrimPrefix(raw, "bolt://")
		if parsed, err := url.Parse(raw); err == nil {
			host := parsed.Hostname()
			port := parsed.Port()
			if port == "7687" || port == "" {
				raw = "http://" + host + ":7474"
			}
		}
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return ""
	}
	return raw + "/db/" + url.PathEscape(database) + "/tx/commit"
}

type cypherRequest struct {
	Statements []cypherStatement `json:"statements"`
}

type cypherStatement struct {
	Statement  string         `json:"statement"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

type cypherResponse struct {
	Results []struct {
		Data []struct {
			Row []any `json:"row"`
		} `json:"data"`
	} `json:"results"`
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (s *Neo4jHTTPStore) execute(ctx context.Context, statements ...cypherStatement) ([][]any, error) {
	payload, err := json.Marshal(cypherRequest{Statements: statements})
	if err != nil {
		return nil, fmt.Errorf("encode cypher request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create neo4j request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.username, s.password)
	response, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute neo4j request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read neo4j response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("neo4j returned HTTP %d", response.StatusCode)
	}
	var decoded cypherResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode neo4j response: %w", err)
	}
	if len(decoded.Errors) > 0 {
		return nil, fmt.Errorf("neo4j cypher error %s: %s", decoded.Errors[0].Code, decoded.Errors[0].Message)
	}
	var rows [][]any
	for _, result := range decoded.Results {
		for _, data := range result.Data {
			rows = append(rows, data.Row)
		}
	}
	return rows, nil
}

func (s *Neo4jHTTPStore) UpsertChunk(ctx context.Context, knowledgeBaseID int64, ref ChunkRef, graph ChunkGraph) error {
	if s == nil || knowledgeBaseID <= 0 || ref.DocumentID <= 0 || ref.Position < 0 {
		return fmt.Errorf("invalid graph chunk")
	}
	entityRows := make([]map[string]any, 0, len(graph.Entities))
	for _, entity := range graph.Entities {
		entityRows = append(entityRows, map[string]any{
			"name": entity.Name, "type": entity.Type, "attributes": entity.Attributes,
		})
	}
	relationRows := make([]map[string]any, 0, len(graph.Relations))
	for _, relation := range graph.Relations {
		relationRows = append(relationRows, map[string]any{
			"source": relation.Source, "target": relation.Target, "type": relation.Type,
		})
	}
	chunkRef := ref.String()
	statements := []cypherStatement{
		{
			Statement: `UNWIND $entities AS entity
MERGE (node:KnowledgeEntity {knowledge_base_id: $knowledge_base_id, name: entity.name})
SET node.type = entity.type,
    node.attributes = entity.attributes,
    node.chunks = CASE WHEN $chunk_ref IN coalesce(node.chunks, [])
                       THEN coalesce(node.chunks, [])
                       ELSE coalesce(node.chunks, []) + $chunk_ref END`,
			Parameters: map[string]any{"entities": entityRows, "knowledge_base_id": knowledgeBaseID, "chunk_ref": chunkRef},
		},
		{
			Statement: `UNWIND $relations AS relation
MATCH (source:KnowledgeEntity {knowledge_base_id: $knowledge_base_id, name: relation.source})
MATCH (target:KnowledgeEntity {knowledge_base_id: $knowledge_base_id, name: relation.target})
MERGE (source)-[edge:RELATES_TO {knowledge_base_id: $knowledge_base_id, type: relation.type}]->(target)
SET edge.chunks = CASE WHEN $chunk_ref IN coalesce(edge.chunks, [])
                       THEN coalesce(edge.chunks, [])
                       ELSE coalesce(edge.chunks, []) + $chunk_ref END`,
			Parameters: map[string]any{"relations": relationRows, "knowledge_base_id": knowledgeBaseID, "chunk_ref": chunkRef},
		},
	}
	_, err := s.execute(ctx, statements...)
	return err
}

func (s *Neo4jHTTPStore) Search(ctx context.Context, knowledgeBaseID int64, entities []string, documentIDs []int64) (SearchResult, error) {
	if s == nil || knowledgeBaseID <= 0 || len(entities) == 0 {
		return SearchResult{}, nil
	}
	rows, err := s.execute(ctx, cypherStatement{
		Statement: `MATCH (node:KnowledgeEntity {knowledge_base_id: $knowledge_base_id})-[edge:RELATES_TO]-(neighbor:KnowledgeEntity {knowledge_base_id: $knowledge_base_id})
WHERE any(term IN $entities WHERE toLower(node.name) CONTAINS toLower(term))
RETURN node.name, node.type, node.attributes, node.chunks,
       edge.type, neighbor.name, neighbor.type, neighbor.attributes, neighbor.chunks
LIMIT $limit`,
		Parameters: map[string]any{"knowledge_base_id": knowledgeBaseID, "entities": entities, "limit": 100},
	})
	if err != nil {
		return SearchResult{}, err
	}
	documentFilter := make(map[int64]struct{}, len(documentIDs))
	for _, documentID := range documentIDs {
		documentFilter[documentID] = struct{}{}
	}
	result := SearchResult{}
	seenNodes := make(map[string]struct{})
	seenRelations := make(map[string]struct{})
	seenChunks := make(map[string]struct{})
	for _, row := range rows {
		if len(row) < 9 {
			continue
		}
		addNode := func(name, entityType string, attributes, chunks any) {
			name = strings.TrimSpace(name)
			if name == "" {
				return
			}
			if _, exists := seenNodes[name]; !exists {
				seenNodes[name] = struct{}{}
				result.Nodes = append(result.Nodes, Entity{Name: name, Type: entityType, Attributes: stringSlice(attributes)})
			}
			for _, ref := range chunkRefs(chunks) {
				if len(documentFilter) > 0 {
					if _, ok := documentFilter[ref.DocumentID]; !ok {
						continue
					}
				}
				if _, exists := seenChunks[ref.String()]; !exists {
					seenChunks[ref.String()] = struct{}{}
					result.Chunks = append(result.Chunks, ref)
				}
			}
		}
		addNode(stringValue(row[0]), stringValue(row[1]), row[2], row[3])
		addNode(stringValue(row[5]), stringValue(row[6]), row[7], row[8])
		source, target, relationType := stringValue(row[0]), stringValue(row[5]), stringValue(row[4])
		key := source + "\x00" + relationType + "\x00" + target
		if source != "" && target != "" && relationType != "" {
			if _, exists := seenRelations[key]; !exists {
				seenRelations[key] = struct{}{}
				result.Relations = append(result.Relations, Relation{Source: source, Target: target, Type: relationType})
			}
		}
	}
	return result, nil
}

func (s *Neo4jHTTPStore) DeleteDocument(ctx context.Context, knowledgeBaseID, documentID int64) error {
	if s == nil || knowledgeBaseID <= 0 || documentID <= 0 {
		return fmt.Errorf("invalid graph document")
	}
	_, err := s.execute(ctx, cypherStatement{
		Statement: `MATCH (node:KnowledgeEntity {knowledge_base_id: $knowledge_base_id})
WITH node, [ref IN coalesce(node.chunks, []) WHERE NOT ref STARTS WITH $prefix] AS remaining
SET node.chunks = remaining
WITH node WHERE size(remaining) = 0
DETACH DELETE node`,
		Parameters: map[string]any{"knowledge_base_id": knowledgeBaseID, "prefix": strconv.FormatInt(documentID, 10) + ":"},
	})
	return err
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func chunkRefs(value any) []ChunkRef {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	refs := make([]ChunkRef, 0, len(items))
	for _, item := range items {
		parts := strings.SplitN(stringValue(item), ":", 2)
		if len(parts) != 2 {
			continue
		}
		documentID, documentErr := strconv.ParseInt(parts[0], 10, 64)
		position, positionErr := strconv.Atoi(parts[1])
		if documentErr == nil && positionErr == nil && documentID > 0 && position >= 0 {
			refs = append(refs, ChunkRef{DocumentID: documentID, Position: position})
		}
	}
	return refs
}
