package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxContentBytes = 2000

var (
	ErrInvalidKnowledgeBase = errors.New("invalid memory knowledge base")
	ErrInvalidMemory        = errors.New("invalid memory ID")
	ErrInvalidContent       = errors.New("invalid memory content")
	ErrNotFound             = errors.New("memory not found")
)

type Memory struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Content         string    `json:"content"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type CreateInput struct {
	KnowledgeBaseID int64
	Content         string
}

type Store interface {
	Create(context.Context, CreateInput) (Memory, error)
	List(context.Context, int64) ([]Memory, error)
	Delete(context.Context, int64, int64) error
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Memory, error) {
	if input.KnowledgeBaseID <= 0 {
		return Memory{}, ErrInvalidKnowledgeBase
	}
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]byte(content)) > MaxContentBytes {
		return Memory{}, ErrInvalidContent
	}
	var result Memory
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_memories (knowledge_base_id, content, source)
		VALUES ($1, $2, 'explicit')
		RETURNING id, knowledge_base_id, content, source, created_at, updated_at`, input.KnowledgeBaseID, content).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Content, &result.Source, &result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return Memory{}, fmt.Errorf("create memory: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Memory, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBase
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, knowledge_base_id, content, source, created_at, updated_at
		FROM agent_memories
		WHERE knowledge_base_id = $1
		ORDER BY updated_at DESC, id DESC`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	result := make([]Memory, 0)
	for rows.Next() {
		var item Memory
		if err := rows.Scan(&item.ID, &item.KnowledgeBaseID, &item.Content, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) Delete(ctx context.Context, knowledgeBaseID, memoryID int64) error {
	if knowledgeBaseID <= 0 {
		return ErrInvalidKnowledgeBase
	}
	if memoryID <= 0 {
		return ErrInvalidMemory
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_memories
		WHERE knowledge_base_id = $1 AND id = $2`, knowledgeBaseID, memoryID)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted memories: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
