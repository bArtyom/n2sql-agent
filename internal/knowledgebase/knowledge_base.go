package knowledgebase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("knowledge base not found")
	ErrConflict = errors.New("knowledge base already exists")
)

type KnowledgeBase struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CreateInput struct {
	Name        string
	Description string
}

type Store interface {
	Create(context.Context, CreateInput) (KnowledgeBase, error)
	List(context.Context) ([]KnowledgeBase, error)
	Delete(context.Context, int64) error
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (KnowledgeBase, error) {
	var knowledgeBase KnowledgeBase
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO knowledge_bases (administrator_id, name, description)
		SELECT administrator_id, $1, $2 FROM system_settings WHERE id = 1
		RETURNING id, name, description`, input.Name, input.Description).Scan(
		&knowledgeBase.ID,
		&knowledgeBase.Name,
		&knowledgeBase.Description,
	)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return KnowledgeBase{}, ErrConflict
		}
		return KnowledgeBase{}, fmt.Errorf("create knowledge base: %w", err)
	}
	return knowledgeBase, nil
}

func (s *PostgresStore) List(ctx context.Context) ([]KnowledgeBase, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description
		FROM knowledge_bases
		WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	defer rows.Close()

	knowledgeBases := make([]KnowledgeBase, 0)
	for rows.Next() {
		var knowledgeBase KnowledgeBase
		if err := rows.Scan(&knowledgeBase.ID, &knowledgeBase.Name, &knowledgeBase.Description); err != nil {
			return nil, fmt.Errorf("scan knowledge base: %w", err)
		}
		knowledgeBases = append(knowledgeBases, knowledgeBase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge bases: %w", err)
	}
	return knowledgeBases, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM knowledge_bases
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id)
	if err != nil {
		return fmt.Errorf("delete knowledge base: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted knowledge bases: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
