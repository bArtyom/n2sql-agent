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
	// DefaultTitle is the placeholder title of a new conversation until the
	// first exchange is saved and AutoTitle replaces it.
	DefaultTitle              = "新对话"
	maxTitleBytes             = 200
	maxMessageSize            = 64 * 1024
	maxIdempotencyKeySize     = 128
	maxChatModelBytes         = 200
	maxSearchQueryBytes       = 200
	defaultConversationLimit  = 50
	maxConversationLimit      = 100
	conversationLockNamespace = 0x6e327361
	maxAutoTitleRunes         = 30
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
	ErrInvalidChatModel       = errors.New("invalid conversation chat model")
	ErrInvalidSearchQuery     = errors.New("invalid conversation search query")
	ErrInvalidSearchLimit     = errors.New("invalid conversation search limit")
	ErrInvalidFeedback        = errors.New("invalid conversation feedback")
	ErrFeedbackUnavailable    = errors.New("conversation feedback is unavailable")
)

type Conversation struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledgeBaseId"`
	Title           string    `json:"title"`
	IsPinned        bool      `json:"isPinned"`
	ChatModel       string    `json:"chatModel,omitempty"`
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

type Feedback struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversationId"`
	MessageID      int64     `json:"messageId"`
	Rating         int       `json:"rating"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type FeedbackStats struct {
	Total        int     `json:"total"`
	Positive     int     `json:"positive"`
	Negative     int     `json:"negative"`
	PositiveRate float64 `json:"positiveRate"`
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
	DocumentID       int64    `json:"documentId"`
	OriginalFilename string   `json:"originalFilename,omitempty"`
	AssetURL         string   `json:"assetUrl,omitempty"`
	AssetURLs        []string `json:"assetUrls,omitempty"`
	Position         int      `json:"position"`
	ChunkKind        string   `json:"chunkKind,omitempty"`
	Content          string   `json:"content"`
	HeadingPath      string   `json:"headingPath,omitempty"`
	ContentTruncated bool     `json:"contentTruncated,omitempty"`
	ParentContent    string   `json:"parentContent,omitempty"`
	ParentPosition   int      `json:"parentPosition,omitempty"`
	Distance         float64  `json:"distance"`
	MatchType        string   `json:"matchType,omitempty"`
	KeywordScore     float64  `json:"keywordScore,omitempty"`
	HeadingScore     float64  `json:"headingScore,omitempty"`
	FusionScore      float64  `json:"fusionScore,omitempty"`
	RerankScore      float64  `json:"rerankScore,omitempty"`
}

// AgentTrace is the small, display-safe part of an Agent run that we restore
// in the conversation UI. It deliberately excludes model reasoning text and
// raw tool payloads.
type AgentTrace struct {
	RunID  string            `json:"run_id,omitempty"`
	Status string            `json:"status,omitempty"`
	Stats  *AgentTraceStats  `json:"stats,omitempty"`
	Steps  []AgentTraceStep  `json:"steps,omitempty"`
	Events []AgentTraceEvent `json:"events,omitempty"`
}

// AgentTraceStats is the small, display-safe summary of one Agent run. It is
// kept next to the trace so a restored conversation can show both the event
// detail and the run-level result without replaying the model call.
type AgentTraceStats struct {
	StepCount           int    `json:"step_count,omitempty"`
	ModelCalls          int    `json:"model_calls,omitempty"`
	ToolCalls           int    `json:"tool_calls,omitempty"`
	SuccessfulToolCalls int    `json:"successful_tool_calls,omitempty"`
	FailedToolCalls     int    `json:"failed_tool_calls,omitempty"`
	PromptTokens        int    `json:"prompt_tokens,omitempty"`
	CompletionTokens    int    `json:"completion_tokens,omitempty"`
	EmbeddingTokens     int    `json:"embedding_tokens,omitempty"`
	TotalTokens         int    `json:"total_tokens,omitempty"`
	DurationMS          int64  `json:"duration_ms,omitempty"`
	FailureCategory     string `json:"failure_category,omitempty"`
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
	Type          string   `json:"type"`
	Step          int      `json:"step,omitempty"`
	ToolCallID    string   `json:"tool_call_id,omitempty"`
	ToolName      string   `json:"tool_name,omitempty"`
	Arguments     string   `json:"arguments,omitempty"`
	ResultSummary string   `json:"result_summary,omitempty"`
	SourceKeys    []string `json:"source_keys,omitempty"`
	Status        string   `json:"status"`
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

type Page struct {
	Items   []Conversation `json:"items"`
	HasMore bool           `json:"has_more"`
	Offset  int            `json:"offset"`
	Limit   int            `json:"limit"`
}

type Store interface {
	Create(context.Context, CreateInput) (Conversation, error)
	Get(context.Context, int64) (Conversation, error)
	List(context.Context, int64) ([]Conversation, error)
	Search(context.Context, int64, string, int) ([]Conversation, error)
	ListPage(context.Context, int64, int, int) ([]Conversation, bool, error)
	SearchPage(context.Context, int64, string, int, int) ([]Conversation, bool, error)
	ListMessages(context.Context, int64) ([]Message, error)
	ListMessagesPage(context.Context, int64, int64, int) ([]Message, bool, error)
	GetSummary(context.Context, int64) (Summary, error)
	SaveSummary(context.Context, int64, int64, string) error
	AppendExchange(context.Context, int64, string, string) error
	UpdateTitle(context.Context, int64, string) (Conversation, error)
	SetPinned(context.Context, int64, bool) (Conversation, error)
	SetChatModel(context.Context, int64, string) (Conversation, error)
	ClearMessages(context.Context, int64) error
	Delete(context.Context, int64) error
	DeleteMany(context.Context, int64, []int64) error
}

type idempotencyStore interface {
	GetIdempotentResponse(context.Context, int64, string) (IdempotentResponse, error)
	SaveIdempotentResponse(context.Context, int64, string, string, []byte) error
}

type messageMetadataStore interface {
	AppendExchangeWithMetadata(context.Context, int64, string, string, MessageMetadata) error
}

type feedbackStore interface {
	SaveFeedback(context.Context, int64, int64, int64, int) (Feedback, error)
}

type feedbackStatsStore interface {
	FeedbackStats(context.Context, int64) (FeedbackStats, error)
}

type latestAssistantMessageStore interface {
	LatestAssistantMessageID(context.Context, int64, int64) (int64, error)
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

func (s *Service) SaveFeedback(ctx context.Context, conversationID, knowledgeBaseID, messageID int64, rating int) (Feedback, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 || messageID <= 0 || (rating != -1 && rating != 1) {
		return Feedback{}, ErrInvalidFeedback
	}
	store, ok := s.store.(feedbackStore)
	if !ok {
		return Feedback{}, ErrFeedbackUnavailable
	}
	return store.SaveFeedback(ctx, conversationID, knowledgeBaseID, messageID, rating)
}

func (s *Service) LatestAssistantMessageID(ctx context.Context, conversationID, knowledgeBaseID int64) (int64, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return 0, ErrInvalidConversation
	}
	store, ok := s.store.(latestAssistantMessageStore)
	if !ok {
		return 0, ErrFeedbackUnavailable
	}
	return store.LatestAssistantMessageID(ctx, conversationID, knowledgeBaseID)
}

func (s *Service) FeedbackStats(ctx context.Context, knowledgeBaseID int64) (FeedbackStats, error) {
	if knowledgeBaseID <= 0 {
		return FeedbackStats{}, ErrInvalidKnowledgeBase
	}
	store, ok := s.store.(feedbackStatsStore)
	if !ok {
		return FeedbackStats{}, ErrFeedbackUnavailable
	}
	return store.FeedbackStats(ctx, knowledgeBaseID)
}

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
		title = DefaultTitle
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

// Search returns conversations in one knowledge base whose title contains query.
// Message content has a separate indexed search endpoint.
func (s *Service) Search(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]Conversation, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBase
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQueryBytes {
		return nil, ErrInvalidSearchQuery
	}
	if limit <= 0 {
		limit = defaultConversationLimit
	}
	if limit > maxConversationLimit {
		return nil, ErrInvalidSearchLimit
	}
	return s.store.Search(ctx, knowledgeBaseID, query, limit)
}

func (s *Service) ListPage(ctx context.Context, knowledgeBaseID int64, limit, offset int) (Page, error) {
	if knowledgeBaseID <= 0 || limit <= 0 || limit > maxConversationLimit || offset < 0 {
		return Page{}, ErrInvalidSearchLimit
	}
	items, hasMore, err := s.store.ListPage(ctx, knowledgeBaseID, limit, offset)
	return Page{Items: items, HasMore: hasMore, Offset: offset, Limit: limit}, err
}

func (s *Service) SearchPage(ctx context.Context, knowledgeBaseID int64, query string, limit, offset int) (Page, error) {
	if knowledgeBaseID <= 0 || limit <= 0 || limit > maxConversationLimit || offset < 0 {
		return Page{}, ErrInvalidSearchLimit
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQueryBytes {
		return Page{}, ErrInvalidSearchQuery
	}
	items, hasMore, err := s.store.SearchPage(ctx, knowledgeBaseID, query, limit, offset)
	return Page{Items: items, HasMore: hasMore, Offset: offset, Limit: limit}, err
}

// Get returns a conversation owned by the current administrator. Callers
// that operate in a knowledge-base scope should use the scoped methods below
// as well, so the knowledge-base relationship is still checked before use.
func (s *Service) Get(ctx context.Context, conversationID int64) (Conversation, error) {
	if conversationID <= 0 {
		return Conversation{}, ErrInvalidConversation
	}
	return s.store.Get(ctx, conversationID)
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

// SetPinned pins or unpins a conversation owned by the current knowledge
// base. Pinning only changes list ordering; it does not affect messages.
func (s *Service) SetPinned(ctx context.Context, conversationID, knowledgeBaseID int64, pinned bool) (Conversation, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return Conversation{}, ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return Conversation{}, err
	}
	return s.store.SetPinned(ctx, conversationID, pinned)
}

// SetChatModel stores the server-validated model selection for a conversation.
// An empty value resets the conversation to the provider default; the chat
// execution boundary performs the provider allowlist check before use.
func (s *Service) SetChatModel(ctx context.Context, conversationID, knowledgeBaseID int64, model string) (Conversation, error) {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return Conversation{}, ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return Conversation{}, err
	}
	model = strings.TrimSpace(model)
	if len(model) > maxChatModelBytes {
		return Conversation{}, ErrInvalidChatModel
	}
	return s.store.SetChatModel(ctx, conversationID, model)
}

// AutoTitle replaces the default placeholder title with a short version of
// the first user question. It is a no-op when the conversation was already
// renamed by the user, so it never overwrites an explicit title.
func (s *Service) AutoTitle(ctx context.Context, conversationID, knowledgeBaseID int64, question string) error {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return ErrInvalidConversation
	}
	record, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID)
	if err != nil {
		return err
	}
	if record.Title != DefaultTitle {
		return nil
	}
	title := titleFromQuestion(question)
	if title == "" {
		return nil
	}
	_, err = s.store.UpdateTitle(ctx, conversationID, title)
	return err
}

// titleFromQuestion collapses whitespace and caps the first line at
// maxAutoTitleRunes runes so a question with newlines or trailing details
// still produces a readable list title.
func titleFromQuestion(question string) string {
	firstLine := strings.Split(strings.TrimSpace(question), "\n")[0]
	text := strings.Join(strings.Fields(firstLine), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxAutoTitleRunes {
		return text
	}
	return string(runes[:maxAutoTitleRunes]) + "…"
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

func (s *Service) DeleteMany(ctx context.Context, knowledgeBaseID int64, ids []int64) error {
	if knowledgeBaseID <= 0 || len(ids) == 0 || len(ids) > 100 {
		return ErrInvalidConversation
	}
	for _, id := range ids {
		if id <= 0 {
			return ErrInvalidConversation
		}
	}
	return s.store.DeleteMany(ctx, knowledgeBaseID, ids)
}

func (s *Service) SetPinnedMany(ctx context.Context, knowledgeBaseID int64, ids []int64, pinned bool) error {
	if knowledgeBaseID <= 0 || len(ids) == 0 || len(ids) > 100 {
		return ErrInvalidConversation
	}
	for _, id := range ids {
		if id <= 0 {
			return ErrInvalidConversation
		}
		if _, err := s.getOwnedConversation(ctx, id, knowledgeBaseID); err != nil {
			return err
		}
		if _, err := s.store.SetPinned(ctx, id, pinned); err != nil {
			return err
		}
	}
	return nil
}

// ClearMessages removes every message, summary, and idempotency record of a
// conversation while keeping the conversation itself, so the list entry and
// its title survive a wipe.
func (s *Service) ClearMessages(ctx context.Context, conversationID, knowledgeBaseID int64) error {
	if conversationID <= 0 || knowledgeBaseID <= 0 {
		return ErrInvalidConversation
	}
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return err
	}
	return s.store.ClearMessages(ctx, conversationID)
}

func (s *Service) Messages(ctx context.Context, conversationID, knowledgeBaseID int64) ([]Message, error) {
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return nil, err
	}
	return s.store.ListMessages(ctx, conversationID)
}

// MessagesPage returns up to limit messages older than beforeID in ascending
// order plus whether an earlier page exists. beforeID <= 0 loads the newest
// page. The conversation must belong to the current knowledge base.
func (s *Service) MessagesPage(ctx context.Context, conversationID, knowledgeBaseID, beforeID int64, limit int) ([]Message, bool, error) {
	if _, err := s.getOwnedConversation(ctx, conversationID, knowledgeBaseID); err != nil {
		return nil, false, err
	}
	return s.store.ListMessagesPage(ctx, conversationID, beforeID, limit)
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
		RETURNING id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at`, input.KnowledgeBaseID, input.Title).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt,
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
		SELECT id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at
		FROM conversations
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, id).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt,
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
		SELECT id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at
		FROM conversations
		WHERE knowledge_base_id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY is_pinned DESC, updated_at DESC, id DESC`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	results := make([]Conversation, 0)
	for rows.Next() {
		var result Conversation
		if err := rows.Scan(&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) ListPage(ctx context.Context, knowledgeBaseID int64, limit, offset int) ([]Conversation, bool, error) {
	return s.queryConversationPage(ctx, knowledgeBaseID, "", limit, offset)
}

func (s *PostgresStore) Search(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.knowledge_base_id, c.title, c.is_pinned, c.chat_model, c.created_at, c.updated_at
		FROM conversations c
		WHERE c.knowledge_base_id = $1
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		  AND lower(c.title) LIKE '%' || lower($2) || '%'
		ORDER BY c.is_pinned DESC, c.updated_at DESC, c.id DESC
		LIMIT $3`, knowledgeBaseID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search conversations: %w", err)
	}
	defer rows.Close()
	results := make([]Conversation, 0)
	for rows.Next() {
		var result Conversation
		if err := rows.Scan(&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan searched conversation: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate searched conversations: %w", err)
	}
	return results, nil
}

