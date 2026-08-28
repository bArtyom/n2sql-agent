package agentruntime

import (
	"context"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

const durableSummaryPrefix = "Agent 短记忆（较早工具结果摘要）：\n"

// InterruptKind identifies a durable pause point in the Agent graph. The
// payload belongs to the unified checkpoint; it is not a second tool or
// context checkpoint.
type InterruptKind string

const (
	InterruptApproval      InterruptKind = "approval"
	InterruptChildren      InterruptKind = "children"
	InterruptClarification InterruptKind = "clarification"
)

// InterruptState is the minimal information needed to show and resume a
// paused run after the original Worker has released its lease.
type InterruptState struct {
	Kind        InterruptKind `json:"kind"`
	ID          string        `json:"id"`
	ToolCallID  string        `json:"tool_call_id"`
	ToolName    string        `json:"tool_name"`
	Arguments   string        `json:"arguments,omitempty"`
	ChildRunIDs []string      `json:"child_run_ids,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
}

// CheckpointState is the single durable Agent state. It is the Go equivalent
// of DeerFlow's LangGraph thread checkpoint: messages, compacted memory and a
// pending tool decision are saved together, so a Worker can resume the graph
// from one coherent state instead of joining separate context/decision/tool
// tables.
type CheckpointState struct {
	Version             int                       `json:"version"`
	LastStep            int                       `json:"last_step"`
	CurrentNode         string                    `json:"current_node,omitempty"`
	Messages            []modelclient.ChatMessage `json:"messages"`
	SummaryText         string                    `json:"summary_text"`
	PendingToolCalls    []modelclient.ToolCall    `json:"pending_tool_calls,omitempty"`
	Interrupt           *InterruptState           `json:"interrupt,omitempty"`
	ApprovedToolCallIDs []string                  `json:"approved_tool_call_ids,omitempty"`
	RejectedToolCallIDs []string                  `json:"rejected_tool_call_ids,omitempty"`
}

// CheckpointSink persists one complete state snapshot at semantic graph
// boundaries. It is never called for individual streamed tokens.
type CheckpointSink func(context.Context, CheckpointState) error

func compactCheckpointState(ctx context.Context, state CheckpointState, maxBytes int, summarizer modelruntime.MessageChatRunner) CheckpointState {
	if len(state.Messages) == 0 {
		return state
	}
	state.Messages = compactConversationWithSummarizer(ctx, state.Messages, maxBytes, summarizer)
	if summary := summaryTextFromMessages(state.Messages); summary != "" {
		state.SummaryText = summary
	} else if summary := hiddenSystemSummary(state.Messages); summary != "" {
		state.SummaryText = summary
	}
	state.Messages = withoutSystemMessages(state.Messages)
	if state.Version <= 0 {
		state.Version = 1
	}
	return state
}

// BuildTurnContext creates model input from the single durable checkpoint.
// The system prompt is rebuilt for the current model/tool registry, while the
// checkpoint carries only hidden state and non-system messages.
func BuildTurnContext(systemPrompt string, state CheckpointState) []modelclient.ChatMessage {
	result := make([]modelclient.ChatMessage, 0, len(state.Messages)+2)
	if strings.TrimSpace(systemPrompt) != "" {
		result = append(result, modelclient.ChatMessage{Role: "system", Content: systemPrompt})
	}
	if summary := strings.TrimSpace(state.SummaryText); summary != "" {
		result = append(result, modelclient.ChatMessage{Role: "system", Content: durableSummaryPrefix + summary})
	}
	for _, message := range state.Messages {
		if message.Role == "system" || (strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 && len(message.ContentParts) == 0) {
			continue
		}
		result = append(result, message)
	}
	return result
}

// rebuildModelContext keeps the current system prompt outside the durable
// checkpoint. The prompt can change with the active model, tools, or policy;
// the checkpoint should only contain resumable conversation state.
func rebuildModelContext(existing []modelclient.ChatMessage, state CheckpointState) []modelclient.ChatMessage {
	systemPrompt := ""
	for _, message := range existing {
		if message.Role == "system" && !strings.HasPrefix(message.Content, durableSummaryPrefix) {
			systemPrompt = message.Content
			break
		}
	}
	return BuildTurnContext(systemPrompt, state)
}

func withoutSystemMessages(messages []modelclient.ChatMessage) []modelclient.ChatMessage {
	result := make([]modelclient.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" {
			continue
		}
		result = append(result, message)
	}
	return result
}

func hiddenSystemSummary(messages []modelclient.ChatMessage) string {
	seenSystemPrompt := false
	for _, message := range messages {
		if message.Role != "system" {
			continue
		}
		if !seenSystemPrompt {
			seenSystemPrompt = true
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content != "" && !strings.HasPrefix(content, durableSummaryPrefix) {
			return content
		}
	}
	return ""
}

func summaryTextFromMessages(messages []modelclient.ChatMessage) string {
	for _, message := range messages {
		if message.Role == "system" && strings.HasPrefix(message.Content, durableSummaryPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(message.Content, durableSummaryPrefix))
		}
	}
	return ""
}
