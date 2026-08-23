package documentchunk_test

import (
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
)

func TestSplitterPreservesTextWithOverlap(t *testing.T) {
	splitter := documentchunk.NewSplitter(10, 3)
	chunks := splitter.Split("abcdefghijKLMNOPQRST")
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d", len(chunks))
	}
	if chunks[0] != "abcdefghij" || !strings.HasPrefix(chunks[1], "hij") {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestSplitterPrefersParagraphBoundaries(t *testing.T) {
	splitter := documentchunk.NewSplitter(20, 0)
	chunks := splitter.Split("first paragraph\n\nsecond paragraph")
	if len(chunks) != 2 || chunks[0] != "first paragraph" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestAdaptiveSplitterKeepsMarkdownHeadingPath(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(200, 0)
	parts := splitter.SplitDocumentParts("guide.md", "# 部署指南\n\n介绍。\n\n## Docker\n\n启动命令。\n\n### Windows\n\n使用 PowerShell。")
	if len(parts) != 3 {
		t.Fatalf("got %d parts = %#v, want one part per section", len(parts), parts)
	}
	if parts[1].HeadingPath != "部署指南 > Docker" || parts[1].Content != "启动命令。" {
		t.Fatalf("nested heading metadata = %#v", parts[1])
	}
	if parts[2].HeadingPath != "部署指南 > Docker > Windows" || strings.Contains(parts[2].Content, "结构路径") {
		t.Fatalf("deep heading metadata/content = %#v", parts[2])
	}
}

func TestAdaptiveSplitterUsesVirtualTitlesWithoutHeadings(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(10, 0)
	chunks := splitter.Split("第一段内容。\n\n第二段内容。\n\n第三段内容。")
	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v, want multiple virtual sections", chunks)
	}
	parts := splitter.SplitDocumentParts("notes.md", "第一段内容。\n\n第二段内容。\n\n第三段内容。")
	if parts[0].HeadingPath != "notes.md > 第 1 段" || strings.Contains(parts[0].Content, "结构路径") {
		t.Fatalf("virtual title metadata/content missing: %#v", parts[0])
	}
}

func TestAdaptiveSplitterDetectsHeuristicSections(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(200, 0)
	parts := splitter.SplitDocumentParts("policy.txt", "第一章 总则\n\n适用范围。\n\n第二章 请假\n\n请假规则。")
	if len(parts) != 2 {
		t.Fatalf("parts = %#v, want heuristic sections", parts)
	}
	if parts[1].HeadingPath != "第二章 请假" {
		t.Fatalf("heuristic path missing: %#v", parts[1])
	}
}

func TestAdaptiveSplitterDetectsMultilingualNumberedAndVisualSections(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(300, 0)
	text := "1. Overview\n\nEnglish section.\n\n2.3 Details\n\nNested section.\n\nKAPITEL 3: Setup\n\nGerman section.\n\n---\n\nFINAL NOTES\n\nLast section."
	chunks := splitter.Split(text)
	if len(chunks) != 4 {
		t.Fatalf("chunks = %#v, want four heuristic sections", chunks)
	}
	for _, want := range []string{"1. Overview", "2.3 Details", "KAPITEL 3: Setup", "FINAL NOTES"} {
		found := false
		for _, part := range splitter.SplitDocumentParts("guide.txt", text) {
			if part.HeadingPath == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing heuristic path %q in %#v", want, chunks)
		}
	}
}

func TestAdaptiveSplitterDoesNotSplitProtectedCodeHeading(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(300, 0)
	text := "说明\n\n```text\n# not a heading\n1. not a section\n```\n\n第二章 真正章节\n\n正文。"
	chunks := splitter.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v, want structured content", chunks)
	}
	if !strings.Contains(strings.Join(chunks, "\n"), "# not a heading") {
		t.Fatalf("protected code was treated as heading: %q", chunks[0])
	}
}

func TestAdaptiveSplitterTreatsFormFeedAsPageBoundary(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(300, 0)
	chunks := splitter.Split("第一页内容\f第二页内容")
	if len(chunks) != 2 {
		t.Fatalf("chunks = %#v, want two page sections", chunks)
	}
}

