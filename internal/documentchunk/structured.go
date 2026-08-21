package documentchunk

import (
	"regexp"
	"strings"
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

type AdaptiveSplitter struct {
	recursive *Splitter
}

func NewAdaptiveSplitter(size, overlap int) *AdaptiveSplitter {
	return &AdaptiveSplitter{recursive: NewSplitter(size, overlap)}
}

// Split keeps the existing TextSplitter interface. Call SplitDocument when a
// filename is available so heading-less documents get a useful virtual title.
func (s *AdaptiveSplitter) Split(text string) []string {
	return s.SplitDocument("文档", text)
}

func (s *AdaptiveSplitter) SplitDocument(filename, text string) []string {
	if s == nil || s.recursive == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "文档"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if sections, ok := parseMarkdownSections(lines); ok {
		return s.renderSections(sections)
	}
	if sections, ok := parseHeuristicSections(lines); ok {
		return s.renderSections(sections)
	}
	parts := s.recursive.Split(text)
	return addVirtualPaths(filename, parts)
}

type structuredSection struct {
	path    []string
	content string
}

var markdownHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)\s*#*\s*$`)
var heuristicHeading = regexp.MustCompile(`^(第[一二三四五六七八九十百千万0-9]+[章节篇部分].{0,80}|[一二三四五六七八九十]+、.{1,80}|[0-9]+[.)、][^。！？]{1,80}|[A-Z][A-Z0-9 _-]{3,80})$`)

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
	flush := func() {
		if current == nil || strings.TrimSpace(current.content) == "" {
			return
		}
		current.content = strings.TrimSpace(current.content)
		sections = append(sections, *current)
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if heuristicHeading.MatchString(trimmed) {
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

func (s *AdaptiveSplitter) renderSections(sections []structuredSection) []string {
	result := make([]string, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section.content) == "" {
			continue
		}
		path := strings.Join(section.path, " > ")
		if path == "" {
			path = "前言"
		}
		prefix := "结构路径：" + path
		parts := s.recursive.Split(section.content)
		if len(parts) == 0 {
			continue
		}
		for _, part := range parts {
			result = append(result, prefix+"\n\n"+part)
		}
	}
	return result
}

func addVirtualPaths(filename string, parts []string) []string {
	result := make([]string, 0, len(parts))
	for index, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		result = append(result, "结构路径："+filename+" > 第 "+itoa(index+1)+" 段\n\n"+part)
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