func (s *PostgresStore) SearchPage(ctx context.Context, knowledgeBaseID int64, query string, limit, offset int) ([]Conversation, bool, error) {
	return s.queryConversationPage(ctx, knowledgeBaseID, query, limit, offset)
}

func (s *PostgresStore) queryConversationPage(ctx context.Context, knowledgeBaseID int64, query string, limit, offset int) ([]Conversation, bool, error) {
	condition := ""
	args := []any{knowledgeBaseID}
	if strings.TrimSpace(query) != "" {
		condition = " AND lower(c.title) LIKE '%' || lower($2) || '%'"
		args = append(args, query)
	}
	args = append(args, limit+1, offset)
	limitArg, offsetArg := "$2", "$3"
	if query != "" {
		limitArg, offsetArg = "$3", "$4"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT c.id, c.knowledge_base_id, c.title, c.is_pinned, c.chat_model, c.created_at, c.updated_at
		FROM conversations c
		WHERE c.knowledge_base_id = $1
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)%s
		ORDER BY c.is_pinned DESC, c.updated_at DESC, c.id DESC
		LIMIT %s OFFSET %s`, condition, limitArg, offsetArg), args...)
	if err != nil {
		return nil, false, fmt.Errorf("page conversations: %w", err)
	}
	defer rows.Close()
	results := make([]Conversation, 0, limit)
	for rows.Next() {
		var result Conversation
		if err := rows.Scan(&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return nil, false, fmt.Errorf("scan paged conversation: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate paged conversations: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	return results, hasMore, nil
}

func (s *PostgresStore) UpdateTitle(ctx context.Context, id int64, title string) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversations
		SET title = $2
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		RETURNING id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at`, id, title).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation title: %w", err)
	}
	return result, nil
}

