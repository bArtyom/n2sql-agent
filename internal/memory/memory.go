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
const MaxProfileBytes = 6000

var (
	ErrInvalidKnowledgeBase = errors.New("invalid memory knowledge base")
	ErrInvalidMemory        = errors.New("invalid memory ID")
	ErrInvalidContent       = errors.New("invalid memory content")
	ErrUnauthorized         = errors.New("memory user is not authenticated")
	ErrNotFound             = errors.New("memory not found")
)

type Memory struct {
	ID              int64     `json:"id"`
	UserID          int64     `json:"userId"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Content         string    `json:"content"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Profile struct {
	UserID    int64     `json:"userId"`
	Content   string    `json:"content"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CreateInput struct {
	KnowledgeBaseID int64
	Content         string
}

type Store interface {
	Create(context.Context, int64, CreateInput) (Memory, error)
	List(context.Context, int64, int64) ([]Memory, error)
	Delete(context.Context, int64, int64, int64) error
}

type ProfileStore interface {
	GetProfile(context.Context, int64) (Profile, error)
	SaveProfile(context.Context, int64, string) (Profile, error)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, userID int64, input CreateInput) (Memory, error) {
	if userID <= 0 {
		return Memory{}, ErrUnauthorized
	}
	if input.KnowledgeBaseID <= 0 {
		return Memory{}, ErrInvalidKnowledgeBase
	}
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]byte(content)) > MaxContentBytes {
		return Memory{}, ErrInvalidContent
	}
	var result Memory
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_memories (user_id, knowledge_base_id, content, source)
		VALUES ($1, $2, $3, 'explicit')
		ON CONFLICT DO NOTHING
		RETURNING id, user_id, knowledge_base_id, content, source, created_at, updated_at`, userID, input.KnowledgeBaseID, content).Scan(
		&result.ID, &result.UserID, &result.KnowledgeBaseID, &result.Content, &result.Source, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `
			SELECT id, knowledge_base_id, content, source, created_at, updated_at
			FROM agent_memories
			WHERE user_id = $1 AND knowledge_base_id = $2 AND lower(content) = lower($3)`, userID, input.KnowledgeBaseID, content).Scan(
			&result.ID, &result.UserID, &result.KnowledgeBaseID, &result.Content, &result.Source, &result.CreatedAt, &result.UpdatedAt,
		)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("create memory: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) List(ctx context.Context, userID, knowledgeBaseID int64) ([]Memory, error) {
	if userID <= 0 {
		return nil, ErrUnauthorized
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBase
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, knowledge_base_id, content, source, created_at, updated_at
		FROM agent_memories
		WHERE user_id = $1 AND knowledge_base_id = $2
		ORDER BY updated_at DESC, id DESC`, userID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()
	result := make([]Memory, 0)
	for rows.Next() {
		var item Memory
		if err := rows.Scan(&item.ID, &item.UserID, &item.KnowledgeBaseID, &item.Content, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) Delete(ctx context.Context, userID, knowledgeBaseID, memoryID int64) error {
	if userID <= 0 {
		return ErrUnauthorized
	}
	if knowledgeBaseID <= 0 {
		return ErrInvalidKnowledgeBase
	}
	if memoryID <= 0 {
		return ErrInvalidMemory
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_memories
		WHERE user_id = $1 AND knowledge_base_id = $2 AND id = $3`, userID, knowledgeBaseID, memoryID)
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

func (s *PostgresStore) GetProfile(ctx context.Context, userID int64) (Profile, error) {
	if userID <= 0 {
		return Profile{}, ErrUnauthorized
	}
	var profile Profile
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, content, version, updated_at
		FROM user_memory_profiles WHERE user_id = $1`, userID).Scan(
		&profile.UserID, &profile.Content, &profile.Version, &profile.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{UserID: userID, Version: 1}, nil
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get memory profile: %w", err)
	}
	return profile, nil
}

func (s *PostgresStore) SaveProfile(ctx context.Context, userID int64, content string) (Profile, error) {
	if userID <= 0 {
		return Profile{}, ErrUnauthorized
	}
	content = strings.TrimSpace(content)
	if len([]byte(content)) > MaxProfileBytes {
		return Profile{}, ErrInvalidContent
	}
	var profile Profile
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO user_memory_profiles (user_id, content, version)
		VALUES ($1, $2, 1)
		ON CONFLICT (user_id) DO UPDATE SET
			content = EXCLUDED.content,
			version = user_memory_profiles.version + 1,
			updated_at = CURRENT_TIMESTAMP
		RETURNING user_id, content, version, updated_at`, userID, content).Scan(
		&profile.UserID, &profile.Content, &profile.Version, &profile.UpdatedAt)
	if err != nil {
		return Profile{}, fmt.Errorf("save memory profile: %w", err)
	}
	return profile, nil
}
