// Package documenttag owns reusable labels attached to documents in a
// knowledge base. Tags are metadata: they constrain retrieval but never enter
// document content or embedding text.
package documenttag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

const (
	MaxTagIDs        = 32
	MaxTagNameBytes  = 80
	MaxTagColorBytes = 32
)

var (
	ErrInvalidTagIDs    = errors.New("invalid tag IDs")
	ErrInvalidTagName   = errors.New("invalid tag name")
	ErrInvalidTagColor  = errors.New("invalid tag color")
	ErrTagNotFound      = errors.New("document tag not found")
	ErrTagConflict      = errors.New("document tag already exists")
	ErrDocumentNotFound = errors.New("document not found")
)

type Tag struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Name            string    `json:"name"`
	Color           string    `json:"color,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type CreateInput struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type UpdateInput struct {
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty"`
}

type Store interface {
	List(context.Context, int64) ([]Tag, error)
	Create(context.Context, int64, CreateInput) (Tag, error)
	Update(context.Context, int64, int64, UpdateInput) (Tag, error)
	Delete(context.Context, int64, int64) error
	ListDocumentTags(context.Context, int64, int64) ([]Tag, error)
	SetDocumentTags(context.Context, int64, int64, []int64) ([]Tag, error)
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) List(ctx context.Context, knowledgeBaseID int64) ([]Tag, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrTagNotFound
	}
	return s.store.List(ctx, knowledgeBaseID)
}

func (s *Service) Create(ctx context.Context, knowledgeBaseID int64, input CreateInput) (Tag, error) {
	if knowledgeBaseID <= 0 {
		return Tag{}, ErrTagNotFound
	}
	name, err := ValidateName(input.Name)
	if err != nil {
		return Tag{}, err
	}
	color, err := ValidateColor(input.Color)
	if err != nil {
		return Tag{}, err
	}
	input.Name, input.Color = name, color
	return s.store.Create(ctx, knowledgeBaseID, input)
}

func (s *Service) Update(ctx context.Context, knowledgeBaseID, tagID int64, input UpdateInput) (Tag, error) {
	if knowledgeBaseID <= 0 || tagID <= 0 || (input.Name == nil && input.Color == nil) {
		return Tag{}, ErrInvalidTagName
	}
	if input.Name != nil {
		name, err := ValidateName(*input.Name)
		if err != nil {
			return Tag{}, err
		}
		input.Name = &name
	}
	if input.Color != nil {
		color, err := ValidateColor(*input.Color)
		if err != nil {
			return Tag{}, err
		}
		input.Color = &color
	}
	return s.store.Update(ctx, knowledgeBaseID, tagID, input)
}

func (s *Service) Delete(ctx context.Context, knowledgeBaseID, tagID int64) error {
	if knowledgeBaseID <= 0 || tagID <= 0 {
		return ErrTagNotFound
	}
	return s.store.Delete(ctx, knowledgeBaseID, tagID)
}

func (s *Service) ListDocumentTags(ctx context.Context, knowledgeBaseID, documentID int64) ([]Tag, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return nil, ErrDocumentNotFound
	}
	return s.store.ListDocumentTags(ctx, knowledgeBaseID, documentID)
}

func (s *Service) SetDocumentTags(ctx context.Context, knowledgeBaseID, documentID int64, tagIDs []int64) ([]Tag, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return nil, ErrDocumentNotFound
	}
	normalized, err := NormalizeIDs(tagIDs)
	if err != nil {
		return nil, err
	}
	return s.store.SetDocumentTags(ctx, knowledgeBaseID, documentID, normalized)
}

func NormalizeIDs(tagIDs []int64) ([]int64, error) {
	if len(tagIDs) > MaxTagIDs {
		return nil, ErrInvalidTagIDs
	}
	seen := make(map[int64]struct{}, len(tagIDs))
	result := make([]int64, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			return nil, ErrInvalidTagIDs
		}
		if _, exists := seen[tagID]; exists {
			continue
		}
		seen[tagID] = struct{}{}
		result = append(result, tagID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func ValidateName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalidTagName
	}
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || len([]byte(value)) > MaxTagNameBytes {
		return "", ErrInvalidTagName
	}
	return value, nil
}

func ValidateColor(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len([]byte(value)) > MaxTagColorBytes || !strings.HasPrefix(value, "#") || len(value) != 7 {
		return "", ErrInvalidTagColor
	}
	for _, character := range value[1:] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return "", ErrInvalidTagColor
		}
	}
	return strings.ToLower(value), nil
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, knowledge_base_id, name, color, created_at, updated_at
		FROM knowledge_base_tags
		WHERE knowledge_base_id = $1
		  AND knowledge_base_id IN (
			SELECT id FROM knowledge_bases
			WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		  )
		ORDER BY lower(name), id`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list document tags: %w", err)
	}
	defer rows.Close()
	return scanTags(rows)
}

func (s *PostgresStore) Create(ctx context.Context, knowledgeBaseID int64, input CreateInput) (Tag, error) {
	var tag Tag
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO knowledge_base_tags (knowledge_base_id, name, color)
		SELECT $1, $2, $3
		WHERE EXISTS (
			SELECT 1 FROM knowledge_bases
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		)
		RETURNING id, knowledge_base_id, name, color, created_at, updated_at`, knowledgeBaseID, input.Name, input.Color).Scan(
		&tag.ID, &tag.KnowledgeBaseID, &tag.Name, &tag.Color, &tag.CreatedAt, &tag.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrTagNotFound
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return Tag{}, ErrTagConflict
		}
		return Tag{}, fmt.Errorf("create document tag: %w", err)
	}
	return tag, nil
}

