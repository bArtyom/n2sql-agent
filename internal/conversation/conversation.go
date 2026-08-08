package conversation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
)

const (
	defaultTitle   = "新对话"
	maxTitleBytes  = 200
	maxMessageSize = 64 * 1024
)

var (
	ErrInvalidKnowledgeBase = errors.New("invalid conversation knowledge base")
	ErrInvalidConversation  = errors.New("invalid conversation ID")
	ErrInvalidTitle         = errors.New("invalid conversation title")
	ErrInvalidMessage       = errors.New("invalid conversation message")
	ErrNotFound             = errors.New("conversation not found")
)

type Conversation struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversationId"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"createdAt"`
}

type CreateInput struct {
	KnowledgeBaseID int64
	Title           string
}

type Store interface {
	Create(context.Context, CreateInput) (Conversation, error)
	Get(context.Context, int64) (Conversation, error)
	List(context.Context, int64) ([]Conversation, error)
	ListMessages(context.Context, int64) ([]Message, error)
	AppendExchange(context.Context, int64, string, string) error
	UpdateTitle(context.Context, int64, string) (Conversation, error)
	Delete(context.Context, int64) error
}

type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Create(ctx context.Context, knowledgeBaseID int64, title string) (Conversation, error) {
	if knowledgeBaseID <= 0 {
		return Conversation{}, ErrInvalidKnowledgeBase
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultTitle
	}
	if len(title) > maxTitleBytes {
		return Conversation{}, ErrInvalidTitle
	}
	return s.store.Create(ctx, CreateInput{KnowledgeBaseID: knowledgeBaseID, Title: title})
}

func (s *Service) List(ctx context.Context, knowledgeBaseID int64) ([]Conversation, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBase
	}
	return s.store.List(ctx, knowledgeBaseID)
}

func (s *Service) Rename(ctx context.Context, conversationID, knowledgeBaseID int64, title string) (Conversation, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return Conversation{}, ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return Conversation{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > maxTitleBytes {
		return Conversation{}, ErrInvalidTitle
	}
	return s.store.UpdateTitle(ctx, conversationID, title)
}

func (s *Service) Delete(ctx context.Context, conversationID, knowledgeBaseID int64) error {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return err
	}
	return s.store.Delete(ctx, conversationID)
}

func (s *Service) Messages(ctx context.Context, conversationID, knowledgeBaseID int64) ([]Message, error) {
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return nil, err
	}
	return s.store.ListMessages(ctx, conversationID)
}

func (s *Service) getOwnedConversation(ctx context.Context, conversationID, knowledgeBaseID int64) (Conversation, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return Conversation{}, ErrInvalidConversation
	}
	conversationRecord, err := s.store.Get(ctx, conversationID)
	if err != nil {
		return Conversation{}, err
	}
	if conversationRecord.KnowledgeBaseID != knowledgeBaseID {
		return Conversation{}, ErrNotFound
	}
	return conversationRecord, nil
}

func (s *Service) History(ctx context.Context, conversationID, knowledgeBaseID int64) ([]agentservice.HistoryMessage, error) {
	messages, err := s.Messages(ctx, conversationID, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	history := make([]agentservice.HistoryMessage, 0, len(messages))
	for _, message := range messages {
		if (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Content) == "" {
			return nil, fmt.Errorf("%w: stored message %d", ErrInvalidMessage, message.ID)
		}
		history = append(history, agentservice.HistoryMessage{Role: message.Role, Content: message.Content})
	}
	return history, nil
}

func (s *Service) SaveExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string) error {
	if conversationID <= 0 {
		return ErrInvalidConversation
	}
	userMessage = strings.TrimSpace(userMessage)
	assistantMessage = strings.TrimSpace(assistantMessage)
	if userMessage == "" || assistantMessage == "" || len(userMessage) > maxMessageSize || len(assistantMessage) > maxMessageSize {
		return ErrInvalidMessage
	}
	return s.store.AppendExchange(ctx, conversationID, userMessage, assistantMessage)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO conversations (administrator_id, knowledge_base_id, title)
		SELECT ss.administrator_id, kb.id, $2
		FROM system_settings ss
		JOIN knowledge_bases kb ON kb.administrator_id = ss.administrator_id
		WHERE ss.id = 1 AND kb.id = $1
		RETURNING id, knowledge_base_id, title, created_at, updated_at`, input.KnowledgeBaseID, input.Title).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) Get(ctx context.Context, id int64) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		SELECT id, knowledge_base_id, title, created_at, updated_at
		FROM conversations
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, knowledge_base_id, title, created_at, updated_at
		FROM conversations
		WHERE knowledge_base_id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY updated_at DESC, id DESC`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	results := make([]Conversation, 0)
	for rows.Next() {
		var result Conversation
		if err := rows.Scan(&result.ID, &result.KnowledgeBaseID, &result.Title, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) UpdateTitle(ctx context.Context, id int64, title string) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversations
		SET title = $2
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		RETURNING id, knowledge_base_id, title, created_at, updated_at`, id, title).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation title: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM conversations
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted conversation: %w", err)
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListMessages(ctx context.Context, conversationID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.conversation_id, m.role, m.content, m.created_at
		FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE m.conversation_id = $1
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY m.id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	results := make([]Message, 0)
	for rows.Next() {
		var result Message
		if err := rows.Scan(&result.ID, &result.ConversationID, &result.Role, &result.Content, &result.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation messages: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) AppendExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation exchange: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	defer rollback()
	for _, message := range []struct {
		role    string
		content string
	}{{"user", userMessage}, {"assistant", assistantMessage}} {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_messages (conversation_id, role, content)
			SELECT id, $2, $3
			FROM conversations
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, conversationID, message.role, message.content)
		if err != nil {
			return fmt.Errorf("append conversation %s message: %w", message.role, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check conversation %s message: %w", message.role, err)
		}
		if affected != 1 {
			return ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations
		SET updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, conversationID); err != nil {
		return fmt.Errorf("refresh conversation timestamp: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation exchange: %w", err)
	}
	return nil
}