func TestAdaptiveSplitterProtectsTablesAndLatex(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(200, 0)
	text := "说明\n\n| 名称 | 说明 |\n| --- | --- |\n| A | 这是表格中的完整内容 |\n| B | 另一行 |\n\n公式：\n$$\na^2 + b^2 = c^2\n$$\n\n结尾。"
	parts := splitter.SplitDocumentParts("guide.md", text)
	joined := strings.Join(func() []string {
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			result = append(result, part.Content)
		}
		return result
	}(), "\n")
	if !strings.Contains(joined, "| A | 这是表格中的完整内容 |\n| B | 另一行 |") {
		t.Fatalf("table rows were split or lost: %q", joined)
	}
	if !strings.Contains(joined, "$$\na^2 + b^2 = c^2\n$$") {
		t.Fatalf("latex block was split or lost: %q", joined)
	}
}

func TestAdaptiveSplitterSplitsOversizedProtectedBlock(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(12, 0)
	parts := splitter.Split("```text\n" + strings.Repeat("very-long-code ", 8) + "\n```")
	if len(parts) < 2 {
		t.Fatalf("oversized protected block should be allowed to split: %#v", parts)
	}
}

func TestAdaptiveSplitterReportsDiagnostics(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(200, 0)
	parts, diagnostics := splitter.SplitDocumentPartsWithDiagnostics("guide.md", "# 标题\n\n正文。\n\n| 名称 | 值 |\n| --- | --- |\n| 模式 | 深度 |\n\n```go\nfmt.Println(1)\n```")
	if len(parts) != diagnostics.ChunkCount || diagnostics.Strategy != documentchunk.StrategyHeading {
		t.Fatalf("parts and diagnostics disagree: parts=%d diagnostics=%#v", len(parts), diagnostics)
	}
	if diagnostics.HeadingCount != 1 || diagnostics.ProtectedBlockCount != 2 {
		t.Fatalf("missing structure diagnostics: %#v", diagnostics)
	}
	if diagnostics.TableBlockCount != 1 || diagnostics.CodeBlockCount != 1 || diagnostics.FormulaBlockCount != 0 {
		t.Fatalf("protected block diagnostics = %#v", diagnostics)
	}
}

func TestAdaptiveSplitterCanPinRecursiveStrategy(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitterWithStrategy(200, 0, documentchunk.StrategyRecursive)
	parts, diagnostics := splitter.SplitDocumentPartsWithDiagnostics("guide.md", "# 标题\n\n正文一。\n\n## 子标题\n\n正文二。")
	if diagnostics.Strategy != documentchunk.StrategyRecursive {
		t.Fatalf("strategy = %q, want recursive", diagnostics.Strategy)
	}
	if len(parts) != 1 || parts[0].HeadingPathKind != documentchunk.HeadingPathVirtual {
		t.Fatalf("recursive pinned parts = %#v, want one virtual-path part", parts)
	}
}

func TestAdaptiveSplitterFallsBackWhenHeadingCreatesTinyChunks(t *testing.T) {
	splitter := documentchunk.NewAdaptiveSplitter(120, 0)
	text := "# 文档\n\n" +
		"## 一\n短。\n\n" +
		"## 二\n短。\n\n" +
		"## 三\n短。\n\n" +
		"## 四\n短。\n\n" +
		"## 五\n短。\n\n" +
		"## 六\n短。\n\n" +
		"## 七\n短。\n\n" +
		"## 八\n短。\n\n" +
		strings.Repeat("这是文档后面的正文内容，用于让总长度明显超过目标块大小。", 8)
	parts, diagnostics := splitter.SplitDocumentPartsWithDiagnostics("guide.md", text)
	if len(parts) == 0 {
		t.Fatal("expected fallback chunks")
	}
	if diagnostics.Strategy != documentchunk.StrategyRecursive {
		t.Fatalf("selected strategy = %q, want recursive; diagnostics=%#v", diagnostics.Strategy, diagnostics)
	}
	if len(diagnostics.StrategyRejections) == 0 || diagnostics.QualityPassed != true {
		t.Fatalf("fallback diagnostics = %#v", diagnostics)
	}
}

func TestStructuredPartEmbeddingContentExcludesVirtualPath(t *testing.T) {
	semantic := documentchunk.StructuredPart{Content: "安装 Docker。", HeadingPath: "部署指南 > Windows", HeadingPathKind: documentchunk.HeadingPathSemantic}
	if got := semantic.EmbeddingContent("内部部署手册"); got != "内部部署手册\n\n部署指南 > Windows\n\n安装 Docker。" {
		t.Fatalf("semantic embedding content = %q", got)
	}
	virtual := documentchunk.StructuredPart{Content: "第一段正文。", HeadingPath: "guide.md > 第 1 段", HeadingPathKind: documentchunk.HeadingPathVirtual}
	if got := virtual.EmbeddingContent("guide.md"); got != "guide.md\n\n第一段正文。" {
		t.Fatalf("virtual embedding content = %q", got)
	}
}
