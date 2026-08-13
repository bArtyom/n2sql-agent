package documentchunk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Splitter struct{ size, overlap int }

func NewSplitter(size, overlap int) *Splitter { return &Splitter{size: size, overlap: overlap} }

func (s *Splitter) Split(text string) []string {
	if s.size <= 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	runes := []rune(text)
	var chunks []string
	for start := 0; start < len(runes); {
		end := start + s.size
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			for i := end - 1; i > start; i-- {
				if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
					end = i
					break
				}
			}
		}
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
		next := end - s.overlap
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

type Store interface {
	Replace(context.Context, int64, []string, [][]float32) error
}

type ParentChunk struct {
	Position int
	Content  string
}

type ChildChunk struct {
	Position       int
	ParentPosition int
	Content        string
}

type SearchResult struct {
	DocumentID        int64          `json:"documentId"`
	OriginalFilename  string         `json:"originalFilename,omitempty"`
	Position          int            `json:"position"`
	Content           string         `json:"content"`
	ParentContent     string         `json:"parentContent,omitempty"`
	ParentPosition    int            `json:"parentPosition,omitempty"`
	ContextBefore     []ContextChunk `json:"contextBefore,omitempty"`
	ContextAfter      []ContextChunk `json:"contextAfter,omitempty"`
	Distance          float64        `json:"distance"`
	MatchType         string         `json:"matchType,omitempty"`
	KeywordScore      float64        `json:"keywordScore,omitempty"`
	KeywordScoreKnown bool           `json:"-"`
	FusionScore       float64        `json:"fusionScore,omitempty"`
	RerankScore       float64        `json:"rerankScore,omitempty"`
}

