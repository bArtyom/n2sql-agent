package knowledgebase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound   = errors.New("knowledge base not found")
	ErrConflict   = errors.New("knowledge base already exists")
	ErrProcessing = errors.New("knowledge base has documents in processing")
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

// FileDeleteStore is an optional extension used by the lifecycle service to
// collect source files before deleting the database record.
type FileDeleteStore interface {
	DeleteWithFiles(context.Context, int64) ([]string, error)
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

// DeleteWithFiles removes one administrator-owned knowledge base and returns
// its source paths for cleanup outside the database transaction. Active
// document tasks are rejected so a Worker cannot race this destructive action.
func (s *PostgresStore) DeleteWithFiles(ctx context.Context, id int64) ([]string, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin knowledge base deletion: %w", err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM knowledge_bases
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		)
	`, id).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check knowledge base for deletion: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	pathsRows, err := tx.QueryContext(ctx, `
		SELECT d.storage_path
		FROM documents AS d
		WHERE d.knowledge_base_id = $1
		ORDER BY d.id
		FOR UPDATE`, id)
	if err != nil {
		return nil, fmt.Errorf("find knowledge base source files: %w", err)
	}
	paths := make([]string, 0)
	for pathsRows.Next() {
		var path string
		if err := pathsRows.Scan(&path); err != nil {
			pathsRows.Close()
			return nil, fmt.Errorf("scan knowledge base source file: %w", err)
		}
		paths = append(paths, path)
	}
	if err := pathsRows.Err(); err != nil {
		pathsRows.Close()
		return nil, fmt.Errorf("iterate knowledge base source files: %w", err)
	}
	pathsRows.Close()

	taskRows, err := tx.QueryContext(ctx, `
		SELECT task.id
		FROM document_processing_tasks AS task
		JOIN documents AS d ON d.id = task.document_id
		WHERE d.knowledge_base_id = $1
		  AND task.status IN ('pending', 'processing')
		FOR UPDATE`, id)
	if err != nil {
		return nil, fmt.Errorf("check knowledge base processing tasks: %w", err)
	}
	activeTaskCount := 0
	for taskRows.Next() {
		var taskID int64
		if err := taskRows.Scan(&taskID); err != nil {
			taskRows.Close()
			return nil, fmt.Errorf("scan knowledge base processing task: %w", err)
		}
		activeTaskCount++
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return nil, fmt.Errorf("iterate knowledge base processing tasks: %w", err)
	}
	taskRows.Close()
	if activeTaskCount > 0 {
		return nil, ErrProcessing
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM knowledge_bases
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id)
	if err != nil {
		return nil, fmt.Errorf("delete knowledge base: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count deleted knowledge bases: %w", err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit knowledge base deletion: %w", err)
	}
	return paths, nil
}
