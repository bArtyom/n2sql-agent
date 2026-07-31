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
