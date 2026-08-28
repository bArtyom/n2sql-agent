// Package skill implements the file-backed Skill catalog used for deferred
// Agent capability discovery. A catalog indexes only frontmatter at startup;
// the Markdown body is read after the model explicitly selects a skill.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxSkillFileBytes  = 256 * 1024
	MaxSkills          = 256
	DefaultSearchLimit = 5
)

var (
	ErrInvalidCatalogRoot = errors.New("invalid skill catalog root")
	ErrInvalidSkill       = errors.New("invalid skill definition")
	ErrSkillNotFound      = errors.New("skill not found")
	ErrUnsafeSkillName    = errors.New("unsafe skill name")
)

type Category string

const (
	CategoryPublic Category = "public"
	CategoryCustom Category = "custom"
)

// Skill is the discoverable metadata for one SKILL.md directory. Body is
// intentionally empty in catalog listings so the initial prompt stays small.
type Skill struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	License      string   `json:"license,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Location     string   `json:"location"`
	Category     Category `json:"category"`
	Body         string   `json:"-"`

	filePath string
}

// Catalog is an immutable-by-convention index. It is safe for concurrent
// reads after construction; no request is allowed to mutate its entries.
type Catalog struct {
	root   string
	skills map[string]Skill
}

func LoadCatalog(root string, category Category) (*Catalog, error) {
	if strings.TrimSpace(root) == "" || category == "" {
		return nil, ErrInvalidCatalogRoot
	}
	rootPath, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve skill catalog root: %w", err)
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("stat skill catalog root: %w", err)
	}
	if !info.IsDir() {
		return nil, ErrInvalidCatalogRoot
	}

	catalog := &Catalog{root: rootPath, skills: make(map[string]Skill)}
	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Name() != "SKILL.md" {
			return nil
		}
		if len(catalog.skills) >= MaxSkills {
			return fmt.Errorf("%w: catalog contains more than %d skills", ErrInvalidSkill, MaxSkills)
		}
		skill, _, parseErr := parseFile(path, rootPath, category)
		if parseErr != nil {
			return parseErr
		}
		if _, exists := catalog.skills[skill.Name]; exists {
			return fmt.Errorf("%w: duplicate name %q", ErrInvalidSkill, skill.Name)
		}
		catalog.skills[skill.Name] = skill
		return nil
	})
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

// LoadCatalogIfExists keeps local development convenient: an absent optional
// skills directory means the Agent simply has no discoverable skills.
func LoadCatalogIfExists(root string, category Category) (*Catalog, error) {
	if strings.TrimSpace(root) == "" {
		return &Catalog{skills: make(map[string]Skill)}, nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return &Catalog{skills: make(map[string]Skill)}, nil
	}
	return LoadCatalog(root, category)
}

func (c *Catalog) List() []Skill {
	if c == nil {
		return nil
	}
	items := make([]Skill, 0, len(c.skills))
	for _, item := range c.skills {
		item.Body = ""
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.skills)
}

func (c *Catalog) Search(query string, limit int) ([]Skill, error) {
	if c == nil {
		return nil, ErrSkillNotFound
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return c.List()[:min(limit, c.Len())], nil
	}
	if strings.HasPrefix(strings.ToLower(query), "select:") {
		names := strings.Split(strings.TrimSpace(query[len("select:"):]), ",")
		selected := make([]Skill, 0, len(names))
		seen := make(map[string]struct{}, len(names))
		for _, rawName := range names {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			item, ok := c.skills[name]
			if !ok {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			item.Body = ""
			selected = append(selected, item)
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
		if len(selected) > limit {
			selected = selected[:limit]
		}
		return selected, nil
	}

	lowerQuery := strings.ToLower(query)
	matched := make([]Skill, 0, len(c.skills))
	for _, item := range c.skills {
		if strings.Contains(strings.ToLower(item.Name), lowerQuery) || strings.Contains(strings.ToLower(item.Description), lowerQuery) {
			item.Body = ""
			matched = append(matched, item)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (c *Catalog) Describe(name string) (Skill, error) {
	if c == nil {
		return Skill{}, ErrSkillNotFound
	}
	if err := validateName(name); err != nil {
		return Skill{}, err
	}
	item, ok := c.skills[name]
	if !ok {
		return Skill{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	item.Body = ""
	return item, nil
}

func (c *Catalog) Read(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if c == nil {
		return "", ErrSkillNotFound
	}
	item, ok := c.skills[name]
	if !ok || item.filePath == "" {
		return "", fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	_, body, err := parseFile(item.filePath, c.root, item.Category)
	if err != nil {
		return "", err
	}
	return body, nil
}

func (c *Catalog) IndexPrompt() string {
	if c == nil || len(c.skills) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<skill_index>\n")
	for _, item := range c.List() {
		fmt.Fprintf(&builder, "- %s: %s\n", item.Name, item.Description)
	}
	builder.WriteString("</skill_index>\n")
	builder.WriteString("需要专门能力时，先调用 skill_describe 搜索或确认 Skill，再调用 skill_read 按名称加载其 SKILL.md；不要猜测 Skill 名称或直接读取文件路径。")
	return builder.String()
}

func parseFile(path, root string, category Category) (Skill, string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, "", fmt.Errorf("read skill metadata: %w", err)
	}
	if len(content) > MaxSkillFileBytes {
		return Skill{}, "", fmt.Errorf("%w: %q exceeds %d bytes", ErrInvalidSkill, path, MaxSkillFileBytes)
	}
	name, description, license, allowedTools, body, err := parseFrontmatter(string(content))
	if err != nil {
		return Skill{}, "", fmt.Errorf("parse %q: %w", path, err)
	}
	if err := validateName(name); err != nil {
		return Skill{}, "", fmt.Errorf("%w: %v", ErrInvalidSkill, err)
	}
	location, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || strings.HasPrefix(location, ".."+string(filepath.Separator)) || location == ".." {
		return Skill{}, "", fmt.Errorf("%w: skill path escapes catalog root", ErrInvalidSkill)
	}
	if filepath.Base(filepath.Dir(path)) != name {
		return Skill{}, "", fmt.Errorf("%w: directory %q must match metadata name %q", ErrInvalidSkill, filepath.Base(filepath.Dir(path)), name)
	}
	return Skill{
		Name: name, Description: description, License: license, AllowedTools: allowedTools,
		Location: filepath.ToSlash(location), Category: category, filePath: path,
	}, body, nil
}

func parseFrontmatter(content string) (name, description, license string, allowedTools []string, body string, err error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", "", "", nil, "", fmt.Errorf("%w: frontmatter must start with ---", ErrInvalidSkill)
	}
	separator := strings.Index(content[4:], "\n---\n")
	if separator < 0 {
		return "", "", "", nil, "", fmt.Errorf("%w: frontmatter closing --- is missing", ErrInvalidSkill)
	}
	separator += 4
	header := content[4:separator]
	body = content[separator+5:]
	lines := strings.Split(header, "\n")
	var listKey string
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if listKey != "allowed-tools" {
				return "", "", "", nil, "", fmt.Errorf("%w: unexpected list item", ErrInvalidSkill)
			}
			item := parseScalar(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			if item != "" {
				allowedTools = append(allowedTools, item)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return "", "", "", nil, "", fmt.Errorf("%w: malformed frontmatter line %q", ErrInvalidSkill, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		listKey = ""
		switch key {
		case "name":
			name = parseScalar(value)
		case "description":
			description = parseScalar(value)
		case "license":
			license = parseScalar(value)
		case "allowed-tools":
			listKey = key
			if value != "" {
				value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(value, "]"), "["))
				for _, item := range strings.Split(value, ",") {
					if parsed := parseScalar(item); parsed != "" {
						allowedTools = append(allowedTools, parsed)
					}
				}
			}
		}
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" {
		return "", "", "", nil, "", fmt.Errorf("%w: name and description are required", ErrInvalidSkill)
	}
	return name, description, license, allowedTools, body, nil
}

func parseScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return ErrUnsafeSkillName
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
