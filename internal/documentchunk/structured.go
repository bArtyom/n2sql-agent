package documentchunk

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// SplitStrategy describes the structure detector selected for a document.
// The order mirrors WeKnora's adaptive approach: explicit headings first,
// recognizable heuristic headings second, and recursive splitting last.
type SplitStrategy string

const (
	StrategyHeading   SplitStrategy = "heading"
	StrategyHeuristic SplitStrategy = "heuristic"
	StrategyRecursive SplitStrategy = "recursive"
)

// SplitDiagnostics is the small, persisted report used by document preview.
// It describes the selected structure strategy and the quality of the final
// chunks without retaining the source document a second time.
type SplitDiagnostics struct {
	Strategy            SplitStrategy `json:"strategy"`
	ChunkCount          int           `json:"chunkCount"`
	HeadingCount        int           `json:"headingCount"`
	ProtectedBlockCount int           `json:"protectedBlockCount"`
	TotalRunes          int           `json:"totalRunes"`
	MinChunkRunes       int           `json:"minChunkRunes"`
	MaxChunkRunes       int           `json:"maxChunkRunes"`
	ShortChunkCount     int           `json:"shortChunkCount"`
	OversizeChunkCount  int           `json:"oversizeChunkCount"`
}

type AdaptiveSplitter struct {
	recursive *Splitter
}

type StructuredPart struct {
	Content     string
	HeadingPath string
}

func NewAdaptiveSplitter(size, overlap int) *AdaptiveSplitter {
	return &AdaptiveSplitter{recursive: NewSplitter(size, overlap)}
}

// Split keeps the generic TextSplitter interface for callers that only need
// content. The Worker uses SplitDocumentParts so metadata is not lost.
func (s *AdaptiveSplitter) Split(text string) []string {
	parts := s.SplitDocumentParts("文档", text)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, part.Content)
	}
	return result
}

func (s *AdaptiveSplitter) SplitDocumentParts(filename, text string) []StructuredPart {
	parts, _ := s.SplitDocumentPartsWithDiagnostics(filename, text)
	return parts
}

