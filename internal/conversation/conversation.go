package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

const (
	defaultTitle                    = "新对话"
	maxTitleBytes                   = 200
	maxMessageSize                  = 64 * 1024
	maxIdempotencyKeySize           = 128
	conversationLockNamespace int64 = 0x6e327361
)

var (
	ErrInvalidKnowledgeBase   = errors.New("invalid conversation knowledge base")
	ErrInvalidConversation    = errors.New("invalid conversation ID")
	ErrInvalidTitle           = errors.New("invalid conversation title")
	ErrInvalidMessage         = errors.New("invalid conversation message")
	ErrInvalidIdempotencyKey  = errors.New("invalid idempotency key")
	ErrIdempotencyConflict    = errors.New("idempotency key reused with a different request")
	ErrIdempotencyUnavailable = errors.New("conversation idempotency is unavailable")
	ErrNotFound               = errors.New("conversation not found")
)

type Conversation struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Message struct {
	ID             int64            `json:"id"`
	ConversationID int64            `json:"conversationId"`
	Role           string           `json:"role"`
	Content        string           `json:"content"`
	Metadata       *MessageMetadata `json:"metadata,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
}

// MessageMetadata contains bounded execution information and source snapshots
// that help the UI restore how an assistant answer was produced. It is
// optional so old messages and user messages remain source-compatible.
type MessageMetadata struct {
	QueryRewrite *usage.QueryRewriteObservation `json:"query_rewrite,omitempty"`
	Retrieval    *usage.RetrievalObservation    `json:"retrieval,omitempty"`
	Sources      []SourceReference              `json:"sources,omitempty"`
	AgentTrace   *AgentTrace                    `json:"agent_trace,omitempty"`
}

// SourceReference is a bounded citation snapshot. The document and position
// identify the original chunk; Content is intentionally capped by the caller
// so reopening a conversation does not turn the messages table into a second
// copy of the document corpus.
type SourceReference struct {
	DocumentID       int64   `json:"documentId"`
	OriginalFilename string  `json:"originalFilename,omitempty"`
	Position         int     `json:"position"`
	Content          string  `json:"content"`
	ContentTruncated bool    `json:"contentTruncated,omitempty"`
	ParentContent    string  `json:"parentContent,omitempty"`
	ParentPosition   int     `json:"parentPosition,omitempty"`
	Distance         float64 `json:"distance"`
	MatchType        string  `json:"matchType,omitempty"`
	KeywordScore     float64 `json:"keywordScore,omitempty"`
	FusionScore      float64 `json:"fusionScore,omitempty"`
	RerankScore      float64 `json:"rerankScore,omitempty"`
}

// AgentTrace is the small, display-safe part of an Agent run that we restore
// in the conversation UI. It deliberately excludes model reasoning text and
// raw tool payloads.
type AgentTrace struct {
	RunID  string            `json:"run_id,omitempty"`
	Status string            `json:"status,omitempty"`
	Steps  []AgentTraceStep  `json:"steps,omitempty"`
	Events []AgentTraceEvent `json:"events,omitempty"`
}

type AgentTraceStep struct {
	Number   int    `json:"number"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	ToolName string `json:"tool_name,omitempty"`
}

