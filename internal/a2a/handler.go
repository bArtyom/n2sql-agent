// Package a2a contains the project's intentionally small HTTP adapter for
// agent-to-agent requests. It is not a complete implementation of the A2A
// protocol; it provides a stable Agent Card and a task-oriented JSON API.
package a2a

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

const (
	defaultTimeout    = 5 * time.Minute
	maxRequestBytes   = 16 * 1024
	maxMessageBytes   = 8000
	defaultAgentName  = "n2sql-agent"
	defaultAgentVer   = "0.1.0"
	defaultAgentSkill = "knowledge_base_question_answering"
)

var (
	ErrInvalidTaskRequest = errors.New("invalid A2A task request")
	ErrTaskNotFound       = errors.New("A2A task not found")
)

type TaskStatus string

const (
	StatusSubmitted TaskStatus = "submitted"
	StatusWorking   TaskStatus = "working"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
)

type TaskRequest struct {
	KnowledgeBaseID int64  `json:"knowledge_base_id"`
	Message         string `json:"message"`
	TopK            int    `json:"top_k,omitempty"`
}

type task struct {
	ID        string
	Request   TaskRequest
	Status    TaskStatus
	Response  multiagent.Response
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type taskView struct {
	ID        string     `json:"id"`
	Status    TaskStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	Error     string     `json:"error,omitempty"`
}

type Handler struct {
	answerer multiagent.Answerer
	timeout  time.Duration
	metrics  *metrics.Registry

	mu    sync.RWMutex
	tasks map[string]*task
}

var _ http.Handler = (*Handler)(nil)

func NewHandler(answerer multiagent.Answerer) http.Handler {
	return NewHandlerWithTimeout(answerer, defaultTimeout)
}

func NewHandlerWithTimeout(answerer multiagent.Answerer, timeout time.Duration) http.Handler {
	return NewHandlerWithTimeoutAndMetrics(answerer, timeout, nil)
}

func NewHandlerWithTimeoutAndMetrics(answerer multiagent.Answerer, timeout time.Duration, registry *metrics.Registry) http.Handler {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Handler{answerer: answerer, timeout: timeout, metrics: registry, tasks: make(map[string]*task)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent.json":
		h.writeJSON(w, http.StatusOK, agentCard())
	case r.Method == http.MethodPost && r.URL.Path == "/api/a2a/tasks":
		h.createTask(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/a2a/tasks/"):
		h.getTask(w, r)
	default:
		h.writeError(w, http.StatusNotFound, "A2A endpoint not found")
	}
}

func agentCard() map[string]any {
	return map[string]any{
		"name":        defaultAgentName,
		"version":     defaultAgentVer,
		"description": "回答当前知识库中的文档问题",
		"url":         "/api/a2a/tasks",
		"skills": []map[string]any{{
			"id":          defaultAgentSkill,
			"name":        "知识库问答",
			"description": "根据知识库检索结果回答问题，并返回引用来源",
			"input":       map[string]any{"knowledge_base_id": "integer", "message": "string", "top_k": "integer"},
		}},
	}
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	if h.answerer == nil {
		h.writeError(w, http.StatusInternalServerError, "A2A service unavailable")
		return
	}
	var request TaskRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		h.writeError(w, http.StatusBadRequest, "invalid A2A task request")
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.TopK == 0 {
		request.TopK = retrieval.DefaultResults
	}
	if request.KnowledgeBaseID <= 0 || request.Message == "" || len(request.Message) > maxMessageBytes || request.TopK < 1 || request.TopK > retrieval.MaxResults {
		h.writeError(w, http.StatusBadRequest, "invalid A2A task request")
		return
	}

	now := time.Now().UTC()
	created := &task{ID: newTaskID(), Request: request, Status: StatusSubmitted, CreatedAt: now, UpdatedAt: now}
	view := taskViewOf(created)
	h.mu.Lock()
	h.tasks[created.ID] = created
	h.mu.Unlock()
	h.observeTask(created, metrics.A2AStatusSubmitted, 0)
	slog.InfoContext(r.Context(), "a2a_task_submitted", "task_id", created.ID, "knowledge_base_id", request.KnowledgeBaseID)
	go h.runTask(created.ID)
	h.writeJSON(w, http.StatusAccepted, view)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/a2a/tasks/")
	if path == "" {
		h.writeError(w, http.StatusNotFound, "A2A task not found")
		return
	}
	if strings.HasSuffix(path, "/result") {
		h.getResult(w, strings.TrimSuffix(path, "/result"))
		return
	}
	h.mu.RLock()
	current, ok := h.tasks[path]
	if ok {
		view := taskViewOf(current)
		h.mu.RUnlock()
		h.writeJSON(w, http.StatusOK, view)
		return
	}
	h.mu.RUnlock()
	h.writeError(w, http.StatusNotFound, "A2A task not found")
}

func (h *Handler) getResult(w http.ResponseWriter, id string) {
	h.mu.RLock()
	current, ok := h.tasks[id]
	if !ok {
		h.mu.RUnlock()
		h.writeError(w, http.StatusNotFound, "A2A task not found")
		return
	}
	status := current.Status
	response := current.Response
	view := taskViewOf(current)
	h.mu.RUnlock()

	switch status {
	case StatusCompleted:
		h.writeJSON(w, http.StatusOK, response)
	case StatusFailed:
		h.writeJSON(w, http.StatusBadGateway, view)
	default:
		h.writeJSON(w, http.StatusAccepted, view)
	}
}

func (h *Handler) runTask(id string) {
	h.mu.Lock()
	current, ok := h.tasks[id]
	if !ok {
		h.mu.Unlock()
		return
	}
	current.Status = StatusWorking
	current.UpdatedAt = time.Now().UTC()
	request := current.Request
	started := current.UpdatedAt
	h.mu.Unlock()
	h.observeTask(current, metrics.A2AStatusStarted, 0)
	slog.Info("a2a_task_started", "task_id", id, "knowledge_base_id", request.KnowledgeBaseID)

	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	response, err := h.answerer.Answer(ctx, request.KnowledgeBaseID, request.Message, request.TopK)

	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok = h.tasks[id]
	if !ok {
		return
	}
	current.UpdatedAt = time.Now().UTC()
	if err != nil {
		current.Status = StatusFailed
		current.Error = publicTaskError(err)
		duration := current.UpdatedAt.Sub(started)
		h.observeTask(current, metrics.A2AStatusFailed, duration)
		slog.Error("a2a_task_failed", "task_id", id, "knowledge_base_id", request.KnowledgeBaseID, "duration_ms", duration.Milliseconds(), "error_kind", current.Error)
		return
	}
	current.Status = StatusCompleted
	current.Response = response
	duration := current.UpdatedAt.Sub(started)
	h.observeTask(current, metrics.A2AStatusCompleted, duration)
	slog.Info("a2a_task_completed", "task_id", id, "knowledge_base_id", request.KnowledgeBaseID, "duration_ms", duration.Milliseconds())
}

func (h *Handler) observeTask(current *task, status string, duration time.Duration) {
	if h.metrics != nil {
		h.metrics.ObserveA2ATask(status, duration)
	}
}

func taskViewOf(current *task) taskView {
	return taskView{ID: current.ID, Status: current.Status, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt, Error: current.Error}
}

func newTaskID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "task-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("task-%d", time.Now().UnixNano())
}

func publicTaskError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "task timed out"
	case errors.Is(err, context.Canceled):
		return "task canceled"
	default:
		return "task execution failed"
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func (h *Handler) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, map[string]string{"error": message})
}
