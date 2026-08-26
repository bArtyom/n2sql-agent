package knowledgebase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound   = errors.New("knowledge base not found")
	ErrConflict   = errors.New("knowledge base already exists")
	ErrProcessing = errors.New("knowledge base has documents in processing")
)

type ParserEngineRule = documentextractor.ParserEngineRule

type KnowledgeBase struct {
	ID                int64                                `json:"id"`
	Name              string                               `json:"name"`
	Description       string                               `json:"description"`
	ParserEngineRules []documentextractor.ParserEngineRule `json:"parserEngineRules,omitempty"`
}

type CreateInput struct {
	Name              string
	Description       string
	ParserEngineRules []documentextractor.ParserEngineRule
}

type Store interface {
	Create(context.Context, CreateInput) (KnowledgeBase, error)
	List(context.Context) ([]KnowledgeBase, error)
	Delete(context.Context, int64) error
}

type ParserRulesStore interface {
	UpdateParserEngineRules(context.Context, int64, []documentextractor.ParserEngineRule) (KnowledgeBase, error)
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
	rules, err := json.Marshal(input.ParserEngineRules)
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("encode parser engine rules: %w", err)
	}
	user, authenticated := auth.UserFromContext(ctx)
	if authenticated && user.ID > 0 {
		err = s.createForUser(ctx, user.ID, input.Name, input.Description, rules, &knowledgeBase)
	} else {
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO knowledge_bases (administrator_id, name, description, parser_engine_rules)
			SELECT administrator_id, $1, $2, $3::jsonb FROM system_settings WHERE id = 1
			RETURNING id, name, description, parser_engine_rules`, input.Name, input.Description, rules).Scan(
			&knowledgeBase.ID,
			&knowledgeBase.Name,
			&knowledgeBase.Description,
			&rules,
		)
	}
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return KnowledgeBase{}, ErrConflict
		}
		return KnowledgeBase{}, fmt.Errorf("create knowledge base: %w", err)
	}
	if err := json.Unmarshal(rules, &knowledgeBase.ParserEngineRules); err != nil {
		return KnowledgeBase{}, fmt.Errorf("decode parser engine rules: %w", err)
	}
	return knowledgeBase, nil
}

func (s *PostgresStore) createForUser(ctx context.Context, userID int64, name, description string, rules []byte, result *KnowledgeBase) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin knowledge base creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO knowledge_bases (administrator_id, name, description, parser_engine_rules)
		SELECT administrator_id, $1, $2, $3::jsonb FROM system_settings WHERE id = 1
		RETURNING id, name, description, parser_engine_rules`, name, description, rules).Scan(
		&result.ID, &result.Name, &result.Description, &rules,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge_base_members (knowledge_base_id, user_id, role)
		VALUES ($1, $2, 'owner')`, result.ID, userID); err != nil {
		return fmt.Errorf("create knowledge base owner membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit knowledge base creation: %w", err)
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context) ([]KnowledgeBase, error) {
	user, authenticated := auth.UserFromContext(ctx)
	query := `
		SELECT id, name, description, parser_engine_rules
		FROM knowledge_bases
		WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY id`
	var rows *sql.Rows
	var err error
	if authenticated && user.ID > 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT kb.id, kb.name, kb.description, kb.parser_engine_rules
			FROM knowledge_bases AS kb
			JOIN knowledge_base_members AS member
			  ON member.knowledge_base_id = kb.id
			WHERE kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
			  AND member.user_id = $1
			ORDER BY kb.id`, user.ID)
	} else {
		rows, err = s.db.QueryContext(ctx, query)
	}
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	defer rows.Close()

	knowledgeBases := make([]KnowledgeBase, 0)
	for rows.Next() {
		var knowledgeBase KnowledgeBase
		var rules []byte
		if err := rows.Scan(&knowledgeBase.ID, &knowledgeBase.Name, &knowledgeBase.Description, &rules); err != nil {
			return nil, fmt.Errorf("scan knowledge base: %w", err)
		}
		if len(rules) > 0 {
			if err := json.Unmarshal(rules, &knowledgeBase.ParserEngineRules); err != nil {
				return nil, fmt.Errorf("decode knowledge base parser engine rules: %w", err)
			}
		}
		knowledgeBases = append(knowledgeBases, knowledgeBase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge bases: %w", err)
	}
	return knowledgeBases, nil
}

