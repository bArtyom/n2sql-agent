package agentruntime

import (
	"context"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

const durableSummaryPrefix = "Agent 短记忆（较早工具结果摘要）：\n"

// ContextState is the durable, resumable state of one Agent run. Messages are
// the latest compacted model context; SummaryText is kept separately so a
// summary is not mistaken for a user/assistant turn in the conversation log.
type ContextState struct {
	Version     int
	LastStep    int
	Messages    []modelclient.ChatMessage
	SummaryText string
}

// ContextCheckpointSink persists a stable semantic boundary. It is called
// before a model decision and after a completed tool batch, never per token.
type ContextCheckpointSink func(context.Context, ContextState) error

func compactContextState(ctx context.Context, state ContextState, maxBytes int, summarizer modelruntime.MessageChatRunner) ContextState {
	if len(state.Messages) == 0 {
		return state
	}
	state.Messages = compactConversationWithSummarizer(ctx, state.Messages, maxBytes, summarizer)
	if summary := summaryTextFromMessages(state.Messages); summary != "" {
		state.SummaryText = summary
	}
	if state.Version <= 0 {
		state.Version = 1
	}
	return state
}

func restoreContextState(state ContextState) ContextState {
	if len(state.Messages) == 0 || strings.TrimSpace(state.SummaryText) == "" {
		return state
	}
	for _, message := range state.Messages {
		if message.Role == "system" && strings.HasPrefix(message.Content, durableSummaryPrefix) {
			return state
		}
	}
	index := 0
	if state.Messages[0].Role == "system" {
		index = 1
	}
	summary := modelclient.ChatMessage{Role: "system", Content: durableSummaryPrefix + state.SummaryText}
	state.Messages = append(state.Messages, modelclient.ChatMessage{})
	copy(state.Messages[index+1:], state.Messages[index:])
	state.Messages[index] = summary
	return state
}

// BuildTurnContext creates the model input for a new conversation turn from
// a durable thread snapshot. The system prompt is rebuilt for the current
// model/tool registry; only the hidden durable memory and non-system message
// state are carried across turns. This prevents a stale system prompt from
// becoming part of the persisted conversation while keeping tool exchanges
// available to the Agent.
func BuildTurnContext(systemPrompt string, state ContextState) []modelclient.ChatMessage {
	result := make([]modelclient.ChatMessage, 0, len(state.Messages)+2)
	if strings.TrimSpace(systemPrompt) != "" {
		result = append(result, modelclient.ChatMessage{Role: "system", Content: systemPrompt})
	}
	if summary := strings.TrimSpace(state.SummaryText); summary != "" {
		result = append(result, modelclient.ChatMessage{Role: "system", Content: durableSummaryPrefix + summary})
	}
	for _, message := range state.Messages {
		if message.Role == "system" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		result = append(result, message)
	}
	return result
}

func summaryTextFromMessages(messages []modelclient.ChatMessage) string {
	for _, message := range messages {
		if message.Role == "system" && strings.HasPrefix(message.Content, durableSummaryPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(message.Content, durableSummaryPrefix))
		}
	}
	return ""
}
