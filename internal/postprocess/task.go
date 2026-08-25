package postprocess

import (
	"context"
	"encoding/json"
	"time"
)

// Kind identifies one durable knowledge post-processing operation.
type Kind string

const (
	KindDocumentSummary  Kind = "document_summary"
	KindSummaryIndex     Kind = "summary_index"
	KindImageOCR         Kind = "image_ocr"
	KindImageCaption     Kind = "image_caption"
	KindFollowUp         Kind = "follow_up"
	KindFAQ              Kind = "faq"
	KindWiki             Kind = "wiki"
	KindRecommendedQuery Kind = "recommended_query"
	KindGraphExtract     Kind = "graph_extract"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
)

type Task struct {
	ID              int64           `json:"id"`
	TaskKey         string          `json:"taskKey"`
	KnowledgeBaseID int64           `json:"knowledgeBaseId"`
	DocumentID      int64           `json:"documentId,omitempty"`
	AssetID         int64           `json:"assetId,omitempty"`
	AssetIndex      int             `json:"assetIndex,omitempty"`
	ChunkPosition   int             `json:"chunkPosition,omitempty"`
	Kind            Kind            `json:"kind"`
	Status          Status          `json:"status"`
	AttemptCount    int             `json:"attemptCount"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	ResultText      string          `json:"result,omitempty"`
	ErrorMessage    string          `json:"errorMessage,omitempty"`
	ModelProvider   string          `json:"modelProvider,omitempty"`
	ModelName       string          `json:"modelName,omitempty"`
	InputTokens     int             `json:"inputTokens,omitempty"`
	OutputTokens    int             `json:"outputTokens,omitempty"`
	CostMicros      int64           `json:"costMicros,omitempty"`
	DurationMS      int64           `json:"durationMs,omitempty"`
	StartedAt       time.Time       `json:"startedAt,omitempty"`
	CompletedAt     time.Time       `json:"completedAt,omitempty"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	LeaseToken      string          `json:"-"`
	LeaseUntil      time.Time       `json:"-"`
}

type EnqueueRequest struct {
	TaskKey         string
	KnowledgeBaseID int64
	DocumentID      int64
	AssetID         int64
	AssetIndex      int
	ChunkPosition   int
	Kind            Kind
	Payload         json.RawMessage
}

type Result struct {
	Text          string
	ModelProvider string
	ModelName     string
	InputTokens   int
	OutputTokens  int
	CostMicros    int64
	DurationMS    int64
	NextTasks     []EnqueueRequest
}

type Store interface {
	Enqueue(context.Context, ...EnqueueRequest) error
	EnqueueDocument(context.Context, int64, DocumentOptions) error
	ClaimNext(context.Context) (Task, error)
	SaveResult(context.Context, Task, Result) error
	MarkSucceeded(context.Context, Task) error
	Requeue(context.Context, Task, string, time.Time) error
	MarkDeadLetter(context.Context, Task, string) error
	ListByDocument(context.Context, int64, int64) ([]Task, error)
}

type DocumentOptions struct {
	EnableSummary         bool
	EnableImageOCR        bool
	EnableImageCaption    bool
	EnableRecommendations bool
	EnableGraph           bool
}

var ErrNoTask = &noTaskError{}

type noTaskError struct{}

func (*noTaskError) Error() string { return "no pending postprocess task" }

type Handler interface {
	Handle(context.Context, Task) (Result, error)
}