func (s *AdaptiveSplitter) SplitDocumentPartsWithDiagnostics(filename, text string) ([]StructuredPart, SplitDiagnostics) {
	diagnostics := SplitDiagnostics{}
	if s == nil || s.recursive == nil || strings.TrimSpace(text) == "" {
		return nil, diagnostics
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "文档"
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\f", "\n"+pageBreakMarker+"\n")
	lines := strings.Split(text, "\n")
	diagnostics.TotalRunes = utf8.RuneCountInString(text)
	diagnostics.ProtectedBlockCount = len(findProtectedBlocks(text))
	if sections, ok := parseMarkdownSections(lines); ok {
		diagnostics.Strategy = StrategyHeading
		diagnostics.HeadingCount = len(sections)
		parts := s.renderSections(sections)
		return parts, finalizeDiagnostics(diagnostics, parts, s.recursive.size)
	}
	if sections, ok := parseHeuristicSections(lines); ok {
		diagnostics.Strategy = StrategyHeuristic
		diagnostics.HeadingCount = len(sections)
		parts := s.renderSections(sections)
		return parts, finalizeDiagnostics(diagnostics, parts, s.recursive.size)
	}
	diagnostics.Strategy = StrategyRecursive
	contentParts := s.splitContent(text)
	parts := addVirtualPaths(filename, contentParts)
	return parts, finalizeDiagnostics(diagnostics, parts, s.recursive.size)
}

func finalizeDiagnostics(diagnostics SplitDiagnostics, parts []StructuredPart, target int) SplitDiagnostics {
	diagnostics.ChunkCount = len(parts)
	if len(parts) == 0 {
		return diagnostics
	}
	diagnostics.MinChunkRunes = -1
	for _, part := range parts {
		length := utf8.RuneCountInString(part.Content)
		if length < diagnostics.MinChunkRunes || diagnostics.MinChunkRunes < 0 {
			diagnostics.MinChunkRunes = length
		}
		if length > diagnostics.MaxChunkRunes {
			diagnostics.MaxChunkRunes = length
		}
		if length < maxInt(32, target/10) {
			diagnostics.ShortChunkCount++
		}
		if length > target {
			diagnostics.OversizeChunkCount++
		}
	}
	return diagnostics
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

type structuredSection struct {
	path    []string
	content string
}

var markdownHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)\s*#*\s*$`)
var (
	chineseChapterHeading = regexp.MustCompile(`^第[ \t]*[一二三四五六七八九十百千万零〇0-9]+[ \t]*(?:章|节|節|部分|篇)[ \t]?.{0,200}$`)
	chineseNumberHeading  = regexp.MustCompile(`^[一二三四五六七八九十]+、.{1,200}$`)
	numberedHeading       = regexp.MustCompile(`^(?:\d+(?:\.\d+){0,3}\.?|[IVX]{1,5}\.)[ \t]+\S.{0,200}$`)
	englishChapterHeading = regexp.MustCompile(`(?i)^(?:chapter|section|part)[ \t]+(?:\d+|[IVX]{1,5})[.:]?[ \t]+\S.{0,200}$`)
	germanChapterHeading  = regexp.MustCompile(`(?i)^(?:kapitel|abschnitt|teil)[ \t]+(?:\d+|[IVX]{1,5})[.:]?[ \t]+\S.{0,200}$`)
	allCapsHeading        = regexp.MustCompile(`^[A-ZÄÖÜ][A-ZÄÖÜ \t\-]{3,80}:?$`)
	visualSeparator       = regexp.MustCompile(`^(?:-{3,}|={3,}|\*{3,}|_{3,})$`)
)

const pageBreakMarker = "\uE000DOCUMENT_PAGE_BREAK\uE001"

func parseMarkdownSections(lines []string) ([]structuredSection, bool) {
	var sections []structuredSection
	stack := make([]string, 0, 6)
	inFence := false
	var current *structuredSection
	var sectionsFound bool
	flush := func() {
		if current == nil || strings.TrimSpace(current.content) == "" {
			return
		}
		current.content = strings.TrimSpace(current.content)
		sections = append(sections, *current)
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		match := markdownHeading.FindStringSubmatch(line)
		if !inFence && match != nil {
			flush()
			level := len(match[1])
			if level <= len(stack) {
				stack = stack[:level-1]
			}
			stack = append(stack, strings.TrimSpace(match[2]))
			current = &structuredSection{path: append([]string(nil), stack...)}
			sectionsFound = true
			continue
		}
		if current == nil {
			current = &structuredSection{}
		}
		current.content += line + "\n"
	}
	flush()
	return sections, sectionsFound
}

func parseHeuristicSections(lines []string) ([]structuredSection, bool) {
	var sections []structuredSection
	var current *structuredSection
	found := false
	inFence := false
	pageNumber := 1
	flush := func() {
		if current == nil || strings.TrimSpace(current.content) == "" {
			return
		}
		current.content = strings.TrimSpace(current.content)
		sections = append(sections, *current)
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == pageBreakMarker {
			flush()
			pageNumber++
			current = &structuredSection{path: []string{"第 " + itoa(pageNumber) + " 页"}}
			found = true
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			if current == nil {
				current = &structuredSection{}
			}
			current.content += line + "\n"
			inFence = !inFence
			continue
		}
		if inFence {
			if current == nil {
				current = &structuredSection{}
			}
			current.content += line + "\n"
			continue
		}
		if visualSeparator.MatchString(trimmed) {
			flush()
			current = nil
			found = true
			continue
		}
		if isHeuristicHeading(trimmed) {
			flush()
			current = &structuredSection{path: []string{trimmed}}
			found = true
			continue
		}
		if current == nil {
			current = &structuredSection{}
		}
		current.content += line + "\n"
	}
	flush()
	return sections, found
}

func isHeuristicHeading(line string) bool {
	if line == "" || len([]rune(line)) > 220 {
		return false
	}
	return chineseChapterHeading.MatchString(line) ||
		chineseNumberHeading.MatchString(line) ||
		numberedHeading.MatchString(line) ||
		englishChapterHeading.MatchString(line) ||
		germanChapterHeading.MatchString(line) ||
		allCapsHeading.MatchString(line)
}

func (s *AdaptiveSplitter) renderSections(sections []structuredSection) []StructuredPart {
	result := make([]StructuredPart, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section.content) == "" {
			continue
		}
		path := strings.Join(section.path, " > ")
		parts := s.splitContent(section.content)
		if len(parts) == 0 {
			continue
		}
		for _, part := range parts {
			result = append(result, StructuredPart{Content: part, HeadingPath: path})
		}
	}
	return result
}

