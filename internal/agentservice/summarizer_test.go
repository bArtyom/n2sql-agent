package agentservice_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type summaryRunnerStub struct {
	call func([]modelclient.ChatMessage) (modelclient.ChatResponse, error)
}

func (s summaryRunnerStub) ChatMessages(_ context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return s.call(messages)
}

func TestModelHistorySummarizerUsesChatWithoutTools(t *testing.T) {
	summarizer := agentservice.NewModelHistorySummarizer(summaryRunnerStub{call: func(messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
		if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
			t.Fatalf("summary request = %#v", messages)
		}
		if !strings.Contains(messages[1].Content, "旧问题") {
			t.Fatalf("summary transcript = %q", messages[1].Content)
		}
		return modelclient.ChatResponse{Message: "用户之前询问了旧问题。"}, nil
	}})
	summary, err := summarizer.Summarize(context.Background(), []agentservice.HistoryMessage{{Role: "user", Content: "旧问题"}})
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if summary.Content != "用户之前询问了旧问题。" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestModelHistorySummarizerRecursivelyRewritesExistingSummary(t *testing.T) {
	summarizer := agentservice.NewModelHistorySummarizer(summaryRunnerStub{call: func(messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
		if !strings.Contains(messages[0].Content, "已有摘要") || !strings.Contains(messages[1].Content, "之前摘要") {
			t.Fatalf("recursive summary request = %#v", messages)
		}
		return modelclient.ChatResponse{Message: "重新整理后的摘要"}, nil
	}})
	result, err := summarizer.SummarizeWithExisting(context.Background(), "之前摘要", []agentservice.HistoryMessage{{Role: "user", Content: "新问题"}})
	if err != nil || result.Content != "重新整理后的摘要" {
		t.Fatalf("recursive summary = %#v, error = %v", result, err)
	}
}

func TestModelHistorySummarizerRejectsEmptyResponse(t *testing.T) {
	summarizer := agentservice.NewModelHistorySummarizer(summaryRunnerStub{call: func([]modelclient.ChatMessage) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{}, nil
	}})
	if _, err := summarizer.Summarize(context.Background(), []agentservice.HistoryMessage{{Role: "user", Content: "旧问题"}}); err == nil {
		t.Fatal("Summarize() error = nil, want invalid tool-call response")
	}
}

func TestModelHistorySummarizerPropagatesChatError(t *testing.T) {
	wantErr := errors.New("model unavailable")
	summarizer := agentservice.NewModelHistorySummarizer(summaryRunnerStub{call: func([]modelclient.ChatMessage) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{}, wantErr
	}})
	if _, err := summarizer.Summarize(context.Background(), []agentservice.HistoryMessage{{Role: "user", Content: "旧问题"}}); !errors.Is(err, wantErr) {
		t.Fatalf("Summarize() error = %v, want wrapped model error", err)
	}
}

func TestModelHistorySummarizerRetriesTransientFailure(t *testing.T) {
	callCount := 0
	summarizer := agentservice.NewModelHistorySummarizer(summaryRunnerStub{call: func([]modelclient.ChatMessage) (modelclient.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return modelclient.ChatResponse{}, errors.New("temporary model failure")
		}
		return modelclient.ChatResponse{Message: "重试后得到的摘要"}, nil
	}})

	result, err := summarizer.Summarize(context.Background(), []agentservice.HistoryMessage{{Role: "user", Content: "旧问题"}})
	if err != nil || result.Content != "重试后得到的摘要" {
		t.Fatalf("summary = %#v, error = %v", result, err)
	}
	if callCount != 2 {
		t.Fatalf("summary model calls = %d, want 2", callCount)
	}
}
