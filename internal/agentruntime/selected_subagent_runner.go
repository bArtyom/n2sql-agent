package agentruntime

import (
	"context"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

// selectedSubagentChatRunner binds a validated model choice to the same
// provider-aware runner used by the parent. It deliberately does not expose
// credentials or URLs to the child configuration.
type selectedSubagentChatRunner struct {
	runner modelruntime.ToolChatRunnerWithModel
	model  string
}

func (r selectedSubagentChatRunner) ChatMessagesWithTools(ctx context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return r.runner.ChatMessagesWithToolsForModel(ctx, r.model, messages, definitions)
}