type protectedBlock struct {
	start int
	end   int
}

// splitContent keeps fenced code, LaTeX blocks and Markdown tables together.
// A block larger than the configured budget is the only case where the
// recursive splitter is allowed to enter it.
func (s *AdaptiveSplitter) splitContent(text string) []string {
	blocks := findProtectedBlocks(text)
	if len(blocks) == 0 {
		return s.recursive.Split(text)
	}
	result := make([]string, 0)
	cursor := 0
	for _, block := range blocks {
		if block.start > cursor {
			result = append(result, s.recursive.Split(text[cursor:block.start])...)
		}
		content := strings.TrimSpace(text[block.start:block.end])
		if content != "" {
			if utf8.RuneCountInString(content) <= s.recursive.size {
				result = append(result, content)
			} else {
				result = append(result, s.recursive.Split(content)...)
			}
		}
		cursor = block.end
	}
	if cursor < len(text) {
		result = append(result, s.recursive.Split(text[cursor:])...)
	}
	return result
}

func findProtectedBlocks(text string) []protectedBlock {
	lines := strings.SplitAfter(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	blocks := make([]protectedBlock, 0)
	offset := 0
	inFence := false
	inLatex := false
	latexStart := 0
	for index := 0; index < len(lines); index++ {
		line := strings.TrimRight(lines[index], "\r\n")
		trimmed := strings.TrimSpace(line)
		start := offset
		offset += len(lines[index])
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				blockStart := start
				closed := false
				for index++; index < len(lines); index++ {
					closing := strings.TrimSpace(strings.TrimRight(lines[index], "\r\n"))
					offset += len(lines[index])
					if strings.HasPrefix(closing, "```") {
						blocks = append(blocks, protectedBlock{start: blockStart, end: offset})
						inFence = false
						closed = true
						break
					}
				}
				if !closed && offset > blockStart {
					blocks = append(blocks, protectedBlock{start: blockStart, end: offset})
				}
			}
			continue
		}
		if trimmed == "$$" || strings.HasPrefix(trimmed, "\\[") {
			if !inLatex {
				latexStart = start
				inLatex = true
			} else if trimmed == "$$" {
				blocks = append(blocks, protectedBlock{start: latexStart, end: offset})
				inLatex = false
			}
			continue
		}
		if inLatex && trimmed == "\\]" {
			blocks = append(blocks, protectedBlock{start: latexStart, end: offset})
			inLatex = false
			continue
		}
		if inLatex {
			continue
		}
		if isTableHeader(lines, index) {
			blockStart := start
			blockEnd := offset
			for index+1 < len(lines) {
				next := strings.TrimSpace(strings.TrimRight(lines[index+1], "\r\n"))
				if !strings.Contains(next, "|") {
					break
				}
				index++
				blockEnd += len(lines[index])
			}
			blocks = append(blocks, protectedBlock{start: blockStart, end: blockEnd})
			offset = blockEnd
		}
	}
	if inLatex && offset > latexStart {
		blocks = append(blocks, protectedBlock{start: latexStart, end: offset})
	}
	return blocks
}

func isTableHeader(lines []string, index int) bool {
	if index+1 >= len(lines) || !strings.Contains(lines[index], "|") {
		return false
	}
	separator := strings.TrimSpace(strings.TrimRight(lines[index+1], "\r\n"))
	if !strings.Contains(separator, "|") {
		return false
	}
	separator = strings.NewReplacer("|", "", ":", "", "-", "", " ", "", "\t", "").Replace(separator)
	return separator == ""
}

func addVirtualPaths(filename string, parts []string) []StructuredPart {
	result := make([]StructuredPart, 0, len(parts))
	for index, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		result = append(result, StructuredPart{Content: part, HeadingPath: filename + " > 第 " + itoa(index+1) + " 段"})
	}
	return result
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