// SetPinned updates the pinned flag of a conversation owned by the current
// administrator. The refresh_updated_at trigger keeps the list position
// consistent with the user's most recent action on that conversation.
func (s *PostgresStore) SetPinned(ctx context.Context, id int64, pinned bool) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversations
		SET is_pinned = $2
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		RETURNING id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at`, id, pinned).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation pinned: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) SetChatModel(ctx context.Context, id int64, model string) (Conversation, error) {
	var result Conversation
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversations
		SET chat_model = $2
		WHERE id = $1
		  AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		RETURNING id, knowledge_base_id, title, is_pinned, chat_model, created_at, updated_at`, id, model).Scan(
		&result.ID, &result.KnowledgeBaseID, &result.Title, &result.IsPinned, &result.ChatModel, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation chat model: %w", err)
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

func (s *PostgresStore) DeleteMany(ctx context.Context, knowledgeBaseID int64, ids []int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE knowledge_base_id = $1 AND id = ANY($2) AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, ids)
	if err != nil {
		return fmt.Errorf("delete conversations: %w", err)
	}
	return nil
}

// ClearMessages wipes the messages, summaries, and idempotency records of one
// conversation in a single transaction. The conversation row itself is kept.
// Every statement re-checks administrator ownership so a stale ID cannot
// clear another administrator's data.
func (s *PostgresStore) ClearMessages(ctx context.Context, conversationID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear conversation messages: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []string{
		`DELETE FROM conversation_messages
		 WHERE conversation_id = $1
		   AND $1 IN (SELECT id FROM conversations
		              WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1))`,
		`DELETE FROM conversation_summaries
		 WHERE conversation_id = $1
		   AND $1 IN (SELECT id FROM conversations
		              WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1))`,
		`DELETE FROM conversation_idempotency_keys
		 WHERE conversation_id = $1
		   AND $1 IN (SELECT id FROM conversations
		              WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1))`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, conversationID); err != nil {
			return fmt.Errorf("clear conversation data: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit clear conversation messages: %w", err)
	}
	return nil
}