// AgentTraceEvent stores one bounded tool call and its result summary. Raw
// tool output is deliberately excluded; citations remain in Sources.
type AgentTraceEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`
	Status        string `json:"status"`
}

type Summary struct {
	ConversationID   int64     `json:"conversationId"`
	ThroughMessageID int64     `json:"throughMessageId"`
	Content          string    `json:"content"`
	UpdatedAt        time.Time `json:"updatedAt"`
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
	GetSummary(context.Context, int64) (Summary, error)
	SaveSummary(context.Context, int64, int64, string) error
	AppendExchange(context.Context, int64, string, string) error
	UpdateTitle(context.Context, int64, string) (Conversation, error)
	Delete(context.Context, int64) error
}

type idempotencyStore interface {
	GetIdempotentResponse(context.Context, int64, string) (IdempotentResponse, error)
	SaveIdempotentResponse(context.Context, int64, string, string, []byte) error
}

type messageMetadataStore interface {
	AppendExchangeWithMetadata(context.Context, int64, string, string, MessageMetadata) error
}

type distributedConversationLocker interface {
	WithConversationLock(context.Context, int64, func() error) error
}

type IdempotentResponse struct {
	RequestHash string
	Response    []byte
}

type Service struct {
	store        Store
	summaryLocks sync.Map
}

func NewService(store Store) *Service { return &Service{store: store} }

// WithSummaryLock serializes all work that reads or writes conversation context.
// The historical name is kept for callers; PostgreSQL-backed stores also add a
// cross-process advisory lock while the callback is running.
func (s *Service) WithSummaryLock(ctx context.Context, conversationID, knowledgeBaseID int64, fn func() error) error {
	if fn == nil {
		return ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return err
	}
	lock := s.summaryLock(conversationID)
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
		if locker, ok := s.store.(distributedConversationLocker); ok {
			return locker.WithConversationLock(ctx, conversationID, fn)
		}
		return fn()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) summaryLock(conversationID int64) chan struct{} {
	if lock, ok := s.summaryLocks.Load(conversationID); ok {
		return lock.(chan struct{})
	}
	created := make(chan struct{}, 1)
	actual, _ := s.summaryLocks.LoadOrStore(conversationID, created)
	return actual.(chan struct{})
}

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
		history = append(history, agentservice.HistoryMessage{ID: message.ID, Role: message.Role, Content: message.Content})
	}
	return history, nil
}

func (s *Service) Summary(ctx context.Context, conversationID, knowledgeBaseID int64) (Summary, error) {
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return Summary{}, err
	}
	return s.store.GetSummary(ctx, conversationID)
}

func (s *Service) SaveSummary(ctx context.Context, conversationID, knowledgeBaseID, throughMessageID int64, content string) error {
	if conversationID <= 0 || knowledgeBaseID <= 0 || throughMessageID <= 0 {
		return ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return err
	}
	content = strings.TrimSpace(content)
	if content == "" || len(content) > maxMessageSize {
		return ErrInvalidMessage
	}
	return s.store.SaveSummary(ctx, conversationID, throughMessageID, content)
}

// ValidateIdempotencyKey reports whether a request key is safe to persist.
// Keys are deliberately restricted to header-friendly ASCII characters so
// they are stable across proxies and can be used as a database key.
func ValidateIdempotencyKey(key string) bool {
	_, ok := normalizeIdempotencyKey(key)
	return ok
}

func normalizeIdempotencyKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxIdempotencyKeySize {
		return "", false
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return "", false
	}
	return key, true
}

func (s *Service) GetIdempotentResponse(ctx context.Context, conversationID, knowledgeBaseID int64, key, requestHash string) ([]byte, error) {
	normalizedKey, ok := normalizeIdempotencyKey(key)
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return nil, ErrInvalidConversation
	}
	if !ok {
		return nil, ErrInvalidIdempotencyKey
	}
	if !validRequestHash(requestHash) {
		return nil, ErrIdempotencyConflict
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return nil, err
	}
	store, ok := s.store.(idempotencyStore)
	if !ok {
		return nil, ErrIdempotencyUnavailable
	}
	stored, err := store.GetIdempotentResponse(ctx, conversationID, normalizedKey)
	if err != nil {
		return nil, err
	}
	if stored.RequestHash != requestHash {
		return nil, ErrIdempotencyConflict
	}
	return stored.Response, nil
}

func (s *Service) SaveIdempotentResponse(ctx context.Context, conversationID, knowledgeBaseID int64, key, requestHash string, response []byte) error {
	normalizedKey, ok := normalizeIdempotencyKey(key)
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return ErrInvalidConversation
	}
	if !ok {
		return ErrInvalidIdempotencyKey
	}
	if !validRequestHash(requestHash) {
		return ErrIdempotencyConflict
	}
	if len(response) == 0 {
		return ErrInvalidMessage
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return err
	}
	store, ok := s.store.(idempotencyStore)
	if !ok {
		return ErrIdempotencyUnavailable
	}
	return store.SaveIdempotentResponse(ctx, conversationID, normalizedKey, requestHash, response)
}

func validRequestHash(requestHash string) bool {
	if len(requestHash) != 64 {
		return false
	}
	for index := 0; index < len(requestHash); index++ {
		character := requestHash[index]
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

func (s *Service) SaveExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string) error {
	return s.saveExchange(ctx, conversationID, userMessage, assistantMessage, nil)
}

// SaveExchangeWithMetadata persists an exchange and optional bounded execution
// metadata. Stores that predate the metadata extension keep the old behavior.
func (s *Service) SaveExchangeWithMetadata(ctx context.Context, conversationID int64, userMessage, assistantMessage string, metadata MessageMetadata) error {
	return s.saveExchange(ctx, conversationID, userMessage, assistantMessage, &metadata)
}

func (s *Service) saveExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string, metadata *MessageMetadata) error {
	if conversationID <= 0 {
		return ErrInvalidConversation
	}
	userMessage = strings.TrimSpace(userMessage)
	assistantMessage = strings.TrimSpace(assistantMessage)
	if userMessage == "" || assistantMessage == "" || len(userMessage) > maxMessageSize || len(assistantMessage) > maxMessageSize {
		return ErrInvalidMessage
	}
	if metadata != nil {
		if store, ok := s.store.(messageMetadataStore); ok {
			return store.AppendExchangeWithMetadata(ctx, conversationID, userMessage, assistantMessage, *metadata)
		}
	}
	return s.store.AppendExchange(ctx, conversationID, userMessage, assistantMessage)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) WithConversationLock(ctx context.Context, conversationID int64, fn func() error) (resultErr error) {
	if conversationID <= 0 || fn == nil {
		return ErrInvalidConversation
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire conversation lock connection: %w", err)
	}
	lockKey := conversationLockKey(conversationID)
	var ignored any
	if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey).Scan(&ignored); err != nil {
		_ = conn.Close()
		return fmt.Errorf("acquire conversation advisory lock: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var unlocked bool
		releaseErr := conn.QueryRowContext(releaseCtx, `SELECT pg_advisory_unlock($1)`, lockKey).Scan(&unlocked)
		cancel()
		if releaseErr == nil && !unlocked {
			releaseErr = errors.New("conversation advisory lock was not held")
		}
		if releaseErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release conversation advisory lock: %w", releaseErr))
		}
		if closeErr := conn.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close conversation lock connection: %w", closeErr))
		}
	}()
	return fn()
}

func conversationLockKey(conversationID int64) int64 {
	return (conversationLockNamespace << 32) ^ conversationID
}

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
		SELECT m.id, m.conversation_id, m.role, m.content, m.metadata, m.created_at
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
		var metadataBytes []byte
		if err := rows.Scan(&result.ID, &result.ConversationID, &result.Role, &result.Content, &metadataBytes, &result.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation message: %w", err)
		}
		if len(metadataBytes) > 0 && string(metadataBytes) != "{}" {
			var metadata MessageMetadata
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				return nil, fmt.Errorf("decode conversation message metadata: %w", err)
			}
			result.Metadata = &metadata
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation messages: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) GetSummary(ctx context.Context, conversationID int64) (Summary, error) {
	var result Summary
	err := s.db.QueryRowContext(ctx, `
		SELECT conversation_id, through_message_id, content, updated_at
		FROM conversation_summaries
		WHERE conversation_id = $1`, conversationID).Scan(
		&result.ConversationID, &result.ThroughMessageID, &result.Content, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, ErrNotFound
	}
	if err != nil {
		return Summary{}, fmt.Errorf("get conversation summary: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) SaveSummary(ctx context.Context, conversationID, throughMessageID int64, content string) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_summaries (conversation_id, through_message_id, content)
		SELECT c.id, $2, $3
		FROM conversations c
		JOIN conversation_messages m ON m.conversation_id = c.id AND m.id = $2
		WHERE c.id = $1
		ON CONFLICT (conversation_id) DO UPDATE
		SET through_message_id = EXCLUDED.through_message_id,
		    content = EXCLUDED.content,
		    updated_at = CURRENT_TIMESTAMP
		WHERE EXCLUDED.through_message_id > conversation_summaries.through_message_id`, conversationID, throughMessageID, content)
	if err != nil {
		return fmt.Errorf("save conversation summary: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check conversation summary: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) GetIdempotentResponse(ctx context.Context, conversationID int64, key string) (IdempotentResponse, error) {
	var result IdempotentResponse
	err := s.db.QueryRowContext(ctx, `
		SELECT request_hash, response
		FROM conversation_idempotency_keys
		WHERE conversation_id = $1 AND idempotency_key = $2`, conversationID, key).Scan(&result.RequestHash, &result.Response)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotentResponse{}, ErrNotFound
	}
	if err != nil {
		return IdempotentResponse{}, fmt.Errorf("get conversation idempotent response: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) SaveIdempotentResponse(ctx context.Context, conversationID int64, key, requestHash string, response []byte) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_idempotency_keys (conversation_id, idempotency_key, request_hash, response)
		VALUES ($1, $2, $3, $4::jsonb)
		ON CONFLICT (conversation_id, idempotency_key) DO NOTHING`, conversationID, key, requestHash, string(response)); err != nil {
		return fmt.Errorf("save conversation idempotent response: %w", err)
	}
	return nil
}

func (s *PostgresStore) AppendExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string) error {
	return s.appendExchange(ctx, conversationID, userMessage, assistantMessage, MessageMetadata{})
}

func (s *PostgresStore) AppendExchangeWithMetadata(ctx context.Context, conversationID int64, userMessage, assistantMessage string, metadata MessageMetadata) error {
	return s.appendExchange(ctx, conversationID, userMessage, assistantMessage, metadata)
}

func (s *PostgresStore) appendExchange(ctx context.Context, conversationID int64, userMessage, assistantMessage string, metadata MessageMetadata) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode conversation message metadata: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation exchange: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	defer rollback()
	for _, message := range []struct {
		role     string
		content  string
		metadata []byte
	}{{"user", userMessage, []byte(`{}`)}, {"assistant", assistantMessage, metadataJSON}} {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO conversation_messages (conversation_id, role, content, metadata)
			SELECT id, $2, $3, $4::jsonb
			FROM conversations
			WHERE id = $1
			  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, conversationID, message.role, message.content, string(message.metadata))
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