func (s *PostgresStore) UpdateParserEngineRules(ctx context.Context, id int64, rules []documentextractor.ParserEngineRule) (KnowledgeBase, error) {
	encoded, err := json.Marshal(rules)
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("encode parser engine rules: %w", err)
	}
	var knowledgeBase KnowledgeBase
	var stored []byte
	user, authenticated := auth.UserFromContext(ctx)
	if authenticated && user.ID > 0 {
		err = s.db.QueryRowContext(ctx, `
			UPDATE knowledge_bases
			SET parser_engine_rules = $2::jsonb
			WHERE id = $1
			  AND EXISTS (
				SELECT 1 FROM knowledge_base_members
				WHERE knowledge_base_id = knowledge_bases.id
				  AND user_id = $3
				  AND role IN ('owner', 'editor')
			  )
			RETURNING id, name, description, parser_engine_rules`, id, encoded, user.ID).Scan(
			&knowledgeBase.ID, &knowledgeBase.Name, &knowledgeBase.Description, &stored,
		)
	} else {
		err = s.db.QueryRowContext(ctx, `
			UPDATE knowledge_bases
			SET parser_engine_rules = $2::jsonb
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
			RETURNING id, name, description, parser_engine_rules`, id, encoded).Scan(
			&knowledgeBase.ID, &knowledgeBase.Name, &knowledgeBase.Description, &stored,
		)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return KnowledgeBase{}, ErrNotFound
	}
	if err != nil {
		return KnowledgeBase{}, fmt.Errorf("update parser engine rules: %w", err)
	}
	if err := json.Unmarshal(stored, &knowledgeBase.ParserEngineRules); err != nil {
		return KnowledgeBase{}, fmt.Errorf("decode updated parser engine rules: %w", err)
	}
	return knowledgeBase, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	user, authenticated := auth.UserFromContext(ctx)
	var result sql.Result
	var err error
	if authenticated && user.ID > 0 {
		result, err = s.db.ExecContext(ctx, `
			DELETE FROM knowledge_bases
			WHERE id = $1
			  AND EXISTS (
				SELECT 1 FROM knowledge_base_members
				WHERE knowledge_base_id = knowledge_bases.id
				  AND user_id = $2 AND role = 'owner'
			  )`, id, user.ID)
	} else {
		result, err = s.db.ExecContext(ctx, `
			DELETE FROM knowledge_bases
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id)
	}
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

	user, authenticated := auth.UserFromContext(ctx)
	var exists bool
	var existsErr error
	if authenticated && user.ID > 0 {
		existsErr = tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM knowledge_bases
				WHERE id = $1
				  AND EXISTS (
					SELECT 1 FROM knowledge_base_members
					WHERE knowledge_base_id = knowledge_bases.id
					  AND user_id = $2 AND role = 'owner'
				  )
			)
		`, id, user.ID).Scan(&exists)
	} else {
		existsErr = tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM knowledge_bases
				WHERE id = $1
				  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
			)
		`, id).Scan(&exists)
	}
	if err := existsErr; err != nil {
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

	var result sql.Result
	if authenticated && user.ID > 0 {
		result, err = tx.ExecContext(ctx, `
			DELETE FROM knowledge_bases
			WHERE id = $1
			  AND EXISTS (
				SELECT 1 FROM knowledge_base_members
				WHERE knowledge_base_id = knowledge_bases.id
				  AND user_id = $2 AND role = 'owner'
			  )`, id, user.ID)
	} else {
		result, err = tx.ExecContext(ctx, `
			DELETE FROM knowledge_bases
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id)
	}
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
