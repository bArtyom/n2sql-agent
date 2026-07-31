package documentchunk

import (
	"context"
	"database/sql"
	"fmt"
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
	Replace(context.Context, int64, []string) error
}
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Replace(ctx context.Context, documentID int64, chunks []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	for position, content := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, content) VALUES ($1, $2, $3)`, documentID, position, content); err != nil {
			return fmt.Errorf("create document chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunk transaction: %w", err)
	}
	return nil
}
