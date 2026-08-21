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
	if len(chunks) != 2 {
		t.Fatalf("chunks = %#v, want two sections", chunks)
	}
	if !strings.Contains(chunks[0], "# not a heading") {
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
