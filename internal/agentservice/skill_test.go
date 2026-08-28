package agentservice_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	skillcatalog "github.com/bArtyom/n2sql-agent/internal/skill"
)

func TestServiceAddsDeferredSkillDiscoveryToAgentRun(t *testing.T) {
	catalog := writeServiceSkill(t)
	chat := chatStub{call: func(messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if !strings.Contains(messages[0].Content, "<skill_index>") || !strings.Contains(messages[0].Content, "pdf-processing") {
			t.Fatalf("system prompt = %q, want the lightweight Skill index", messages[0].Content)
		}
		seen := make(map[string]bool, len(definitions))
		for _, definition := range definitions {
			seen[definition.Name] = true
		}
		if !seen["skill_describe"] || !seen["skill_read"] {
			t.Fatalf("tool definitions = %#v, want deferred Skill tools", definitions)
		}
		return modelclient.ChatResponse{Message: "已完成"}, nil
	}}
	service, err := agentservice.NewService(chat, &searcherStub{}, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.SetSkillCatalog(catalog)
	if response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{Message: "分析 PDF"}); err != nil || response.Answer != "已完成" {
		t.Fatalf("Answer() = %#v, error = %v", response, err)
	}
}

func writeServiceSkill(t *testing.T) *skillcatalog.Catalog {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pdf-processing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "---\nname: pdf-processing\ndescription: process PDF\n---\n读取 PDF。"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	catalog, err := skillcatalog.LoadCatalog(root, skillcatalog.CategoryPublic)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	return catalog
}
