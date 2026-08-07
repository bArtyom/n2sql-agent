package agentservice

import (
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

// HistoryMessage is a trusted-shape conversation message supplied for short-term context.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest contains the current question and an optional bounded conversation history.
type ChatRequest struct {
	Message string           `json:"message"`
	History []HistoryMessage `json:"history,omitempty"`
}

func buildHistoryMessages(history []HistoryMessage, maxMessages, maxBytes int) ([]modelclient.ChatMessage, error) {
	if maxMessages <= 0 || maxBytes <= 0 {
		return nil, ErrInvalidRequest
	}

	normalized := make([]HistoryMessage, len(history))
	for index, message := range history {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, ErrInvalidRequest
		}
		message.Content = strings.TrimSpace(message.Content)
		if message.Content == "" {
			return nil, ErrInvalidRequest
		}
		normalized[index] = message
	}
	if len(normalized) > maxMessages {
		normalized = normalized[len(normalized)-maxMessages:]
	}

	keptReversed := make([]modelclient.ChatMessage, 0, len(normalized))
	remainingBytes := maxBytes
	for index := len(normalized) - 1; index >= 0 && remainingBytes > 0; index-- {
		message := normalized[index]
		content := truncateUTF8(message.Content, remainingBytes)
		if content == "" {
			break
		}
		keptReversed = append(keptReversed, modelclient.ChatMessage{Role: message.Role, Content: content})
		remainingBytes -= len(content)
	}

	historyMessages := make([]modelclient.ChatMessage, len(keptReversed))
	for index := range keptReversed {
		historyMessages[len(keptReversed)-1-index] = keptReversed[index]
	}
	return historyMessages, nil
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := 0
	for index, runeValue := range value {
		runeBytes := len(string(runeValue))
		if end+runeBytes > maxBytes {
			break
		}
		end = index + runeBytes
	}
	return value[:end]
}