func (s *PostgresStore) SaveFeedback(ctx context.Context, conversationID, knowledgeBaseID, messageID int64, rating int) (Feedback, error) {
	var result Feedback
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO conversation_message_feedback (administrator_id, conversation_id, message_id, rating)
		SELECT c.administrator_id, c.id, m.id, $4
		FROM conversations c
		JOIN conversation_messages m ON m.conversation_id = c.id
		WHERE c.id = $1
		  AND c.knowledge_base_id = $2
		  AND m.id = $3
		  AND m.role = 'assistant'
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ON CONFLICT (administrator_id, message_id) DO UPDATE
		SET rating = EXCLUDED.rating, updated_at = CURRENT_TIMESTAMP
		RETURNING id, conversation_id, message_id, rating, created_at, updated_at`, conversationID, knowledgeBaseID, messageID, rating).
		Scan(&result.ID, &result.ConversationID, &result.MessageID, &result.Rating, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Feedback{}, ErrNotFound
	}
	if err != nil {
		return Feedback{}, fmt.Errorf("save conversation feedback: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) FeedbackStats(ctx context.Context, knowledgeBaseID int64) (FeedbackStats, error) {
	var stats FeedbackStats
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE f.rating = 1)::int,
		       COUNT(*) FILTER (WHERE f.rating = -1)::int
		FROM conversation_message_feedback f
		JOIN conversations c ON c.id = f.conversation_id
		WHERE c.knowledge_base_id = $1
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID).
		Scan(&stats.Total, &stats.Positive, &stats.Negative)
	if err != nil {
		return FeedbackStats{}, fmt.Errorf("get conversation feedback stats: %w", err)
	}
	if stats.Total > 0 {
		stats.PositiveRate = float64(stats.Positive) / float64(stats.Total)
	}
	return stats, nil
}

func (s *PostgresStore) LatestAssistantMessageID(ctx context.Context, conversationID, knowledgeBaseID int64) (int64, error) {
	var messageID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id
		FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE c.id = $1
		  AND c.knowledge_base_id = $2
		  AND m.role = 'assistant'
		  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY m.id DESC
		LIMIT 1`, conversationID, knowledgeBaseID).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get latest assistant message: %w", err)
	}
	return messageID, nil
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

