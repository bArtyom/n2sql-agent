package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCatalogParsesMetadataAndKeepsSkillBodyDeferred(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "pdf-processing", `---
name: pdf-processing
description: 处理 PDF 文件并提取结构化内容
license: MIT
allowed-tools:
  - document_read
  - document_summary
---
# PDF Processing

先判断 PDF 是否有原生文字层。
`)

	catalog, err := LoadCatalog(root, CategoryPublic)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	items := catalog.List()
	if len(items) != 1 {
		t.Fatalf("List() length = %d, want 1", len(items))
	}
	if items[0].Name != "pdf-processing" || items[0].Description == "" {
		t.Fatalf("metadata = %#v, want parsed name and description", items[0])
	}
	if len(items[0].AllowedTools) != 2 || items[0].AllowedTools[0] != "document_read" {
		t.Fatalf("allowed tools = %#v, want ordered frontmatter list", items[0].AllowedTools)
	}
	if items[0].Body != "" {
		t.Fatalf("catalog list must not load skill body, got %q", items[0].Body)
	}

	body, err := catalog.Read("pdf-processing")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !strings.Contains(body, "先判断 PDF") {
		t.Fatalf("Read() body = %q, want skill instructions", body)
	}
}

func TestCatalogSearchSupportsExactSelectionAndBoundedTextSearch(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "pdf-processing", "---\nname: pdf-processing\ndescription: process PDF\n---\nPDF instructions")
	writeSkillFile(t, root, "rag-research", "---\nname: rag-research\ndescription: research with retrieval\n---\nRAG instructions")
	writeSkillFile(t, root, "unrelated", "---\nname: unrelated\ndescription: cooking\n---\nCooking instructions")

	catalog, err := LoadCatalog(root, CategoryPublic)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	selected, err := catalog.Search("select:rag-research,pdf-processing", 5)
	if err != nil {
		t.Fatalf("Search(select:) error = %v", err)
	}
	if got := []string{selected[0].Name, selected[1].Name}; got[0] != "pdf-processing" || got[1] != "rag-research" {
		t.Fatalf("selected names = %#v, want sorted selected skills", got)
	}

	matched, err := catalog.Search("retrieval", 1)
	if err != nil {
		t.Fatalf("Search(text) error = %v", err)
	}
	if len(matched) != 1 || matched[0].Name != "rag-research" {
		t.Fatalf("text matches = %#v, want bounded retrieval match", matched)
	}
}

func TestLoadCatalogRejectsUnsafeOrInvalidSkillDefinitions(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "mismatched", "---\nname: different\ndescription: invalid\n---\nbody")
	if _, err := LoadCatalog(root, CategoryPublic); err == nil {
		t.Fatal("LoadCatalog() accepted a skill whose metadata name differs from directory")
	}

	if _, err := (&Catalog{}).Read("../secret"); err == nil {
		t.Fatal("Read() accepted a path traversal name")
	}
}

func writeSkillFile(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