func (s *PostgresStore) Update(ctx context.Context, knowledgeBaseID, tagID int64, input UpdateInput) (Tag, error) {
	var tag Tag
	err := s.db.QueryRowContext(ctx, `
		UPDATE knowledge_base_tags AS tag
		SET name = COALESCE($3, tag.name),
		    color = COALESCE($4, tag.color),
		    updated_at = CURRENT_TIMESTAMP
		FROM knowledge_bases AS kb
		WHERE tag.id = $1
		  AND tag.knowledge_base_id = $2
		  AND kb.id = tag.knowledge_base_id
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		RETURNING tag.id, tag.knowledge_base_id, tag.name, tag.color, tag.created_at, tag.updated_at`, tagID, knowledgeBaseID, nullableString(input.Name), nullableString(input.Color)).Scan(
		&tag.ID, &tag.KnowledgeBaseID, &tag.Name, &tag.Color, &tag.CreatedAt, &tag.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ErrTagNotFound
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return Tag{}, ErrTagConflict
		}
		return Tag{}, fmt.Errorf("update document tag: %w", err)
	}
	return tag, nil
}

func (s *PostgresStore) Delete(ctx context.Context, knowledgeBaseID, tagID int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM knowledge_base_tags AS tag
		USING knowledge_bases AS kb
		WHERE tag.id = $1 AND tag.knowledge_base_id = $2
		  AND kb.id = tag.knowledge_base_id
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, tagID, knowledgeBaseID)
	if err != nil {
		return fmt.Errorf("delete document tag: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted document tags: %w", err)
	}
	if affected == 0 {
		return ErrTagNotFound
	}
	return nil
}

func (s *PostgresStore) ListDocumentTags(ctx context.Context, knowledgeBaseID, documentID int64) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tag.id, tag.knowledge_base_id, tag.name, tag.color, tag.created_at, tag.updated_at
		FROM knowledge_base_tags AS tag
		JOIN document_tags AS link ON link.tag_id = tag.id
		JOIN documents AS document ON document.id = link.document_id
		JOIN knowledge_bases AS kb ON kb.id = document.knowledge_base_id
		WHERE document.id = $1 AND document.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY lower(tag.name), tag.id`, documentID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list document tags for document: %w", err)
	}
	defer rows.Close()
	return scanTags(rows)
}

func (s *PostgresStore) SetDocumentTags(ctx context.Context, knowledgeBaseID, documentID int64, tagIDs []int64) ([]Tag, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin document tag update: %w", err)
	}
	defer tx.Rollback()
	var documentExists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM documents AS document
			JOIN knowledge_bases AS kb ON kb.id = document.knowledge_base_id
			WHERE document.id = $1 AND document.knowledge_base_id = $2
			  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		)`, documentID, knowledgeBaseID).Scan(&documentExists); err != nil {
		return nil, fmt.Errorf("check document for tags: %w", err)
	}
	if !documentExists {
		return nil, ErrDocumentNotFound
	}
	if len(tagIDs) > 0 {
		var validCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM knowledge_base_tags AS tag
			JOIN knowledge_bases AS kb ON kb.id = tag.knowledge_base_id
			WHERE tag.knowledge_base_id = $1 AND tag.id = ANY($2::bigint[])
			  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, pq.Array(tagIDs)).Scan(&validCount); err != nil {
			return nil, fmt.Errorf("check document tag ownership: %w", err)
		}
		if validCount != len(tagIDs) {
			return nil, ErrTagNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_tags WHERE document_id = $1`, documentID); err != nil {
		return nil, fmt.Errorf("clear document tags: %w", err)
	}
	if len(tagIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_tags (document_id, tag_id) SELECT $1, unnest($2::bigint[])`, documentID, pq.Array(tagIDs)); err != nil {
			return nil, fmt.Errorf("assign document tags: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit document tags: %w", err)
	}
	return s.ListDocumentTags(ctx, knowledgeBaseID, documentID)
}

func scanTags(rows *sql.Rows) ([]Tag, error) {
	tags := make([]Tag, 0)
	for rows.Next() {
		var tag Tag
		if err := rows.Scan(&tag.ID, &tag.KnowledgeBaseID, &tag.Name, &tag.Color, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document tag: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document tags: %w", err)
	}
	return tags, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