// ListMessagesPage fetches at most limit messages with id < beforeID in
// ascending order. It reads limit+1 rows to report whether an earlier page
// exists. beforeID <= 0 returns the newest page.
func (s *PostgresStore) ListMessagesPage(ctx context.Context, conversationID, beforeID int64, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, conversation_id, role, content, metadata, created_at
		FROM (
			SELECT m.id, m.conversation_id, m.role, m.content, m.metadata, m.created_at
			FROM conversation_messages m
			JOIN conversations c ON c.id = m.conversation_id
			WHERE m.conversation_id = $1
			  AND ($2 <= 0 OR m.id < $2)
			  AND c.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
			ORDER BY m.id DESC
			LIMIT $3
		) newest
		ORDER BY id ASC`, conversationID, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list conversation messages page: %w", err)
	}
	defer rows.Close()
	results := make([]Message, 0, limit)
	for rows.Next() {
		var result Message
		var metadataBytes []byte
		if err := rows.Scan(&result.ID, &result.ConversationID, &result.Role, &result.Content, &metadataBytes, &result.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan conversation message page: %w", err)
		}
		if len(metadataBytes) > 0 && string(metadataBytes) != "{}" {
			var metadata MessageMetadata
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				return nil, false, fmt.Errorf("decode conversation message metadata: %w", err)
			}
			result.Metadata = &metadata
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate conversation message page: %w", err)
	}
	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}
	return results, hasMore, nil
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