// ContextChunk is a nearby chunk loaded after retrieval. It stays separate
// from Content so callers can distinguish the original hit from its context.
type ContextChunk struct {
	Position int    `json:"position"`
	Content  string `json:"content"`
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Replace(ctx context.Context, documentID int64, chunks []string, embeddings [][]float32) error {
	if len(embeddings) != 0 && len(embeddings) != len(chunks) {
		return fmt.Errorf("embedding count = %d, want %d", len(embeddings), len(chunks))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_parent_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document parent chunks: %w", err)
	}
	for position, content := range chunks {
		var embedding any
		if len(embeddings) != 0 {
			embedding = vectorLiteral(embeddings[position])
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, content, embedding) VALUES ($1, $2, $3, $4::vector)`, documentID, position, content, embedding); err != nil {
			return fmt.Errorf("create document chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunk transaction: %w", err)
	}
	return nil
}

// ReplaceHierarchical stores parent chunks without embeddings and child chunks
// with embeddings. Parent positions are local to one document; child positions
// remain the citation positions exposed by the existing search API.
func (s *PostgresStore) ReplaceHierarchical(ctx context.Context, documentID int64, parents []ParentChunk, children []ChildChunk, embeddings [][]float32) error {
	if documentID <= 0 || len(parents) == 0 || len(children) == 0 || len(embeddings) != len(children) {
		return fmt.Errorf("invalid hierarchical chunk data")
	}
	parentByPosition := make(map[int]int64, len(parents))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hierarchical chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_parent_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document parent chunks: %w", err)
	}
	for _, parent := range parents {
		var parentID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO document_parent_chunks (document_id, position, content) VALUES ($1, $2, $3) RETURNING id`, documentID, parent.Position, parent.Content).Scan(&parentID)
		if err != nil {
			return fmt.Errorf("create document parent chunk: %w", err)
		}
		parentByPosition[parent.Position] = parentID
	}
	for index, child := range children {
		parentID, ok := parentByPosition[child.ParentPosition]
		if !ok {
			return fmt.Errorf("child position %d references unknown parent position %d", child.Position, child.ParentPosition)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, parent_chunk_id, content, embedding) VALUES ($1, $2, $3, $4, $5::vector)`, documentID, child.Position, parentID, child.Content, vectorLiteral(embeddings[index])); err != nil {
			return fmt.Errorf("create document child chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hierarchical chunk transaction: %w", err)
	}
	return nil
}

// ParentForChunk returns the parent context for a child. A missing parent is
// not an error so legacy chunks can continue through neighbor expansion.
func (s *PostgresStore) ParentForChunk(ctx context.Context, knowledgeBaseID, documentID int64, position int) (ParentChunk, bool, error) {
	var parent ParentChunk
	err := s.db.QueryRowContext(ctx, `SELECT parents.position, parents.content
		FROM document_chunks AS chunks
		JOIN document_parent_chunks AS parents ON parents.id = chunks.parent_chunk_id
		JOIN documents AS documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1 AND chunks.document_id = $2 AND chunks.position = $3`, knowledgeBaseID, documentID, position).Scan(&parent.Position, &parent.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return ParentChunk{}, false, nil
	}
	if err != nil {
		return ParentChunk{}, false, fmt.Errorf("query parent chunk: %w", err)
	}
	return parent, true, nil
}

func (s *PostgresStore) Search(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int) ([]SearchResult, error) {
	return s.SearchWithDocuments(ctx, knowledgeBaseID, embedding, limit, nil)
}

func (s *PostgresStore) SearchWithDocuments(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int, documentIDs []int64) ([]SearchResult, error) {
	queryVector := vectorLiteral(embedding)
	query := "SELECT chunks.document_id, documents.original_filename, chunks.position, chunks.content, " +
		"chunks.embedding <=> $2::vector AS distance " +
		"FROM document_chunks AS chunks JOIN documents AS documents ON documents.id = chunks.document_id " +
		"WHERE documents.knowledge_base_id = $1 AND chunks.embedding IS NOT NULL " +
		"AND ($4::bigint[] IS NULL OR chunks.document_id = ANY($4::bigint[])) " +
		"ORDER BY chunks.embedding <=> $2::vector, chunks.position LIMIT $3"
	rows, err := s.db.QueryContext(ctx, query, knowledgeBaseID, queryVector, limit, documentIDsArgument(documentIDs))
	if err != nil {
		return nil, fmt.Errorf("query filtered document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.DocumentID, &result.OriginalFilename, &result.Position, &result.Content, &result.Distance); err != nil {
			return nil, fmt.Errorf("scan filtered document chunk: %w", err)
		}
		result.MatchType = "vector"
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate filtered document chunks: %w", err)
	}
	return results, nil
}

// SearchKeyword returns chunks that contain the query text. It is a small
// lexical companion to vector search for exact names, codes and commands.
func (s *PostgresStore) SearchKeyword(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]SearchResult, error) {
	return s.SearchKeywordWithDocuments(ctx, knowledgeBaseID, query, limit, nil)
}

func (s *PostgresStore) SearchKeywordWithDocuments(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64) ([]SearchResult, error) {
	exactPattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(query)) + "%"
	sqlQuery := "WITH search_query AS (" +
		"SELECT plainto_tsquery('simple', $2) AS terms" +
		"), scored AS (" +
		"SELECT chunks.document_id, documents.original_filename, chunks.position, chunks.content, " +
		"ts_rank_cd(chunks.content_search, search_query.terms) AS keyword_score, " +
		"CASE WHEN lower(chunks.content) LIKE $3 ESCAPE E'\\\\' THEN 1.0 ELSE 0.0 END AS exact_score " +
		"FROM document_chunks AS chunks JOIN documents AS documents ON documents.id = chunks.document_id " +
		"CROSS JOIN search_query WHERE documents.knowledge_base_id = $1 " +
		"AND (chunks.content_search @@ search_query.terms OR lower(chunks.content) LIKE $3 ESCAPE E'\\\\') " +
		"AND ($4::bigint[] IS NULL OR chunks.document_id = ANY($4::bigint[]))" +
		") SELECT document_id, original_filename, position, content, 0::float8 AS distance, GREATEST(keyword_score, exact_score) AS keyword_score " +
		"FROM scored ORDER BY exact_score DESC, keyword_score DESC, position, document_id LIMIT $5"
	rows, err := s.db.QueryContext(ctx, sqlQuery, knowledgeBaseID, query, exactPattern, documentIDsArgument(documentIDs), limit)
	if err != nil {
		return nil, fmt.Errorf("query filtered keyword document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.DocumentID, &result.OriginalFilename, &result.Position, &result.Content, &result.Distance, &result.KeywordScore); err != nil {
			return nil, fmt.Errorf("scan filtered keyword document chunk: %w", err)
		}
		result.MatchType = "keyword"
		result.KeywordScoreKnown = true
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate filtered keyword document chunks: %w", err)
	}
	return results, nil
}

// SearchNeighbors returns nearby chunks from the same document and knowledge
// base. The knowledge-base condition prevents context expansion from crossing
// the retrieval permission boundary.
func (s *PostgresStore) SearchNeighbors(ctx context.Context, knowledgeBaseID, documentID int64, position, before, after int) ([]SearchResult, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 || position < 0 || before < 0 || after < 0 {
		return nil, errors.New("invalid chunk context lookup")
	}
	query := `SELECT chunks.position, chunks.content
		FROM document_chunks AS chunks
		JOIN documents AS documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1
		  AND chunks.document_id = $2
		  AND chunks.position BETWEEN $3 AND $4
		ORDER BY chunks.position`
	rows, err := s.db.QueryContext(ctx, query, knowledgeBaseID, documentID, position-before, position+after)
	if err != nil {
		return nil, fmt.Errorf("query neighboring document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, before+after+1)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.Position, &result.Content); err != nil {
			return nil, fmt.Errorf("scan neighboring document chunk: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate neighboring document chunks: %w", err)
	}
	return results, nil
}

func documentIDsArgument(documentIDs []int64) any {
	if len(documentIDs) == 0 {
		return nil
	}
	parts := make([]string, len(documentIDs))
	for index, documentID := range documentIDs {
		parts[index] = strconv.FormatInt(documentID, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func vectorLiteral(vector []float32) string {
	values := make([]string, len(vector))
	for index, value := range vector {
		values[index] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	return "[" + strings.Join(values, ",") + "]"
}
