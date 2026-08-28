package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillcatalog "github.com/bArtyom/n2sql-agent/internal/skill"
)

func TestSkillDescribeToolReturnsMetadataWithoutBody(t *testing.T) {
	catalog := newSkillCatalogForTest(t)
	tool := NewSkillDescribeTool(catalog)

	result, err := tool.Call(context.Background(), json.RawMessage(`{"name":"pdf-processing"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var payload struct {
		Skills []skillcatalog.Skill `json:"skills"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode result = %v", err)
	}
	if len(payload.Skills) != 1 || payload.Skills[0].Name != "pdf-processing" {
		t.Fatalf("skills = %#v, want one selected skill", payload.Skills)
	}
	if strings.Contains(result.Content, "先判断 PDF") {
		t.Fatal("describe result leaked the deferred skill body")
	}
}

func TestSkillReadToolLoadsOnlyCatalogedSkillBody(t *testing.T) {
	catalog := newSkillCatalogForTest(t)
	tool := NewSkillReadTool(catalog)

	result, err := tool.Call(context.Background(), json.RawMessage(`{"name":"pdf-processing"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var payload struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode result = %v", err)
	}
	if payload.Name != "pdf-processing" || payload.Location != "pdf-processing" || !strings.Contains(payload.Content, "先判断 PDF") {
		t.Fatalf("read payload = %#v, want selected skill body", payload)
	}

	if _, err := tool.Call(context.Background(), json.RawMessage(`{"name":"../secret"}`)); err == nil {
		t.Fatal("Read tool accepted a path traversal name")
	}
}

func newSkillCatalogForTest(t *testing.T) *skillcatalog.Catalog {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "pdf-processing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := "---\nname: pdf-processing\ndescription: process PDF\nallowed-tools:\n  - document_read\n---\n先判断 PDF 是否有原生文字层。"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	catalog, err := skillcatalog.LoadCatalog(root, skillcatalog.CategoryPublic)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	return catalog
}
