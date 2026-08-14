package agentservice

import (
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/security"
)

const (
	maxTraceEvents    = 32
	maxTraceTextBytes = 512
)

// TraceEvent is the bounded, display-safe record of one knowledge tool call.
// It intentionally stores a parameter preview and a result summary instead of
// raw tool output or model reasoning text.
type TraceEvent struct {
	Type          string `json:"type"`
	Step          int    `json:"step,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`
	Status        string `json:"status"`
}

type traceCollector struct {
	events []TraceEvent
}

func newTraceCollector() *traceCollector {
	return &traceCollector{events: make([]TraceEvent, 0, maxTraceEvents)}
}

func (c *traceCollector) Sink(next agentruntime.EventSink) agentruntime.EventSink {
	return func(event agent.Event) error {
		c.observe(event)
		if next == nil {
			return nil
		}
		return next(event)
	}
}

func (c *traceCollector) observe(event agent.Event) {
	if c == nil {
		return
	}
	data, _ := event.Data.(map[string]any)
	switch event.Type {
	case agent.EventToolCalled:
		if len(c.events) >= maxTraceEvents {
			return
		}
		c.events = append(c.events, TraceEvent{
			Type:       "tool_call",
			Step:       event.StepNumber,
			ToolCallID: traceString(data, "tool_call_id"),
			ToolName:   traceString(data, "tool_name"),
			Arguments:  boundedTraceText(traceString(data, "arguments")),
			Status:     "running",
		})
	case agent.EventToolFinished:
		index := c.find(traceString(data, "tool_call_id"))
		if index < 0 {
			if len(c.events) >= maxTraceEvents {
				return
			}
			c.events = append(c.events, TraceEvent{
				Type:       "tool_call",
				Step:       event.StepNumber,
				ToolCallID: traceString(data, "tool_call_id"),
				ToolName:   traceString(data, "tool_name"),
				Status:     "succeeded",
			})
			index = len(c.events) - 1
		}
		c.events[index].Status = "succeeded"
		c.events[index].ResultSummary = boundedTraceText(traceResultSummary(data))
	case agent.EventRunFailed, agent.EventRunCanceled:
		for index := len(c.events) - 1; index >= 0; index-- {
			if c.events[index].Status == "running" {
				c.events[index].Status = "failed"
				c.events[index].ResultSummary = "Agent 在工具完成前结束"
				break
			}
		}
	}
}

func (c *traceCollector) find(toolCallID string) int {
	if c == nil || toolCallID == "" {
		if c != nil && len(c.events) > 0 {
			return len(c.events) - 1
		}
		return -1
	}
	for index := len(c.events) - 1; index >= 0; index-- {
		if c.events[index].ToolCallID == toolCallID {
			return index
		}
	}
	return -1
}

func (c *traceCollector) Events() []TraceEvent {
	if c == nil || len(c.events) == 0 {
		return nil
	}
	result := make([]TraceEvent, len(c.events))
	copy(result, c.events)
	return result
}

func traceString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func traceResultSummary(data map[string]any) string {
	if summary := traceString(data, "result_summary"); summary != "" {
		return summary
	}
	if noRelevant, _ := data["no_relevant_results"].(bool); noRelevant {
		return "没有命中相关资料"
	}
	if sources, ok := data["sources"].([]any); ok {
		return fmt.Sprintf("返回 %d 条资料", len(sources))
	}
	if sources, ok := data["sources"].([]map[string]any); ok {
		return fmt.Sprintf("返回 %d 条资料", len(sources))
	}
	return "工具调用完成"
}

func boundedTraceText(value string) string {
	value = security.RedactText(strings.TrimSpace(value))
	if len(value) <= maxTraceTextBytes {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && len(string(runes)) > maxTraceTextBytes {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}
