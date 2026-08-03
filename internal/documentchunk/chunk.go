package documentchunk

import (
	"context"
	"database/sql"
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

type SearchResult struct {
	DocumentID int64   `json:"documentId"`
	Position   int     `json:"position"`
	Content    string  `json:"content"`
	Distance   float64 `json:"distance"`
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

func (s *PostgresStore) Search(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int) ([]SearchResult, error) {
	queryVector := vectorLiteral(embedding)
	rows, err := s.db.QueryContext(ctx, `
		SELECT chunks.document_id, chunks.position, chunks.content,
		       chunks.embedding <=> $2::vector AS distance
		FROM document_chunks AS chunks
		JOIN documents AS documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1
		  AND chunks.embedding IS NOT NULL
		ORDER BY chunks.embedding <=> $2::vector, chunks.position
		LIMIT $3`, knowledgeBaseID, queryVector, limit)
	if err != nil {
		return nil, fmt.Errorf("query similar document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.DocumentID, &result.Position, &result.Content, &result.Distance); err != nil {
			return nil, fmt.Errorf("scan similar document chunk: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate similar document chunks: %w", err)
	}
	return results, nil
}

func vectorLiteral(vector []float32) string {
	values := make([]string, len(vector))
	for index, value := range vector {
		values[index] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	return "[" + strings.Join(values, ",") + "]"
}
