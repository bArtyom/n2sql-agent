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
	Message        string           `json:"message"`
	History        []HistoryMessage `json:"history,omitempty"`
	ConversationID int64            `json:"conversation_id,omitempty"`
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
	turns := historyTurns(normalized)
	keptReversed := make([]modelclient.ChatMessage, 0, maxMessages)
	remainingBytes := maxBytes
	keptMessages := 0
	for _, turn := range turns {
		if keptMessages+len(turn) > maxMessages {
			break
		}
		turnBytes := 0
		for _, message := range turn {
			turnBytes += len(message.Content)
		}
		if turnBytes > remainingBytes {
			// Never split a complete user/assistant turn. A lone latest user
			// message may still be truncated to honor the byte budget.
			if keptMessages == 0 && len(turn) == 1 && remainingBytes > 0 {
				content := truncateUTF8(turn[0].Content, remainingBytes)
				if content != "" {
					keptReversed = append(keptReversed, modelclient.ChatMessage{Role: turn[0].Role, Content: content})
				}
			}
			break
		}
		for index := len(turn) - 1; index >= 0; index-- {
			message := turn[index]
			keptReversed = append(keptReversed, modelclient.ChatMessage{Role: message.Role, Content: message.Content})
		}
		keptMessages += len(turn)
		remainingBytes -= turnBytes
	}

	historyMessages := make([]modelclient.ChatMessage, len(keptReversed))
	for index := range keptReversed {
		historyMessages[len(keptReversed)-1-index] = keptReversed[index]
	}
	return historyMessages, nil
}

// historyTurns groups the newest history into complete user/assistant turns.
// An orphan assistant message is ignored because it has no user question to
// provide context for. A trailing user message is retained as an incomplete
// latest turn, which supports clients that send the current draft history.
func historyTurns(history []HistoryMessage) [][]HistoryMessage {
	turns := make([][]HistoryMessage, 0, len(history)/2+1)
	for index := len(history) - 1; index >= 0; {
		if history[index].Role == "assistant" {
			if index > 0 && history[index-1].Role == "user" {
				turns = append(turns, history[index-1:index+1])
				index -= 2
				continue
			}
			index--
			continue
		}
		turns = append(turns, history[index:index+1])
		index--
	}
	return turns
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
